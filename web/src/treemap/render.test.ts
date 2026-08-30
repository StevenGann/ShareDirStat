import { describe, expect, it } from 'vitest';
import type { TreemapNode } from '../api';
import { layoutTreemap, type Cell } from './layout';
import {
  ancestorsOf,
  cellColor,
  drawHighlight,
  fitText,
  renderTreemap,
  type Ctx2D,
  type RenderOptions,
  type RenderTheme,
} from './render';

/** Records every drawing call so tests can assert on what was painted. */
class RecordingCtx implements Ctx2D {
  fillStyle: string | CanvasGradient | CanvasPattern = '';
  strokeStyle: string | CanvasGradient | CanvasPattern = '';
  lineWidth = 1;
  font = '';
  textBaseline: CanvasTextBaseline = 'alphabetic';
  calls: { op: string; args: unknown[]; fill?: string }[] = [];
  /** Width per character, so measureText is predictable. */
  charWidth = 6;

  private record(op: string, ...args: unknown[]) {
    this.calls.push({ op, args, fill: typeof this.fillStyle === 'string' ? this.fillStyle : 'gradient' });
  }
  save() { this.record('save'); }
  restore() { this.record('restore'); }
  clearRect(...a: number[]) { this.record('clearRect', ...a); }
  fillRect(...a: number[]) { this.record('fillRect', ...a); }
  strokeRect(...a: number[]) { this.record('strokeRect', ...a); }
  beginPath() { this.record('beginPath'); }
  rect(...a: number[]) { this.record('rect', ...a); }
  moveTo(...a: number[]) { this.record('moveTo', ...a); }
  lineTo(...a: number[]) { this.record('lineTo', ...a); }
  stroke() { this.record('stroke'); }
  clip() { this.record('clip'); }
  fillText(text: string, x: number, y: number) { this.record('fillText', text, x, y); }
  measureText(text: string) { return { width: text.length * this.charWidth }; }
  createLinearGradient() {
    const stops: [number, string][] = [];
    return { addColorStop: (o: number, c: string) => stops.push([o, c]) } as unknown as CanvasGradient;
  }

  ops(op: string) { return this.calls.filter((c) => c.op === op); }
  texts() { return this.ops('fillText').map((c) => c.args[0] as string); }
}

const base = {
  name: '', kind: 'file' as const, alloc: 0, mtime: '2026-06-01T00:00:00Z',
  mode: '0644', perms: '-rw-r--r--', uid: 0, gid: 0,
  flags: { partial: false, mountpoint: false, hardlink_dup: false, excluded: false, unscanned: false },
  files: 0, dirs: 0, children: 0, pct_of_parent: 0, pct_of_share: 0,
};

const file = (path: string, size: number, ext?: string): TreemapNode => ({
  ...base, name: path.split('/').pop() ?? path, path, size, alloc: size, ext,
});

const dir = (path: string, kids: TreemapNode[], truncated?: { children: number; size: number; alloc: number }): TreemapNode => ({
  ...base, kind: 'dir', name: path.split('/').pop() ?? path, path,
  size: kids.reduce((s, k) => s + k.size, 0) + (truncated?.size ?? 0),
  alloc: kids.reduce((s, k) => s + k.alloc, 0) + (truncated?.alloc ?? 0),
  children: kids.length, children_list: kids, truncated,
});

const THEME: RenderTheme = {
  dark: false,
  background: '#ffffff',
  frame: '#cccccc',
  headerBackground: '#eeeeee',
  headerText: '#222222',
  selection: '#0066cc',
  hover: '#333333',
  truncatedFill: '#dddddd',
  truncatedLine: '#999999',
};

const VIEW = { x: 0, y: 0, w: 800, h: 500 };

function options(over: Partial<RenderOptions> = {}): RenderOptions {
  return {
    theme: THEME,
    width: VIEW.w,
    height: VIEW.h,
    basis: 'apparent',
    scheme: 'extension',
    cushion: true,
    now: Date.parse('2026-08-29T00:00:00Z'),
    ...over,
  };
}

