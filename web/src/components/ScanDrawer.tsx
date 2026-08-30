import { useEffect, useState } from 'react';
import { api, type AuditEntry, type ScanError, type ScanRecord } from '../api';
import { absoluteTime, formatBytes, formatCount, formatDuration, relativeTime } from '../format';

interface Props {
  shareId: string;
  generation: string | null;
  onClose: () => void;
}

/** Scan history and the error list of the current results (FR-UI-20). */
export function ScanDrawer({ shareId, generation, onClose }: Props) {
  const [scans, setScans] = useState<ScanRecord[]>([]);
  const [errors, setErrors] = useState<ScanError[]>([]);
  const [errorTotal, setErrorTotal] = useState(0);
  const [deletions, setDeletions] = useState<AuditEntry[]>([]);
  const [problem, setProblem] = useState<string | null>(null);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const history = await api.history(shareId);
        if (!cancelled) setScans(history.scans);
        if (generation) {
          const errs = await api.errors(shareId);
          if (!cancelled) {
            setErrors(errs.errors);
            setErrorTotal(errs.total + errs.dropped);
          }
        }
        const audit = await api.audit(100);
        if (!cancelled) {
          setDeletions(audit.deletions.filter((d) => d.share === shareId));
        }
      } catch (e) {
        if (!cancelled) setProblem((e as Error).message);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [shareId, generation]);

  return (
    <aside className="drawer" role="dialog" aria-label="Scan history and errors">
      <div className="drawer-head">
        <h2>Scan history</h2>
        <button type="button" onClick={onClose} aria-label="Close">
          ✕
        </button>
      </div>

      {problem && (
        <div className="banner error" role="alert">
          {problem}
        </div>
      )}

      <table className="mini">
        <thead>
          <tr>
            <th>Started</th>
            <th>Trigger</th>
            <th>Outcome</th>
            <th className="num">Files</th>
            <th className="num">Size</th>
            <th className="num">Took</th>
          </tr>
        </thead>
        <tbody>
          {scans.length === 0 && (
            <tr>
              <td colSpan={6} className="muted">
                No scans recorded yet.
              </td>
            </tr>
          )}
          {scans.map((s) => (
            <tr key={s.id}>
              <td title={absoluteTime(s.started_at)}>{relativeTime(s.started_at)}</td>
              <td>
                {s.trigger}
                {s.path && <span className="muted"> · {s.path}</span>}
              </td>
              <td>
                <span className={`pill outcome-${s.outcome}`}>{s.outcome}</span>
                {s.error && <div className="muted small">{s.error}</div>}
              </td>
              <td className="num">{formatCount(s.files)}</td>
              <td className="num">{formatBytes(s.bytes)}</td>
              <td className="num">{formatDuration(s.duration_ms)}</td>
            </tr>
          ))}
        </tbody>
      </table>

      <h2>
        Unreadable paths{' '}
        {errorTotal > 0 && <span className="pill bad-pill">{formatCount(errorTotal)}</span>}
      </h2>
      {errorTotal === 0 ? (
        <p className="muted">Every directory in the last scan was readable.</p>
      ) : (
        <table className="mini">
          <thead>
            <tr>
              <th>Path</th>
              <th>Operation</th>
              <th>Error</th>
            </tr>
          </thead>
          <tbody>
            {errors.map((e, i) => (
              <tr key={`${e.path}-${i}`}>
                <td className="mono wrap">{e.path}</td>
                <td>{e.op}</td>
                <td>
                  {e.errno && <code>{e.errno}</code>} <span className="muted">{e.message}</span>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {errorTotal > errors.length && (
        <p className="muted">
          Showing the first {formatCount(errors.length)} of {formatCount(errorTotal)}.
        </p>
      )}

      <h2>Recent deletions</h2>
      {deletions.length === 0 ? (
        <p className="muted">Nothing has been deleted through ShareDirStat on this share.</p>
      ) : (
        <table className="mini">
          <thead>
            <tr>
              <th>When</th>
              <th>Path</th>
              <th className="num">Reclaimed</th>
              <th>Outcome</th>
              <th>By</th>
            </tr>
          </thead>
          <tbody>
            {deletions.map((d, i) => (
              <tr key={`${d.time}-${d.path}-${i}`}>
                <td title={absoluteTime(d.time)}>{relativeTime(d.time)}</td>
                <td className="mono wrap">
                  {d.path}
                  {d.trash && <div className="muted small">moved to trash</div>}
                </td>
                <td className="num">{formatBytes(d.size)}</td>
                <td>
                  <span
                    className={`pill outcome-${
                      d.outcome === 'deleted' ? 'completed' : d.outcome === 'partial' ? 'cancelled' : 'failed'
                    }`}
                  >
                    {d.outcome}
                  </span>
                  {d.error && <div className="muted small">{d.error}</div>}
                </td>
                <td className="muted small">{d.client_ip}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </aside>
  );
}
