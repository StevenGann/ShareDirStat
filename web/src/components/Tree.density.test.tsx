import { render, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Node, ShareInfo, TreeResponse } from '../api';
import { mockMatchMedia } from '../testUtils';
import { Tree } from './Tree';

function makeNode(partial: Partial<Node>): Node {
  return {
    name: '', path: '', kind: 'file', size: 0, alloc: 0,
    mtime: '2026-01-01T00:00:00Z', mode: '-rw-r--r--', perms: '-rw-r--r--',
    uid: 1000, gid: 1000,
    flags: { partial: false, mountpoint: false, hardlink_dup: false, excluded: false, unscanned: false },
    files: 0, dirs: 0, children: 0, pct_of_parent: 0, pct_of_share: 0,
    ...partial,
  };
}

const share: ShareInfo = {
  id: 'media', name: 'Media', path: '/shares/media', state: 'ready',
  discovered: false, allow_delete: true, allow_download: true,
  concurrency: 4, schedule: '', next_scan: null, size_basis: 'apparent',
  excludes: [], filesystem: null, generation: 'g1',
  last_scan: '2026-01-01T00:00:00Z', stats: null,
  checked_at: '2026-01-01T00:00:00Z', scan: null,
  trash_enabled: false, zip_enabled: true,
};

function stubTree(children: Node[]) {
  const body: TreeResponse = {
    share: 'media', generation: 'g1', basis: 'apparent',
    node: makeNode({ kind: 'dir', name: 'Media', children: children.length }),
    ancestors: [], children, total: children.length, offset: 0, limit: 500,
  };
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => new Response(JSON.stringify(body), { status: 200 })),
  );
}

function renderTree() {
  return render(
    <Tree
      share={share}
      basis="apparent"
      generation="g1"
      columns={['size']}
      sort="size"
      desc
      onSortChange={() => {}}
      selected={null}
      selectedPaths={new Set()}
      onSelect={() => {}}
      onActivate={() => {}}
      expanded={new Set([''])}
      onExpandedChange={() => {}}
    />,
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('tree row density', () => {
  const children = [
    makeNode({ name: 'big.mkv', path: 'big.mkv', size: 2000, ext: 'mkv' }),
    makeNode({ name: 'small.mkv', path: 'small.mkv', size: 1000, ext: 'mkv' }),
  ];

  it('positions rows every 22px with a fine pointer', async () => {
    stubTree(children);
    const { container } = renderTree();
    await waitFor(() => expect(container.querySelectorAll('.tr')).toHaveLength(2));
    const rows = container.querySelectorAll<HTMLElement>('.tr');
    expect(rows[0]?.style.top).toBe('0px');
    expect(rows[1]?.style.top).toBe('22px');
  });

  it('positions rows every 40px with a coarse pointer', async () => {
    const restore = mockMatchMedia({ '(pointer: coarse)': true });
    try {
      stubTree(children);
      const { container } = renderTree();
      await waitFor(() => expect(container.querySelectorAll('.tr')).toHaveLength(2));
      const rows = container.querySelectorAll<HTMLElement>('.tr');
      expect(rows[0]?.style.top).toBe('0px');
      expect(rows[1]?.style.top).toBe('40px');
    } finally {
      restore();
    }
  });
});
