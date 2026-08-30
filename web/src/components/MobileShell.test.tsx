import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from '../App';
import { mockMatchMedia } from '../testUtils';

const stats = {
  files: 5, dirs: 3, symlinks: 0, others: 0, size: 10642, alloc: 12288,
  hardlink_dups: 0, excluded: 0, errors: 0, max_depth: 3,
};

const share = {
  id: 'media', name: 'Media', path: '/shares/media', state: 'ready',
  discovered: false, allow_delete: true, allow_download: true,
  concurrency: 4, schedule: '', next_scan: null, size_basis: 'apparent',
  excludes: [], filesystem: null, generation: 'GEN1',
  last_scan: '2026-08-29T00:00:00Z', stats, checked_at: '2026-08-29T00:00:00Z',
  scan: null, trash_enabled: false, zip_enabled: true,
};

const node = (over: Record<string, unknown>) => ({
  name: 'x', path: 'x', kind: 'file', size: 100, alloc: 100,
  mtime: '2026-08-01T00:00:00Z', mode: '0644', perms: '-rw-r--r--',
  uid: 1000, gid: 1000, owner: 'steven', group: 'steven',
  flags: { partial: false, mountpoint: false, hardlink_dup: false, excluded: false, unscanned: false },
  files: 0, dirs: 0, children: 0, pct_of_parent: 0.1, pct_of_share: 0.01,
  ...over,
});

const rootNode = node({ name: '', path: '', kind: 'dir', size: 10642, files: 5, dirs: 3 });
const moviesNode = node({ name: 'Movies', path: 'Movies', kind: 'dir', size: 10100, files: 3, dirs: 1 });
const readmeNode = node({ name: 'readme.txt', path: 'readme.txt', size: 42, ext: 'txt' });
const bigNode = node({ name: 'big.mkv', path: 'Movies/big.mkv', size: 8000, ext: 'mkv' });

const rootTree = {
  share: 'media', generation: 'GEN1', basis: 'apparent',
  node: rootNode, ancestors: [], children: [moviesNode, readmeNode],
  total: 2, offset: 0, limit: 500,
};
const moviesTree = { ...rootTree, node: moviesNode, ancestors: [rootNode], children: [bigNode], total: 1 };

class FakeEventSource {
  listeners = new Map<string, EventListener>();
  addEventListener(name: string, fn: EventListener) {
    this.listeners.set(name, fn);
  }
  removeEventListener(name: string) {
    this.listeners.delete(name);
  }
  close() {}
}

interface Call {
  url: string;
  init?: RequestInit;
}

function mockApi(): Call[] {
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      const path = url.split('?')[0] ?? '';
      const params = new URL(url, 'http://x').searchParams;
      const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

      if (path.endsWith('/shares')) return json({ shares: [share] });
      if (path.endsWith('/version')) {
        return json({ version: '1.0.0', commit: 'abc', build_date: 'x', go: 'go1.27', os: 'linux', arch: 'arm64' });
      }
      if (path.endsWith('/tree')) return json(params.get('path') === 'Movies' ? moviesTree : rootTree);
      if (path.endsWith('/node')) {
        const p = params.get('path') ?? '';
        const found = [rootNode, moviesNode, readmeNode, bigNode].find((n) => n.path === p);
        return found
          ? json({ node: found, ancestors: p === '' ? [] : [rootNode] })
          : json({ error: { code: 'path_not_found', message: 'nope' } }, 404);
      }
      if (path.endsWith('/treemap')) {
        return json({
          share: 'media', generation: 'GEN1', basis: 'apparent',
          root: { ...rootNode, children_list: [moviesNode, readmeNode] },
          nodes: 3, min_fraction: 0.0002, max_depth: 8,
        });
      }
      if (path.endsWith('/extensions')) {
        return json({ extensions: [{ ext: 'mkv', files: 2, size: 8100, alloc: 8192 }] });
      }
      if (path.endsWith('/search')) {
        return json({
          share: 'media', generation: 'GEN1', basis: 'apparent',
          matches: [bigNode], total: 1, truncated: false, scanned: 5,
        });
      }
      if (path.endsWith('/top')) return json({ items: [bigNode] });
      if (path.endsWith('/scans')) return json({ scans: [], running: null });
      if (path.endsWith('/errors')) return json({ errors: [], total: 0, dropped: 0 });
      if (path.endsWith('/audit/deletes')) return json({ deletions: [] });
      if (path.endsWith('/delete/preview')) {
        const body = JSON.parse(String(init?.body ?? '{}')) as { paths: string[] };
        return json({
          share: 'media', generation: 'GEN1',
          targets: body.paths.map((p) => ({
            path: p, kind: 'file', size: 100, alloc: 100, files: 0, dirs: 0, exists: true, warnings: [],
          })),
          total_size: 100 * body.paths.length, total_alloc: 100, total_files: body.paths.length,
          total_dirs: 0, confirm: 'TOKEN-123', expires_at: '2099-01-01T00:00:00Z',
          confirm_mode: 'simple', name_to_type: '', trash: false,
        });
      }
      return json({ error: { code: 'not_found', message: `no route for ${url}` } }, 404);
    }),
  );
  return calls;
}

