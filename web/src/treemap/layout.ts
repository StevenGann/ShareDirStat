import type { Basis, TreemapNode } from '../api';
import { inset, squarify, type Rect } from './squarify';

/**
 * One painted rectangle. Cells are produced in paint order: a directory frame
 * is emitted before the children drawn inside it.
 */
export interface Cell {
  /** Share-relative path; truncated cells reuse their parent's with a suffix. */
  key: string;
  node: TreemapNode | null;
  rect: Rect;
  /** The content box a directory's children were laid out in. */
  content: Rect | null;
  /** The header strip, when the cell was tall enough to carry a label. */
  header: Rect | null;
  depth: number;
  kind: 'dir' | 'leaf' | 'truncated';
  /** Size of the truncated remainder, for its tooltip. */
  truncatedSize: number;
  truncatedCount: number;
}

export interface LayoutOptions {
  basis: Basis;
  /** Gap around a directory's children, in CSS pixels. */
  padding?: number;
  /** Height of a directory's label strip. */
  headerHeight?: number;
  /** Cells smaller than this in either dimension are not emitted (FR-UI-16). */
  minCell?: number;
  /** Hard cap on emitted cells, as a last line of defence. */
  maxCells?: number;
}

const DEFAULTS = {
  padding: 1,
  headerHeight: 13,
  minCell: 2,
  maxCells: 60000,
} as const;

/** A directory needs to be at least this big before it gets a label strip. */
const HEADER_MIN_HEIGHT = 42;
const HEADER_MIN_WIDTH = 46;

function sizeOf(n: TreemapNode, basis: Basis): number {
  return basis === 'allocated' ? n.alloc : n.size;
}

/**
 * Lays out a pruned treemap tree inside `viewport`, returning a flat list of
 * cells in paint order.
 *
 * The root itself is not emitted: the component draws a breadcrumb above the
 * canvas instead of spending vertical space on a header for it.
 */
export function layoutTreemap(root: TreemapNode, viewport: Rect, opts: LayoutOptions): Cell[] {
  const { basis } = opts;
  const padding = opts.padding ?? DEFAULTS.padding;
  const headerHeight = opts.headerHeight ?? DEFAULTS.headerHeight;
  const minCell = opts.minCell ?? DEFAULTS.minCell;
  const maxCells = opts.maxCells ?? DEFAULTS.maxCells;

  const cells: Cell[] = [];
  if (viewport.w <= 0 || viewport.h <= 0) return cells;

  const place = (parent: TreemapNode, box: Rect, depth: number) => {
    if (cells.length >= maxCells) return;
    const kids = parent.children_list ?? [];
    const trunc = parent.truncated;
    if (kids.length === 0 && !trunc) return;

    const values = kids.map((k) => sizeOf(k, basis));
    if (trunc) values.push(basis === 'allocated' ? trunc.alloc : trunc.size);
    const rects = squarify(values, box);

    for (let i = 0; i < kids.length; i++) {
      if (cells.length >= maxCells) return;
      const kid = kids[i] as TreemapNode;
      const rect = rects[i] as Rect;
      if (rect.w < minCell || rect.h < minCell) continue;

      const hasChildren = (kid.children_list?.length ?? 0) > 0 || !!kid.truncated;
      if (kid.kind !== 'dir' || !hasChildren) {
        cells.push({
          key: kid.path,
          node: kid,
          rect,
          content: null,
          header: null,
          depth,
          kind: 'leaf',
          truncatedSize: 0,
          truncatedCount: 0,
        });
        continue;
      }

      const framed = inset(rect, padding);
      const wantsHeader = framed.h >= HEADER_MIN_HEIGHT && framed.w >= HEADER_MIN_WIDTH;
      const header = wantsHeader ? { x: framed.x, y: framed.y, w: framed.w, h: headerHeight } : null;
      const content = header
        ? { x: framed.x, y: framed.y + header.h, w: framed.w, h: Math.max(framed.h - header.h, 0) }
        : framed;

      cells.push({
        key: kid.path,
        node: kid,
        rect,
        content,
        header,
        depth,
        kind: 'dir',
        truncatedSize: 0,
        truncatedCount: 0,
      });

      if (content.w >= minCell && content.h >= minCell) {
        place(kid, content, depth + 1);
      }
    }

    if (trunc) {
      const rect = rects[kids.length] as Rect | undefined;
      if (rect && rect.w >= minCell && rect.h >= minCell) {
        cells.push({
          key: `${parent.path} truncated`,
          node: null,
          rect,
          content: null,
          header: null,
          depth,
          kind: 'truncated',
          truncatedSize: basis === 'allocated' ? trunc.alloc : trunc.size,
          truncatedCount: trunc.children,
        });
      }
    }
  };

  place(root, viewport, 0);
  return cells;
}

/**
 * Finds the deepest cell under a point. Cells are in paint order, so scanning
 * backwards returns the one drawn last, which is the topmost.
 */
export function cellAt(cells: Cell[], x: number, y: number): Cell | null {
  for (let i = cells.length - 1; i >= 0; i--) {
    const c = cells[i] as Cell;
    const r = c.rect;
    if (x >= r.x && x < r.x + r.w && y >= r.y && y < r.y + r.h) return c;
  }
  return null;
}

/** Finds the cell for a given path, if it is currently laid out. */
export function cellForPath(cells: Cell[], path: string | null): Cell | null {
  if (path === null) return null;
  for (const c of cells) {
    if (c.node && c.node.path === path) return c;
  }
  return null;
}
