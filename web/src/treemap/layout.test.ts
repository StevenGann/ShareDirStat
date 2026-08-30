import { describe, expect, it } from 'vitest';
import type { TreemapNode } from '../api';
import { cellAt, cellForPath, layoutTreemap, type Cell } from './layout';

const base = {
  name: '',
  kind: 'file' as const,
  alloc: 0,
  mtime: '2026-01-01T00:00:00Z',
  mode: '0644',
  perms: '-rw-r--r--',
  uid: 0,
  gid: 0,
  flags: {
    partial: false,
    mountpoint: false,
    hardlink_dup: false,
    excluded: false,
    unscanned: false,
  },
  files: 0,
  dirs: 0,
  children: 0,
  pct_of_parent: 0,
  pct_of_share: 0,
};

const file = (path: string, size: number): TreemapNode => ({
  ...base,
  name: path.split('/').pop() ?? path,
  path,
  size,
  alloc: size,
});

const dir = (
  path: string,
  kids: TreemapNode[],
  truncated?: { children: number; size: number; alloc: number },
): TreemapNode => ({
  ...base,
  kind: 'dir',
  name: path.split('/').pop() ?? path,
  path,
  size: kids.reduce((s, k) => s + k.size, 0) + (truncated?.size ?? 0),
  alloc: kids.reduce((s, k) => s + k.alloc, 0) + (truncated?.alloc ?? 0),
  children: kids.length,
  children_list: kids,
  truncated,
});

const VIEW = { x: 0, y: 0, w: 800, h: 500 };
const area = (c: Cell) => c.rect.w * c.rect.h;

