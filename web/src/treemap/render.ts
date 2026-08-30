import type { Basis } from '../api';
import { cushionGradient, depthColor, extensionColor, mtimeColor, type ColorScheme } from './colors';
import type { Cell } from './layout';
import type { Rect } from './squarify';

/**
 * The slice of CanvasRenderingContext2D the renderer uses. Narrowing it this
 * way keeps the drawing code testable: jsdom has no canvas implementation, so
 * the tests pass a recording stub instead.
 */
export interface Ctx2D {
  fillStyle: string | CanvasGradient | CanvasPattern;
  strokeStyle: string | CanvasGradient | CanvasPattern;
  lineWidth: number;
  font: string;
  textBaseline: CanvasTextBaseline;
  save(): void;
  restore(): void;
  clearRect(x: number, y: number, w: number, h: number): void;
  fillRect(x: number, y: number, w: number, h: number): void;
  strokeRect(x: number, y: number, w: number, h: number): void;
  beginPath(): void;
  rect(x: number, y: number, w: number, h: number): void;
  moveTo(x: number, y: number): void;
  lineTo(x: number, y: number): void;
  stroke(): void;
  clip(): void;
  fillText(text: string, x: number, y: number): void;
  measureText(text: string): { width: number };
  createLinearGradient(x0: number, y0: number, x1: number, y1: number): CanvasGradient;
}

/** Colours the renderer takes from the page theme. */
export interface RenderTheme {
  dark: boolean;
  background: string;
  frame: string;
  headerBackground: string;
  headerText: string;
  selection: string;
  hover: string;
  truncatedFill: string;
  truncatedLine: string;
}

export interface RenderOptions {
  theme: RenderTheme;
  width: number;
  height: number;
  basis: Basis;
  scheme: ColorScheme;
  cushion: boolean;
  /** Reference time for the mtime colour scheme. */
  now: number;
  /** Extension to emphasise; every other cell is dimmed. */
  highlightExt?: string | null;
}

/** The label font, kept in one place so measurement and drawing agree. */
const LABEL_FONT = '600 10px system-ui, -apple-system, "Segoe UI", Roboto, sans-serif';

/** A cell must be at least this wide before a label is attempted. */
const LABEL_MIN_WIDTH = 34;

/** Picks the base colour of a cell under the active colour scheme. */
export function cellColor(cell: Cell, opts: RenderOptions): string {
  const { theme, scheme } = opts;
  if (cell.kind === 'truncated') return theme.truncatedFill;
  const node = cell.node;
  if (!node) return theme.truncatedFill;
  switch (scheme) {
    case 'depth':
      return depthColor(cell.depth, theme.dark);
    case 'mtime':
      return mtimeColor(Date.parse(node.mtime), opts.now, theme.dark);
    default:
      return extensionColor(node.ext ?? '', theme.dark);
  }
}

/**
 * Paints the whole treemap. Cells arrive in paint order, so directories are
 * drawn before the children that sit inside them and no explicit z-ordering
 * is needed.
 */
export function renderTreemap(ctx: Ctx2D, cells: Cell[], opts: RenderOptions): void {
  const { theme } = opts;
  ctx.clearRect(0, 0, opts.width, opts.height);
  ctx.fillStyle = theme.background;
  ctx.fillRect(0, 0, opts.width, opts.height);
  ctx.font = LABEL_FONT;
  ctx.textBaseline = 'middle';

  for (const cell of cells) {
    switch (cell.kind) {
      case 'dir':
        drawDirectory(ctx, cell, opts);
        break;
      case 'truncated':
        drawTruncated(ctx, cell, opts);
        break;
      default:
        drawLeaf(ctx, cell, opts);
    }
  }
}

function dimmed(cell: Cell, opts: RenderOptions): boolean {
  const ext = opts.highlightExt;
  if (!ext) return false;
  if (cell.kind !== 'leaf' || !cell.node) return false;
  return (cell.node.ext ?? '') !== ext;
}

function drawLeaf(ctx: Ctx2D, cell: Cell, opts: RenderOptions): void {
  const { x, y, w, h } = cell.rect;
  ctx.fillStyle = cellColor(cell, opts);
  ctx.fillRect(x, y, w, h);

  if (opts.cushion) {
    const g = cushionGradient(ctx, x, y, w, h, opts.theme.dark);
    if (g) {
      ctx.fillStyle = g;
      ctx.fillRect(x, y, w, h);
    }
  }
  if (dimmed(cell, opts)) {
    ctx.fillStyle = opts.theme.dark ? 'rgba(10,12,14,0.66)' : 'rgba(250,250,250,0.72)';
    ctx.fillRect(x, y, w, h);
  }
}

