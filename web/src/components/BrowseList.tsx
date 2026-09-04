import { useCallback, useState } from 'react';
import { api, type Basis, type Node, type ShareInfo } from '../api';
import { formatBytes, formatCount, formatPlaytime, relativeTime } from '../format';
import { useAsyncData, useLongPress } from '../hooks';
import { IconCheck, IconChevron, IconFile, IconFolder, IconKebab } from './icons';
import { NodeTags, type SortField } from './Tree';

/** Children fetched per directory; matches the tree's page (FR-UI-06). */
const PAGE = 500;

interface Props {
  share: ShareInfo;
  basis: Basis;
  generation: string | null;
  /** Directory being shown; drill-down navigation, one level at a time. */
  path: string;
  onNavigate: (path: string) => void;
  sort: SortField;
  desc: boolean;
  onSortChange: (sort: SortField, desc: boolean) => void;
  selected: Node | null;
  selectedPaths: Set<string>;
  selectMode: boolean;
  onSelectModeChange: (v: boolean) => void;
  onSelect: (node: Node, mode: 'replace' | 'toggle') => void;
  onOpenDetail: (node: Node) => void;
  onMenu: (node: Node, x: number, y: number) => void;
}

/**
 * The phone Browse tab: a one-directory-at-a-time list with touch-sized
 * rows, in place of the desktop's indented tree. Same data (/tree), same
 * selection contract, so the treemap and detail sheet stay in sync.
 */
export function BrowseList({
  share,
  basis,
  generation,
  path,
  onNavigate,
  sort,
  desc,
  onSortChange,
  selected,
  selectedPaths,
  selectMode,
  onSelectModeChange,
  onSelect,
  onOpenDetail,
  onMenu,
}: Props) {
  // "Show more" grows the page rather than appending, so useAsyncData's
  // key-based cache stays the single source of truth; the server caps at
  // 5000. Limits are kept per folder, so navigating away and back needs no
  // reset — every other folder simply starts at its own default.
  const [limits, setLimits] = useState<Record<string, number>>({});
  const limit = limits[path] ?? PAGE;

  const listKey = `${share.id}|${generation ?? ''}|${path}|${basis}|${sort}|${desc ? 'desc' : 'asc'}`;
  const load = useCallback(
    () => api.tree(share.id, path, { basis, sort, order: desc ? 'desc' : 'asc', limit }),
    [share.id, path, basis, sort, desc, limit],
  );
  // The holdKey keeps the current page on screen while a bigger one loads,
  // so "Show more" neither blanks the list nor resets the scroll; any other
  // change (folder, sort, basis) drops it as usual.
  const { data, error, loading } = useAsyncData(
    `${listKey}|${limit}`,
    load,
    Boolean(generation),
    listKey,
  );

  if (!share.generation) {
    return (
      <div className="empty">
        <h2>No results yet</h2>
        <p>
          This share has not been scanned. Choose <strong>Scan now</strong> to build the tree.
        </p>
      </div>
    );
  }

  const dir = data?.node ?? null;
  const parentSize = dir ? (basis === 'allocated' ? dir.alloc : dir.size) : 0;
  const crumbs = ['', ...path.split('/').filter(Boolean).map((_, i, parts) => parts.slice(0, i + 1).join('/'))];

  return (
    <section className="browse-pane" aria-label={`${share.name} folders`}>
      <div className="browse-head">
        <nav className="crumbs" aria-label="Folder location">
          {crumbs.map((c, i) => {
            const label = c === '' ? share.name : (c.split('/').pop() ?? c);
            return (
              <span key={c || 'root'}>
                {i > 0 && (
                  <span className="crumb-sep" aria-hidden="true">
                    /
                  </span>
                )}
                {i === crumbs.length - 1 ? (
                  <span className="crumb current">{label}</span>
                ) : (
                  <button type="button" className="crumb" onClick={() => onNavigate(c)}>
                    {label}
                  </button>
                )}
              </span>
            );
          })}
        </nav>
        <span className="spacer" />
        <label className="field">
          <span className="sr-only">Sort by</span>
          <select
            value={sort}
            aria-label="Sort by"
            onChange={(e) => {
              const s = e.target.value as SortField;
              onSortChange(s, s !== 'name');
            }}
          >
            <option value="size">Largest first</option>
            <option value="name">By name</option>
            <option value="mtime">Newest first</option>
            <option value="files">Most files</option>
            <option value="duration">Longest media</option>
            <option value="spm">Largest per minute</option>
          </select>
        </label>
        <button
          type="button"
          aria-pressed={selectMode}
          onClick={() => onSelectModeChange(!selectMode)}
        >
          {selectMode ? 'Done' : 'Select'}
        </button>
      </div>

      {error && (
        <div className="banner error" role="alert">
          Could not load this folder: {error}
        </div>
      )}

      {dir && (
        <div className="browse-here">
          <span className="browse-here-facts">
            <strong>{formatBytes(parentSize)}</strong>
            <span className="muted small">
              {formatCount(dir.files)} files · {formatCount(dir.dirs)} folders
            </span>
          </span>
          <button
            type="button"
            onClick={() => {
              onSelect(dir, 'replace');
              onOpenDetail(dir);
            }}
          >
            This folder…
          </button>
        </div>
      )}

      <div className="pane-scroll">
        {loading && !data && <p className="muted pane-note">Loading…</p>}
        {data && data.children.length === 0 && <p className="muted pane-note">This folder is empty.</p>}
        {data && data.children.length > 0 && (
          <ul className="browse-list">
            {data.children.map((n) => (
              <BrowseRow
                key={n.path}
                node={n}
                basis={basis}
                parentSize={parentSize}
                focused={selected?.path === n.path}
                checked={selectedPaths.has(n.path)}
                selectMode={selectMode}
                onNavigate={onNavigate}
                onSelect={onSelect}
                onOpenDetail={onOpenDetail}
                onMenu={onMenu}
              />
            ))}
          </ul>
        )}
        {data && data.total > data.children.length && (
          <p className="muted pane-note">
            Showing the largest {formatCount(data.children.length)} of {formatCount(data.total)}{' '}
            entries.{' '}
            <button
              type="button"
              className="link"
              disabled={loading}
              onClick={() => setLimits((prev) => ({ ...prev, [path]: limit + PAGE }))}
            >
              Show more
            </button>
          </p>
        )}
      </div>
    </section>
  );
}

