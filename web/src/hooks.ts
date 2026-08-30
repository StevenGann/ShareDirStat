import { useEffect, useRef, useState } from 'react';

export interface AsyncState<T> {
  data: T | null;
  error: string | null;
  loading: boolean;
}

/**
 * Loads data whenever `key` changes, keeping the result tagged with the key
 * it belongs to.
 *
 * State is only ever written from the promise callbacks, never synchronously
 * while the effect runs: "loading" is derived by comparing the stored key
 * with the current one, so a stale result can never be mistaken for a fresh
 * one and no extra render is needed to clear it.
 */
export function useAsyncData<T>(key: string, load: () => Promise<T>, enabled = true): AsyncState<T> {
  const [state, setState] = useState<{ key: string; data: T | null; error: string | null }>({
    key: '',
    data: null,
    error: null,
  });

  // The loader closes over props that change on every render; keeping it in a
  // ref means only `key` decides when to refetch.
  const loadRef = useRef(load);
  useEffect(() => {
    loadRef.current = load;
  }, [load]);

  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    loadRef
      .current()
      .then((data) => {
        if (!cancelled) setState({ key, data, error: null });
      })
      .catch((e: unknown) => {
        if (!cancelled) setState({ key, data: null, error: (e as Error).message });
      });
    return () => {
      cancelled = true;
    };
  }, [key, enabled]);

  const fresh = state.key === key;
  return {
    data: enabled && fresh ? state.data : null,
    error: fresh ? state.error : null,
    loading: enabled && !fresh,
  };
}

/**
 * The current time, refreshed on an interval rather than read during render.
 * Used by the age colour scheme, whose buckets are coarse enough that an
 * hourly tick is plenty.
 */
export function useNow(intervalMs = 3_600_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), intervalMs);
    return () => window.clearInterval(id);
  }, [intervalMs]);
  return now;
}

/** Tracks the viewer's colour scheme so canvas colours match the CSS. */
export function usePrefersDark(): boolean {
  const [dark, setDark] = useState(() =>
    typeof window !== 'undefined' && window.matchMedia
      ? window.matchMedia('(prefers-color-scheme: dark)').matches
      : false,
  );
  useEffect(() => {
    if (!window.matchMedia) return;
    const mq = window.matchMedia('(prefers-color-scheme: dark)');
    const update = (e: MediaQueryListEvent) => setDark(e.matches);
    mq.addEventListener('change', update);
    return () => mq.removeEventListener('change', update);
  }, []);
  return dark;
}

/** Observes an element's size without reading layout during render. */
export function useElementSize<T extends HTMLElement>(): [
  React.RefObject<T | null>,
  { w: number; h: number },
] {
  const ref = useRef<T | null>(null);
  const [size, setSize] = useState({ w: 0, h: 0 });
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    const ro = new ResizeObserver((entries) => {
      const box = entries[0]?.contentRect;
      if (box) setSize({ w: Math.floor(box.width), h: Math.floor(box.height) });
    });
    ro.observe(el);
    return () => ro.disconnect();
  }, []);
  return [ref, size];
}
