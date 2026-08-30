import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import App from './App';
import { apiBase } from './api';
import { formatBytes, formatCount, formatPercent, relativeTime, truncatePath } from './format';
import { parseSize } from './components/ResultsView';

const stats = {
  files: 5,
  dirs: 3,
  symlinks: 0,
  others: 0,
  size: 10642,
  alloc: 12288,
  hardlink_dups: 0,
  excluded: 0,
  errors: 0,
  max_depth: 3,
};

const shareReady = {
  id: 'media',
  name: 'Media',
  path: '/shares/media',
  state: 'ready',
  discovered: false,
  allow_delete: false,
  allow_download: true,
  concurrency: 4,
  schedule: '0 3 * * *',
  next_scan: null,
  size_basis: 'apparent',
  excludes: [],
  filesystem: { type: 'nfs4', mount_point: '/shares/media', readonly: false },
  generation: 'GEN1',
  last_scan: '2026-08-29T00:00:00Z',
  stats,
  checked_at: '2026-08-29T00:00:00Z',
  scan: null,
  trash_enabled: false,
  zip_enabled: true,
};

const node = (over: Record<string, unknown>) => ({
  name: 'x',
  path: 'x',
  kind: 'file',
  size: 100,
  alloc: 100,
  mtime: '2026-08-01T00:00:00Z',
  mode: '0644',
  perms: '-rw-r--r--',
  uid: 1000,
  gid: 1000,
  owner: 'steven',
  group: 'steven',
  flags: {
    partial: false,
    mountpoint: false,
    hardlink_dup: false,
    excluded: false,
    unscanned: false,
  },
  files: 0,
  dirs: 0,
  children: 0,
  pct_of_parent: 0.1,
  pct_of_share: 0.01,
  ...over,
});

const rootNode = node({ name: '', path: '', kind: 'dir', size: 10642, files: 5, dirs: 3, children: 2 });
const moviesNode = node({
  name: 'Movies',
  path: 'Movies',
  kind: 'dir',
  size: 10100,
  files: 3,
  dirs: 1,
  children: 2,
  pct_of_parent: 0.949,
});
const readmeNode = node({ name: 'readme.txt', path: 'readme.txt', size: 42, ext: 'txt' });
const bigNode = node({ name: 'big.mkv', path: 'Movies/big.mkv', size: 8000, ext: 'mkv' });

const rootTree = {
  share: 'media',
  generation: 'GEN1',
  basis: 'apparent',
  node: rootNode,
  ancestors: [],
  children: [moviesNode, readmeNode],
  total: 2,
  offset: 0,
  limit: 500,
};

const moviesTree = {
  ...rootTree,
  node: moviesNode,
  ancestors: [rootNode],
  children: [bigNode],
  total: 1,
};

const treemap = {
  share: 'media',
  generation: 'GEN1',
  basis: 'apparent',
  root: { ...rootNode, children_list: [{ ...moviesNode, children_list: [bigNode] }, readmeNode] },
  nodes: 4,
  min_fraction: 0.0002,
  max_depth: 8,
};

const extensions = {
  extensions: [
    { ext: 'mkv', files: 2, size: 8100, alloc: 8192 },
    { ext: 'txt', files: 1, size: 42, alloc: 512 },
  ],
};

class FakeEventSource {
  static instances: FakeEventSource[] = [];
  listeners = new Map<string, EventListener>();
  constructor(public url: string) {
    FakeEventSource.instances.push(this);
  }
  addEventListener(name: string, fn: EventListener) {
    this.listeners.set(name, fn);
  }
  removeEventListener(name: string) {
    this.listeners.delete(name);
  }
  close() {}
  emit(name: string, data: unknown) {
    this.listeners.get(name)?.(new MessageEvent(name, { data: JSON.stringify(data) }) as Event);
  }
}

interface Call {
  url: string;
  init?: RequestInit;
}

