import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import {
  api,
  type Basis,
  type DeleteResult,
  type Node,
  type ScanStatus,
  type ShareInfo,
  type VersionInfo,
} from './api';
import { useEvents } from './useEvents';
import { buildHash, parseHash, sameState, type UrlState } from './urlState';
import { formatBytes } from './format';
import type { ColorScheme } from './treemap/colors';
import { ShareBar } from './components/ShareBar';
import { COLUMNS, Tree, type SortField } from './components/Tree';
import { Treemap } from './components/Treemap';
import { Extensions } from './components/Extensions';
import { DetailBar } from './components/DetailBar';
import { ScanDrawer } from './components/ScanDrawer';
import { Splitter } from './components/Splitter';
import { ResultsView, type ResultsMode } from './components/ResultsView';
import { DeleteDialog } from './components/DeleteDialog';
import { TrashView } from './components/TrashView';

const DEFAULT_COLUMNS = ['size', 'pct', 'files', 'dirs', 'mtime', 'owner'];
const OPTIONAL_COLUMNS = COLUMNS.filter((c) => c.key !== 'name');

function readStored<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key);
    return raw === null ? fallback : (JSON.parse(raw) as T);
  } catch {
    return fallback;
  }
}

function store(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    /* private browsing or blocked storage: layout preferences are optional */
  }
}

