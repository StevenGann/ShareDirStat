import { buildNodeActions, type ActionContext } from '../actions';
import type { Basis, Node, ShareInfo } from '../api';
import { NodeFacts, SelectionFacts } from './DetailBar';
import { Sheet } from './Sheet';

interface Props {
  share: ShareInfo;
  node: Node | null;
  selection: Node[];
  basis: Basis;
  busy: boolean;
  onClose: () => void;
  onRescan: (path: string) => void;
  onZoom: (path: string) => void;
  onLargestHere: (path: string) => void;
  onDelete: (nodes: Node[]) => void;
}

/**
 * The phone replacement for the detail bar: the same facts and the same
 * actions (FR-UI-18), stacked at thumb size in a bottom sheet.
 */
export function DetailSheet({
  share,
  node,
  selection,
  basis,
  busy,
  onClose,
  onRescan,
  onZoom,
  onLargestHere,
  onDelete,
}: Props) {
  const ctx: ActionContext = { busy, onDelete, onZoom, onLargestHere, onRescan };
  const multi = selection.length > 1;
  const nodes = multi ? selection : node ? [node] : [];
  if (nodes.length === 0) return null;

  return (
    <Sheet
      title={multi ? `${selection.length} items selected` : (node?.name || share.name)}
      variant="bottom"
      onClose={onClose}
    >
      {multi ? (
        <SelectionFacts selection={selection} basis={basis} />
      ) : (
        node && <NodeFacts share={share} node={node} basis={basis} />
      )}
      <div className="sheet-actions">
        {buildNodeActions(share, nodes, ctx).map((it) =>
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
              onClick={() => {
                it.run?.();
                if (it.id !== 'copy') onClose();
              }}
            >
              {it.label}
            </button>
          ),
        )}
      </div>
    </Sheet>
  );
}
