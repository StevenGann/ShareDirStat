import { COLUMNS, Tree } from './Tree';
import { Treemap } from './Treemap';
import { Extensions } from './Extensions';
import { DetailBar } from './DetailBar';
import { Splitter } from './Splitter';
import { ResultsView } from './ResultsView';
import type { ColorScheme } from '../treemap/colors';
import type { ShellProps } from './shell';
import { usePointerCoarse } from '../hooks';

const OPTIONAL_COLUMNS = COLUMNS.filter((c) => c.key !== 'name');

/**
 * The three-pane desktop layout (FR-UI-01): toolbar, tree/results +
 * extensions above the treemap, detail bar below. Moved verbatim out of App
 * so the phone shell can be its sibling; App still owns every piece of state.
 */
export function DesktopShell(p: ShellProps) {
  const { share: current, view, results, selected, selection, basis } = p;
  const coarse = usePointerCoarse();

  return (
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
                onChange={() => p.setBasis(b)}
              />
              {b === 'apparent' ? 'Apparent size' : 'On disk'}
            </label>
          ))}
        </fieldset>

        <label className="field">
          <span className="sr-only">Colour by</span>
          <select
            value={p.scheme}
            onChange={(e) => p.setScheme(e.target.value as ColorScheme)}
          >
            <option value="extension">Colour by type</option>
            <option value="depth">Colour by depth</option>
            <option value="mtime">Colour by age</option>
          </select>
        </label>

        <label className="check">
          <input
            type="checkbox"
            checked={p.cushion}
            onChange={(e) => p.setCushion(e.target.checked)}
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
                  checked={p.columns.includes(c.key)}
                  onChange={() => p.toggleColumn(c.key)}
                />
                {c.label}
              </label>
            ))}
          </div>
        </details>

        <span className="spacer" />

        {coarse && (
          <button
            type="button"
            aria-pressed={p.selectMode}
            onClick={() => p.setSelectMode(!p.selectMode)}
          >
            {p.selectMode ? 'Done selecting' : 'Select'}
          </button>
        )}

        <button type="button" onClick={() => p.setResults({ kind: 'search', query: '' })}>
          Search <kbd>/</kbd>
        </button>
        <button type="button" onClick={() => p.setResults({ kind: 'largest', path: view.root })}>
          Largest files
        </button>
        <button type="button" onClick={p.openHistory}>
          Scan history
          {current.stats && current.stats.errors > 0 ? ` · ${current.stats.errors} errors` : ''}
        </button>
        {current.trash_enabled && (
          <button type="button" onClick={p.openTrash}>
            Trash
          </button>
        )}
      </div>

      <div className="panes">
        <div className="panes-top" style={{ height: p.topHeight }}>
          <div className="pane-left" style={{ width: p.leftWidth }}>
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
                selectedPaths={new Set(selection.map((n) => n.path))}
                onSelect={p.selectNode}
                onReveal={p.reveal}
                onMenu={p.openMenu}
                selectMode={p.selectMode}
                onSelectModeChange={p.setSelectMode}
                onSelectAll={p.selectMany}
                onClose={() => p.setResults(null)}
              />
            ) : (
              <Tree
                share={current}
                basis={basis}
                generation={current.generation}
                columns={p.columns}
                sort={p.sort}
                desc={p.desc}
                onSortChange={p.onSortChange}
                selected={selected}
                selectedPaths={new Set(selection.map((n) => n.path))}
                onSelect={p.selectNode}
                onActivate={(n) => (n.kind === 'dir' ? p.zoom(n.path) : p.selectNode(n))}
                onMenu={p.openMenu}
                selectMode={p.selectMode}
                expanded={p.expanded}
                onExpandedChange={p.setExpanded}
              />
            )}
          </div>

          <Splitter
            orientation="vertical"
            value={p.leftWidth}
            min={320}
            max={1400}
            onChange={p.setLeftWidth}
            label="Resize the tree pane"
          />

          <div className="pane-right">
            <Extensions
              shareId={current.id}
              generation={current.generation}
              basis={basis}
              scheme={p.scheme}
              root={view.root}
              selected={p.highlightExt}
              onSelect={p.setHighlightExt}
              onShowFiles={(ext) => p.setResults({ kind: 'search', query: '', ext })}
            />
          </div>
        </div>

        <Splitter
          orientation="horizontal"
          value={p.topHeight}
          min={140}
          max={900}
          onChange={p.setTopHeight}
          label="Resize the treemap"
        />

        <div className="panes-bottom">
          <Treemap
            shareId={current.id}
            generation={current.generation}
            basis={basis}
            scheme={p.scheme}
            cushion={p.cushion}
            root={view.root}
            selectedPath={selected?.path ?? null}
            highlightExt={p.highlightExt}
            onZoom={p.zoom}
            onSelect={(n) => p.selectNode(n, 'replace')}
            onMenu={p.openMenu}
          />
        </div>
      </div>

      <DetailBar
        share={current}
        node={selected}
        selection={selection}
        basis={basis}
        busy={p.busy}
        onRescan={(path) => p.onScan(path)}
        onZoom={p.zoom}
        onLargestHere={(path) => p.setResults({ kind: 'largest', path })}
        onDelete={p.requestDelete}
      />
    </>
  );
}
