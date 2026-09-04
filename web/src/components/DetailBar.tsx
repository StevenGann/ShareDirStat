import { buildNodeActions, ActionButtons, type ActionContext } from '../actions';
import type { Basis, Node, ShareInfo } from '../api';
import {
  absoluteTime,
  bytesPerMinute,
  formatBytes,
  formatBytesPerMin,
  formatCount,
  formatPercent,
  formatPlaytime,
} from '../format';

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
  const ctx: ActionContext = { busy, onDelete, onZoom, onLargestHere, onRescan };

  if (selection.length > 1) {
    return (
      <footer className="detail">
        <SelectionFacts selection={selection} basis={basis} />
        <div className="detail-actions">
          <ActionButtons items={buildNodeActions(share, selection, ctx)} />
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

  return (
    <footer className="detail">
      <NodeFacts share={share} node={node} basis={basis} />
      <div className="detail-actions">
        <ActionButtons items={buildNodeActions(share, [node], ctx)} />
      </div>
    </footer>
  );
}

/** Aggregate facts for a multi-selection; shared with the mobile sheet. */
export function SelectionFacts({ selection, basis }: { selection: Node[]; basis: Basis }) {
  const size = selection.reduce((s, n) => s + (basis === 'allocated' ? n.alloc : n.size), 0);
  const files = selection.reduce((s, n) => s + (n.kind === 'dir' ? n.files : 1), 0);
  const dirs = selection.reduce((s, n) => s + (n.kind === 'dir' ? n.dirs + 1 : 0), 0);
  const playtime = selection.reduce((s, n) => s + (n.duration ?? 0), 0);
  return (
    <div className="detail-main">
      <span className="detail-path">{formatCount(selection.length)} items selected</span>
      <span className="detail-facts">
        <strong>{formatBytes(size)}</strong>
        <span className="muted">
          {formatCount(files)} files · {formatCount(dirs)} folders
        </span>
        {playtime > 0 && <span className="muted">{formatPlaytime(playtime)} of media</span>}
      </span>
    </div>
  );
}

/** Path plus the fact list for one node; shared with the mobile sheet. */
export function NodeFacts({ share, node, basis }: { share: ShareInfo; node: Node; basis: Basis }) {
  const isDir = node.kind === 'dir';
  const displayPath = node.path === '' ? share.name : `${share.name}/${node.path}`;
  const primary = basis === 'allocated' ? node.alloc : node.size;
  const secondary = basis === 'allocated' ? node.size : node.alloc;
  const fullPath = node.path === '' ? share.path : `${share.path}/${node.path}`;

  return (
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
        {node.duration ? (
          <span
            className="muted"
            title={
              isDir
                ? `${formatBytes(node.media_size ?? 0)} of media playing for ${formatPlaytime(node.duration)}`
                : `Playing time ${formatPlaytime(node.duration)}`
            }
          >
            {formatPlaytime(node.duration)}
            {' · '}
            {formatBytesPerMin(bytesPerMinute(node))}
          </span>
        ) : null}
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
  );
}
