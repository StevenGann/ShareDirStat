// Client for the ShareDirStat JSON API (specification §9).

export type ShareState =
  | 'unavailable'
  | 'never-scanned'
  | 'scanning'
  | 'ready'
  | 'ready-stale'
  | 'error';

export type Basis = 'apparent' | 'allocated';
export type Kind = 'dir' | 'file' | 'symlink' | 'other' | 'deleted';

export interface Filesystem {
  type: string;
  mount_point: string;
  readonly: boolean;
}

export interface Stats {
  files: number;
  dirs: number;
  symlinks: number;
  others: number;
  size: number;
  alloc: number;
  hardlink_dups: number;
  excluded: number;
  errors: number;
  max_depth: number;
  media_size: number;
  media_duration: number;
}

export interface Progress {
  dirs: number;
  files: number;
  bytes: number;
  errors: number;
  excluded: number;
  pending: number;
  current_path: string;
  elapsed_ms: number;
  rate_files_per_s: number;
  paused: boolean;
}

export interface ScanStatus {
  id: string;
  share_id: string;
  trigger: string;
  path: string;
  started_at: string;
  queued: boolean;
  paused: boolean;
  progress: Progress;
}

export interface ScanRecord {
  id: string;
  share_id: string;
  trigger: string;
  path: string;
  started_at: string;
  finished_at: string | null;
  duration_ms: number;
  outcome: 'completed' | 'cancelled' | 'failed';
  error?: string;
  generation?: string;
  files: number;
  dirs: number;
  bytes: number;
  errors: number;
}

export interface ShareInfo {
  id: string;
  name: string;
  path: string;
  state: ShareState;
  error?: string;
  discovered: boolean;
  allow_delete: boolean;
  allow_download: boolean;
  concurrency: number;
  schedule: string;
  next_scan: string | null;
  size_basis: Basis;
  excludes: string[];
  filesystem: Filesystem | null;
  generation: string | null;
  last_scan: string | null;
  stats: Stats | null;
  checked_at: string;
  scan: ScanStatus | null;
  /** Why deleting is unavailable; empty when it is available. */
  delete_blocked?: string;
  trash_enabled: boolean;
  zip_enabled: boolean;
}

export interface NodeFlags {
  partial: boolean;
  mountpoint: boolean;
  hardlink_dup: boolean;
  excluded: boolean;
  unscanned: boolean;
}

export interface Node {
  name: string;
  name_b64?: string;
  path: string;
  kind: Kind;
  size: number;
  alloc: number;
  mtime: string;
  mode: string;
  perms: string;
  uid: number;
  gid: number;
  owner?: string;
  group?: string;
  ext?: string;
  flags: NodeFlags;
  files: number;
  dirs: number;
  children: number;
  /** Media playing time in seconds (aggregate for folders); absent when
   *  nothing beneath has a known duration. */
  duration?: number;
  /** Bytes of media covered by `duration`; equals `size` for a media file. */
  media_size?: number;
  pct_of_parent: number;
  pct_of_share: number;
}

/** A node as returned inside a treemap response, with its pruned children. */
export interface TreemapNode extends Node {
  children_list?: TreemapNode[];
  children_total?: number;
  truncated?: { children: number; size: number; alloc: number };
}

export interface TreemapResponse {
  share: string;
  generation: string;
  basis: Basis;
  root: TreemapNode;
  nodes: number;
  min_fraction: number;
  max_depth: number;
}

export interface ExtensionStat {
  ext: string;
  files: number;
  size: number;
  alloc: number;
}

export interface SearchResponse {
  share: string;
  generation: string;
  basis: Basis;
  matches: Node[];
  total: number;
  truncated: boolean;
  scanned: number;
}

export interface TreeResponse {
  share: string;
  generation: string;
  basis: Basis;
  node: Node;
  ancestors: Node[];
  children: Node[];
  total: number;
  offset: number;
  limit: number;
}