export default function App() {
  const [shares, setShares] = useState<ShareInfo[] | null>(null);
  const [version, setVersion] = useState<VersionInfo | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // --- view state, mirrored into the URL ---------------------------------
  const [view, setView] = useState<UrlState>(() => parseHash(window.location.hash));
  const [selected, setSelected] = useState<Node | null>(null);
  const [selection, setSelection] = useState<Node[]>([]);
  const [expanded, setExpanded] = useState<Set<string>>(() => new Set(['']));
  const [results, setResults] = useState<ResultsMode | null>(null);
  const [drawer, setDrawer] = useState(false);
  const [trashOpen, setTrashOpen] = useState(false);
  const [highlightExt, setHighlightExt] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<Node[] | null>(null);
  const [notice, setNotice] = useState<string | null>(null);

  // --- preferences -------------------------------------------------------
  const [basis, setBasis] = useState<Basis>(() => readStored<Basis>('sds.basis', 'apparent'));
  const [columns, setColumns] = useState<string[]>(() => readStored('sds.columns', DEFAULT_COLUMNS));
  const [scheme, setScheme] = useState<ColorScheme>(() => readStored<ColorScheme>('sds.scheme', 'extension'));
  const [cushion, setCushion] = useState<boolean>(() => readStored('sds.cushion', true));
  const [sort, setSort] = useState<SortField>(() => readStored<SortField>('sds.sort', 'size'));
  const [desc, setDesc] = useState<boolean>(() => readStored('sds.desc', true));
  const [topHeight, setTopHeight] = useState<number>(() => readStored('sds.topHeight', 340));
  const [leftWidth, setLeftWidth] = useState<number>(() => readStored('sds.leftWidth', 720));

  const refreshTimer = useRef<number | undefined>(undefined);

  const refresh = useCallback(async () => {
    try {
      const res = await api.shares();
      setShares(res.shares);
      setError(null);
    } catch (e) {
      setError((e as Error).message);
    }
  }, []);

  // Initial load, inline so the effect can abandon a late response.
  useEffect(() => {
    let cancelled = false;
    void (async () => {
      const [sharesRes, versionRes] = await Promise.allSettled([api.shares(), api.version()]);
      if (cancelled) return;
      if (sharesRes.status === 'fulfilled') {
        const list = sharesRes.value.shares;
        setShares(list);
        setView((v) => (list.some((s) => s.id === v.share) ? v : { ...v, share: list[0]?.id ?? '' }));
      } else {
        setError((sharesRes.reason as Error).message);
      }
      if (versionRes.status === 'fulfilled') setVersion(versionRes.value);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  const current = useMemo(
    () => shares?.find((s) => s.id === view.share) ?? shares?.[0] ?? null,
    [shares, view.share],
  );

  // --- URL <-> state ------------------------------------------------------

  useEffect(() => {
    const onHash = () => {
      const next = parseHash(window.location.hash);
      setView((prev) => (sameState(prev, next) ? prev : next));
    };
    window.addEventListener('hashchange', onHash);
    return () => window.removeEventListener('hashchange', onHash);
  }, []);

  // Mirror the view into the address bar. Comparing against the current hash
  // is what keeps this from fighting the listener above: a state change that
  // came from the URL already matches, so nothing is written back.
  useEffect(() => {
    if (!view.share) return;
    const hash = buildHash(view);
    if (window.location.hash !== hash) window.history.replaceState(null, '', hash);
  }, [view]);

  // Resolve the selected path from the URL into a real node once results exist.
  useEffect(() => {
    if (!current?.generation || !view.path || selected?.path === view.path) return;
    let cancelled = false;
    void (async () => {
      try {
        const res = await api.node(current.id, view.path, basis);
        if (!cancelled) {
          setSelected(res.node);
          setSelection([res.node]);
        }
      } catch {
        // The path may not exist in these results; leave the selection alone.
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [current?.id, current?.generation, view.path, basis, selected?.path]);

  // --- live events --------------------------------------------------------

  useEvents(
    useCallback(
      (name, data) => {
        if (name === 'scan.progress') {
          const p = data as { share_id: string; scan_id: string; paused?: boolean };
          setShares(
            (prev) =>
              prev?.map((s) =>
                s.id === p.share_id
                  ? {
                      ...s,
                      scan: {
                        ...(s.scan ?? {
                          id: p.scan_id,
                          share_id: p.share_id,
                          trigger: '',
                          path: '',
                          started_at: '',
                          queued: false,
                          paused: false,
                        }),
                        id: p.scan_id,
                        queued: false,
                        paused: p.paused ?? false,
                        progress: data as unknown as ScanStatus['progress'],
                      },
                    }
                  : s,
              ) ?? prev,
          );
          return;
        }
        window.clearTimeout(refreshTimer.current);
        refreshTimer.current = window.setTimeout(() => void refresh(), 120);
      },
      [refresh],
    ),
  );

  useEffect(() => () => window.clearTimeout(refreshTimer.current), []);

  // --- actions ------------------------------------------------------------

  const startScan = useCallback(
    async (path = '') => {
      if (!current) return;
      setBusy(true);
      try {
        await api.startScan(current.id, path);
        await refresh();
      } catch (e) {
        setError((e as Error).message);
      } finally {
        setBusy(false);
      }
    },
    [current, refresh],
  );

  const cancelScan = useCallback(async () => {
    if (!current?.scan) return;
    try {
      await api.cancelScan(current.id, current.scan.id);
    } catch (e) {
      setError((e as Error).message);
    }
  }, [current]);

  const pauseToggle = useCallback(async () => {
    if (!current?.scan) return;
    try {
      if (current.scan.paused) await api.resumeScan(current.id, current.scan.id);
      else await api.pauseScan(current.id, current.scan.id);
      await refresh();
    } catch (e) {
      setError((e as Error).message);
    }
  }, [current, refresh]);

  /** Selection is shared by the tree, the treemap and the results list. */
  const selectNode = useCallback(
    (node: Node, mode: 'replace' | 'toggle' | 'range' = 'replace') => {
      setSelected(node);
      setView((v) => ({ ...v, path: node.path }));
      setSelection((prev) => {
        if (mode === 'toggle') {
          return prev.some((n) => n.path === node.path)
            ? prev.filter((n) => n.path !== node.path)
            : [...prev, node];
        }
        if (mode === 'range' && prev.length > 0) {
          // A true range needs the visible row order, which lives in the
          // tree; adding to the set is the useful approximation here.
          return prev.some((n) => n.path === node.path) ? prev : [...prev, node];
        }
        return [node];
      });
    },
    [],
  );

  const zoom = useCallback((path: string) => {
    setView((v) => ({ ...v, root: path }));
  }, []);

  /** Expands the tree down to a node and selects it. */
  const reveal = useCallback(
    (node: Node) => {
      const parts = node.path.split('/').filter(Boolean);
      const next = new Set(expanded);
      next.add('');
      for (let i = 0; i < parts.length - 1; i++) {
        next.add(parts.slice(0, i + 1).join('/'));
      }
      setExpanded(next);
      setResults(null);
      selectNode(node, 'replace');
    },
    [expanded, selectNode],
  );

  /** After a delete the selection is stale and the results have moved on. */
  const afterDelete = useCallback(
    (result: DeleteResult) => {
      const gone = new Set(
        result.results.filter((r) => r.outcome === 'deleted').map((r) => r.path),
      );
      setSelection((prev) => prev.filter((n) => !gone.has(n.path)));
      setSelected((prev) => (prev && gone.has(prev.path) ? null : prev));
      setView((v) => (gone.has(v.path) ? { ...v, path: '' } : v));
      setNotice(
        `Reclaimed ${formatBytes(result.freed_total)} from ${result.results.length} ${
          result.results.length === 1 ? 'item' : 'items'
        }.`,
      );
      void refresh();
    },
    [refresh],
  );

  /**
   * Switching shares must clear everything scoped to the old one. Resetting
   * only the view leaves `selected`/`selection` holding nodes from the
   * previous share while `current` is the new one, so the detail bar, the ZIP
   * link and the delete dialog all address the *new* share with the *old*
   * paths -- and if both shares happen to contain that path, the preview looks
   * entirely plausible and deletes the wrong file.
   */
  const selectShare = useCallback((id: string) => {
    setView({ share: id, path: '', root: '' });
    setSelected(null);
    setSelection([]);
    setExpanded(new Set(['']));
    setResults(null);
    setHighlightExt(null);
    setDeleting(null);
  }, []);

  const setPref = useCallback(<T,>(key: string, value: T, apply: (v: T) => void) => {
    apply(value);
    store(key, value);
  }, []);

  const toggleColumn = useCallback(
    (name: string) => {
      setColumns((prev) => {
        const next = prev.includes(name) ? prev.filter((c) => c !== name) : [...prev, name];
        store('sds.columns', next);
        return next;
      });
    },
    [],
  );

  // Global shortcuts: '/' opens search, Escape leaves it (FR-UI-07).
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      const target = e.target as HTMLElement | null;
      const typing =
        target && (target.tagName === 'INPUT' || target.tagName === 'SELECT' || target.isContentEditable);
      if (e.key === '/' && !typing) {
        e.preventDefault();
        setResults({ kind: 'search', query: '' });
      } else if (e.key === 'Delete' && !typing && selection.length > 0 && !current?.delete_blocked) {
        e.preventDefault();
        setDeleting(selection);
      } else if (e.key === 'Escape' && results) {
        setResults(null);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [results, selection, current?.delete_blocked]);

  // --- render -------------------------------------------------------------

  if (shares === null && !error) {
    return <p className="loading muted">Loading shares…</p>;
  }

  return (
    <div className="app">
      <ShareBar
        shares={shares ?? []}
        current={current}
        scan={current?.scan ?? null}
        busy={busy}
        onSelect={selectShare}
        onScan={() => void startScan('')}
        onCancel={() => void cancelScan()}
        onPauseToggle={() => void pauseToggle()}
      />

      {error && (
        <div className="banner error" role="alert">
          {error}
          <button type="button" onClick={() => void refresh()}>
            Retry
          </button>
        </div>
      )}

      {notice && (
        <div className="banner" role="status">
          {notice}
          <span className="spacer" />
          <button type="button" onClick={() => setNotice(null)}>
            Dismiss
          </button>
        </div>
      )}

      {shares && shares.length === 0 && (
        <div className="empty">
          <h2>No shares configured</h2>
          <p>
            Mount directories under <code>/shares</code>, or add a <code>shares:</code> list to the
            configuration file, then restart.
          </p>
        </div>
      )}

      {current && (
        <>
          <div className="toolbar">
            <fieldset className="segmented">
              <legend className="sr-only">Size basis</legend>
              {(['apparent', 'allocated'] as const).map((b) => (
                <label key={b} className={basis === b ? 'on' : ''}>
                  <input
                    type="radio"
                    name="basis"
                    checked={basis === b}
                    onChange={() => setPref('sds.basis', b, setBasis)}
                  />
                  {b === 'apparent' ? 'Apparent size' : 'On disk'}
                </label>
              ))}
            </fieldset>

            <label className="field">
              <span className="sr-only">Colour by</span>
              <select
                value={scheme}
                onChange={(e) => setPref('sds.scheme', e.target.value as ColorScheme, setScheme)}
              >
                <option value="extension">Colour by type</option>
                <option value="depth">Colour by depth</option>
                <option value="mtime">Colour by age</option>
              </select>
            </label>

            <label className="check">
              <input
                type="checkbox"
                checked={cushion}
                onChange={(e) => setPref('sds.cushion', e.target.checked, setCushion)}
              />
              Cushions
            </label>

            <details className="menu">
              <summary>Columns</summary>
              <div className="menu-body">
                {OPTIONAL_COLUMNS.map((c) => (
                  <label key={c.key}>
                    <input
                      type="checkbox"
                      checked={columns.includes(c.key)}
                      onChange={() => toggleColumn(c.key)}
                    />
                    {c.label}
                  </label>
                ))}
              </div>
            </details>

            <span className="spacer" />

            <button type="button" onClick={() => setResults({ kind: 'search', query: '' })}>
              Search <kbd>/</kbd>
            </button>
            <button type="button" onClick={() => setResults({ kind: 'largest', path: view.root })}>
              Largest files
            </button>
            <button type="button" onClick={() => setDrawer((d) => !d)}>
              Scan history
              {current.stats && current.stats.errors > 0 ? ` · ${current.stats.errors} errors` : ''}
            </button>
            {current.trash_enabled && (
              <button type="button" onClick={() => setTrashOpen((t) => !t)}>
                Trash
              </button>
            )}
          </div>

          <div className="panes">
            <div className="panes-top" style={{ height: topHeight }}>
              <div className="pane-left" style={{ width: leftWidth }}>
                {results ? (
                  <ResultsView
                    key={`${results.kind}|${
                      results.kind === 'search' ? `${results.query}|${results.ext ?? ''}` : results.path
                    }`}
                    shareId={current.id}
                    generation={current.generation}
                    basis={basis}
                    mode={results}
                    scope={view.root}
                    selected={selected}
                    onSelect={(n) => selectNode(n, 'replace')}
                    onReveal={reveal}
                    onClose={() => setResults(null)}
                  />
                ) : (
                  <Tree
                    share={current}
                    basis={basis}
                    generation={current.generation}
                    columns={columns}
                    sort={sort}
                    desc={desc}
                    onSortChange={(s, d) => {
                      setPref('sds.sort', s, setSort);
                      setPref('sds.desc', d, setDesc);
                    }}
                    selected={selected}
                    selectedPaths={new Set(selection.map((n) => n.path))}
                    onSelect={selectNode}
                    onActivate={(n) => (n.kind === 'dir' ? zoom(n.path) : selectNode(n))}
                    expanded={expanded}
                    onExpandedChange={setExpanded}
                  />
                )}
              </div>

              <Splitter
                orientation="vertical"
                value={leftWidth}
                min={320}
                max={1400}
                onChange={(v) => setPref('sds.leftWidth', v, setLeftWidth)}
                label="Resize the tree pane"
              />

              <div className="pane-right">
                <Extensions
                  shareId={current.id}
                  generation={current.generation}
                  basis={basis}
                  scheme={scheme}
                  root={view.root}
                  selected={highlightExt}
                  onSelect={setHighlightExt}
                  onShowFiles={(ext) => setResults({ kind: 'search', query: '', ext })}
                />
              </div>
            </div>

            <Splitter
              orientation="horizontal"
              value={topHeight}
              min={140}
              max={900}
              onChange={(v) => setPref('sds.topHeight', v, setTopHeight)}
              label="Resize the treemap"
            />

            <div className="panes-bottom">
              <Treemap
                shareId={current.id}
                generation={current.generation}
                basis={basis}
                scheme={scheme}
                cushion={cushion}
                root={view.root}
                selectedPath={selected?.path ?? null}
                highlightExt={highlightExt}
                onZoom={zoom}
                onSelect={(n) => selectNode(n, 'replace')}
              />
            </div>
          </div>

          <DetailBar
            share={current}
            node={selected}
            selection={selection}
            basis={basis}
            busy={busy}
            onRescan={(path) => void startScan(path)}
            onZoom={zoom}
            onLargestHere={(path) => setResults({ kind: 'largest', path })}
            onDelete={(nodes) => setDeleting(nodes)}
          />

          {deleting && deleting.length > 0 && (
            <DeleteDialog
              share={current}
              nodes={deleting}
              onDone={afterDelete}
              onClose={() => setDeleting(null)}
            />
          )}

          {drawer && (
            <ScanDrawer
              shareId={current.id}
              generation={current.generation}
              onClose={() => setDrawer(false)}
            />
          )}

          {trashOpen && (
            <TrashView
              share={current}
              onClose={() => setTrashOpen(false)}
              onChanged={() => void refresh()}
            />
          )}
        </>
      )}

      {version && (
        <div
          className="version-tag muted"
          title={`${version.commit} · ${version.go} ${version.os}/${version.arch}`}
        >
          v{version.version}
        </div>
      )}
    </div>
  );
}
