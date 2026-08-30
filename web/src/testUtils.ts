/**
 * Replaces window.matchMedia with a version whose `matches` is looked up per
 * query, for tests that need a coarse pointer or a narrow viewport. The
 * default stub in setupTests answers false to everything, so the suite as a
 * whole renders the desktop layout.
 *
 * Returns a restore function; call it in the test's cleanup.
 */
export function mockMatchMedia(map: Record<string, boolean>): () => void {
  const original = window.matchMedia;
  const listeners = new Map<string, Set<(e: MediaQueryListEvent) => void>>();

  window.matchMedia = ((query: string) => ({
    matches: map[query] ?? false,
    media: query,
    onchange: null,
    addEventListener: (_type: string, cb: (e: MediaQueryListEvent) => void) => {
      let set = listeners.get(query);
      if (!set) listeners.set(query, (set = new Set()));
      set.add(cb);
    },
    removeEventListener: (_type: string, cb: (e: MediaQueryListEvent) => void) => {
      listeners.get(query)?.delete(cb);
    },
    addListener: () => {},
    removeListener: () => {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;

  return () => {
    window.matchMedia = original;
  };
}
