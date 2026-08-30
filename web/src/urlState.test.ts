import { describe, expect, it } from 'vitest';
import { buildHash, parseHash, sameState } from './urlState';

describe('parseHash', () => {
  it('reads share, path and treemap root', () => {
    expect(parseHash('#/media/Movies/2019?root=Movies')).toEqual({
      share: 'media',
      path: 'Movies/2019',
      root: 'Movies',
    });
  });

  it('handles a share on its own', () => {
    expect(parseHash('#/media')).toEqual({ share: 'media', path: '', root: '' });
  });

  it('treats an empty hash as no state', () => {
    for (const h of ['', '#', '#/']) {
      expect(parseHash(h)).toEqual({ share: '', path: '', root: '' });
    }
  });

  it('decodes names with awkward characters', () => {
    const hash = buildHash({ share: 'media', path: 'My Films/#1 & best?.mkv', root: 'My Films' });
    expect(parseHash(hash)).toEqual({
      share: 'media',
      path: 'My Films/#1 & best?.mkv',
      root: 'My Films',
    });
  });

  it('survives a malformed escape instead of throwing', () => {
    expect(() => parseHash('#/media/bad%ZZname')).not.toThrow();
    expect(parseHash('#/media/bad%ZZname').path).toBe('bad%ZZname');
  });

  it('ignores stray separators', () => {
    expect(parseHash('#//media//a//b/')).toEqual({ share: 'media', path: 'a/b', root: '' });
  });
});

describe('buildHash', () => {
  it('round-trips through parseHash', () => {
    const cases = [
      { share: 'media', path: '', root: '' },
      { share: 'media', path: 'Movies', root: '' },
      { share: 'media', path: 'Movies/2019/film.mkv', root: 'Movies/2019' },
      { share: 'back-ups_2', path: 'a b/c+d/e%f', root: '' },
    ];
    for (const state of cases) {
      expect(parseHash(buildHash(state))).toEqual(state);
    }
  });

  it('omits defaults to keep the URL short', () => {
    expect(buildHash({ share: 'media', path: '', root: '' })).toBe('#/media');
    expect(buildHash({ share: '', path: 'x', root: 'y' })).toBe('#/');
  });

  it('keeps path separators readable', () => {
    expect(buildHash({ share: 'media', path: 'a/b/c', root: '' })).toBe('#/media/a/b/c');
  });
});

describe('sameState', () => {
  it('compares every field', () => {
    const a = { share: 'm', path: 'p', root: 'r' };
    expect(sameState(a, { ...a })).toBe(true);
    expect(sameState(a, { ...a, root: 'other' })).toBe(false);
  });
});
