import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it } from 'vitest';
import { applyThemePref, getThemePref, useResolvedDark, useThemePref } from './theme';

afterEach(() => {
  act(() => applyThemePref('system'));
  localStorage.clear();
});

describe('theme override (FR-UI-24)', () => {
  it('defaults to following the system', () => {
    expect(getThemePref()).toBe('system');
    expect(document.documentElement.dataset.theme).toBeUndefined();
  });

  it('pins the theme via a root attribute and persists the choice', () => {
    act(() => applyThemePref('dark'));
    expect(document.documentElement.dataset.theme).toBe('dark');
    expect(localStorage.getItem('sds.theme')).toBe('"dark"');

    act(() => applyThemePref('light'));
    expect(document.documentElement.dataset.theme).toBe('light');
    expect(localStorage.getItem('sds.theme')).toBe('"light"');
  });

  it('removes the attribute and the stored key when set back to system', () => {
    act(() => applyThemePref('dark'));
    act(() => applyThemePref('system'));
    expect(document.documentElement.dataset.theme).toBeUndefined();
    expect(localStorage.getItem('sds.theme')).toBeNull();
  });

  it('notifies useThemePref and useResolvedDark subscribers', () => {
    const pref = renderHook(() => useThemePref());
    const dark = renderHook(() => useResolvedDark());
    expect(pref.result.current[0]).toBe('system');
    // The test stub's matchMedia never matches, so system resolves light.
    expect(dark.result.current).toBe(false);

    act(() => pref.result.current[1]('dark'));
    expect(pref.result.current[0]).toBe('dark');
    expect(dark.result.current).toBe(true);
  });
});
