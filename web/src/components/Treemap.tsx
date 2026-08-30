import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { api, type Basis, type Node } from '../api';
import { formatBytes, formatCount, formatPercent } from '../format';
import { useAsyncData, useElementSize, useNow } from '../hooks';
import type { ColorScheme } from '../treemap/colors';
import { cellAt, cellForPath, layoutTreemap, type Cell } from '../treemap/layout';
import {
  ancestorsOf,
  drawHighlight,
  renderTreemap,
  type RenderOptions,
  type RenderTheme,
} from '../treemap/render';

interface Props {
  shareId: string;
  generation: string | null;
  basis: Basis;
  scheme: ColorScheme;
  cushion: boolean;
  /** Directory the treemap is zoomed to. */
  root: string;
  /** Currently selected node path, kept in sync with the tree pane. */
  selectedPath: string | null;
  /** Extension to emphasise, from the extensions pane. */
  highlightExt: string | null;
  onZoom: (path: string) => void;
  onSelect: (node: Node) => void;
}

interface Tooltip {
  x: number;
  y: number;
  cell: Cell;
}

/** Reads the canvas palette from CSS so it follows the page theme. */
function readTheme(el: HTMLElement): RenderTheme {
  const cs = getComputedStyle(el);
  const v = (name: string, fallback: string) => cs.getPropertyValue(name).trim() || fallback;
  return {
    dark: v('--is-dark', '0') === '1',
    background: v('--treemap-bg', '#ffffff'),
    frame: v('--treemap-frame', '#c9cdd0'),
    headerBackground: v('--treemap-header-bg', '#e6e9ea'),
    headerText: v('--treemap-header-text', '#33383c'),
    selection: v('--treemap-selection', '#1f6feb'),
    hover: v('--treemap-hover', '#1b1d1e'),
    truncatedFill: v('--treemap-truncated', '#d5d8da'),
    truncatedLine: v('--treemap-truncated-line', '#a9aeb2'),
  };
}