function mockApi(overrides: Record<string, unknown> = {}) {
  const routes: Record<string, unknown> = {
    '/api/v1/shares': { shares: [shareReady] },
    '/api/v1/version': {
      version: '1.0.0',
      commit: 'abc',
      build_date: 'x',
      go: 'go1.27',
      os: 'linux',
      arch: 'arm64',
    },
    ...overrides,
  };
  const calls: Call[] = [];
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      calls.push({ url, init });
      const path = url.split('?')[0] ?? '';
      const params = new URL(url, 'http://x').searchParams;
      const json = (body: unknown, status = 200) =>
        new Response(JSON.stringify(body), { status });

      if (path.endsWith('/tree')) {
        return json(params.get('path') === 'Movies' ? moviesTree : rootTree);
      }
      if (path.endsWith('/node')) {
        const p = params.get('path') ?? '';
        const found = [rootNode, moviesNode, readmeNode, bigNode].find((n) => n.path === p);
        return found
          ? json({ node: found, ancestors: p === '' ? [] : [rootNode] })
          : json({ error: { code: 'path_not_found', message: 'nope' } }, 404);
      }
      if (path.endsWith('/treemap')) return json(treemap);
      if (path.endsWith('/extensions')) return json(extensions);
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
      if (path.endsWith('/trash/restore')) {
        const body = JSON.parse(String(init?.body ?? '{}')) as { path: string };
        return json({
          batch: 'B', trash_path: body.path, original_path: 'Movies/big.mkv',
          deleted_at: '2026-08-29T00:00:00Z', kind: 'file', size: 8000, blocked: false,
        });
      }
      if (path.endsWith('/trash/empty')) return json({ removed: 2 });
      if (path.endsWith('/trash')) {
        return json({
          items: [
            {
              batch: '20260829T000000Z', trash_path: '.sharedirstat-trash/20260829T000000Z/Movies/big.mkv',
              original_path: 'Movies/big.mkv', deleted_at: '2026-08-29T00:00:00Z', kind: 'file',
              size: 8000, blocked: false,
            },
            {
              batch: '20260829T000000Z', trash_path: '.sharedirstat-trash/20260829T000000Z/readme.txt',
              original_path: 'readme.txt', deleted_at: '2026-08-29T00:00:00Z', kind: 'file',
              size: 42, blocked: true,
            },
          ],
        });
      }
      if (path.endsWith('/delete/preview')) {
        const body = JSON.parse(String(init?.body ?? '{}')) as { paths: string[] };
        const targets = body.paths.map((p) => ({
          path: p, kind: p.includes('.') ? 'file' : 'dir',
          size: 8000, alloc: 8192, files: 2, dirs: 1, exists: true, warnings: [],
        }));
        return json({
          share: 'media', generation: 'GEN1', targets,
          total_size: 8000 * targets.length, total_alloc: 8192, total_files: 2, total_dirs: 1,
          confirm: 'TOKEN-123', expires_at: '2099-01-01T00:00:00Z',
          confirm_mode: 'name',
          name_to_type: targets.some((t) => t.kind === 'dir') ? 'Movies' : '',
          trash: false,
        });
      }
      if (path.endsWith('/delete')) {
        const body = JSON.parse(String(init?.body ?? '{}')) as { paths: string[]; confirm: string };
        if (body.confirm !== 'TOKEN-123') {
          return json({ error: { code: 'confirmation_required', message: 'bad token' } }, 403);
        }
        return json({
          share: 'media',
          results: body.paths.map((p) => ({
            path: p, outcome: 'deleted', freed: 8000, freed_alloc: 8192, removed: 1, failed: 0,
          })),
          freed_total: 8000 * body.paths.length,
          reconcile: null,
        });
      }

      for (const [suffix, body] of Object.entries(routes)) {
        if (path.endsWith(suffix)) return json(body);
      }
      return json({ error: { code: 'not_found', message: `no route for ${url}` } }, 404);
    }),
  );
  return calls;
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal('EventSource', FakeEventSource);
  window.location.hash = '';
  localStorage.clear();
});

afterEach(() => vi.unstubAllGlobals());

