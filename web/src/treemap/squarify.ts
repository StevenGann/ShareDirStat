/**
 * Squarified treemap layout (Bruls, Huizing & van Wijk, 2000).
 *
 * The algorithm fills a rectangle with sub-rectangles whose areas are
 * proportional to their values, greedily choosing row breaks so the cells
 * stay as close to square as possible — long thin slivers are both ugly and
 * impossible to click.
 */

export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

const EMPTY: Rect = { x: 0, y: 0, w: 0, h: 0 };

/**
 * Lays out `values` inside `rect`, returning one rectangle per value in the
 * same order. Values are expected to be sorted descending, which is how the
 * API already returns children; unsorted input still tiles correctly but
 * produces worse aspect ratios.
 *
 * Zero and negative values collapse to empty rectangles rather than being
 * dropped, so callers can index the result by the original position.
 */
export function squarify(values: number[], rect: Rect): Rect[] {
  const out: Rect[] = values.map(() => EMPTY);
  if (rect.w <= 0 || rect.h <= 0) return out;

  // Only positive values take part in the layout.
  const live: number[] = [];
  for (let i = 0; i < values.length; i++) {
    const v = values[i] ?? 0;
    if (v > 0) live.push(i);
  }
  if (live.length === 0) return out;

  let total = 0;
  for (const i of live) total += values[i] ?? 0;
  if (total <= 0) return out;

  // Work in area units so a row's area maps directly onto pixels.
  const scale = (rect.w * rect.h) / total;
  let free: Rect = { ...rect };
  let cursor = 0;

  while (cursor < live.length) {
    const short = Math.min(free.w, free.h);
    if (short <= 0) break;

    // Grow the row while the worst aspect ratio keeps improving.
    let rowSum = 0;
    let rowMin = Infinity;
    let rowMax = 0;
    let best = Infinity;
    let end = cursor;

    while (end < live.length) {
      const area = (values[live[end] as number] ?? 0) * scale;
      const nextSum = rowSum + area;
      const nextMin = Math.min(rowMin, area);
      const nextMax = Math.max(rowMax, area);
      const ratio = worstAspect(nextSum, nextMin, nextMax, short);
      if (end > cursor && ratio > best) break;
      best = ratio;
      rowSum = nextSum;
      rowMin = nextMin;
      rowMax = nextMax;
      end++;
    }

    free = placeRow(values, live, cursor, end, rowSum, scale, free, out);
    cursor = end;
  }
  return out;
}

/**
 * The worst (largest) aspect ratio in a row of total area `sum` laid across a
 * side of length `short`, given the row's smallest and largest cell areas.
 */
function worstAspect(sum: number, min: number, max: number, short: number): number {
  if (sum <= 0 || min <= 0) return Infinity;
  const s2 = sum * sum;
  const short2 = short * short;
  return Math.max((short2 * max) / s2, s2 / (short2 * min));
}

/**
 * Places one row of cells along the shorter side of `free` and returns the
 * rectangle left over for the following rows.
 */
function placeRow(
  values: number[],
  live: number[],
  start: number,
  end: number,
  rowSum: number,
  scale: number,
  free: Rect,
  out: Rect[],
): Rect {
  const horizontal = free.w >= free.h;
  const along = horizontal ? free.h : free.w; // the row's cross length
  const depth = along > 0 ? rowSum / along : 0; // how far the row extends

  let offset = 0;
  for (let i = start; i < end; i++) {
    const idx = live[i] as number;
    const area = (values[idx] ?? 0) * scale;
    const extent = depth > 0 ? area / depth : 0;
    out[idx] = horizontal
      ? { x: free.x, y: free.y + offset, w: depth, h: extent }
      : { x: free.x + offset, y: free.y, w: extent, h: depth };
    offset += extent;
  }

  // Guard against floating-point drift eating or inventing pixels.
  const consumed = Math.min(depth, horizontal ? free.w : free.h);
  return horizontal
    ? { x: free.x + consumed, y: free.y, w: Math.max(free.w - consumed, 0), h: free.h }
    : { x: free.x, y: free.y + consumed, w: free.w, h: Math.max(free.h - consumed, 0) };
}

/** Shrinks a rectangle by `pad` on every side, never below zero size. */
export function inset(r: Rect, pad: number): Rect {
  const w = Math.max(r.w - pad * 2, 0);
  const h = Math.max(r.h - pad * 2, 0);
  return { x: r.x + pad, y: r.y + pad, w, h };
}

/** Reports whether a point falls inside a rectangle. */
export function contains(r: Rect, x: number, y: number): boolean {
  return x >= r.x && x < r.x + r.w && y >= r.y && y < r.y + r.h;
}
