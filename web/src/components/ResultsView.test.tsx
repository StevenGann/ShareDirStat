import { fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from '../App';

const stats = {
  files: 3, dirs: 1, symlinks: 0, others: 0, size: 20000, alloc: 20480,
  hardlink_dups: 0, excluded: 0, errors: 0, max_depth: 2,
  media_size: 17000, media_duration: 4300,
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

const rootNode = node({ name: '', path: '', kind: 'dir', size: 20000, files: 3, dirs: 1 });
const bigNode = node({
  name: 'big.mkv', path: 'Movies/big.mkv', size: 12000, ext: 'mkv',
  duration: 7112, media_size: 12000,
});
const midNode = node({ name: 'mid.mkv', path: 'Movies/mid.mkv', size: 5000, ext: 'mkv' });
const docNode = node({ name: 'notes.pdf', path: 'notes.pdf', size: 3000, ext: 'pdf' });

class FakeEventSource {
  addEventListener() {}
  removeEventListener() {}
  close() {}
}

function mockApi() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      const path = url.split('?')[0] ?? '';
      const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status });

      if (path.endsWith('/shares')) return json({ shares: [share] });
      if (path.endsWith('/version')) {
        return json({ version: '1.0.0', commit: 'abc', build_date: 'x', go: 'go1.27', os: 'linux', arch: 'arm64' });
      }
      if (path.endsWith('/tree')) {
        return json({
          share: 'media', generation: 'GEN1', basis: 'apparent',
          node: rootNode, ancestors: [], children: [bigNode, midNode, docNode],
          total: 3, offset: 0, limit: 500,
        });
      }
      if (path.endsWith('/treemap')) {
        return json({
          share: 'media', generation: 'GEN1', basis: 'apparent',
          root: { ...rootNode, children_list: [] }, nodes: 1, min_fraction: 0.0002, max_depth: 8,
        });
      }
      if (path.endsWith('/extensions')) return json({ extensions: [] });
      if (path.endsWith('/top')) return json({ items: [bigNode, midNode, docNode] });
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
}

beforeEach(() => {
  vi.stubGlobal('EventSource', FakeEventSource);
  window.location.hash = '';
  localStorage.clear();
});

afterEach(() => {
  vi.unstubAllGlobals();
});

const rowFor = (path: string) => {
  const cell = screen.getByText(path);
  const row = cell.closest('tr');
  if (!row) throw new Error(`no row for ${path}`);
  return row;
};

async function openLargest(user: ReturnType<typeof userEvent.setup>) {
  render(<App />);
  await user.click(await screen.findByRole('button', { name: 'Largest files' }));
  await screen.findByText('Movies/big.mkv');
}

describe('results list multi-select (FR-UI-08)', () => {
  it('ctrl-click builds a selection that the detail bar aggregates', async () => {
    mockApi();
    const user = userEvent.setup();
    await openLargest(user);

    fireEvent.click(rowFor('Movies/big.mkv'));
    fireEvent.click(rowFor('Movies/mid.mkv'), { ctrlKey: true });

    expect(await screen.findByText('2 items selected')).toBeInTheDocument();
    // Ctrl-clicking a selected row removes it again.
    fireEvent.click(rowFor('Movies/mid.mkv'), { ctrlKey: true });
    expect(screen.queryByText('2 items selected')).not.toBeInTheDocument();
  });

  it('select mode offers checkboxes and Select all shown', async () => {
    mockApi();
    const user = userEvent.setup();
    await openLargest(user);

    await user.click(screen.getByRole('button', { name: 'Select' }));
    await user.click(screen.getByRole('button', { name: 'Select all shown' }));
    expect(await screen.findByText('3 items selected')).toBeInTheDocument();

    // In select mode a plain click toggles membership off.
    fireEvent.click(rowFor('notes.pdf'));
    expect(await screen.findByText('2 items selected')).toBeInTheDocument();
  });

  it('hands the multi-selection to the delete dialog', async () => {
    mockApi();
    const user = userEvent.setup();
    await openLargest(user);

    fireEvent.click(rowFor('Movies/big.mkv'));
    fireEvent.click(rowFor('Movies/mid.mkv'), { ctrlKey: true });
    await screen.findByText('2 items selected');

    await user.click(screen.getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete permanently' });
    expect(await within(dialog).findByText('Movies/big.mkv')).toBeInTheDocument();
    expect(within(dialog).getByText('Movies/mid.mkv')).toBeInTheDocument();
  });

  it('shows media playing time in the Length column', async () => {
    mockApi();
    const user = userEvent.setup();
    await openLargest(user);

    expect(within(rowFor('Movies/big.mkv')).getByText('1:58:32')).toBeInTheDocument();
    // A file without a known duration shows an em dash, not zero.
    expect(within(rowFor('notes.pdf')).getByText('—')).toBeInTheDocument();
  });
});
