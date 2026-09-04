import { useCallback, useLayoutEffect, useMemo, useRef, useState } from 'react';
import { api, type Basis, type Node } from '../api';
import {
  bytesPerMinute,
  formatBytes,
  formatBytesPerMin,
  formatCount,
  formatPercent,
  formatPlaytime,
} from '../format';
import { useAsyncData, useElementSize, useLongPress, useNow } from '../hooks';
import { useResolvedDark } from '../theme';
import { IconClose, IconZoomIn, IconZoomOut } from './icons';
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
  /** Opens the action menu for a cell: right-click or touch long-press. */
  onMenu?: (node: Node, x: number, y: number) => void;
  /** Phone layout: the pinned card's "Details" opens the detail sheet. */
  onOpenDetail?: (node: Node) => void;
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
  onMenu,
  onOpenDetail,
}: Props) {
  const [wrapRef, size] = useElementSize<HTMLDivElement>();
  const baseRef = useRef<HTMLCanvasElement | null>(null);
  const overlayRef = useRef<HTMLCanvasElement | null>(null);

  const [theme, setTheme] = useState<RenderTheme | null>(null);
  // The tooltip is stored with the cell array it was hit-tested against, so a
  // relayout invalidates it by comparison instead of through an effect.
  const [hover, setHover] = useState<{ cells: Cell[]; tip: Tooltip } | null>(null);
  // Touch has no hover: a tap pins this card instead, invalidated the same
  // way. It carries the tapped cell's facts plus explicit actions.
  const [pinned, setPinned] = useState<{ cells: Cell[]; cell: Cell } | null>(null);
  const lastPointerType = useRef('mouse');
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

  // useResolvedDark re-renders on both the OS preference and the manual
  // override; the attribute is already mutated by the time this effect runs,
  // so getComputedStyle sees the new variable values.
  const dark = useResolvedDark();
  useLayoutEffect(() => {
    const el = wrapRef.current;
    if (el) setTheme(readTheme(el));
  }, [wrapRef, dark]);

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

  /** Hit-tests viewport coordinates, for the long-press handler. */
  const cellAtClient = useCallback(
    (clientX: number, clientY: number): Cell | null => {
      const el = overlayRef.current;
      if (!el) return null;
      const box = el.getBoundingClientRect();
      return cellAt(cells, clientX - box.left, clientY - box.top);
    },
    [cells],
  );

  const longPress = useLongPress((x, y) => {
    const cell = cellAtClient(x, y);
    if (cell?.node && onMenu) {
      onSelect(cell.node);
      onMenu(cell.node, x, y);
    }
  });

  const onContextMenu = useCallback(
    (e: React.MouseEvent) => {
      if (!onMenu) return;
      e.preventDefault();
      longPress.cancel();
      if (longPress.firedRecently()) return;
      const cell = locate(e);
      if (cell?.node) {
        onSelect(cell.node);
        onMenu(cell.node, e.clientX, e.clientY);
      }
    },
    [onMenu, longPress, locate, onSelect],
  );

  const onPointerDown = useCallback(
    (e: React.PointerEvent) => {
      lastPointerType.current = e.pointerType;
      longPress.handlers.onPointerDown(e);
    },
    [longPress],
  );

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      longPress.handlers.onPointerMove(e);
      // A finger is not hovering; the tap-pinned card covers touch.
      if (e.pointerType === 'touch') return;
      const cell = locate(e);
      if (!cell) {
        setHover(null);
        return;
      }
      const box = e.currentTarget.getBoundingClientRect();
      setHover({ cells, tip: { x: e.clientX - box.left, y: e.clientY - box.top, cell } });
    },
    [locate, cells, longPress],
  );

  const onClick = useCallback(
    (e: React.MouseEvent) => {
      const cell = locate(e);
      if (cell?.node) onSelect(cell.node);
      // On touch a tap pins the info card — the hovercard's stand-in.
      if (cell && lastPointerType.current === 'touch') setPinned({ cells, cell });
      else setPinned(null);
    },
    [locate, onSelect, cells],
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

  const pinnedCell = pinned?.cells === cells ? pinned.cell : null;

  /** The folder an explicit "Zoom in" would enter: the pinned or selected
   *  directory, when it is not already the root being shown. */
  const zoomTarget = useMemo(() => {
    const candidate = pinnedCell?.node ?? cellForPath(cells, selectedPath)?.node ?? null;
    return candidate && candidate.kind === 'dir' && candidate.path !== root
      ? candidate.path
      : null;
  }, [pinnedCell, cells, selectedPath, root]);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.preventDefault();
        if (pinnedCell) {
          setPinned(null);
          return;
        }
        zoomOut();
      }
    },
    [zoomOut, pinnedCell],
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
        <button
          type="button"
          className="icon-button"
          aria-label="Zoom in"
          title="Zoom into the selected folder"
          disabled={!zoomTarget}
          onClick={() => zoomTarget && onZoom(zoomTarget)}
        >
          <IconZoomIn />
        </button>
        <button
          type="button"
          className="icon-button"
          aria-label="Zoom out"
          title="Zoom out one level"
          disabled={root === ''}
          onClick={zoomOut}
        >
          <IconZoomOut />
        </button>
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
          aria-label={`Treemap of ${root === '' ? 'the share root' : root}. Tap or click a cell to inspect it; the zoom buttons, a double-click or Escape change the level.`}
          onPointerDown={onPointerDown}
          onPointerUp={longPress.handlers.onPointerUp}
          onPointerCancel={longPress.handlers.onPointerCancel}
          onPointerMove={onPointerMove}
          onPointerLeave={() => setHover(null)}
          onClick={onClick}
          onDoubleClick={onDoubleClick}
          onContextMenu={onContextMenu}
          onKeyDown={onKeyDown}
        />
        {tooltip && !pinnedCell && <HoverCard tip={tooltip} basis={basis} bounds={size} />}
        {pinnedCell && (
          <PinnedCard
            cell={pinnedCell}
            basis={basis}
            bounds={size}
            onClose={() => setPinned(null)}
            onZoom={
              pinnedCell.node?.kind === 'dir' && pinnedCell.node.path !== root
                ? () => onZoom(pinnedCell.node!.path)
                : undefined
            }
            onDetails={
              pinnedCell.node && onOpenDetail
                ? () => onOpenDetail(pinnedCell.node!)
                : undefined
            }
          />
        )}
      </div>
    </section>
  );
}

