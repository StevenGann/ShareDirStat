import { useCallback, useEffect, useMemo, useState } from 'react';
import { api, type Basis, type Node } from '../api';
import { absoluteTime, formatBytes, formatCount, formatPercent, relativeTime } from '../format';
import { useAsyncData } from '../hooks';
import { IconKebab } from './icons';

/** What the results pane is showing. */
export type ResultsMode =
  | { kind: 'search'; query: string; ext?: string }
  | { kind: 'largest'; path: string };

interface Props {
  shareId: string;
  generation: string | null;
  basis: Basis;
  mode: ResultsMode;
  /** Scope for a search: the treemap's current root. */
  scope: string;
  selected: Node | null;
  onSelect: (node: Node) => void;
  onReveal: (node: Node) => void;
  /** Opens the action menu for a row (kebab button or right-click). */
  onMenu?: (node: Node, x: number, y: number) => void;
  onClose: () => void;
}

interface Filters {
  query: string;
  kind: '' | 'file' | 'dir';
  ext: string;
  minSize: string;
  scoped: boolean;
}

/** Parses "500MB", "1.5 GiB", "1024" into bytes; empty or invalid gives 0. */
export function parseSize(text: string): number {
  const m = /^\s*([\d.]+)\s*([kmgtp]?i?b?)\s*$/i.exec(text);
  if (!m) return 0;
  const value = Number(m[1]);
  if (!Number.isFinite(value)) return 0;
  const unit = (m[2] ?? '').toLowerCase();
  const binary = unit.includes('i');
  const base = binary ? 1024 : 1000;
  const exp = unit.startsWith('k') ? 1 : unit.startsWith('m') ? 2 : unit.startsWith('g') ? 3 : unit.startsWith('t') ? 4 : unit.startsWith('p') ? 5 : 0;
  return Math.round(value * base ** exp);
}