export interface ScanError {
  path: string;
  op: string;
  errno: string;
  message: string;
}

export interface DeleteTarget {
  path: string;
  kind: Kind;
  size: number;
  alloc: number;
  files: number;
  dirs: number;
  exists: boolean;
  warnings: string[];
}

export interface DeletePreview {
  share: string;
  generation: string;
  targets: DeleteTarget[];
  total_size: number;
  total_alloc: number;
  total_files: number;
  total_dirs: number;
  confirm: string;
  expires_at: string;
  confirm_mode: 'simple' | 'name';
  /** The exact text the user must type; empty means a plain confirmation. */
  name_to_type: string;
  trash: boolean;
}

export interface DeleteEntryError {
  path: string;
  errno?: string;
  message: string;
}

export interface DeletePathResult {
  path: string;
  outcome: 'deleted' | 'partial' | 'failed';
  freed: number;
  freed_alloc: number;
  removed: number;
  failed: number;
  errno?: string;
  message?: string;
  trash?: string;
  errors?: DeleteEntryError[];
}

export interface DeleteResult {
  share: string;
  results: DeletePathResult[];
  freed_total: number;
  reconcile: string[] | null;
}

export interface AuditEntry {
  time: string;
  share: string;
  path: string;
  kind: string;
  size: number;
  files: number;
  dirs: number;
  outcome: string;
  removed: number;
  failed: number;
  errno?: string;
  error?: string;
  trash?: string;
  client_ip?: string;
  user_agent?: string;
}

export interface TrashItem {
  batch: string;
  trash_path: string;
  original_path: string;
  deleted_at: string;
  kind: string;
  size: number;
  /** The original location is occupied again, so a restore would collide. */
  blocked: boolean;
}

export interface VersionInfo {
  version: string;
  commit: string;
  build_date: string;
  go: string;
  os: string;
  arch: string;
}

/** Header required on every mutating request (FR-SEC-02). */
export const CSRF_HEADER = { 'X-Requested-With': 'ShareDirStat' } as const;

/**
 * The API lives next to index.html, so with hash routing the current
 * directory of the pathname is the base path. One build then works under any
 * server.base_path.
 */
export function apiBase(loc: { pathname: string } = window.location): string {
  const dir = loc.pathname.endsWith('/') ? loc.pathname : loc.pathname.replace(/[^/]*$/, '');
  return `${dir.replace(/\/$/, '')}/api/v1`;
}

export class ApiRequestError extends Error {
  constructor(
    public readonly status: number,
    public readonly code: string,
    message: string,
  ) {
    super(message);
    this.name = 'ApiRequestError';
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const res = await fetch(`${apiBase()}${path}`, {
    ...init,
    headers: { Accept: 'application/json', ...(init.headers ?? {}) },
  });
  if (!res.ok) {
    let code = 'http_error';
    let message = `${res.status} ${res.statusText}`;
    try {
      const body = (await res.json()) as { error: { code: string; message: string } };
      code = body.error.code;
      message = body.error.message;
    } catch {
      /* the body was not the JSON error envelope */
    }
    throw new ApiRequestError(res.status, code, message);
  }
  return (await res.json()) as T;
}

function post<T>(path: string, body?: unknown): Promise<T> {
  return request<T>(path, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...CSRF_HEADER },
    body: JSON.stringify(body ?? {}),
  });
}

const q = (params: Record<string, string | number | undefined>) => {
  const s = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '') s.set(k, String(v));
  }
  const str = s.toString();
  return str ? `?${str}` : '';
};

const share = (id: string) => `/shares/${encodeURIComponent(id)}`;

