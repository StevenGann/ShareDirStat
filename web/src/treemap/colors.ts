/**
 * Deterministic colours for treemap cells (FR-UI-14).
 *
 * The same extension always gets the same colour, in this session and every
 * future one, because the colour is derived from the extension string itself
 * rather than from its rank in a list that shifts as files come and go.
 *
 * Hues sit at mid lightness so a cell reads the same whether the surrounding
 * chrome is light or dark; only saturation is nudged for dark mode, where
 * fully saturated fills glare.
 */

/**
 * Twenty-four hues, spaced to stay distinguishable rather than mathematically
 * even: the yellow-green band is thinned out because neighbouring hues there
 * are hard to tell apart, and the blues are given more room because that is
 * where large media files usually land.
 */
const HUES = [
  4, 18, 32, 44, 54, 68, 84, 104, 124, 142, 158, 172, 186, 198, 210, 220, 232, 244, 256, 270, 286,
  302, 320, 340,
];

/** Files with no extension get a neutral so they read as "uncategorised". */
const NO_EXT_HUE = 210;

export type ColorScheme = 'extension' | 'depth' | 'mtime';

/** FNV-1a: small, fast, and well spread for short strings. */
function hash(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

interface Hsl {
  h: number;
  s: number;
  l: number;
}

function hsl({ h, s, l }: Hsl): string {
  return `hsl(${h} ${s}% ${l}%)`;
}

/** The base colour for a file extension. */
export function extensionColor(ext: string, dark: boolean): string {
  return hsl(extensionHsl(ext, dark));
}

function extensionHsl(ext: string, dark: boolean): Hsl {
  const normalized = ext.toLowerCase();
  if (normalized === '') {
    return { h: NO_EXT_HUE, s: dark ? 8 : 10, l: dark ? 46 : 62 };
  }
  const hue = HUES[hash(normalized) % HUES.length] as number;
  // A second, independent slice of the hash varies saturation a little, so
  // two extensions landing on the same hue still look different.
  const jitter = ((hash(`${normalized}#s`) >>> 8) % 14) - 7;
  return {
    h: hue,
    s: (dark ? 46 : 58) + jitter,
    l: dark ? 52 : 58,
  };
}

/**
 * Colour by depth in the tree: an even ramp from the accent hue.
 *
 * The ramp is longer than the deepest level the server will emit (the default
 * max_depth is 8, i.e. depths 0..8), so the wrap cannot make the deepest
 * visible level identical to the root -- which a period of 8 guaranteed.
 */
const DEPTH_RAMP = 12;

export function depthColor(depth: number, dark: boolean): string {
  const step = Math.abs(depth) % DEPTH_RAMP;
  return hsl({
    h: 205 + step * 5,
    s: dark ? 40 : 48,
    l: (dark ? 34 : 72) + step * (dark ? 3 : -2.5),
  });
}

/** The mtime buckets, oldest last, used for colouring and for the legend. */
export const MTIME_BUCKETS = [
  'this week',
  'this month',
  '6 months',
  'this year',
  '3 years',
  'older',
] as const;

const MTIME_HUES = [8, 30, 48, 150, 200, 232];

/** The bucket index for a modification time. */
export function mtimeBucket(mtimeMs: number, now: number): number {
  // An unparseable mtime would otherwise fall through every `<` comparison
  // (they are all false for NaN) and land silently in "older", where it is
  // indistinguishable from a genuinely ancient file. Be explicit about it.
  if (!Number.isFinite(mtimeMs)) return MTIME_BUCKETS.length - 1;
  const days = Math.max((now - mtimeMs) / 86_400_000, 0);
  if (days < 7) return 0;
  if (days < 30) return 1;
  if (days < 180) return 2;
  if (days < 365) return 3;
  if (days < 1095) return 4;
  return 5;
}

/** Colour by age: recent files are warm, old files cool. */
export function mtimeColor(mtimeMs: number, now: number, dark: boolean): string {
  const hue = MTIME_HUES[mtimeBucket(mtimeMs, now)] as number;
  return hsl({ h: hue, s: dark ? 42 : 54, l: dark ? 50 : 60 });
}

/**
 * A cushioned fill for one cell. WinDirStat computes a true per-pixel cushion
 * surface; at 10 000 cells that is far too slow in a browser, so a diagonal
 * gradient stands in — it gives the same pillowed read at a fraction of the
 * cost.
 */
export function cushionGradient(
  ctx: { createLinearGradient(x0: number, y0: number, x1: number, y1: number): CanvasGradient },
  x: number,
  y: number,
  w: number,
  h: number,
  dark: boolean,
): CanvasGradient | null {
  if (w < 4 || h < 4) return null;
  const g = ctx.createLinearGradient(x, y, x + w, y + h);
  const light = dark ? 0.26 : 0.44;
  const shade = dark ? 0.34 : 0.22;
  g.addColorStop(0, `rgba(255,255,255,${light})`);
  g.addColorStop(0.45, 'rgba(255,255,255,0)');
  g.addColorStop(0.62, 'rgba(0,0,0,0)');
  g.addColorStop(1, `rgba(0,0,0,${shade})`);
  return g;
}