describe('App', () => {
  it('shows the share summary and the root listing', async () => {
    mockApi();
    render(<App />);
    expect(await screen.findByText('Media')).toBeInTheDocument();
    expect(screen.getByText('Ready')).toBeInTheDocument();
    expect(await screen.findByText('Movies')).toBeInTheDocument();
    expect(screen.getByText('readme.txt')).toBeInTheDocument();
    // 10642 bytes in binary units, three significant figures.
    expect(screen.getAllByText('10.4 KiB').length).toBeGreaterThan(0);
  });

  it('expands a directory on demand', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Expand Movies' }));
    expect(await screen.findByText('big.mkv')).toBeInTheDocument();
  });

  it('selects a row and shows it in the detail bar', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('readme.txt'));
    const footer = document.querySelector('.detail') as HTMLElement;
    expect(within(footer).getByText('Media/readme.txt')).toBeInTheDocument();
    expect(within(footer).getByText('-rw-r--r--')).toBeInTheDocument();
  });

  it('records the selection in the URL so the view can be bookmarked', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('readme.txt'));
    await waitFor(() => expect(window.location.hash).toBe('#/media/readme.txt'));
  });

  it('lists file types with a colour swatch', async () => {
    mockApi();
    render(<App />);
    const pane = await screen.findByLabelText('Space by file type');
    expect(await within(pane).findByText('.mkv')).toBeInTheDocument();
    expect(within(pane).getByText('Matroska video')).toBeInTheDocument();
    expect(pane.querySelectorAll('.swatch').length).toBe(2);
  });

  it('highlights a file type when its row is chosen', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    const pane = await screen.findByLabelText('Space by file type');
    await user.click(await within(pane).findByText('.mkv'));
    expect(within(pane).getByRole('button', { name: 'Clear highlight' })).toBeInTheDocument();
  });

  it('renders a treemap region with a breadcrumb', async () => {
    mockApi();
    render(<App />);
    const pane = await screen.findByLabelText('Treemap');
    expect(within(pane).getByLabelText('Treemap location')).toBeInTheDocument();
    expect(within(pane).getByText('Share root')).toBeInTheDocument();
  });

  it('switches the size basis', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText('Movies');
    await user.click(screen.getByRole('radio', { name: 'On disk' }));
    await waitFor(() => expect(localStorage.getItem('sds.basis')).toBe('"allocated"'));
  });

  it('adds a column from the chooser', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText('Movies');
    await user.click(screen.getByText('Columns'));
    await user.click(screen.getByRole('checkbox', { name: 'Permissions' }));
    const tree = screen.getByLabelText('Media directory tree');
    expect(await within(tree).findByText('Permissions')).toBeInTheDocument();
    await waitFor(() => expect(localStorage.getItem('sds.columns')).toContain('perms'));
  });

  it('opens search from the toolbar and lists matches', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText('Movies');
    await user.click(screen.getByRole('button', { name: /Search/ }));
    const pane = await screen.findByLabelText('Search');
    expect(await within(pane).findByText('Movies/big.mkv')).toBeInTheDocument();
    expect(within(pane).getByText('1 match', { exact: false })).toBeInTheDocument();
  });

  it('starts a scan with the CSRF header', async () => {
    const calls = mockApi({ '/scan': { id: 'S1', share_id: 'media' } });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByRole('button', { name: 'Rescan' }));
    await waitFor(() => {
      const post = calls.find((c) => c.init?.method === 'POST');
      expect(post?.url).toContain('/scan');
      const headers = (post?.init?.headers ?? {}) as Record<string, string>;
      expect(headers['X-Requested-With']).toBe('ShareDirStat');
    });
  });

  it('renders live scan progress from the event stream', async () => {
    mockApi();
    render(<App />);
    await screen.findByText('Movies');
    const es = FakeEventSource.instances[0];
    expect(es?.url).toContain('/api/v1/events');
    es?.emit('scan.progress', {
      scan_id: 'S1',
      share_id: 'media',
      path: '',
      dirs: 12,
      files: 3400,
      bytes: 99999,
      errors: 0,
      excluded: 0,
      pending: 4,
      current_path: '/shares/media/Movies/2019',
      elapsed_ms: 1200,
      rate_files_per_s: 2833,
      paused: false,
    });
    expect(await screen.findByText('Scanning')).toBeInTheDocument();
    expect(screen.getByText('3,400 files')).toBeInTheDocument();
    expect(screen.getByText('3k files/s')).toBeInTheDocument();
  });

  it('explains an unscanned share instead of showing an empty table', async () => {
    mockApi({
      '/api/v1/shares': {
        shares: [{ ...shareReady, state: 'never-scanned', generation: null, last_scan: null, stats: null }],
      },
    });
    render(<App />);
    expect(await screen.findByText('No results yet')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Scan now' })).toBeInTheDocument();
  });

  it('surfaces a server error', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(
        async () =>
          new Response(JSON.stringify({ error: { code: 'boom', message: 'server exploded' } }), {
            status: 500,
          }),
      ),
    );
    render(<App />);
    expect(await screen.findByRole('alert')).toHaveTextContent('server exploded');
  });

  it('offers an empty state when nothing is configured', async () => {
    mockApi({ '/api/v1/shares': { shares: [] } });
    render(<App />);
    expect(await screen.findByText('No shares configured')).toBeInTheDocument();
  });
});

