import { describe, expect, it } from 'vitest';
import { contains, inset, squarify, type Rect } from './squarify';

const RECT: Rect = { x: 0, y: 0, w: 600, h: 400 };
const area = (r: Rect) => r.w * r.h;

/** Do two rectangles overlap by more than a rounding error? */
function overlaps(a: Rect, b: Rect): boolean {
  const eps = 1e-6;
  return (
    a.x + a.w - eps > b.x && b.x + b.w - eps > a.x && a.y + a.h - eps > b.y && b.y + b.h - eps > a.y
  );
}

describe('squarify', () => {
  it('gives every cell an area proportional to its value', () => {
    const values = [600, 300, 200, 100, 50, 25];
    const total = values.reduce((a, b) => a + b, 0);
    const rects = squarify(values, RECT);
    expect(rects).toHaveLength(values.length);
    rects.forEach((r, i) => {
      const expected = ((values[i] as number) / total) * area(RECT);
      expect(area(r)).toBeCloseTo(expected, 4);
    });
  });

  it('fills the rectangle exactly', () => {
    const rects = squarify([5, 4, 3, 2, 1], RECT);
    const total = rects.reduce((sum, r) => sum + area(r), 0);
    expect(total).toBeCloseTo(area(RECT), 4);
  });

  it('produces no overlapping cells', () => {
    const values = Array.from({ length: 40 }, (_, i) => 100 - i * 2);
    const rects = squarify(values, RECT);
    for (let i = 0; i < rects.length; i++) {
      for (let j = i + 1; j < rects.length; j++) {
        expect(overlaps(rects[i] as Rect, rects[j] as Rect)).toBe(false);
      }
    }
  });

  it('keeps every cell inside the bounds', () => {
    const rects = squarify([10, 9, 8, 7, 6, 5, 4, 3, 2, 1], RECT);
    for (const r of rects) {
      expect(r.x).toBeGreaterThanOrEqual(RECT.x - 1e-6);
      expect(r.y).toBeGreaterThanOrEqual(RECT.y - 1e-6);
      expect(r.x + r.w).toBeLessThanOrEqual(RECT.x + RECT.w + 1e-6);
      expect(r.y + r.h).toBeLessThanOrEqual(RECT.y + RECT.h + 1e-6);
    }
  });

  it('keeps cells reasonably square', () => {
    // The whole point of squarifying: a naive slice-and-dice of 30 equal
    // values in a 600x400 box yields 20:1 slivers.
    const rects = squarify(Array.from({ length: 30 }, () => 1), RECT);
    const worst = Math.max(...rects.map((r) => Math.max(r.w / r.h, r.h / r.w)));
    expect(worst).toBeLessThan(3);
  });

  it('handles a single value by filling the whole rectangle', () => {
    const [only] = squarify([42], RECT);
    // Exact float equality is not meaningful here: the area round-trips
    // through a scale factor, so compare within a pixel.
    expect(only?.x).toBeCloseTo(RECT.x, 6);
    expect(only?.y).toBeCloseTo(RECT.y, 6);
    expect(only?.w).toBeCloseTo(RECT.w, 6);
    expect(only?.h).toBeCloseTo(RECT.h, 6);
  });

  it('collapses zero and negative values instead of dropping them', () => {
    const rects = squarify([100, 0, 50, -5], RECT);
    expect(rects).toHaveLength(4);
    expect(area(rects[1] as Rect)).toBe(0);
    expect(area(rects[3] as Rect)).toBe(0);
    // The positive values still tile the whole box.
    expect(area(rects[0] as Rect) + area(rects[2] as Rect)).toBeCloseTo(area(RECT), 4);
  });

  it('returns empty cells for degenerate input', () => {
    expect(squarify([], RECT)).toEqual([]);
    expect(squarify([1, 2], { x: 0, y: 0, w: 0, h: 100 }).every((r) => area(r) === 0)).toBe(true);
    expect(squarify([0, 0], RECT).every((r) => area(r) === 0)).toBe(true);
  });

  it('tiles a tall rectangle as well as a wide one', () => {
    const tall = { x: 10, y: 20, w: 200, h: 900 };
    const rects = squarify([9, 7, 5, 3, 1], tall);
    const total = rects.reduce((sum, r) => sum + area(r), 0);
    expect(total).toBeCloseTo(area(tall), 4);
  });

  it('scales with a large number of cells', () => {
    const values = Array.from({ length: 5000 }, (_, i) => 5000 - i);
    const started = performance.now();
    const rects = squarify(values, { x: 0, y: 0, w: 1600, h: 900 });
    expect(performance.now() - started).toBeLessThan(200);
    const total = rects.reduce((sum, r) => sum + area(r), 0);
    expect(total).toBeCloseTo(1600 * 900, 2);
  });
});

describe('inset', () => {
  it('shrinks on every side and never goes negative', () => {
    expect(inset({ x: 10, y: 10, w: 100, h: 50 }, 2)).toEqual({ x: 12, y: 12, w: 96, h: 46 });
    expect(inset({ x: 0, y: 0, w: 3, h: 3 }, 5)).toEqual({ x: 5, y: 5, w: 0, h: 0 });
  });
});

describe('contains', () => {
  it('treats the rectangle as half-open', () => {
    const r = { x: 0, y: 0, w: 10, h: 10 };
    expect(contains(r, 0, 0)).toBe(true);
    expect(contains(r, 9.9, 9.9)).toBe(true);
    expect(contains(r, 10, 5)).toBe(false);
    expect(contains(r, -1, 5)).toBe(false);
  });
});
