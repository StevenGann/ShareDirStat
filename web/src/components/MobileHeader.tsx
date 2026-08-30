import type { ScanStatus, ShareInfo } from '../api';
import { formatBytes, formatCount } from '../format';
import { IconSearch, IconSettings, Logo } from './icons';
import { ScanProgress, STATE_LABEL } from './ShareBar';

interface Props {
  shares: ShareInfo[];
  current: ShareInfo;
  scan: ScanStatus | null;
  busy: boolean;
  onSelectShare: (id: string) => void;
  onScan: () => void;
  onCancel: () => void;
  onPauseToggle: () => void;
  onSearch: () => void;
  onSettings: () => void;
}

/** The compact phone topbar: share picker, state, and the two entry points
 *  (search, settings) that replace the desktop toolbar. */
export function MobileHeader({
  shares,
  current,
  scan,
  busy,
  onSelectShare,
  onScan,
  onCancel,
  onPauseToggle,
  onSearch,
  onSettings,
}: Props) {
  const stats = current.stats;
  return (
    <header className="topbar mobile-header">
      <div className="topbar-row">
        <Logo />
        <label className="field share-field">
          <span className="sr-only">Share</span>
          <select value={current.id} onChange={(e) => onSelectShare(e.target.value)}>
            {shares.map((s) => (
              <option key={s.id} value={s.id}>
                {s.name}
              </option>
            ))}
          </select>
        </label>

        <span className="spacer" />

        <button type="button" className="icon-button" onClick={onSearch} aria-label="Search">
          <IconSearch />
        </button>
        <button type="button" className="icon-button" onClick={onSettings} aria-label="View options">
          <IconSettings />
        </button>
      </div>

      <div className="topbar-row mobile-facts">
        <span className={`pill state-${current.state}`}>{STATE_LABEL[current.state]}</span>
        {stats && (
          <span className="summary">
            <strong>{formatBytes(stats.size)}</strong>
            <span className="muted small">{formatCount(stats.files)} files</span>
          </span>
        )}
        <span className="spacer" />
        {scan ? (
          <>
            <button type="button" onClick={onPauseToggle}>
              {scan.paused ? 'Resume' : 'Pause'}
            </button>
            <button type="button" className="danger" onClick={onCancel}>
              Cancel
            </button>
          </>
        ) : (
          <button
            type="button"
            className="primary"
            onClick={onScan}
            disabled={busy || current.state === 'unavailable'}
          >
            {current.state === 'never-scanned' ? 'Scan now' : 'Rescan'}
          </button>
        )}
      </div>

      {scan && <ScanProgress scan={scan} />}

      {current.error && (
        <div className="banner error" role="alert">
          {current.error}
        </div>
      )}
    </header>
  );
}