/** The facts shown for one cell, shared by the hovercard and pinned card. */
function CardFacts({ cell, basis }: { cell: Cell; basis: Basis }) {
  if (cell.kind === 'truncated') {
    return (
      <>
        <div className="hovercard-title">{formatCount(cell.truncatedCount)} smaller items</div>
        <div className="muted">{formatBytes(cell.truncatedSize)} combined</div>
        <div className="muted small">Too small to draw individually. Zoom in to see them.</div>
      </>
    );
  }
  const node = cell.node;
  if (!node) return null;
  const size = basis === 'allocated' ? node.alloc : node.size;
  return (
    <>
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
      {node.duration ? (
        <div className="muted">
          {formatPlaytime(node.duration)} · {formatBytesPerMin(bytesPerMinute(node))}
        </div>
      ) : null}
    </>
  );
}

/** Positions a card beside a point, flipped near the right or bottom edge so
 *  it never falls outside the pane. */
function cardPosition(x: number, y: number, bounds: { w: number; h: number }): React.CSSProperties {
  const flipX = x > bounds.w - 260;
  const flipY = y > bounds.h - 140;
  return {
    left: flipX ? undefined : x + 14,
    right: flipX ? bounds.w - x + 14 : undefined,
    top: flipY ? undefined : y + 16,
    bottom: flipY ? bounds.h - y + 16 : undefined,
  };
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
  if (cell.kind !== 'truncated' && !cell.node) return null;
  return (
    <div className="hovercard" style={cardPosition(tip.x, tip.y, bounds)} role="tooltip">
      <CardFacts cell={cell} basis={basis} />
      {cell.node && (
        <div className="muted small">
          {cell.node.kind === 'dir' ? 'Double-click to zoom in' : (cell.node.ext ?? 'no extension')}
        </div>
      )}
    </div>
  );
}

/** The touch stand-in for the hovercard: pinned by a tap, dismissed
 *  explicitly, and carrying the actions hover cannot offer a finger. */
function PinnedCard({
  cell,
  basis,
  bounds,
  onClose,
  onZoom,
  onDetails,
}: {
  cell: Cell;
  basis: Basis;
  bounds: { w: number; h: number };
  onClose: () => void;
  onZoom?: () => void;
  onDetails?: () => void;
}) {
  if (cell.kind !== 'truncated' && !cell.node) return null;
  const style = cardPosition(
    Math.min(cell.rect.x + cell.rect.w / 2, bounds.w),
    Math.min(cell.rect.y + cell.rect.h / 2, bounds.h),
    bounds,
  );
  return (
    <div className="hovercard pinned" style={style} role="status">
      <CardFacts cell={cell} basis={basis} />
      <div className="pinned-actions">
        {onZoom && (
          <button type="button" onClick={onZoom}>
            Zoom in
          </button>
        )}
        {onDetails && (
          <button type="button" onClick={onDetails}>
            Details
          </button>
        )}
        <button type="button" className="icon-button" onClick={onClose} aria-label="Close">
          <IconClose />
        </button>
      </div>
    </div>
  );
}