export const api = {
  shares: () => request<{ shares: ShareInfo[] }>('/shares'),
  share: (id: string) => request<ShareInfo>(share(id)),
  version: () => request<VersionInfo>('/version'),

  tree: (
    id: string,
    path: string,
    opts: { basis?: Basis; sort?: string; order?: string; limit?: number; offset?: number } = {},
  ) => request<TreeResponse>(`${share(id)}/tree${q({ path, ...opts })}`),

  node: (id: string, path: string, basis?: Basis) =>
    request<{ node: Node; ancestors: Node[] }>(`${share(id)}/node${q({ path, basis })}`),

  treemap: (
    id: string,
    path: string,
    opts: { basis?: Basis; min_fraction?: number; max_nodes?: number; max_depth?: number } = {},
  ) => request<TreemapResponse>(`${share(id)}/treemap${q({ path, ...opts })}`),

  extensions: (id: string, path = '') =>
    request<{ extensions: ExtensionStat[] }>(`${share(id)}/extensions${q({ path })}`),

  top: (id: string, opts: { path?: string; n?: number; kind?: 'file' | 'dir'; basis?: Basis } = {}) =>
    request<{ items: Node[] }>(`${share(id)}/top${q(opts)}`),

  search: (
    id: string,
    opts: {
      q?: string;
      path?: string;
      kind?: string;
      ext?: string;
      min_size?: number;
      max_size?: number;
      mtime_after?: string;
      mtime_before?: string;
      basis?: Basis;
      limit?: number;
    },
  ) => request<SearchResponse>(`${share(id)}/search${q(opts)}`),

  errors: (id: string, limit = 200) =>
    request<{ errors: ScanError[]; total: number; dropped: number }>(`${share(id)}/errors${q({ limit })}`),

  history: (id: string) =>
    request<{ scans: ScanRecord[]; running: ScanStatus | null }>(`${share(id)}/scans`),

  deletePreview: (id: string, paths: string[]) =>
    post<DeletePreview>(`${share(id)}/delete/preview`, { paths }),

  deleteConfirm: (id: string, paths: string[], confirm: string) =>
    post<DeleteResult>(`${share(id)}/delete`, { paths, confirm }),

  audit: (limit = 200) => request<{ deletions: AuditEntry[] }>(`/audit/deletes${q({ limit })}`),

  trash: (id: string) => request<{ items: TrashItem[] }>(`${share(id)}/trash`),
  restoreTrash: (id: string, path: string) => post<TrashItem>(`${share(id)}/trash/restore`, { path }),
  emptyTrash: (id: string, batch = '') => post<{ removed: number }>(`${share(id)}/trash/empty`, { batch }),

  startScan: (id: string, path = '') => post<ScanStatus>(`${share(id)}/scan`, { path }),
  cancelScan: (id: string, scanId: string) => post<unknown>(`${share(id)}/scan/${scanId}/cancel`),
  pauseScan: (id: string, scanId: string) => post<unknown>(`${share(id)}/scan/${scanId}/pause`),
  resumeScan: (id: string, scanId: string) => post<unknown>(`${share(id)}/scan/${scanId}/resume`),
};

/** Event names streamed by GET /api/v1/events (§9.4). */
export type EventName =
  | 'scan.started'
  | 'scan.progress'
  | 'scan.completed'
  | 'scan.cancelled'
  | 'scan.failed'
  | 'scan.paused'
  | 'scan.resumed'
  | 'share.state'
  | 'node.deleted'
  | 'reconnect';

export function eventsUrl(): string {
  return `${apiBase()}/events`;
}

/** Direct link for downloading one file; the browser streams it itself. */
export function downloadUrl(id: string, path: string): string {
  return `${apiBase()}${share(id)}/download${q({ path })}`;
}

/**
 * Direct link for downloading a folder or a selection as a ZIP. Repeated
 * path parameters keep this a plain GET, so the browser streams the archive
 * to disk instead of a fetch buffering gigabytes in memory.
 */
export function zipUrl(id: string, paths: string[]): string {
  const params = new URLSearchParams();
  for (const p of paths) params.append('path', p);
  return `${apiBase()}${share(id)}/download/zip?${params.toString()}`;
}
