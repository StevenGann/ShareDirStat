import { describe, expect, it } from 'vitest';
import { depthColor, extensionColor, mtimeBucket, mtimeColor } from './colors';

describe('extensionColor', () => {
  it('is stable for the same extension', () => {
    expect(extensionColor('mkv', false)).toBe(extensionColor('mkv', false));
    expect(extensionColor('mkv', true)).toBe(extensionColor('mkv', true));
  });

  it('is case-insensitive', () => {
    expect(extensionColor('MKV', false)).toBe(extensionColor('mkv', false));
  });

  it('separates the extensions a media share is full of', () => {
    const exts = ['mkv', 'mp4', 'avi', 'flac', 'mp3', 'jpg', 'png', 'iso', 'zip', 'txt', 'pdf', 'log'];
    const colors = new Set(exts.map((e) => extensionColor(e, false)));
    expect(colors.size).toBe(exts.length);
  });

  it('gives files without an extension a desaturated neutral', () => {
    const saturation = Number(/hsl\(\d+ (\d+)%/.exec(extensionColor('', false))?.[1]);
    expect(saturation).toBeLessThan(20);
  });

  it('produces valid CSS in both themes', () => {
    for (const dark of [false, true]) {
      for (const ext of ['mkv', '', 'a', 'verylongextension']) {
        expect(extensionColor(ext, dark)).toMatch(/^hsl\(\d+ \d+% \d+%\)$/);
      }
    }
  });

  it('keeps lightness mid-range so cells read on either theme', () => {
    for (const dark of [false, true]) {
      for (const ext of ['mkv', 'flac', 'iso', 'jpg']) {
        const l = Number(/hsl\(\d+ \d+% (\d+)%\)/.exec(extensionColor(ext, dark))?.[1]);
        expect(l).toBeGreaterThan(40);
        expect(l).toBeLessThan(75);
      }
    }
  });
});

describe('depthColor', () => {
  it('varies with depth and stays valid', () => {
    const shades = new Set([0, 1, 2, 3].map((d) => depthColor(d, false)));
    expect(shades.size).toBe(4);
    expect(depthColor(0, true)).toMatch(/^hsl\(/);
  });
});

describe('mtimeColor', () => {
  const now = Date.parse('2026-08-29T00:00:00Z');
  const day = 86_400_000;

  it('buckets by age', () => {
    expect(mtimeBucket(now - day, now)).toBe(0);
    expect(mtimeBucket(now - 20 * day, now)).toBe(1);
    expect(mtimeBucket(now - 2000 * day, now)).toBe(5);
    expect(mtimeColor(now - day, now, false)).not.toBe(mtimeColor(now - 2000 * day, now, false));
    expect(mtimeColor(now - day, now, false)).toBe(mtimeColor(now - 3 * day, now, false));
  });

  it('handles a future mtime without producing nonsense', () => {
    expect(mtimeBucket(now + 100000, now)).toBe(0);
    expect(mtimeColor(now + 100000, now, false)).toMatch(/^hsl\(/);
  });
});
