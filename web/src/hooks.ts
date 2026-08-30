import { useCallback, useEffect, useMemo, useRef, useState } from 'react';

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

export interface LongPress {
  handlers: {
    onPointerDown: (e: React.PointerEvent) => void;
    onPointerMove: (e: React.PointerEvent) => void;
    onPointerUp: () => void;
    onPointerCancel: () => void;
  };
  /** Abandons a pending press (e.g. when a native contextmenu arrives first). */
  cancel: () => void;
  /** True just after firing; guards against the double-fire on Android, where
   *  a long press also raises a native contextmenu event. */
  firedRecently: () => boolean;
}

/**
 * Long-press detection for touch pointers, the touch stand-in for
 * right-click. Mouse and pen are ignored — they have a real contextmenu.
 */
export function useLongPress(
  handler: (x: number, y: number) => void,
  { ms = 500, moveTolerance = 10 }: { ms?: number; moveTolerance?: number } = {},
): LongPress {
  const pending = useRef<{ timer: number; x: number; y: number } | null>(null);
  const firedAt = useRef(0);
  const handlerRef = useRef(handler);
  useEffect(() => {
    handlerRef.current = handler;
  }, [handler]);

  const cancel = useCallback(() => {
    if (pending.current) {
      window.clearTimeout(pending.current.timer);
      pending.current = null;
    }
  }, []);

  // Timers must not leak past unmount (StrictMode mounts twice).
  useEffect(() => cancel, [cancel]);

  const onPointerDown = useCallback(
    (e: React.PointerEvent) => {
      if (e.pointerType !== 'touch') return;
      cancel();
      const { clientX: x, clientY: y } = e;
      pending.current = {
        x,
        y,
        timer: window.setTimeout(() => {
          pending.current = null;
          firedAt.current = Date.now();
          handlerRef.current(x, y);
        }, ms),
      };
    },
    [ms, cancel],
  );

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      const p = pending.current;
      if (!p) return;
      if (Math.hypot(e.clientX - p.x, e.clientY - p.y) > moveTolerance) cancel();
    },
    [moveTolerance, cancel],
  );

  return useMemo(
    () => ({
      handlers: { onPointerDown, onPointerMove, onPointerUp: cancel, onPointerCancel: cancel },
      cancel,
      firedRecently: () => Date.now() - firedAt.current < 800,
    }),
    [onPointerDown, onPointerMove, cancel],
  );
}

/**
 * Whether the primary pointer is imprecise (touch). Read once per session,
 * deliberately without a change subscription: the tree's virtualisation
 * derives its fixed row height from this, and a fixed height is what lets the
 * visible window be a division instead of a measurement pass. The CSS
 * counterpart is the (pointer: coarse) block in styles/tokens.css.
 */
export function usePointerCoarse(): boolean {
  const [coarse] = useState(
    () => window.matchMedia?.('(pointer: coarse)').matches ?? false,
  );
  return coarse;
}

/**
 * Whether the phone layout applies, WITH a change subscription — unlike the
 * pointer type, the viewport changes on rotation and window resize. Keep the
 * query in step with the (max-width: 767px) block in styles/mobile.css.
 */
export function usePhoneLayout(): boolean {
  const [phone, setPhone] = useState(
    () => window.matchMedia?.('(max-width: 767px)').matches ?? false,
  );
  useEffect(() => {
    if (!window.matchMedia) return;
    const mq = window.matchMedia('(max-width: 767px)');
    const update = (e: MediaQueryListEvent) => setPhone(e.matches);
    mq.addEventListener('change', update);
    return () => mq.removeEventListener('change', update);
  }, []);
  return phone;
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
