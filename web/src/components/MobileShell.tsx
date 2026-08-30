import { useCallback, useState } from 'react';
import { buildNodeActions } from '../actions';
import { BrowseList } from './BrowseList';
import { DetailSheet } from './DetailSheet';
import { Extensions } from './Extensions';
import { MobileHeader } from './MobileHeader';
import { ResultsView } from './ResultsView';
import { SettingsSheet } from './SettingsSheet';
import type { ShellProps } from './shell';
import { TabBar, type MobileTab } from './TabBar';
import { Treemap } from './Treemap';

function parentOf(path: string): string {
  return path.includes('/') ? path.slice(0, path.lastIndexOf('/')) : '';
}

/**
 * The under-768px layout: one view at a time behind a bottom tab bar, a
 * drill-down browse list instead of the indented tree, and bottom sheets for
 * detail and options. All state of consequence lives in App (the ShellProps
 * contract), so rotating across the breakpoint keeps selection, results and
 * the URL; only the active tab and browse position are local.
 */
export function MobileShell(p: ShellProps) {
  const { share: current, basis } = p;
  const [tab, setTab] = useState<MobileTab>('browse');
  // Where Browse is, seeded from a bookmarked selection's parent folder.
  const [browsePath, setBrowsePath] = useState(() => parentOf(p.view.path));
  const [sheet, setSheet] = useState<'settings' | 'detail' | null>(null);

  const zoomTo = useCallback(
    (path: string) => {
      p.zoom(path);
      setTab('treemap');
    },
    [p],
  );

  const exitSelect = useCallback(() => p.setSelectMode(false), [p]);

  return (
    <div className="mobile-shell">
      <MobileHeader
        shares={p.shares}
        current={current}
        scan={current.scan}
        busy={p.busy}
        onSelectShare={p.onSelectShare}
        onScan={() => p.onScan('')}
        onCancel={p.onCancelScan}
        onPauseToggle={p.onPauseToggle}
        onSearch={() => p.setResults({ kind: 'search', query: '' })}
        onSettings={() => setSheet('settings')}
      />

      <div className="mobile-content">
        {tab === 'browse' && (
          <BrowseList
            share={current}
            basis={basis}
            generation={current.generation}
            path={browsePath}
            onNavigate={setBrowsePath}
            sort={p.sort}
            desc={p.desc}
            onSortChange={p.onSortChange}
            selected={p.selected}
            selectedPaths={new Set(p.selection.map((n) => n.path))}
            selectMode={p.selectMode}
            onSelectModeChange={p.setSelectMode}
            onSelect={p.selectNode}
            onOpenDetail={() => setSheet('detail')}
            onMenu={p.openMenu}
          />
        )}
        {tab === 'treemap' && (
          <Treemap
            shareId={current.id}
            generation={current.generation}
            basis={basis}
            scheme={p.scheme}
            cushion={p.cushion}
            root={p.view.root}
            selectedPath={p.selected?.path ?? null}
            highlightExt={p.highlightExt}
            onZoom={p.zoom}
            onSelect={(n) => p.selectNode(n, 'replace')}
            onMenu={p.openMenu}
            onOpenDetail={() => setSheet('detail')}
          />
        )}
        {tab === 'types' && (
          <Extensions
            shareId={current.id}
            generation={current.generation}
            basis={basis}
            scheme={p.scheme}
            root={p.view.root}
            selected={p.highlightExt}
            onSelect={(ext) => {
              p.setHighlightExt(ext);
            }}
            onShowFiles={(ext) => p.setResults({ kind: 'search', query: '', ext })}
          />
        )}
      </div>

      {p.results && (
        <div className="mobile-results">
          <ResultsView
            key={`${p.results.kind}|${
              p.results.kind === 'search'
                ? `${p.results.query}|${p.results.ext ?? ''}`
                : p.results.path
            }`}
            shareId={current.id}
            generation={current.generation}
            basis={basis}
            mode={p.results}
            scope={p.view.root}
            selected={p.selected}
            onSelect={(n) => p.selectNode(n, 'replace')}
            onReveal={(n) => {
              // There is no tree to reveal into: go to the folder instead.
              p.selectNode(n, 'replace');
              setBrowsePath(n.kind === 'dir' ? n.path : parentOf(n.path));
              setTab('browse');
              p.setResults(null);
            }}
            onMenu={p.openMenu}
            onClose={() => p.setResults(null)}
          />
        </div>
      )}

      {p.selectMode && (
        <div className="select-strip" role="toolbar" aria-label="Selection actions">
          <span className="select-count">
            {p.selection.length} selected
          </span>
          <span className="spacer" />
          {buildNodeActions(current, p.selection, {
            busy: p.busy,
            onDelete: p.requestDelete,
            onZoom: zoomTo,
            onLargestHere: (path) => p.setResults({ kind: 'largest', path }),
            onRescan: (path) => p.onScan(path),
          })
            .filter((it) => it.id === 'zip' || it.id === 'delete')
            .map((it) =>
              it.href && !it.disabled ? (
                <a key={it.id} className="button" href={it.href} title={it.title}>
                  {it.label}
                </a>
              ) : (
                <button
                  key={it.id}
                  type="button"
                  className={it.danger ? 'danger' : undefined}
                  disabled={it.disabled || p.selection.length === 0}
                  title={it.title}
                  onClick={it.run}
                >
                  {it.label}
                </button>
              ),
            )}
          <button type="button" onClick={exitSelect}>
            Done
          </button>
        </div>
      )}

      <TabBar tab={tab} onChange={setTab} />

      {sheet === 'detail' && (
        <DetailSheet
          share={current}
          node={p.selected}
          selection={p.selection}
          basis={basis}
          busy={p.busy}
          onClose={() => setSheet(null)}
          onRescan={(path) => p.onScan(path)}
          onZoom={zoomTo}
          onLargestHere={(path) => {
            setSheet(null);
            p.setResults({ kind: 'largest', path });
          }}
          onDelete={(nodes) => {
            setSheet(null);
            p.requestDelete(nodes);
          }}
        />
      )}

      {sheet === 'settings' && (
        <SettingsSheet
          share={current}
          basis={basis}
          scheme={p.scheme}
          cushion={p.cushion}
          setBasis={p.setBasis}
          setScheme={p.setScheme}
          setCushion={p.setCushion}
          onLargest={() => p.setResults({ kind: 'largest', path: p.view.root })}
          onHistory={p.openHistory}
          onTrash={p.openTrash}
          onClose={() => setSheet(null)}
        />
      )}
    </div>
  );
}
