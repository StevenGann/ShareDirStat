import { downloadUrl, zipUrl, type Node, type ShareInfo } from './api';

/**
 * One action on a node or selection. The same list feeds the detail bar, the
 * context/long-press menu (FR-UI-10) and the mobile detail sheet, so every
 * surface offers exactly the detail-bar actions (FR-UI-18).
 */
export interface ActionItem {
  id: string;
  label: string;
  /** Rendered as a real link: downloads must stay browser-handled GETs. */
  href?: string;
  danger?: boolean;
  disabled?: boolean;
  title?: string;
  run?: () => void;
}

export interface ActionContext {
  busy: boolean;
  onDelete: (nodes: Node[]) => void;
  onZoom: (path: string) => void;
  onLargestHere: (path: string) => void;
  onRescan: (path: string) => void;
}

export function buildNodeActions(share: ShareInfo, nodes: Node[], ctx: ActionContext): ActionItem[] {
  if (nodes.length === 0) return [];
  const single = nodes.length === 1 ? nodes[0] : null;
  const items: ActionItem[] = [];

  if (single && single.kind === 'file' && share.allow_download) {
    items.push({
      id: 'download',
      label: 'Download',
      href: downloadUrl(share.id, single.path),
      title: 'Download this file',
    });
  }

  if (share.zip_enabled && (nodes.length > 1 || single?.kind === 'dir')) {
    items.push({
      id: 'zip',
      label: 'Download as ZIP',
      href: zipUrl(share.id, nodes.map((n) => n.path)),
      title: single
        ? 'Download this folder as one ZIP archive'
        : 'Download the selection as one ZIP archive',
    });
  }

  // When deleting is unavailable the item stays visible but disabled,
  // carrying the server's own reason: a control that silently vanishes
  // leaves the user wondering what they did wrong.
  const blocked = share.delete_blocked;
  const deletableRoot = nodes.every((n) => n.path !== '');
  items.push({
    id: 'delete',
    label: share.trash_enabled ? 'Move to trash' : 'Delete',
    danger: true,
    disabled: Boolean(blocked) || !deletableRoot,
    title: blocked || (deletableRoot ? undefined : 'The share root itself cannot be deleted'),
    run: () => ctx.onDelete(nodes),
  });

  if (single) {
    const fullPath = single.path === '' ? share.path : `${share.path}/${single.path}`;
    items.push({
      id: 'copy',
      label: 'Copy path',
      run: () => void navigator.clipboard?.writeText(fullPath),
    });
    if (single.kind === 'dir') {
      items.push({ id: 'zoom', label: 'Zoom treemap here', run: () => ctx.onZoom(single.path) });
      items.push({
        id: 'largest',
        label: 'Largest files here',
        run: () => ctx.onLargestHere(single.path),
      });
      items.push({
        id: 'rescan',
        label: 'Rescan folder',
        disabled: ctx.busy,
        run: () => ctx.onRescan(single.path),
      });
    }
  }

  return items;
}

/** The detail bar's rendering of a action list: links stay links. */
export function ActionButtons({ items }: { items: ActionItem[] }) {
  return (
    <>
      {items.map((it) =>
        it.href && !it.disabled ? (
          <a key={it.id} className="button" href={it.href} title={it.title}>
            {it.label}
          </a>
        ) : (
          <button
            key={it.id}
            type="button"
            className={it.danger ? 'danger' : undefined}
            disabled={it.disabled}
            title={it.title}
            onClick={it.run}
          >
            {it.label}
          </button>
        ),
      )}
    </>
  );
}
