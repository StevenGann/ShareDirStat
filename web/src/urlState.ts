/**
 * Hash-routed view state (FR-UI-03), so a view can be bookmarked and the
 * browser's back button steps through it.
 *
 *   #/media/Movies/2019?root=Movies
 *    └ share  └ selected path      └ treemap root
 *
 * Paths are encoded segment by segment: the separators stay readable in the
 * address bar while names containing spaces, '#' or '?' survive the round
 * trip. Filenames are raw bytes on Linux, so nothing may be assumed about
 * what they contain.
 */

export interface UrlState {
  share: string;
  /** Selected node, share-relative. Empty means the share root. */
  path: string;
  /** Directory the treemap is zoomed to. */
  root: string;
}

const EMPTY: UrlState = { share: '', path: '', root: '' };

function encodePath(path: string): string {
  return path
    .split('/')
    .filter((s) => s !== '')
    .map(encodeURIComponent)
    .join('/');
}

function decodePath(path: string): string {
  return path
    .split('/')
    .filter((s) => s !== '')
    .map((s) => {
      try {
        return decodeURIComponent(s);
      } catch {
        // A hand-edited URL can contain a broken escape; keep it literal
        // rather than throwing away the whole location.
        return s;
      }
    })
    .join('/');
}

/** Parses a location hash. Unknown or malformed input yields empty fields. */
export function parseHash(hash: string): UrlState {
  const raw = hash.startsWith('#') ? hash.slice(1) : hash;
  if (raw === '' || raw === '/') return { ...EMPTY };

  const [pathPart = '', queryPart = ''] = raw.replace(/^\//, '').split('?', 2);
  const segments = pathPart.split('/').filter((s) => s !== '');
  const share = segments.length > 0 ? decodeURIComponent(segments[0] as string) : '';
  const path = decodePath(segments.slice(1).join('/'));

  let root = '';
  if (queryPart) {
    const params = new URLSearchParams(queryPart);
    root = decodePath(params.get('root') ?? '');
  }
  return { share, path, root };
}

/** Builds the hash for a view state, omitting anything at its default. */
export function buildHash(state: UrlState): string {
  if (!state.share) return '#/';
  let out = `#/${encodeURIComponent(state.share)}`;
  if (state.path) out += `/${encodePath(state.path)}`;
  if (state.root) out += `?root=${encodePath(state.root)}`;
  return out;
}

/** True when two states describe the same view. */
export function sameState(a: UrlState, b: UrlState): boolean {
  return a.share === b.share && a.path === b.path && a.root === b.root;
}