let restoreMedia: () => void;

beforeEach(() => {
  restoreMedia = mockMatchMedia({ '(max-width: 767px)': true, '(pointer: coarse)': true });
  vi.stubGlobal('EventSource', FakeEventSource);
  window.location.hash = '';
  localStorage.clear();
});

afterEach(() => {
  restoreMedia();
  vi.unstubAllGlobals();
});

describe('phone layout', () => {
  it('shows the tab bar and browses into a folder', async () => {
    const calls = mockApi();
    const user = userEvent.setup();
    render(<App />);

    const tabs = await screen.findByRole('navigation', { name: 'Views' });
    within(tabs).getByRole('button', { name: 'Browse' });
    within(tabs).getByRole('button', { name: 'Treemap' });
    within(tabs).getByRole('button', { name: 'File types' });

    // The root listing, then drill down one level. The anchored name skips
    // the row's own "Actions for Movies" kebab.
    await user.click(await screen.findByRole('button', { name: /^Movies/ }));
    expect(await screen.findByText('big.mkv')).toBeInTheDocument();
    expect(calls.some((c) => c.url.includes('/tree') && c.url.includes('path=Movies'))).toBe(true);
    // The breadcrumb now carries the share name as a link back up.
    const crumbs = screen.getByRole('navigation', { name: 'Folder location' });
    within(crumbs).getByRole('button', { name: 'Media' });
  });

  it('opens the detail sheet for a file, with a real download link', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);

    await user.click(await screen.findByRole('button', { name: /^readme\.txt/ }));
    const sheet = await screen.findByRole('dialog', { name: 'readme.txt' });
    const download = within(sheet).getByRole('link', { name: 'Download' });
    expect(download).toHaveAttribute('href', expect.stringContaining('/download?path=readme.txt'));
    await waitFor(() => expect(window.location.hash).toBe('#/media/readme.txt'));
  });

  it('multi-selects with checkboxes and hands the batch to the delete dialog', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);

    await screen.findByText(/readme\.txt/);
    await user.click(screen.getByRole('button', { name: 'Select' }));
    await user.click(screen.getByRole('button', { name: /Movies/ }));
    await user.click(screen.getByRole('button', { name: /readme\.txt/ }));

    const strip = screen.getByRole('toolbar', { name: 'Selection actions' });
    expect(within(strip).getByText('2 selected')).toBeInTheDocument();

    await user.click(within(strip).getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete permanently' });
    expect(await within(dialog).findByText('Movies')).toBeInTheDocument();
    expect(within(dialog).getByText('readme.txt')).toBeInTheDocument();
  });

  it('opens full-screen search from the header', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);

    await screen.findByText(/readme\.txt/);
    await user.click(screen.getByRole('button', { name: 'Search' }));
    expect(await screen.findByRole('searchbox', { name: 'Name' })).toBeInTheDocument();
    expect(await screen.findByText('Movies/big.mkv')).toBeInTheDocument();
  });
});
