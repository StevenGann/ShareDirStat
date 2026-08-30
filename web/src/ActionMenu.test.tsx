import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { buildNodeActions, type ActionContext } from './actions';
import type { Node, ShareInfo } from './api';
import { ActionMenu } from './components/ActionMenu';

const share: ShareInfo = {
  id: 'media', name: 'Media', path: '/shares/media', state: 'ready',
  discovered: false, allow_delete: true, allow_download: true,
  concurrency: 4, schedule: '', next_scan: null, size_basis: 'apparent',
  excludes: [], filesystem: null, generation: 'g1',
  last_scan: '2026-01-01T00:00:00Z', stats: null,
  checked_at: '2026-01-01T00:00:00Z', scan: null,
  trash_enabled: false, zip_enabled: true,
};

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

const noopCtx: ActionContext = {
  busy: false,
  onDelete: () => {},
  onZoom: () => {},
  onLargestHere: () => {},
  onRescan: () => {},
};

describe('buildNodeActions (FR-UI-18)', () => {
  it('gives a file Download, Delete and Copy path', () => {
    const items = buildNodeActions(share, [makeNode({ name: 'a.mkv', path: 'a.mkv' })], noopCtx);
    expect(items.map((i) => i.id)).toEqual(['download', 'delete', 'copy']);
    expect(items[0]?.href).toContain('/download?path=a.mkv');
  });

  it('gives a folder ZIP, Delete, Copy, Zoom, Largest and Rescan', () => {
    const items = buildNodeActions(
      share,
      [makeNode({ name: 'Movies', path: 'Movies', kind: 'dir' })],
      noopCtx,
    );
    expect(items.map((i) => i.id)).toEqual(['zip', 'delete', 'copy', 'zoom', 'largest', 'rescan']);
  });

  it('reduces a multi-selection to ZIP and Delete', () => {
    const items = buildNodeActions(
      share,
      [makeNode({ path: 'a.mkv' }), makeNode({ path: 'b.mkv' })],
      noopCtx,
    );
    expect(items.map((i) => i.id)).toEqual(['zip', 'delete']);
    expect(items[0]?.href).toContain('path=a.mkv');
    expect(items[0]?.href).toContain('path=b.mkv');
  });

  it("carries the server's delete-blocked reason as the tooltip", () => {
    const blocked = { ...share, delete_blocked: 'the mount is read-only' };
    const items = buildNodeActions(blocked, [makeNode({ path: 'a.mkv' })], noopCtx);
    const del = items.find((i) => i.id === 'delete');
    expect(del?.disabled).toBe(true);
    expect(del?.title).toBe('the mount is read-only');
  });
});

describe('ActionMenu (FR-UI-10)', () => {
  it('runs an action and closes, keeping downloads as real links', async () => {
    const user = userEvent.setup();
    const onDelete = vi.fn();
    const onClose = vi.fn();
    const node = makeNode({ name: 'a.mkv', path: 'a.mkv' });
    render(
      <ActionMenu
        items={buildNodeActions(share, [node], { ...noopCtx, onDelete })}
        x={10}
        y={10}
        label="Actions for a.mkv"
        onClose={onClose}
      />,
    );

    const menu = screen.getByRole('menu', { name: 'Actions for a.mkv' });
    expect(menu).toBeInTheDocument();
    const download = screen.getByRole('menuitem', { name: 'Download' });
    expect(download).toHaveAttribute('href', expect.stringContaining('/download?path=a.mkv'));

    await user.click(screen.getByRole('menuitem', { name: 'Delete' }));
    expect(onDelete).toHaveBeenCalledWith([node]);
    expect(onClose).toHaveBeenCalled();
  });

  it('closes on Escape without letting the key bubble', async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    const bubbled = vi.fn();
    window.addEventListener('keydown', bubbled);
    try {
      render(
        <ActionMenu
          items={buildNodeActions(share, [makeNode({ path: 'a.mkv' })], noopCtx)}
          x={10}
          y={10}
          label="Actions for a.mkv"
          onClose={onClose}
        />,
      );
      await user.keyboard('{Escape}');
      expect(onClose).toHaveBeenCalled();
      expect(bubbled).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener('keydown', bubbled);
    }
  });
});
