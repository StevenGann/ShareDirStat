import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, type Basis, type Node, type ShareInfo } from '../api';
import { absoluteTime, formatBytes, formatCount, formatPercent, relativeTime } from '../format';

/** Row height in CSS pixels. Fixed, so the window can be computed by division. */
const ROW_HEIGHT = 22;
/** Rows rendered above and below the viewport to cover fast scrolling. */
const OVERSCAN = 8;
/** Children fetched per directory; the server caps this at 5000. */
const PAGE = 500;

export type SortField = 'size' | 'name' | 'mtime' | 'files';

export interface ColumnDef {
  key: string;
  label: string;
  width: string;
  numeric?: boolean;
  sort?: SortField;
}

/** Every column the tree can show, in display order (FR-UI-05). */
export const COLUMNS: ColumnDef[] = [
  { key: 'name', label: 'Name', width: 'minmax(240px, 3fr)', sort: 'name' },
  { key: 'size', label: 'Size', width: '92px', numeric: true, sort: 'size' },
  { key: 'pct', label: '%', width: '104px', numeric: true },
  { key: 'alloc', label: 'On disk', width: '92px', numeric: true },
  { key: 'files', label: 'Files', width: '78px', numeric: true, sort: 'files' },
  { key: 'dirs', label: 'Folders', width: '78px', numeric: true },
  { key: 'items', label: 'Items', width: '78px', numeric: true },
  { key: 'mtime', label: 'Modified', width: '124px', sort: 'mtime' },
  { key: 'owner', label: 'Owner', width: '132px' },
  { key: 'perms', label: 'Permissions', width: '104px' },
  { key: 'ext', label: 'Type', width: '76px' },
];

interface Loaded {
  children: Node[];
  total: number;
  error?: string;
}

/** The cache is tagged with the query it belongs to, so a change of share,
 *  generation, basis or sort order invalidates it by comparison rather than
 *  by clearing state from an effect. */
interface Cache {
  key: string;
  root: Node | null;
  entries: Record<string, Loaded>;
}

const EMPTY_ENTRIES: Record<string, Loaded> = {};

interface Row {
  node: Node;
  depth: number;
  parentSize: number;
}

interface Props {
  share: ShareInfo;
  basis: Basis;
  generation: string | null;
  columns: string[];
  sort: SortField;
  desc: boolean;
  onSortChange: (sort: SortField, desc: boolean) => void;
  selected: Node | null;
  selectedPaths: Set<string>;
  onSelect: (node: Node, mode: 'replace' | 'toggle' | 'range') => void;
  onActivate: (node: Node) => void;
  expanded: Set<string>;
  onExpandedChange: (next: Set<string>) => void;
}

