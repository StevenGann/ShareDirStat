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
import { type SortField } from './components/Tree';
import { DesktopShell } from './components/DesktopShell';
import { MobileShell } from './components/MobileShell';
import { type ShellProps } from './components/shell';
import { usePhoneLayout } from './hooks';
import { ScanDrawer } from './components/ScanDrawer';
import { type ResultsMode } from './components/ResultsView';
import { DeleteDialog } from './components/DeleteDialog';
import { TrashView } from './components/TrashView';
import { ActionMenu } from './components/ActionMenu';
import { buildNodeActions } from './actions';

const DEFAULT_COLUMNS = ['size', 'pct', 'files', 'dirs', 'mtime', 'owner'];

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
  const [menu, setMenu] = useState<{ nodes: Node[]; x: number; y: number } | null>(null);
  const [selectMode, setSelectMode] = useState(false);
  const phone = usePhoneLayout();

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
        setShares(sharesRes.value.shares);
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
  //
  // The share is resolved rather than taken from view.share verbatim: the
  // URL may name no share (bare fragment) or one that no longer exists, and
  // while the panes keep working off the same shares[0] fallback `current`
  // uses, an unresolved view.share used to make this effect bail — so
  // selections silently stopped reaching the URL.
  useEffect(() => {
    const share = shares?.some((s) => s.id === view.share) ? view.share : (shares?.[0]?.id ?? '');
    if (!share) return;
    const hash = buildHash({ ...view, share });
    if (window.location.hash !== hash) window.history.replaceState(null, '', hash);
  }, [view, shares]);

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

  /** Bulk add for "Select all shown": one state write, focus stays put. */
  const selectMany = useCallback((nodes: Node[]) => {
    if (nodes.length === 0) return;
    setSelection((prev) => {
      const have = new Set(prev.map((n) => n.path));
      const add = nodes.filter((n) => !have.has(n.path));
      return add.length > 0 ? [...prev, ...add] : prev;
    });
    setSelected((prev) => prev ?? nodes[0] ?? null);
  }, []);

  const zoom = useCallback((path: string) => {
    setView((v) => ({ ...v, root: path }));
  }, []);

  /**
   * Opens the action menu for a node. A node already in the multi-selection
   * targets the whole selection (matching the file managers people know);
   * anything else becomes the new single selection first.
   */
  const openMenuFor = useCallback(
    (node: Node, x: number, y: number) => {
      const inSelection = selection.some((n) => n.path === node.path);
      const nodes = inSelection && selection.length > 1 ? selection : [node];
      if (!inSelection) selectNode(node, 'replace');
      setMenu({ nodes, x, y });
    },
    [selection, selectNode],
  );

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

  /**
   * Reveals a path that only exists as a string, e.g. an unreadable path
   * from the scan-errors drawer. Resolved through the API because the tree
   * needs a Node to select; a path missing from the current results (it may
   * have been deleted since the scan) is reported rather than ignored.
   */
  const revealPath = useCallback(
    async (path: string) => {
      if (!current?.generation) return;
      const shareId = current.id;
      try {
        const res = await api.node(shareId, path, basis);
        setDrawer(false);
        reveal(res.node);
      } catch {
        setNotice(`${path} is not in the current results — it may have been removed since the scan.`);
      }
    },
    [current, basis, reveal],
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
    setMenu(null);
    setSelectMode(false);
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

  // Global shortcuts: '/' opens search, Escape leaves it, Ctrl/Cmd+C copies
  // the selected path (FR-UI-07).
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
      } else if ((e.ctrlKey || e.metaKey) && e.key === 'c' && !typing && selected && current) {
        // Only when no text is highlighted: copying prose the user swept up
        // must keep working, the shortcut fills the empty-copy case.
        const sel = window.getSelection();
        if (!sel || sel.isCollapsed) {
          e.preventDefault();
          const full = selected.path === '' ? current.path : `${current.path}/${selected.path}`;
          void navigator.clipboard?.writeText(full);
          setNotice(`Copied ${full}`);
        }
      } else if (e.key === 'Escape' && results) {
        setResults(null);
      } else if (e.key === 'Escape' && selectMode) {
        setSelectMode(false);
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [results, selection, selected, current, selectMode]);

  // --- render -------------------------------------------------------------

  if (shares === null && !error) {
    return <p className="loading muted">Loading shares…</p>;
  }

  const shell: ShellProps | null = current
    ? {
        share: current,
        shares: shares ?? [],
        busy,
        view,
        selected,
        selection,
        expanded,
        results,
        highlightExt,
        selectMode,
        setSelectMode,
        basis,
        columns,
        scheme,
        cushion,
        sort,
        desc,
        topHeight,
        leftWidth,
        onSelectShare: selectShare,
        onScan: (path = '') => void startScan(path),
        onCancelScan: () => void cancelScan(),
        onPauseToggle: () => void pauseToggle(),
        selectNode,
        selectMany,
        zoom,
        reveal,
        setResults,
        setExpanded,
        onSortChange: (s, d) => {
          setPref('sds.sort', s, setSort);
          setPref('sds.desc', d, setDesc);
        },
        setBasis: (b) => setPref('sds.basis', b, setBasis),
        setScheme: (s) => setPref('sds.scheme', s, setScheme),
        setCushion: (v) => setPref('sds.cushion', v, setCushion),
        toggleColumn,
        setTopHeight: (v) => setPref('sds.topHeight', v, setTopHeight),
        setLeftWidth: (v) => setPref('sds.leftWidth', v, setLeftWidth),
        setHighlightExt,
        openHistory: () => setDrawer((d) => !d),
        openTrash: () => setTrashOpen((t) => !t),
        requestDelete: (nodes) => setDeleting(nodes),
        openMenu: openMenuFor,
      }
    : null;

  return (
    <div className="app">
      {!phone && (
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
      )}

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

      {current && shell && (
        <>
          {phone ? <MobileShell key={current.id} {...shell} /> : <DesktopShell {...shell} />}

          {menu && (
            <ActionMenu
              items={buildNodeActions(current, menu.nodes, {
                busy,
                onDelete: (nodes) => setDeleting(nodes),
                onZoom: zoom,
                onLargestHere: (path) => setResults({ kind: 'largest', path }),
                onRescan: (path) => void startScan(path),
              })}
              x={menu.x}
              y={menu.y}
              label={
                menu.nodes.length === 1
                  ? `Actions for ${menu.nodes[0]?.name ?? ''}`
                  : `Actions for ${menu.nodes.length} items`
              }
              onClose={() => setMenu(null)}
            />
          )}

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
              onReveal={phone ? undefined : (path) => void revealPath(path)}
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