export function Treemap({
  shareId,
  generation,
  basis,
  scheme,
  cushion,
  root,
  selectedPath,
  highlightExt,
  onZoom,
  onSelect,
}: Props) {
  const [wrapRef, size] = useElementSize<HTMLDivElement>();
  const baseRef = useRef<HTMLCanvasElement | null>(null);
  const overlayRef = useRef<HTMLCanvasElement | null>(null);

  const [theme, setTheme] = useState<RenderTheme | null>(null);
  // The tooltip is stored with the cell array it was hit-tested against, so a
  // relayout invalidates it by comparison instead of through an effect.
  const [hover, setHover] = useState<{ cells: Cell[]; tip: Tooltip } | null>(null);
  const now = useNow();

  const key = `${shareId}|${generation ?? ''}|${root}|${basis}`;
  const load = useCallback(
    async () =>
      Promise.all([
        api.treemap(shareId, root, { basis, max_nodes: 12000, min_fraction: 0.0002 }),
        api.node(shareId, root, basis),
      ]),
    [shareId, root, basis],
  );
  const { data, error, loading } = useAsyncData(key, load, Boolean(generation));
  const map = data?.[0].root ?? null;
  const ancestors = useMemo(() => data?.[1].ancestors ?? [], [data]);

  // --- theme --------------------------------------------------------------

  useLayoutEffect(() => {
    const el = wrapRef.current;
    if (!el) return;
    const read = () => setTheme(readTheme(el));
    read();
    if (!window.matchMedia) return;
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    mq.addEventListener('change', read);
    return () => mq.removeEventListener('change', read);
  }, [wrapRef]);

  // --- layout -------------------------------------------------------------

  const cells = useMemo(() => {
    if (!map || size.w <= 0 || size.h <= 0) return [];
    return layoutTreemap(map, { x: 0, y: 0, w: size.w, h: size.h }, { basis });
  }, [map, size.w, size.h, basis]);

  const renderOptions: RenderOptions | null = useMemo(() => {
    if (!theme) return null;
    return {
      theme,
      width: size.w,
      height: size.h,
      basis,
      scheme,
      cushion,
      now,
      highlightExt,
    };
  }, [theme, size.w, size.h, basis, scheme, cushion, now, highlightExt]);

  // --- painting -----------------------------------------------------------

  useLayoutEffect(() => {
    const canvas = baseRef.current;
    if (!canvas || !renderOptions || size.w <= 0 || size.h <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.floor(size.w * dpr);
    canvas.height = Math.floor(size.h * dpr);
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    renderTreemap(ctx, cells, renderOptions);
  }, [cells, renderOptions, size.w, size.h]);

  // Sizing the overlay is kept out of the hover effect below. Assigning
  // canvas.width/height reinitialises the bitmap even when the value is
  // unchanged (it is the canonical clear idiom), so leaving it in an effect
  // that depends on `hover` reallocated and zeroed the whole backing store on
  // every pointermove -- roughly 23 MB per event at 1600x900 on a 2x display.
  useLayoutEffect(() => {
    const canvas = overlayRef.current;
    if (!canvas || size.w <= 0 || size.h <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    canvas.width = Math.floor(size.w * dpr);
    canvas.height = Math.floor(size.h * dpr);
  }, [size.w, size.h]);

  useLayoutEffect(() => {
    const canvas = overlayRef.current;
    if (!canvas || !theme || size.w <= 0 || size.h <= 0) return;
    const dpr = window.devicePixelRatio || 1;
    const ctx = canvas.getContext('2d');
    if (!ctx) return;
    ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
    ctx.clearRect(0, 0, size.w, size.h);

    const selected = cellForPath(cells, selectedPath);
    if (selected) drawHighlight(ctx, selected, ancestorsOf(cells, selected), theme, theme.selection);
    const tip = hover?.cells === cells ? hover.tip : null;
    if (tip && tip.cell !== selected) {
      drawHighlight(ctx, tip.cell, ancestorsOf(cells, tip.cell), theme, theme.hover);
    }
  }, [cells, theme, hover, selectedPath, size.w, size.h]);

  const tooltip = hover?.cells === cells ? hover.tip : null;

  // --- interaction --------------------------------------------------------

  const locate = useCallback(
    (e: React.PointerEvent | React.MouseEvent): Cell | null => {
      const el = overlayRef.current;
      if (!el) return null;
      const box = el.getBoundingClientRect();
      return cellAt(cells, e.clientX - box.left, e.clientY - box.top);
    },
    [cells],
  );

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      const cell = locate(e);
      if (!cell) {
        setHover(null);
        return;
      }
      const box = e.currentTarget.getBoundingClientRect();
      setHover({ cells, tip: { x: e.clientX - box.left, y: e.clientY - box.top, cell } });
    },
    [locate, cells],
  );

  const onClick = useCallback(
    (e: React.MouseEvent) => {
      const cell = locate(e);
      if (cell?.node) onSelect(cell.node);
    },
    [locate, onSelect],
  );

  const onDoubleClick = useCallback(
    (e: React.MouseEvent) => {
      const cell = locate(e);
      if (cell?.node && cell.node.kind === 'dir') onZoom(cell.node.path);
    },
    [locate, onZoom],
  );

  const zoomOut = useCallback(() => {
    if (root === '') return;
    onZoom(root.includes('/') ? root.slice(0, root.lastIndexOf('/')) : '');
  }, [root, onZoom]);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        zoomOut();
      }
    },
    [zoomOut],
  );

  // --- render -------------------------------------------------------------

  const crumbs = useMemo(() => {
    const out = ancestors.map((a) => ({
      label: a.path === '' ? 'Share root' : a.name,
      path: a.path,
    }));
    if (root !== '') out.push({ label: root.split('/').pop() ?? root, path: root });
    else if (out.length === 0) out.push({ label: 'Share root', path: '' });
    return out;
  }, [ancestors, root]);

  return (
    <section className="treemap-pane" aria-label="Treemap">
      <div className="treemap-head">
        <nav className="crumbs" aria-label="Treemap location">
          {crumbs.map((c, i) => (
            <span key={c.path || 'root'}>
              {i > 0 && (
                <span className="crumb-sep" aria-hidden="true">
                  /
                </span>
              )}
              {i === crumbs.length - 1 ? (
                <span className="crumb current">{c.label}</span>
              ) : (
                <button type="button" className="crumb" onClick={() => onZoom(c.path)}>
                  {c.label}
                </button>
              )}
            </span>
          ))}
        </nav>
        <span className="spacer" />
        {root !== '' && (
          <button type="button" onClick={zoomOut}>
            Zoom out
          </button>
        )}
        <span className="muted small">
          {loading && generation ? 'building…' : `${formatCount(cells.length)} cells`}
        </span>
      </div>

      <div className="treemap-canvas" ref={wrapRef}>
        {error && (
          <div className="banner error" role="alert">
            {error}
          </div>
        )}
        {!error && !generation && (
          <p className="muted treemap-empty">Scan this share to see the treemap.</p>
        )}
        <canvas ref={baseRef} style={{ width: size.w, height: size.h }} aria-hidden="true" />
        <canvas
          ref={overlayRef}
          style={{ width: size.w, height: size.h }}
          className="treemap-overlay"
          tabIndex={0}
          role="img"
          aria-label={`Treemap of ${root === '' ? 'the share root' : root}. Double-click a folder to zoom in, Escape to zoom out.`}
          onPointerMove={onPointerMove}
          onPointerLeave={() => setHover(null)}
          onClick={onClick}
          onDoubleClick={onDoubleClick}
          onKeyDown={onKeyDown}
        />
        {tooltip && <HoverCard tip={tooltip} basis={basis} bounds={size} />}
      </div>
    </section>
  );
}