function drawDirectory(ctx: Ctx2D, cell: Cell, opts: RenderOptions): void {
  const { theme } = opts;
  const { x, y, w, h } = cell.rect;

  // The frame is painted across the whole cell; children then cover the
  // middle, leaving the frame visible as a border.
  ctx.fillStyle = theme.frame;
  ctx.fillRect(x, y, w, h);

  if (!cell.header) return;
  const hd = cell.header;
  ctx.fillStyle = theme.headerBackground;
  ctx.fillRect(hd.x, hd.y, hd.w, hd.h);

  const label = cell.node?.name ?? '';
  if (label && hd.w >= LABEL_MIN_WIDTH) {
    ctx.save();
    ctx.beginPath();
    ctx.rect(hd.x, hd.y, hd.w, hd.h);
    ctx.clip();
    ctx.fillStyle = theme.headerText;
    ctx.fillText(fitText(ctx, label, hd.w - 8), hd.x + 4, hd.y + hd.h / 2);
    ctx.restore();
  }
}

function drawTruncated(ctx: Ctx2D, cell: Cell, opts: RenderOptions): void {
  const { theme } = opts;
  const { x, y, w, h } = cell.rect;
  ctx.fillStyle = theme.truncatedFill;
  ctx.fillRect(x, y, w, h);

  // Diagonal hatching says "this is a summary, not a real item".
  if (w < 4 || h < 4) return;
  ctx.save();
  ctx.beginPath();
  ctx.rect(x, y, w, h);
  ctx.clip();
  ctx.strokeStyle = theme.truncatedLine;
  ctx.lineWidth = 1;
  ctx.beginPath();
  const step = 6;
  for (let offset = -h; offset < w; offset += step) {
    ctx.moveTo(x + offset, y + h);
    ctx.lineTo(x + offset + h, y);
  }
  ctx.stroke();
  ctx.restore();
}

/**
 * Outlines a cell and the frames of its ancestors, so hovering a deep file
 * also shows which folders contain it.
 */
export function drawHighlight(
  ctx: Ctx2D,
  cell: Cell,
  ancestors: Cell[],
  theme: RenderTheme,
  color: string,
): void {
  ctx.save();
  ctx.lineWidth = 1;
  ctx.strokeStyle = theme.frame;
  for (const a of ancestors) {
    strokeInside(ctx, a.rect, 1);
  }
  ctx.lineWidth = 2;
  ctx.strokeStyle = color;
  strokeInside(ctx, cell.rect, 2);
  ctx.restore();
}

/** Strokes just inside a rectangle so the line is not clipped at the edges. */
function strokeInside(ctx: Ctx2D, r: Rect, width: number): void {
  const half = width / 2;
  const w = Math.max(r.w - width, 0);
  const h = Math.max(r.h - width, 0);
  if (w <= 0 || h <= 0) return;
  ctx.strokeRect(r.x + half, r.y + half, w, h);
}

/**
 * Trims a label to fit a width, appending an ellipsis. Measurement is done
 * with a binary search so a long name costs a handful of measureText calls
 * rather than one per character.
 */
export function fitText(ctx: Ctx2D, text: string, maxWidth: number): string {
  if (maxWidth <= 0) return '';
  if (ctx.measureText(text).width <= maxWidth) return text;

  const ellipsis = '…';
  if (ctx.measureText(ellipsis).width > maxWidth) return '';

  let lo = 0;
  let hi = text.length;
  while (lo < hi) {
    const mid = Math.ceil((lo + hi) / 2);
    if (ctx.measureText(text.slice(0, mid) + ellipsis).width <= maxWidth) {
      lo = mid;
    } else {
      hi = mid - 1;
    }
  }
  return lo > 0 ? text.slice(0, lo) + ellipsis : '';
}

/** Collects the ancestor cells of a cell, outermost first. */
export function ancestorsOf(cells: Cell[], cell: Cell): Cell[] {
  const path = cell.node?.path ?? cell.key;
  const out: Cell[] = [];
  for (const c of cells) {
    if (c === cell || c.kind !== 'dir' || !c.node) continue;
    const p = c.node.path;
    if (p !== '' && path.startsWith(`${p}/`)) out.push(c);
  }
  return out;
}
