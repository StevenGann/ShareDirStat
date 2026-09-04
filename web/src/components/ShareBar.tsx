import type { ShareInfo, ScanStatus } from '../api';
import {
  formatBytes,
  formatCount,
  formatPlaytime,
  formatRate,
  relativeTime,
  absoluteTime,
  truncatePath,
} from '../format';
import { useThemePref, type ThemePref } from '../theme';
import { Logo } from './icons';

export const STATE_LABEL: Record<ShareInfo['state'], string> = {
  unavailable: 'Unavailable',
  'never-scanned': 'Never scanned',
  scanning: 'Scanning',
  ready: 'Ready',
  'ready-stale': 'Rescanning',
  error: 'Error',
};

interface Props {
  shares: ShareInfo[];
  current: ShareInfo | null;
  scan: ScanStatus | null;
  busy: boolean;
  onSelect: (id: string) => void;
  onScan: () => void;
  onCancel: () => void;
  onPauseToggle: () => void;
}

export function ShareBar({ shares, current, scan, busy, onSelect, onScan, onCancel, onPauseToggle }: Props) {
  const stats = current?.stats ?? null;
  return (
    <header className="topbar">
      <div className="topbar-row">
        <div className="brand">
          <Logo />
          <h1>ShareDirStat</h1>
        </div>

        <label className="field">
          <span className="sr-only">Share</span>
          <select
            value={current?.id ?? ''}
            onChange={(e) => onSelect(e.target.value)}
            disabled={shares.length === 0}
          >
            {shares.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        </label>

        {current && (
          <span className={`pill state-${current.state}`}>{STATE_LABEL[current.state]}</span>
        )}

        {current && stats && (
          <span className="summary">
            <strong>{formatBytes(stats.size)}</strong>
            <span className="muted">
              {formatCount(stats.files)} files · {formatCount(stats.dirs)} folders
              {stats.media_duration > 0 &&
                ` · ${formatPlaytime(stats.media_duration)} of media`}
            </span>
          </span>
        )}

        {current && (
          <span className="muted" title={absoluteTime(current.last_scan)}>
            scanned {relativeTime(current.last_scan)}
          </span>
        )}

        <span className="spacer" />

        <ThemeSelect />

        {scan ? (
          <>
            <button type="button" onClick={onPauseToggle}>
              {scan.paused ? 'Resume' : 'Pause'}
            </button>
            <button type="button" className="danger" onClick={onCancel}>
              Cancel scan
            </button>
          </>
        ) : (
          <button
            type="button"
            className="primary"
            onClick={onScan}
            disabled={busy || !current || current.state === 'unavailable'}
          >
            {current?.state === 'never-scanned' ? 'Scan now' : 'Rescan'}
          </button>
        )}
      </div>

      {scan && <ScanProgress scan={scan} />}

      {current?.error && (
        <div className="banner error" role="alert">
          {current.error}
        </div>
      )}
    </header>
  );
}

/** The FR-UI-24 manual theme override; 'system' keeps following the OS. */
export function ThemeSelect() {
  const [pref, setPref] = useThemePref();
  return (
    <label className="field">
      <span className="sr-only">Theme</span>
      <select value={pref} onChange={(e) => setPref(e.target.value as ThemePref)} aria-label="Theme">
        <option value="system">System theme</option>
        <option value="light">Light</option>
        <option value="dark">Dark</option>
      </select>
    </label>
  );
}

/** Shared with the phone header, which has no room for the full topbar. A
 *  full crawl has no knowable total, so this reports work done rather than a
 *  percentage it cannot honestly compute (FR-SCAN-07). */
export function ScanProgress({ scan }: { scan: ScanStatus }) {
  const p = scan.progress;
  return (
    <div className="progress" role="status" aria-live="polite">
      <div className={`progress-bar${scan.paused ? ' paused' : ''}`} aria-hidden="true">
        <span />
      </div>
      <div className="progress-text">
        {scan.queued ? (
          <span>Queued behind another scan…</span>
        ) : (
          <>
            <strong>{scan.paused ? 'Paused' : 'Scanning'}</strong>
            <span>{formatCount(p.files)} files</span>
            <span>{formatCount(p.dirs)} folders</span>
            <span>{formatBytes(p.bytes)}</span>
            <span>{formatRate(p.rate_files_per_s)}</span>
            {p.errors > 0 && <span className="bad">{formatCount(p.errors)} errors</span>}
            <span className="muted current-path" title={p.current_path}>
              {truncatePath(p.current_path, 70)}
            </span>
          </>
        )}
      </div>
    </div>
  );
}