interface RowProps {
  node: Node;
  basis: Basis;
  parentSize: number;
  focused: boolean;
  checked: boolean;
  selectMode: boolean;
  onNavigate: (path: string) => void;
  onSelect: (node: Node, mode: 'replace' | 'toggle') => void;
  onOpenDetail: (node: Node) => void;
  onMenu: (node: Node, x: number, y: number) => void;
}

function BrowseRow({
  node,
  basis,
  parentSize,
  focused,
  checked,
  selectMode,
  onNavigate,
  onSelect,
  onOpenDetail,
  onMenu,
}: RowProps) {
  const isDir = node.kind === 'dir';
  const size = basis === 'allocated' ? node.alloc : node.size;
  const pct = parentSize > 0 ? size / parentSize : node.pct_of_parent;
  const longPress = useLongPress((x, y) => {
    onSelect(node, 'replace');
    onMenu(node, x, y);
  });

  const activate = () => {
    if (selectMode) {
      onSelect(node, 'toggle');
    } else if (isDir) {
      onSelect(node, 'replace');
      onNavigate(node.path);
    } else {
      onSelect(node, 'replace');
      onOpenDetail(node);
    }
  };

  return (
    <li className={`browse-item${focused ? ' focused' : ''}${checked ? ' selected' : ''}`}>
      <button
        type="button"
        className="browse-main"
        aria-pressed={selectMode ? checked : undefined}
        onClick={activate}
        onContextMenu={(e) => {
          e.preventDefault();
          longPress.cancel();
          if (!longPress.firedRecently()) {
            onSelect(node, 'replace');
            onMenu(node, e.clientX, e.clientY);
          }
        }}
        {...longPress.handlers}
      >
        {selectMode && (
          <span className={`row-check${checked ? ' on' : ''}`} aria-hidden="true">
            {checked && <IconCheck />}
          </span>
        )}
        {isDir ? <IconFolder className="browse-kind" /> : <IconFile className="browse-kind" />}
        <span className="browse-name">
          <span className={`name kind-${node.kind}`}>
            {node.name}
            <NodeTags node={node} />
          </span>
          <span className="muted small">
            {relativeTime(node.mtime)}
            {isDir && ` · ${formatCount(node.files)} files`}
            {node.duration ? ` · ${formatPlaytime(node.duration)}` : ''}
          </span>
        </span>
        <span className="browse-size num">
          <strong>{formatBytes(size)}</strong>
          <span className="bar" aria-hidden="true">
            <span style={{ width: `${Math.min(pct * 100, 100).toFixed(1)}%` }} />
          </span>
        </span>
        {isDir && !selectMode && <IconChevron className="browse-go" />}
      </button>
      {!selectMode && (
        <button
          type="button"
          className="icon-button browse-menu"
          aria-label={`Actions for ${node.name}`}
          onClick={(e) => {
            e.stopPropagation();
            onSelect(node, 'replace');
            const r = e.currentTarget.getBoundingClientRect();
            onMenu(node, r.left, r.bottom + 2);
          }}
        >
          <IconKebab />
        </button>
      )}
    </li>
  );
}