describe('apiBase', () => {
  it('derives the API prefix from the page location', () => {
    expect(apiBase({ pathname: '/' })).toBe('/api/v1');
    expect(apiBase({ pathname: '/sds/' })).toBe('/sds/api/v1');
    expect(apiBase({ pathname: '/sds/index.html' })).toBe('/sds/api/v1');
  });
});

describe('formatting', () => {
  it('formats bytes in binary units by default', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1536)).toBe('1.50 KiB');
    expect(formatBytes(4831838208)).toBe('4.50 GiB');
    expect(formatBytes(4831838208, true)).toBe('4.83 GB');
    expect(formatBytes(-1)).toBe('—');
  });

  it('formats counts and percentages', () => {
    expect(formatCount(1234567)).toBe('1,234,567');
    expect(formatPercent(0.0512)).toBe('5.1%');
    expect(formatPercent(0.87)).toBe('87%');
  });

  it('formats relative time', () => {
    const now = Date.parse('2026-08-29T12:00:00Z');
    expect(relativeTime('2026-08-29T11:59:30Z', now)).toContain('second');
    expect(relativeTime('2026-08-29T09:00:00Z', now)).toContain('hour');
    expect(relativeTime(null)).toBe('never');
  });

  it('truncates long paths from the left', () => {
    expect(truncatePath('/a/very/long/path/to/a/file.mkv', 12)).toBe('…/a/file.mkv');
    expect(truncatePath('/short', 12)).toBe('/short');
  });
});

describe('parseSize', () => {
  it('accepts the units people type', () => {
    expect(parseSize('1024')).toBe(1024);
    expect(parseSize('500MB')).toBe(500_000_000);
    expect(parseSize('1.5 GiB')).toBe(1_610_612_736);
    expect(parseSize('2tb')).toBe(2_000_000_000_000);
  });

  it('returns zero for anything it cannot read', () => {
    expect(parseSize('')).toBe(0);
    expect(parseSize('lots')).toBe(0);
    expect(parseSize('12 parsecs')).toBe(0);
  });
});