describe('renderTreemap', () => {
  const root = dir('', [
    dir('Movies', [file('Movies/a.mkv', 6000, 'mkv'), file('Movies/b.mp4', 3000, 'mp4')]),
    file('big.iso', 4000, 'iso'),
  ]);

  it('clears and paints the background before any cell', () => {
    const ctx = new RecordingCtx();
    renderTreemap(ctx, layoutTreemap(root, VIEW, { basis: 'apparent' }), options());
    expect(ctx.calls[0]?.op).toBe('clearRect');
    expect(ctx.calls[1]?.op).toBe('fillRect');
    expect(ctx.calls[1]?.fill).toBe(THEME.background);
  });

  it('paints one filled rectangle per cell', () => {
    const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });
    const ctx = new RecordingCtx();
    renderTreemap(ctx, cells, options({ cushion: false }));
    // One background fill plus one per cell, plus a header fill per labelled
    // directory.
    const headers = cells.filter((c) => c.header).length;
    expect(ctx.ops('fillRect')).toHaveLength(1 + cells.length + headers);
  });

  it('labels directories that have a header strip', () => {
    const ctx = new RecordingCtx();
    renderTreemap(ctx, layoutTreemap(root, VIEW, { basis: 'apparent' }), options());
    expect(ctx.texts()).toContain('Movies');
  });

  it('does not label a directory with no room for a header', () => {
    const ctx = new RecordingCtx();
    const cells = layoutTreemap(root, { x: 0, y: 0, w: 200, h: 18 }, { basis: 'apparent' });
    renderTreemap(ctx, cells, options({ width: 200, height: 18 }));
    expect(ctx.texts()).toHaveLength(0);
  });

  it('adds a cushion pass only when cushions are on', () => {
    const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });
    const plain = new RecordingCtx();
    renderTreemap(plain, cells, options({ cushion: false }));
    const cushioned = new RecordingCtx();
    renderTreemap(cushioned, cells, options({ cushion: true }));
    expect(cushioned.ops('fillRect').length).toBeGreaterThan(plain.ops('fillRect').length);
    expect(cushioned.calls.some((c) => c.fill === 'gradient')).toBe(true);
  });

  it('hatches the truncated cell', () => {
    const withTrunc = dir('', [file('a.mkv', 6000, 'mkv')], { children: 9, size: 4000, alloc: 4000 });
    const ctx = new RecordingCtx();
    renderTreemap(ctx, layoutTreemap(withTrunc, VIEW, { basis: 'apparent' }), options());
    expect(ctx.ops('stroke').length).toBeGreaterThan(0);
    expect(ctx.ops('lineTo').length).toBeGreaterThan(5);
  });

  it('dims cells that do not match the highlighted extension', () => {
    const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });
    const plain = new RecordingCtx();
    renderTreemap(plain, cells, options({ cushion: false }));
    const highlighted = new RecordingCtx();
    renderTreemap(highlighted, cells, options({ cushion: false, highlightExt: 'mkv' }));
    // Two of the three leaves are not .mkv, so two extra dimming fills.
    expect(highlighted.ops('fillRect').length).toBe(plain.ops('fillRect').length + 2);
  });
});

describe('cellColor', () => {
  const cells = layoutTreemap(
    dir('', [file('a.mkv', 5000, 'mkv'), file('b.mkv', 3000, 'mkv'), file('c.iso', 2000, 'iso')]),
    VIEW,
    { basis: 'apparent' },
  );

  it('gives the same colour to the same extension', () => {
    expect(cellColor(cells[0] as Cell, options())).toBe(cellColor(cells[1] as Cell, options()));
    expect(cellColor(cells[0] as Cell, options())).not.toBe(cellColor(cells[2] as Cell, options()));
  });

  it('switches scheme', () => {
    const byExt = cellColor(cells[0] as Cell, options({ scheme: 'extension' }));
    const byDepth = cellColor(cells[0] as Cell, options({ scheme: 'depth' }));
    const byTime = cellColor(cells[0] as Cell, options({ scheme: 'mtime' }));
    expect(new Set([byExt, byDepth, byTime]).size).toBeGreaterThan(1);
  });

  it('uses the truncated fill for the summary cell', () => {
    const withTrunc = layoutTreemap(
      dir('', [file('a', 6000)], { children: 3, size: 4000, alloc: 4000 }),
      VIEW,
      { basis: 'apparent' },
    );
    const trunc = withTrunc.find((c) => c.kind === 'truncated') as Cell;
    expect(cellColor(trunc, options())).toBe(THEME.truncatedFill);
  });
});

describe('fitText', () => {
  const ctx = new RecordingCtx();

  it('returns the text unchanged when it fits', () => {
    expect(fitText(ctx, 'movie.mkv', 100)).toBe('movie.mkv');
  });

  it('truncates with an ellipsis to fit', () => {
    const out = fitText(ctx, 'a-very-long-file-name.mkv', 60);
    expect(out.endsWith('…')).toBe(true);
    expect(ctx.measureText(out).width).toBeLessThanOrEqual(60);
    expect(out.length).toBeGreaterThan(1);
  });

  it('returns nothing when even an ellipsis will not fit', () => {
    expect(fitText(ctx, 'anything', 3)).toBe('');
    expect(fitText(ctx, 'anything', 0)).toBe('');
  });
});

describe('ancestorsOf', () => {
  it('lists the directory frames containing a cell', () => {
    const root = dir('', [dir('a', [dir('a/b', [file('a/b/c.mkv', 1000)])])]);
    const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });
    const leaf = cells.find((c) => c.key === 'a/b/c.mkv') as Cell;
    expect(ancestorsOf(cells, leaf).map((c) => c.key)).toEqual(['a', 'a/b']);
  });

  it('returns nothing for a top-level cell', () => {
    const cells = layoutTreemap(dir('', [file('x', 1)]), VIEW, { basis: 'apparent' });
    expect(ancestorsOf(cells, cells[0] as Cell)).toEqual([]);
  });
});

describe('drawHighlight', () => {
  it('outlines the cell and its ancestors', () => {
    const root = dir('', [dir('a', [file('a/x.mkv', 1000)])]);
    const cells = layoutTreemap(root, VIEW, { basis: 'apparent' });
    const leaf = cells.find((c) => c.key === 'a/x.mkv') as Cell;
    const ctx = new RecordingCtx();
    drawHighlight(ctx, leaf, ancestorsOf(cells, leaf), THEME, THEME.hover);
    expect(ctx.ops('strokeRect')).toHaveLength(2);
  });

  it('skips rectangles too small to outline', () => {
    const ctx = new RecordingCtx();
    const tiny: Cell = {
      key: 't', node: null, rect: { x: 0, y: 0, w: 1, h: 1 },
      content: null, header: null, depth: 0, kind: 'leaf', truncatedSize: 0, truncatedCount: 0,
    };
    drawHighlight(ctx, tiny, [], THEME, THEME.hover);
    expect(ctx.ops('strokeRect')).toHaveLength(0);
  });
});
