const BINARY = ['B', 'KiB', 'MiB', 'GiB', 'TiB', 'PiB'];
const DECIMAL = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];

/** Formats a byte count (FR-UI-26). Binary units by default. */
export function formatBytes(n: number, decimal = false): string {
  if (!Number.isFinite(n) || n < 0) return '—';
  const base = decimal ? 1000 : 1024;
  const units = decimal ? DECIMAL : BINARY;
  let i = 0;
  let v = n;
  while (v >= base && i < units.length - 1) {
    v /= base;
    i++;
  }
  const digits = i === 0 ? 0 : v < 10 ? 2 : v < 100 ? 1 : 0;
  return `${v.toFixed(digits)} ${units[i]}`;
}

const counts = new Intl.NumberFormat();

/** Formats a plain count with thousands separators. */
export function formatCount(n: number): string {
  return Number.isFinite(n) ? counts.format(n) : '—';
}

/** Formats a fraction (0..1) as a percentage. */
export function formatPercent(f: number): string {
  if (!Number.isFinite(f)) return '—';
  const pct = f * 100;
  return `${pct < 10 ? pct.toFixed(1) : pct.toFixed(0)}%`;
}

/** "3 minutes ago" style relative time. */
export function relativeTime(iso: string | null | undefined, now = Date.now()): string {
  if (!iso) return 'never';
  const t = Date.parse(iso);
  if (Number.isNaN(t)) return iso;
  const s = Math.round((now - t) / 1000);
  const rtf = new Intl.RelativeTimeFormat('en', { numeric: 'auto' });
  const abs = Math.abs(s);
  if (abs < 60) return rtf.format(-s, 'second');
  if (abs < 3600) return rtf.format(-Math.round(s / 60), 'minute');
  if (abs < 86400) return rtf.format(-Math.round(s / 3600), 'hour');
  if (abs < 2592000) return rtf.format(-Math.round(s / 86400), 'day');
  return rtf.format(-Math.round(s / 2592000), 'month');
}

/** Absolute timestamp for tooltips. */
export function absoluteTime(iso: string | null | undefined): string {
  if (!iso) return '';
  const t = new Date(iso);
  return Number.isNaN(t.getTime()) ? iso : t.toLocaleString();
}

/** Formats a duration given in milliseconds. */
export function formatDuration(ms: number): string {
  if (!Number.isFinite(ms) || ms < 0) return '—';
  if (ms < 1000) return `${Math.round(ms)} ms`;
  const s = ms / 1000;
  if (s < 60) return `${s.toFixed(1)} s`;
  const m = Math.floor(s / 60);
  const rest = Math.round(s % 60);
  if (m < 60) return `${m}m ${rest}s`;
  return `${Math.floor(m / 60)}h ${m % 60}m`;
}

/** Formats a scan rate. */
export function formatRate(filesPerSecond: number): string {
  if (!Number.isFinite(filesPerSecond) || filesPerSecond <= 0) return '—';
  if (filesPerSecond >= 1000) return `${Math.round(filesPerSecond / 1000)}k files/s`;
  return `${Math.round(filesPerSecond)} files/s`;
}

/** Truncates a long path from the left, keeping the informative tail. */
export function truncatePath(path: string, max = 64): string {
  if (path.length <= max) return path;
  return `…${path.slice(path.length - max + 1)}`;
}