export function Tree({
  share,
  basis,
  generation,
  columns,
  sort,
  desc,
  onSortChange,
  selected,
  selectedPaths,
  onSelect,
  onActivate,
  expanded,
  onExpandedChange,
}: Props) {
  const [cache, setCache] = useState<Cache>({ key: '', root: null, entries: {} });
  const [scrollTop, setScrollTop] = useState(0);
  const [viewport, setViewport] = useState(600);
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const inFlight = useRef<Set<string>>(new Set());

  const visible = useMemo(
    () => COLUMNS.filter((c) => c.key === 'name' || columns.includes(c.key)),
    [columns],
  );
  const template = useMemo(() => visible.map((c) => c.width).join(' '), [visible]);

  const cacheKey = `${share.id}|${generation ?? ''}|${basis}|${sort}|${desc ? 'desc' : 'asc'}`;
  const fresh = cache.key === cacheKey;
  const entries = fresh ? cache.entries : EMPTY_ENTRIES;
  const root = fresh ? cache.root : null;
  const error = entries['']?.error ?? null;

  // Every state write happens after the await, so nothing is set
  // synchronously while an effect runs.
  const load = useCallback(
    async (path: string) => {
      const merge = (entry: Loaded, node?: Node) =>
        setCache((prev) => {
          const base = prev.key === cacheKey ? prev.entries : {};
          return {
            key: cacheKey,
            root: path === '' ? (node ?? null) : prev.key === cacheKey ? prev.root : null,
            entries: { ...base, [path]: entry },
          };
        });
      try {
        const res = await api.tree(share.id, path, {
          basis,
          sort,
          order: desc ? 'desc' : 'asc',
          limit: PAGE,
        });
        merge({ children: res.children, total: res.total }, res.node);
      } catch (e) {
        merge({ children: [], total: 0, error: (e as Error).message });
      }
    },
    [share.id, basis, sort, desc, cacheKey],
  );

  // Fetch the root listing and any expanded directory that is not cached yet.
  // This also covers paths expanded programmatically by "Show in tree".
  useEffect(() => {
    if (!share.generation) return;
    const wanted = new Set<string>(['']);
    for (const path of expanded) wanted.add(path);
    for (const path of wanted) {
      const token = `${cacheKey}|${path}`;
      if (entries[path] || inFlight.current.has(token)) continue;
      inFlight.current.add(token);
      void load(path).finally(() => inFlight.current.delete(token));
    }
  }, [share.generation, expanded, entries, cacheKey, load]);

  const toggle = useCallback(
    (node: Node) => {
      const next = new Set(expanded);
      if (next.has(node.path)) next.delete(node.path);
      else next.add(node.path);
      onExpandedChange(next);
    },
    [expanded, onExpandedChange],
  );

  const rows = useMemo(() => {
    const out: Row[] = [];
    const sizeOf = (n: Node) => (basis === 'allocated' ? n.alloc : n.size);
    const walk = (path: string, depth: number, parentSize: number) => {
      const entry = entries[path];
      if (!entry) return;
      for (const child of entry.children) {
        out.push({ node: child, depth, parentSize });
        if (child.kind === 'dir' && expanded.has(child.path)) {
          walk(child.path, depth + 1, sizeOf(child));
        }
      }
    };
    walk('', 0, root ? sizeOf(root) : 0);
    return out;
  }, [entries, expanded, root, basis]);

  // --- keyboard navigation (FR-UI-07) ------------------------------------

  const index = useMemo(() => rows.findIndex((r) => r.node.path === selected?.path), [rows, selected]);

  const move = useCallback(
    (delta: number) => {
      if (rows.length === 0) return;
      const next = Math.min(Math.max(index + delta, 0), rows.length - 1);
      const row = rows[next];
      if (!row) return;
      onSelect(row.node, 'replace');
      const top = next * ROW_HEIGHT;
      const el = scrollRef.current;
      if (!el) return;
      if (top < el.scrollTop) el.scrollTop = top;
      else if (top + ROW_HEIGHT > el.scrollTop + el.clientHeight) {
        el.scrollTop = top + ROW_HEIGHT - el.clientHeight;
      }
    },
    [rows, index, onSelect],
  );

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      const current = index >= 0 ? rows[index]?.node : undefined;
      switch (e.key) {
        case 'ArrowDown':
          e.preventDefault();
          move(1);
          break;
        case 'ArrowUp':
          e.preventDefault();
          move(-1);
          break;
        case 'PageDown':
          e.preventDefault();
          move(Math.floor(viewport / ROW_HEIGHT));
          break;
        case 'PageUp':
          e.preventDefault();
          move(-Math.floor(viewport / ROW_HEIGHT));
          break;
        case 'Home':
          e.preventDefault();
          move(-rows.length);
          break;
        case 'End':
          e.preventDefault();
          move(rows.length);
          break;
        case 'ArrowRight':
          if (current?.kind === 'dir' && !expanded.has(current.path)) {
            e.preventDefault();
            toggle(current);
          } else if (current?.kind === 'dir') {
            e.preventDefault();
            move(1);
          }
          break;
        case 'ArrowLeft':
          if (current?.kind === 'dir' && expanded.has(current.path)) {
            e.preventDefault();
            toggle(current);
          }
          break;
        case 'Enter':
          if (current) {
            e.preventDefault();
            onActivate(current);
          }
          break;
        default:
      }
    },
    [index, rows, move, expanded, toggle, onActivate, viewport],
  );

  // --- windowing ----------------------------------------------------------

  useEffect(() => {
    const el = scrollRef.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const h = entries[0]?.contentRect.height;
      if (h) setViewport(h);
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  const first = Math.max(Math.floor(scrollTop / ROW_HEIGHT) - OVERSCAN, 0);
  const last = Math.min(first + Math.ceil(viewport / ROW_HEIGHT) + OVERSCAN * 2, rows.length);
  const window = rows.slice(first, last);

  const truncations = useMemo(
    () => Object.entries(entries).filter(([, e]) => e.total > e.children.length),
    [entries],
  );

  if (error) {
    return (
      <div className="banner error" role="alert">
        Could not load the tree: {error}
      </div>
    );
  }
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

  const sizeOf = (n: Node) => (basis === 'allocated' ? n.alloc : n.size);

  return (
    <section className="tree-pane" aria-label={`${share.name} directory tree`}>
      <div className="tree-header" style={{ gridTemplateColumns: template }} role="row">
        {visible.map((c) => (
          <div
            key={c.key}
            className={`th${c.numeric ? ' num' : ''}${c.sort ? ' sortable' : ''}`}
            role="columnheader"
            aria-sort={c.sort === sort ? (desc ? 'descending' : 'ascending') : undefined}
            onClick={() => c.sort && onSortChange(c.sort, c.sort === sort ? !desc : true)}
            tabIndex={c.sort ? 0 : -1}
            onKeyDown={(e) => {
              if (c.sort && (e.key === 'Enter' || e.key === ' ')) {
                e.preventDefault();
                onSortChange(c.sort, c.sort === sort ? !desc : true);
              }
            }}
          >
            {c.label}
            {c.sort === sort && <span className="sort-arrow" aria-hidden="true">{desc ? '▾' : '▴'}</span>}
          </div>
        ))}
      </div>

      <div
        className="tree-scroll"
        ref={scrollRef}
        onScroll={(e) => setScrollTop(e.currentTarget.scrollTop)}
        role="treegrid"
        aria-rowcount={rows.length}
        tabIndex={0}
        onKeyDown={onKeyDown}
      >
        <div style={{ height: rows.length * ROW_HEIGHT, position: 'relative' }}>
          {window.map((row, i) => {
            const rowIndex = first + i;
            return (
              <TreeRow
                key={row.node.path}
                row={row}
                top={rowIndex * ROW_HEIGHT}
                rowIndex={rowIndex}
                template={template}
                columns={visible}
                basis={basis}
                expanded={expanded.has(row.node.path)}
                loading={expanded.has(row.node.path) && !entries[row.node.path]}
                selected={selectedPaths.has(row.node.path)}
                focused={selected?.path === row.node.path}
                sizeOf={sizeOf}
                onToggle={() => toggle(row.node)}
                onSelect={(mode) => onSelect(row.node, mode)}
                onActivate={() => onActivate(row.node)}
              />
            );
          })}
        </div>
      </div>

      {truncations.length > 0 && (
        <div className="tree-foot muted">
          {truncations.map(([path, e]) => (
            <div key={path}>
              {path === '' ? 'Share root' : path}: showing the largest {formatCount(e.children.length)} of{' '}
              {formatCount(e.total)} entries — use search to narrow.
            </div>
          ))}
        </div>
      )}
    </section>
  );
}

interface RowProps {
  row: Row;
  top: number;
  rowIndex: number;
  template: string;
  columns: ColumnDef[];
  basis: Basis;
  expanded: boolean;
  loading: boolean;
  selected: boolean;
  focused: boolean;
  sizeOf: (n: Node) => number;
  onToggle: () => void;
  onSelect: (mode: 'replace' | 'toggle' | 'range') => void;
  onActivate: () => void;
}

const KIND_MARK: Record<Node['kind'], string> = {
  dir: '',
  file: '·',
  symlink: '↗',
  other: '?',
  deleted: '×',
};

function TreeRow({
  row,
  top,
  rowIndex,
  template,
  columns,
  basis,
  expanded,
  loading,
  selected,
  focused,
  sizeOf,
  onToggle,
  onSelect,
  onActivate,
}: RowProps) {
  const { node, depth, parentSize } = row;
  const isDir = node.kind === 'dir';
  const size = sizeOf(node);
  const pct = parentSize > 0 ? size / parentSize : node.pct_of_parent;

  const cell = (key: string) => {
    switch (key) {
      case 'size':
        return formatBytes(size);
      case 'alloc':
        return formatBytes(basis === 'allocated' ? node.size : node.alloc);
      case 'files':
        return isDir ? formatCount(node.files) : '';
      case 'dirs':
        return isDir ? formatCount(node.dirs) : '';
      case 'items':
        return isDir ? formatCount(node.files + node.dirs) : '';
      case 'owner':
        return `${node.owner ?? node.uid}:${node.group ?? node.gid}`;
      case 'perms':
        return node.perms;
      case 'ext':
        return node.ext ? `.${node.ext}` : isDir ? '' : '—';
      default:
        return '';
    }
  };

  return (
    <div
      className={`tr${selected ? ' selected' : ''}${focused ? ' focused' : ''}`}
      style={{ top, gridTemplateColumns: template }}
      role="row"
      aria-rowindex={rowIndex + 1}
      aria-selected={selected}
      aria-expanded={isDir ? expanded : undefined}
      aria-level={depth + 1}
      onMouseDown={(e) => {
        if (e.shiftKey) onSelect('range');
        else if (e.ctrlKey || e.metaKey) onSelect('toggle');
        else onSelect('replace');
      }}
      onDoubleClick={() => (isDir ? onToggle() : onActivate())}
    >
      {columns.map((c) =>
        c.key === 'name' ? (
          <div className="td name-cell" role="gridcell" key="name">
            <span className="indent" style={{ paddingLeft: `${depth * 14}px` }}>
              {isDir ? (
                <button
                  type="button"
                  className={`chevron${expanded ? ' open' : ''}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    onToggle();
                  }}
                  tabIndex={-1}
                  aria-label={expanded ? `Collapse ${node.name}` : `Expand ${node.name}`}
                >
                  ▸
                </button>
              ) : (
                <span className="chevron-space" aria-hidden="true">
                  {KIND_MARK[node.kind]}
                </span>
              )}
              <span className={`name kind-${node.kind}`} title={node.path}>
                {node.name}
              </span>
              {node.flags.partial && (
                <span className="tag warn" title="Some entries below this folder could not be read">
                  partial
                </span>
              )}
              {node.flags.mountpoint && (
                <span className="tag" title="A separate filesystem; not scanned">
                  mount
                </span>
              )}
              {node.flags.hardlink_dup && (
                <span className="tag" title="Another name for a file already counted; adds no space">
                  hard link
                </span>
              )}
              {node.name_b64 && (
                <span className="tag warn" title="This name is not valid UTF-8; shown with replacements">
                  raw name
                </span>
              )}
              {loading && <span className="tag muted">loading…</span>}
            </span>
          </div>
        ) : c.key === 'pct' ? (
          <div className="td num" role="gridcell" key="pct">
            <span className="bar" aria-hidden="true">
              <span style={{ width: `${Math.min(pct * 100, 100).toFixed(1)}%` }} />
            </span>
            <span className="pct-text">{formatPercent(pct)}</span>
          </div>
        ) : c.key === 'mtime' ? (
          <div className="td" role="gridcell" key="mtime" title={absoluteTime(node.mtime)}>
            {relativeTime(node.mtime)}
          </div>
        ) : (
          <div
            className={`td${c.numeric ? ' num' : ''}${c.key === 'owner' || c.key === 'perms' ? ' mono' : ''}`}
            role="gridcell"
            key={c.key}
          >
            {cell(c.key)}
          </div>
        ),
      )}
    </div>
  );
}