export function ResultsView({
  shareId,
  generation,
  basis,
  mode,
  scope,
  selected,
  onSelect,
  onReveal,
  onMenu,
  onClose,
}: Props) {
  // The caller remounts this component when `mode` changes (it passes a key
  // derived from the mode), so the form state is seeded once from props and
  // never has to be resynchronised from an effect.
  const initial: Filters = {
    query: mode.kind === 'search' ? mode.query : '',
    kind: '',
    ext: mode.kind === 'search' ? (mode.ext ?? '') : '',
    minSize: '',
    scoped: false,
  };
  const [filters, setFilters] = useState<Filters>(initial);
  // Typing should not fire a request per keystroke, so the filters used for
  // fetching lag the ones in the form by a short debounce.
  const [applied, setApplied] = useState<Filters>(initial);

  useEffect(() => {
    if (filters === applied) return;
    const t = window.setTimeout(() => setApplied(filters), 180);
    return () => window.clearTimeout(t);
  }, [filters, applied]);

  const key =
    mode.kind === 'largest'
      ? `top|${shareId}|${generation ?? ''}|${mode.path}|${basis}`
      : [
          'search', shareId, generation ?? '', basis, scope,
          applied.query, applied.kind, applied.ext, applied.minSize, String(applied.scoped),
        ].join('|');

  const load = useCallback(async () => {
    if (mode.kind === 'largest') {
      const res = await api.top(shareId, { path: mode.path, n: 200, kind: 'file', basis });
      return { items: res.items, total: res.items.length, truncated: false };
    }
    const res = await api.search(shareId, {
      q: applied.query,
      path: applied.scoped ? scope : '',
      kind: applied.kind || undefined,
      ext: applied.ext || undefined,
      min_size: parseSize(applied.minSize) || undefined,
      basis,
      limit: 500,
    });
    return { items: res.matches, total: res.total, truncated: res.truncated };
  }, [shareId, basis, mode, applied, scope]);

  const { data, error, loading } = useAsyncData(key, load, Boolean(generation));
  const items = data?.items ?? null;
  const meta = data ? { total: data.total, truncated: data.truncated } : null;
  const busy = loading || filters !== applied;

  const title = mode.kind === 'largest'
    ? `Largest files${mode.path ? ` in ${mode.path}` : ''}`
    : 'Search';

  const totalSize = useMemo(
    () => (items ?? []).reduce((s, n) => s + (basis === 'allocated' ? n.alloc : n.size), 0),
    [items, basis],
  );

  return (
    <section className="results-pane" aria-label={title}>
      <div className="pane-head">
        <h2>{title}</h2>
        <span className="spacer" />
        <button type="button" onClick={onClose}>
          Back to tree
        </button>
      </div>

      {mode.kind === 'search' && (
        <form className="search-form" onSubmit={(e) => { e.preventDefault(); setApplied(filters); }}>
          <input
            type="search"
            autoFocus
            placeholder="Name contains… (* and ? work)"
            value={filters.query}
            onChange={(e) => setFilters({ ...filters, query: e.target.value })}
            aria-label="Name"
          />
          <input
            type="text"
            placeholder="Extension"
            size={8}
            value={filters.ext}
            onChange={(e) => setFilters({ ...filters, ext: e.target.value.replace(/^\./, '') })}
            aria-label="Extension"
          />
          <input
            type="text"
            placeholder="Min size"
            size={8}
            value={filters.minSize}
            onChange={(e) => setFilters({ ...filters, minSize: e.target.value })}
            aria-label="Minimum size, for example 500MB"
          />
          <select
            value={filters.kind}
            onChange={(e) => setFilters({ ...filters, kind: e.target.value as Filters['kind'] })}
            aria-label="Kind"
          >
            <option value="">Anything</option>
            <option value="file">Files</option>
            <option value="dir">Folders</option>
          </select>
          <label className="check">
            <input
              type="checkbox"
              checked={filters.scoped}
              onChange={(e) => setFilters({ ...filters, scoped: e.target.checked })}
            />
            Only in {scope === '' ? 'the whole share' : scope}
          </label>
        </form>
      )}

      {error && (
        <div className="banner error" role="alert">
          {error}
        </div>
      )}

      {meta && (
        <p className="muted pane-note" role="status">
          {formatCount(meta.total)} {meta.total === 1 ? 'match' : 'matches'} ·{' '}
          {formatBytes(totalSize)} shown
          {meta.truncated && ` · showing the largest ${formatCount(items?.length ?? 0)}`}
          {busy && ' · searching…'}
        </p>
      )}

      <div className="pane-scroll">
        <table className="results">
          <thead>
            <tr>
              <th>Path</th>
              <th className="num">Size</th>
              <th className="num">% of share</th>
              <th>Modified</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {items?.length === 0 && (
              <tr>
                <td colSpan={5} className="muted">
                  Nothing matched.
                </td>
              </tr>
            )}
            {items?.map((n) => (
              <tr
                key={n.path}
                className={selected?.path === n.path ? 'selected' : undefined}
                onClick={() => onSelect(n)}
                onDoubleClick={() => onReveal(n)}
                tabIndex={0}
                onKeyDown={(e) => {
                  if (e.key === 'Enter') {
                    e.preventDefault();
                    onReveal(n);
                  }
                }}
                onContextMenu={
                  onMenu &&
                  ((e) => {
                    e.preventDefault();
                    onSelect(n);
                    onMenu(n, e.clientX, e.clientY);
                  })
                }
              >
                <td className="mono wrap">{n.path}</td>
                <td className="num">{formatBytes(basis === 'allocated' ? n.alloc : n.size)}</td>
                <td className="num">{formatPercent(n.pct_of_share)}</td>
                <td title={absoluteTime(n.mtime)}>{relativeTime(n.mtime)}</td>
                <td className="row-actions">
                  <button
                    type="button"
                    className="link"
                    onClick={(e) => {
                      e.stopPropagation();
                      onReveal(n);
                    }}
                  >
                    Show in tree
                  </button>
                  {onMenu && (
                    <button
                      type="button"
                      className="icon-button"
                      aria-label={`Actions for ${n.path}`}
                      onClick={(e) => {
                        e.stopPropagation();
                        onSelect(n);
                        const r = e.currentTarget.getBoundingClientRect();
                        onMenu(n, r.left, r.bottom + 2);
                      }}
                    >
                      <IconKebab />
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </section>
  );
}
