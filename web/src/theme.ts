import { useSyncExternalStore } from 'react';

/**
 * Manual theme override (FR-UI-24). 'system' follows prefers-color-scheme;
 * 'light'/'dark' pin the theme via a data-theme attribute on <html> that the
 * stylesheet keys its variable blocks on. The canvas treemap reads its
 * palette from those variables, so everything that paints colours subscribes
 * through useResolvedDark and re-reads when either the override or the OS
 * preference changes.
 */
export type ThemePref = 'system' | 'light' | 'dark';

const KEY = 'sds.theme';
const EVENT = 'sds:theme';

function readStoredPref(): ThemePref {
  try {
    const raw = localStorage.getItem(KEY);
    const value = raw === null ? 'system' : (JSON.parse(raw) as unknown);
    return value === 'light' || value === 'dark' ? value : 'system';
  } catch {
    return 'system';
  }
}

// Cached so useSyncExternalStore snapshots do not parse JSON per call.
let current: ThemePref | null = null;

export function getThemePref(): ThemePref {
  if (current === null) current = readStoredPref();
  return current;
}

/** Applies and persists a preference; safe to call before React mounts. */
export function applyThemePref(pref: ThemePref): void {
  current = pref;
  const root = document.documentElement;
  if (pref === 'system') delete root.dataset.theme;
  else root.dataset.theme = pref;
  try {
    if (pref === 'system') localStorage.removeItem(KEY);
    else localStorage.setItem(KEY, JSON.stringify(pref));
  } catch {
    /* private browsing or blocked storage: the choice just won't persist */
  }
  window.dispatchEvent(new Event(EVENT));
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener(EVENT, onChange);
  const mq = window.matchMedia?.('(prefers-color-scheme: dark)');
  mq?.addEventListener('change', onChange);
  return () => {
    window.removeEventListener(EVENT, onChange);
    mq?.removeEventListener('change', onChange);
  };
}

export function useThemePref(): [ThemePref, (pref: ThemePref) => void] {
  const pref = useSyncExternalStore(subscribe, getThemePref);
  return [pref, applyThemePref];
}

/** Whether the page is dark right now, override and OS preference combined. */
export function useResolvedDark(): boolean {
  return useSyncExternalStore(subscribe, resolveDark);
}

function resolveDark(): boolean {
  const pref = getThemePref();
  if (pref === 'dark') return true;
  if (pref === 'light') return false;
  return window.matchMedia?.('(prefers-color-scheme: dark)').matches ?? false;
}