function HoverCard({
  tip,
  basis,
  bounds,
}: {
  tip: Tooltip;
  basis: Basis;
  bounds: { w: number; h: number };
}) {
  const { cell } = tip;
  // Flip the card to the other side of the cursor near the right or bottom
  // edge so it never falls outside the pane.
  const flipX = tip.x > bounds.w - 260;
  const flipY = tip.y > bounds.h - 110;
  const style: React.CSSProperties = {
    left: flipX ? undefined : tip.x + 14,
    right: flipX ? bounds.w - tip.x + 14 : undefined,
    top: flipY ? undefined : tip.y + 16,
    bottom: flipY ? bounds.h - tip.y + 16 : undefined,
  };

  if (cell.kind === 'truncated') {
    return (
      <div className="hovercard" style={style} role="tooltip">
        <div className="hovercard-title">{formatCount(cell.truncatedCount)} smaller items</div>
        <div className="muted">{formatBytes(cell.truncatedSize)} combined</div>
        <div className="muted small">Too small to draw individually. Zoom in to see them.</div>
      </div>
    );
  }
  const node = cell.node;
  if (!node) return null;
  const size = basis === 'allocated' ? node.alloc : node.size;
  return (
    <div className="hovercard" style={style} role="tooltip">
      <div className="hovercard-title">{node.path === '' ? 'Share root' : node.path}</div>
      <div>
        <strong>{formatBytes(size)}</strong>{' '}
        <span className="muted">{formatPercent(node.pct_of_share)} of share</span>
      </div>
      {node.kind === 'dir' && (
        <div className="muted">
          {formatCount(node.files)} files · {formatCount(node.dirs)} folders
        </div>
      )}
      <div className="muted small">
        {node.kind === 'dir' ? 'Double-click to zoom in' : (node.ext ?? 'no extension')}
      </div>
    </div>
  );
}