describe('layoutTreemap', () => {
  it('emits a cell per visible child, in paint order', () => {
    const root = dir('', [
      dir('Movies', [file('Movies/a.mkv', 6000), file('Movies/b.mkv', 4000)]),
      file('big.iso', 5000),
    ]);
    const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });
    expect(cells.map((c) => c.key)).toEqual(['Movies', 'Movies/a.mkv', 'Movies/b.mkv', 'big.iso']);
    // A directory is painted before the children drawn inside it.
    expect(cells[0]?.kind).toBe('dir');
    expect(cells[1]?.depth).toBe(1);
  });

  it('gives sibling cells areas proportional to their sizes', () => {
    const root = dir('', [file('a', 3000), file('b', 1000)]);
    const [a, b] = layoutTreemap(root, VIEW, { basis: 'apparent' });
    expect(area(a as Cell) / area(b as Cell)).toBeCloseTo(3, 1);
  });

  it('nests children strictly inside their parent frame', () => {
    const root = dir('', [dir('d', [file('d/x', 700), file('d/y', 300)]), file('e', 1000)]);
    const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });
    const parent = cells.find((c) => c.key === 'd') as Cell;
    for (const child of cells.filter((c) => c.key.startsWith('d/'))) {
      expect(child.rect.x).toBeGreaterThanOrEqual(parent.rect.x);
      expect(child.rect.y).toBeGreaterThanOrEqual(parent.rect.y);
      expect(child.rect.x + child.rect.w).toBeLessThanOrEqual(parent.rect.x + parent.rect.w + 1e-6);
      expect(child.rect.y + child.rect.h).toBeLessThanOrEqual(parent.rect.y + parent.rect.h + 1e-6);
    }
  });

  it('reserves a header strip only when the directory is big enough', () => {
    const big = dir('', [dir('d', [file('d/x', 1)])]);
    const [cell] = layoutTreemap(big, VIEW, { basis: 'apparent' });
    expect(cell?.header).not.toBeNull();

    // The same directory in a sliver of a viewport gets no label strip.
    const tiny = layoutTreemap(big, { x: 0, y: 0, w: 300, h: 20 }, { basis: 'apparent' });
    expect(tiny[0]?.header).toBeNull();
  });

  it('draws the truncated remainder so no area is lost', () => {
    const root = dir('', [file('a', 6000)], { children: 42, size: 4000, alloc: 4000 });
    const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });
    const trunc = cells.find((c) => c.kind === 'truncated');
    expect(trunc).toBeDefined();
    expect(trunc?.truncatedCount).toBe(42);
    expect(trunc?.node).toBeNull();
    const total = cells.filter((c) => c.depth === 0).reduce((s, c) => s + area(c), 0);
    expect(total).toBeCloseTo(VIEW.w * VIEW.h, 2);
  });

  it('skips cells too small to see or click (FR-UI-16)', () => {
    const kids = [
      file('big', 1_000_000),
      ...Array.from({ length: 400 }, (_, i) => file(`tiny${i}`, 1)),
    ];
    const cells = layoutTreemap(dir('', kids), VIEW, { basis: 'apparent', minCell: 3 });
    expect(cells.length).toBeLessThan(kids.length);
    for (const c of cells) {
      expect(c.rect.w).toBeGreaterThanOrEqual(3);
      expect(c.rect.h).toBeGreaterThanOrEqual(3);
    }
  });

  it('honours the allocated basis', () => {
    const sparse: TreemapNode = { ...file('sparse.img', 1_000_000), alloc: 4096 };
    const real: TreemapNode = { ...file('real.bin', 100_000), alloc: 100_000 };
    const root: TreemapNode = { ...dir('', [sparse, real]), size: 1_100_000, alloc: 104_096 };
    const byAlloc = layoutTreemap(root, VIEW, { basis: 'allocated' });
    const sparseCell = byAlloc.find((c) => c.key === 'sparse.img') as Cell;
    const realCell = byAlloc.find((c) => c.key === 'real.bin') as Cell;
    expect(area(realCell)).toBeGreaterThan(area(sparseCell));
  });

  it('returns nothing for a degenerate viewport', () => {
    const root = dir('', [file('a', 10)]);
    expect(layoutTreemap(root, { x: 0, y: 0, w: 0, h: 500 }, { basis: 'apparent' })).toEqual([]);
    expect(layoutTreemap(dir('', []), VIEW, { basis: 'apparent' })).toEqual([]);
  });

  it('respects the cell cap', () => {
    const kids = Array.from({ length: 5000 }, (_, i) => file(`f${i}`, 5000 - i));
    const cells = layoutTreemap(
      dir('', kids),
      { x: 0, y: 0, w: 2000, h: 1500 },
      { basis: 'apparent', maxCells: 100 },
    );
    expect(cells.length).toBeLessThanOrEqual(100);
  });

  it('lays out thousands of cells quickly (NFR-6)', () => {
    const kids = Array.from({ length: 200 }, (_, i) =>
      dir(
        `d${i}`,
        Array.from({ length: 50 }, (_, j) => file(`d${i}/f${j}`, 50 - j + 1)),
      ),
    );
    const started = performance.now();
    const cells = layoutTreemap(
      dir('', kids),
      { x: 0, y: 0, w: 1600, h: 900 },
      { basis: 'apparent' },
    );
    const elapsed = performance.now() - started;
    expect(cells.length).toBeGreaterThan(1000);
    expect(elapsed).toBeLessThan(100);
  });
});

describe('cellAt', () => {
  const root = dir('', [dir('d', [file('d/x', 700), file('d/y', 300)]), file('e', 1000)]);
  const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });

  it('returns the deepest cell under the point', () => {
    const leaf = cells.find((c) => c.key === 'd/x') as Cell;
    const hit = cellAt(cells, leaf.rect.x + leaf.rect.w / 2, leaf.rect.y + leaf.rect.h / 2);
    expect(hit?.key).toBe('d/x');
  });

  it('returns null outside every cell', () => {
    expect(cellAt(cells, -5, -5)).toBeNull();
    expect(cellAt(cells, 10_000, 10_000)).toBeNull();
  });
});

describe('cellForPath', () => {
  it('finds a laid-out node by path', () => {
    const cells = layoutTreemap(dir('', [file('a', 1)]), VIEW, { basis: 'apparent' });
    expect(cellForPath(cells, 'a')?.key).toBe('a');
    expect(cellForPath(cells, 'missing')).toBeNull();
    expect(cellForPath(cells, null)).toBeNull();
  });
});
