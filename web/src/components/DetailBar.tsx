import { downloadUrl, zipUrl, type Basis, type Node, type ShareInfo } from '../api';
import { absoluteTime, formatBytes, formatCount, formatPercent } from '../format';

interface Props {
  share: ShareInfo;
  node: Node | null;
  /** Every selected node, when more than one row is picked (FR-UI-08). */
  selection: Node[];
  basis: Basis;
  busy: boolean;
  onRescan: (path: string) => void;
  onZoom: (path: string) => void;
  onLargestHere: (path: string) => void;
  onDelete: (nodes: Node[]) => void;
}

export function DetailBar({
  share,
  node,
  selection,
  basis,
  busy,
  onRescan,
  onZoom,
  onLargestHere,
  onDelete,
}: Props) {
  if (selection.length > 1) {
    const size = selection.reduce((s, n) => s + (basis === 'allocated' ? n.alloc : n.size), 0);
    const files = selection.reduce((s, n) => s + (n.kind === 'dir' ? n.files : 1), 0);
    const dirs = selection.reduce((s, n) => s + (n.kind === 'dir' ? n.dirs + 1 : 0), 0);
    return (
      <footer className="detail">
        <div className="detail-main">
          <span className="detail-path">{formatCount(selection.length)} items selected</span>
          <span className="detail-facts">
            <strong>{formatBytes(size)}</strong>
            <span className="muted">
              {formatCount(files)} files · {formatCount(dirs)} folders
            </span>
          </span>
        </div>
        <div className="detail-actions">
          {share.zip_enabled && (
            <a
              className="button"
              href={zipUrl(share.id, selection.map((n) => n.path))}
              title="Download the selection as one ZIP archive"
            >
              Download as ZIP
            </a>
          )}
          <DeleteButton share={share} nodes={selection} onDelete={onDelete} />
        </div>
      </footer>
    );
  }

  if (!node) {
    return (
      <footer className="detail empty-detail">
        <span className="muted">Select an item to see its details.</span>
      </footer>
    );
  }

  const isDir = node.kind === 'dir';
  const displayPath = node.path === '' ? share.name : `${share.name}/${node.path}`;
  const primary = basis === 'allocated' ? node.alloc : node.size;
  const secondary = basis === 'allocated' ? node.size : node.alloc;
  const fullPath = node.path === '' ? share.path : `${share.path}/${node.path}`;

  return (
    <footer className="detail">
      <div className="detail-main">
        <span className="detail-path mono" title={fullPath}>
          {displayPath}
        </span>
        <span className="detail-facts">
          <span className={`tag kind-${node.kind}`}>{node.kind}</span>
          <strong>{formatBytes(primary)}</strong>
          <span className="muted">
            {basis === 'allocated' ? 'apparent' : 'on disk'} {formatBytes(secondary)}
          </span>
          {isDir && (
            <span className="muted">
              {formatCount(node.files)} files · {formatCount(node.dirs)} folders
            </span>
          )}
          <span className="muted">{formatPercent(node.pct_of_share)} of share</span>
          <span className="muted" title={absoluteTime(node.mtime)}>
            {absoluteTime(node.mtime)}
          </span>
          <span className="mono muted" title={node.mode}>
            {node.perms}
          </span>
          <span className="mono muted">
            {node.owner ?? node.uid}:{node.group ?? node.gid}
          </span>
          {node.flags.hardlink_dup && (
            <span className="tag" title="Another name for a file already counted elsewhere">
              hard link
            </span>
          )}
        </span>
      </div>
      <div className="detail-actions">
        {node.kind === 'file' && share.allow_download && (
          <a className="button" href={downloadUrl(share.id, node.path)} title="Download this file">
            Download
          </a>
        )}
        {isDir && share.zip_enabled && (
          <a
            className="button"
            href={zipUrl(share.id, [node.path])}
            title="Download this folder as one ZIP archive"
          >
            Download as ZIP
          </a>
        )}
        <DeleteButton share={share} nodes={[node]} onDelete={onDelete} />
        <button type="button" onClick={() => void navigator.clipboard?.writeText(fullPath)}>
          Copy path
        </button>
        {isDir && (
          <>
            <button type="button" onClick={() => onZoom(node.path)}>
              Zoom treemap here
            </button>
            <button type="button" onClick={() => onLargestHere(node.path)}>
              Largest files here
            </button>
            <button type="button" onClick={() => onRescan(node.path)} disabled={busy}>
              Rescan folder
            </button>
          </>
        )}
      </div>
    </footer>
  );
}

/**
 * The delete action. When deleting is unavailable the button stays visible
 * but disabled, carrying the server's own reason as its tooltip: a control
 * that silently vanishes leaves the user wondering what they did wrong.
 */
function DeleteButton({
  share,
  nodes,
  onDelete,
}: {
  share: ShareInfo;
  nodes: Node[];
  onDelete: (nodes: Node[]) => void;
}) {
  const blocked = share.delete_blocked;
  const label = share.trash_enabled ? 'Move to trash' : 'Delete';
  const deletableRoot = nodes.every((n) => n.path !== '');
  return (
    <button
      type="button"
      className="danger"
      disabled={Boolean(blocked) || !deletableRoot}
      title={blocked || (deletableRoot ? undefined : 'The share root itself cannot be deleted')}
      onClick={() => onDelete(nodes)}
    >
      {label}
    </button>
  );
}