describe('delete', () => {
  it('offers a download link and a delete button for a file', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('readme.txt'));
    const footer = document.querySelector('.detail') as HTMLElement;
    const link = within(footer).getByRole('link', { name: 'Download' });
    expect(link).toHaveAttribute('href', expect.stringContaining('/download?path=readme.txt'));
    expect(within(footer).getByRole('button', { name: 'Delete' })).toBeEnabled();
  });

  it('requires the typed name before deleting a folder', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('Movies'));
    const footer = document.querySelector('.detail') as HTMLElement;
    await user.click(within(footer).getByRole('button', { name: 'Delete' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('heading', { name: 'Delete permanently' })).toBeInTheDocument();
    const confirmButton = within(dialog).getByRole('button', { name: 'Delete permanently' });
    expect(confirmButton).toBeDisabled();

    // The wrong text keeps it disabled.
    const field = within(dialog).getByLabelText('Type Movies to confirm');
    await user.type(field, 'Movie');
    expect(confirmButton).toBeDisabled();

    await user.type(field, 's');
    expect(confirmButton).toBeEnabled();
  });

  it('deletes after confirmation and reports what was reclaimed', async () => {
    const calls = mockApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('readme.txt'));
    const footer = document.querySelector('.detail') as HTMLElement;
    await user.click(within(footer).getByRole('button', { name: 'Delete' }));

    const dialog = await screen.findByRole('dialog');
    // A single file only needs the acknowledgement checkbox.
    await user.click(within(dialog).getByLabelText('I understand what will be removed'));
    await user.click(within(dialog).getByRole('button', { name: 'Delete permanently' }));

    // The app announces the outcome in its status banner.
    const banner = await screen.findByRole('status');
    expect(banner).toHaveTextContent(/Reclaimed .* from 1 item/i);
    const sent = calls.find((c) => c.url.includes('/delete') && !c.url.includes('preview'));
    const body = JSON.parse(String(sent?.init?.body ?? '{}')) as { confirm: string; paths: string[] };
    expect(body.confirm).toBe('TOKEN-123');
    expect(body.paths).toEqual(['readme.txt']);
    const headers = (sent?.init?.headers ?? {}) as Record<string, string>;
    expect(headers['X-Requested-With']).toBe('ShareDirStat');
  });

  it('cancels without sending a delete', async () => {
    const calls = mockApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('readme.txt'));
    const footer = document.querySelector('.detail') as HTMLElement;
    await user.click(within(footer).getByRole('button', { name: 'Delete' }));
    const dialog = await screen.findByRole('dialog');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(calls.some((c) => c.url.endsWith('/delete') && c.init?.method === 'POST')).toBe(false);
  });

  it('disables deleting with the server’s reason when the share forbids it', async () => {
    mockApi({
      '/api/v1/shares': {
        shares: [{ ...shareReady, delete_blocked: 'set allow_delete on the share to enable it' }],
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('readme.txt'));
    const footer = document.querySelector('.detail') as HTMLElement;
    const button = within(footer).getByRole('button', { name: 'Delete' });
    expect(button).toBeDisabled();
    expect(button).toHaveAttribute('title', 'set allow_delete on the share to enable it');
  });

  it('labels the action as trash when trash mode is on', async () => {
    mockApi({
      '/api/v1/shares': { shares: [{ ...shareReady, trash_enabled: true }] },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('readme.txt'));
    const footer = document.querySelector('.detail') as HTMLElement;
    expect(within(footer).getByRole('button', { name: 'Move to trash' })).toBeInTheDocument();
  });

  it('offers a ZIP link for a folder', async () => {
    mockApi();
    const user = userEvent.setup();
    render(<App />);
    await user.click(await screen.findByText('Movies'));
    const footer = document.querySelector('.detail') as HTMLElement;
    const link = within(footer).getByRole('link', { name: 'Download as ZIP' });
    expect(link).toHaveAttribute('href', expect.stringContaining('download/zip?path=Movies'));
  });
});

describe('trash', () => {
  const trashShare = { ...shareReady, trash_enabled: true };

  it('is hidden unless trash mode is on', async () => {
    mockApi();
    render(<App />);
    await screen.findByText('Movies');
    expect(screen.queryByRole('button', { name: 'Trash' })).not.toBeInTheDocument();
  });

  it('lists trashed items and blocks a collision', async () => {
    mockApi({ '/api/v1/shares': { shares: [trashShare] } });
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText('Movies');
    await user.click(screen.getByRole('button', { name: 'Trash' }));

    const view = await screen.findByRole('dialog', { name: 'Trash' });
    expect(await within(view).findByText('Movies/big.mkv')).toBeInTheDocument();
    expect(within(view).getByText('readme.txt')).toBeInTheDocument();
    const buttons = within(view).getAllByRole('button', { name: 'Restore' });
    expect(buttons).toHaveLength(2);
    // The item whose location is occupied again cannot be restored.
    expect(buttons[1]).toBeDisabled();
    expect(within(view).getByText(/Something else now exists/)).toBeInTheDocument();
  });

  it('restores an item with the CSRF header', async () => {
    const calls = mockApi({ '/api/v1/shares': { shares: [trashShare] } });
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText('Movies');
    await user.click(screen.getByRole('button', { name: 'Trash' }));
    const view = await screen.findByRole('dialog', { name: 'Trash' });
    const [restore] = await within(view).findAllByRole('button', { name: 'Restore' });
    await user.click(restore as HTMLElement);

    await waitFor(() => {
      const sent = calls.find((c) => c.url.endsWith('/trash/restore'));
      expect(sent).toBeDefined();
      const body = JSON.parse(String(sent?.init?.body ?? '{}')) as { path: string };
      expect(body.path).toBe('.sharedirstat-trash/20260829T000000Z/Movies/big.mkv');
      const headers = (sent?.init?.headers ?? {}) as Record<string, string>;
      expect(headers['X-Requested-With']).toBe('ShareDirStat');
    });
  });

  it('asks before emptying', async () => {
    const calls = mockApi({ '/api/v1/shares': { shares: [trashShare] } });
    const user = userEvent.setup();
    render(<App />);
    await screen.findByText('Movies');
    await user.click(screen.getByRole('button', { name: 'Trash' }));
    const view = await screen.findByRole('dialog', { name: 'Trash' });
    await within(view).findByText('Movies/big.mkv');

    await user.click(within(view).getByRole('button', { name: 'Empty trash…' }));
    expect(calls.some((c) => c.url.endsWith('/trash/empty'))).toBe(false);
    await user.click(within(view).getByRole('button', { name: 'Empty trash' }));
    await waitFor(() => expect(calls.some((c) => c.url.endsWith('/trash/empty'))).toBe(true));
  });
});
