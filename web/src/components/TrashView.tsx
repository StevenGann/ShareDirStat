import { useCallback, useRef, useState } from 'react';
import { api, type ShareInfo, type TrashItem } from '../api';
import { absoluteTime, formatBytes, formatCount, relativeTime } from '../format';
import { useAsyncData } from '../hooks';
import { IconClose } from './icons';
import { useModalBehavior } from './useModalBehavior';

interface Props {
  share: ShareInfo;
  onClose: () => void;
  /** Called after a restore or empty so the caller can refresh the tree. */
  onChanged: () => void;
}

/**
 * Items moved to the trash, with restore and empty (FR-DEL-07). Only shown
 * when trash mode is on; with direct unlinking there is nothing to list.
 */
export function TrashView({ share, onClose, onChanged }: Props) {
  const drawerRef = useRef<HTMLElement | null>(null);
  const [version, setVersion] = useState(0);
  const [busy, setBusy] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [confirmEmpty, setConfirmEmpty] = useState(false);
  const { onKeyDown } = useModalBehavior(drawerRef, { onClose, closable: busy === null });

  const load = useCallback(async () => (await api.trash(share.id)).items, [share.id]);
  const { data: items, error, loading } = useAsyncData(`trash|${share.id}|${version}`, load);

  const restore = async (item: TrashItem) => {
    setBusy(item.trash_path);
    setProblem(null);
    try {
      await api.restoreTrash(share.id, item.trash_path);
      setVersion((v) => v + 1);
      onChanged();
    } catch (e) {
      setProblem((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const empty = async () => {
    setBusy('*');
    setProblem(null);
    try {
      await api.emptyTrash(share.id);
      setConfirmEmpty(false);
      setVersion((v) => v + 1);
      onChanged();
    } catch (e) {
      setProblem((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const total = (items ?? []).reduce((s, i) => s + i.size, 0);

  return (
    <aside
      className="drawer"
      role="dialog"
      aria-modal="true"
      aria-label="Trash"
      ref={drawerRef}
      tabIndex={-1}
      onKeyDown={onKeyDown}
    >
      <div className="drawer-head">
        <h2>Trash · {share.name}</h2>
        <button type="button" className="icon-button" onClick={onClose} aria-label="Close">
          <IconClose />
        </button>
      </div>

      {error && (
        <div className="banner error" role="alert">
          {error}
        </div>
      )}
      {problem && (
        <div className="banner error" role="alert">
          {problem}
        </div>
      )}

      {items && items.length > 0 && (
        <p className="muted">
          {formatCount(items.length)} {items.length === 1 ? 'item' : 'items'} · {formatBytes(total)} still
          taking up space. Items are purged automatically after the retention period.
        </p>
      )}

      {loading && !items && <p className="muted">Loading…</p>}
      {items && items.length === 0 && <p className="muted">The trash is empty.</p>}

      {items && items.length > 0 && (
        <table className="mini">
          <thead>
            <tr>
              <th>Deleted</th>
              <th>Original location</th>
              <th className="num">Size</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {items.map((it) => (
              <tr key={it.trash_path}>
                <td title={absoluteTime(it.deleted_at)}>{relativeTime(it.deleted_at)}</td>
                <td className="mono wrap">
                  {it.original_path}
                  {it.kind === 'dir' && <span className="tag">folder</span>}
                  {it.blocked && (
                    <div className="muted small">
                      Something else now exists at this location; rename or remove it first.
                    </div>
                  )}
                </td>
                <td className="num">{formatBytes(it.size)}</td>
                <td>
                  <button
                    type="button"
                    disabled={it.blocked || busy !== null}
                    onClick={() => void restore(it)}
                  >
                    {busy === it.trash_path ? 'Restoring…' : 'Restore'}
                  </button>
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {items && items.length > 0 && (
        <div className="modal-actions">
          {confirmEmpty ? (
            <>
              <span className="warn-text">Permanently delete everything in the trash?</span>
              <button type="button" onClick={() => setConfirmEmpty(false)} disabled={busy !== null}>
                Keep
              </button>
              <button
                type="button"
                className="danger-solid"
                onClick={() => void empty()}
                disabled={busy !== null}
              >
                {busy === '*' ? 'Emptying…' : 'Empty trash'}
              </button>
            </>
          ) : (
            <button type="button" className="danger" onClick={() => setConfirmEmpty(true)}>
              Empty trash…
            </button>
          )}
        </div>
      )}
    </aside>
  );
}
