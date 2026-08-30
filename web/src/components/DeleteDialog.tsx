import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, type DeletePreview, type DeleteResult, type Node, type ShareInfo } from '../api';
import { formatBytes, formatCount } from '../format';

interface Props {
  share: ShareInfo;
  /** The items the user asked to delete. */
  nodes: Node[];
  onDone: (result: DeleteResult) => void;
  onClose: () => void;
}

type Phase =
  | { step: 'loading' }
  | { step: 'confirm'; preview: DeletePreview }
  | { step: 'working'; preview: DeletePreview }
  | { step: 'done'; result: DeleteResult }
  | { step: 'error'; message: string };

/**
 * The delete confirmation (FR-DEL-02). Nothing is removed until the server
 * has said exactly what would go and the user has confirmed that specific
 * list; the token the preview returns is only good for those paths.
 */
export function DeleteDialog({ share, nodes, onDone, onClose }: Props) {
  const [phase, setPhase] = useState<Phase>({ step: 'loading' });
  const [typed, setTyped] = useState('');
  const dialogRef = useRef<HTMLDivElement | null>(null);
  // The path list is what the request and its confirmation token are bound
  // to, so it is memoised on its own contents rather than on the node
  // objects, which are replaced on every refresh of the share list.
  const pathKey = nodes.map((n) => n.path).join('\u0000');
  const paths = useMemo(() => pathKey.split('\u0000'), [pathKey]);

  useEffect(() => {
    let cancelled = false;
    api
      .deletePreview(share.id, paths)
      .then((preview) => {
        if (!cancelled) setPhase({ step: 'confirm', preview });
      })
      .catch((e: Error) => {
        if (!cancelled) setPhase({ step: 'error', message: e.message });
      });
    return () => {
      cancelled = true;
    };
  }, [share.id, paths]);

  // Focus the dialog so Escape works and screen readers announce it -- but
  // only when nothing inside it has already claimed focus. React applies
  // `autoFocus` during commit and this effect runs afterwards, so focusing
  // unconditionally stole focus back from the typed-confirmation input: the
  // user was told to type the folder name and their keystrokes went nowhere.
  useEffect(() => {
    const dialog = dialogRef.current;
    if (!dialog) return;
    if (dialog.contains(document.activeElement) && document.activeElement !== dialog) return;
    dialog.focus();
  }, [phase.step]);

  // A modal that does not trap Tab is only decoratively modal: focus walks out
  // onto the tree and the toolbar behind the backdrop, where the user can
  // start a scan or change the basis while a delete confirmation is open.
  const onKeyDownTrap = useCallback((e: React.KeyboardEvent) => {
    if (e.key !== 'Tab') return;
    const dialog = dialogRef.current;
    if (!dialog) return;
    const focusable = dialog.querySelectorAll<HTMLElement>(
      'a[href], button:not([disabled]), input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])',
    );
    if (focusable.length === 0) return;
    const first = focusable[0] as HTMLElement;
    const last = focusable[focusable.length - 1] as HTMLElement;
    const active = document.activeElement;
    if (e.shiftKey && (active === first || active === dialog)) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && active === last) {
      e.preventDefault();
      first.focus();
    }
  }, []);

  const confirm = useCallback(async () => {
    if (phase.step !== 'confirm') return;
    const preview = phase.preview;
    setPhase({ step: 'working', preview });
    try {
      const result = await api.deleteConfirm(share.id, paths, preview.confirm);
      setPhase({ step: 'done', result });
      onDone(result);
    } catch (e) {
      setPhase({ step: 'error', message: (e as Error).message });
    }
  }, [phase, share.id, paths, onDone]);

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape' && phase.step !== 'working') {
      e.preventDefault();
      // App also listens for Escape on window and would close the results
      // pane underneath the dialog the user was only trying to dismiss.
      e.stopPropagation();
      onClose();
      return;
    }
    onKeyDownTrap(e);
  };

  return (
    <div
      className="modal-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget && phase.step !== 'working') onClose();
      }}
    >
      <div
        className="modal"
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-title"
        tabIndex={-1}
        ref={dialogRef}
        onKeyDown={onKeyDown}
      >
        <h2 id="delete-title">
          {phase.step === 'done'
            ? 'Deleted'
            : share.trash_enabled
              ? 'Move to trash'
              : 'Delete permanently'}
        </h2>

        {phase.step === 'loading' && <p className="muted">Checking what this would remove…</p>}

        {phase.step === 'error' && (
          <>
            <div className="banner error" role="alert">
              {phase.message}
            </div>
            <div className="modal-actions">
              <button type="button" onClick={onClose}>
                Close
              </button>
            </div>
          </>
        )}

        {(phase.step === 'confirm' || phase.step === 'working') && (
          <ConfirmBody
            preview={phase.preview}
            share={share}
            typed={typed}
            onTyped={setTyped}
            working={phase.step === 'working'}
            onConfirm={() => void confirm()}
            onClose={onClose}
          />
        )}

        {phase.step === 'done' && <ResultBody result={phase.result} onClose={onClose} />}
      </div>
    </div>
  );
}

function ConfirmBody({
  preview,
  share,
  typed,
  onTyped,
  working,
  onConfirm,
  onClose,
}: {
  preview: DeletePreview;
  share: ShareInfo;
  typed: string;
  onTyped: (v: string) => void;
  working: boolean;
  onConfirm: () => void;
  onClose: () => void;
}) {
  const [acknowledged, setAcknowledged] = useState(false);
  const needsTyping = preview.name_to_type !== '';
  // Compare in NFC. A folder created on macOS is stored decomposed, while a
  // keyboard produces the composed form, so a byte-exact comparison would
  // never match and the folder would be undeletable through the UI. Case and
  // whitespace are still significant -- that friction is the point.
  const ready = needsTyping
    ? typed.normalize('NFC') === preview.name_to_type.normalize('NFC')
    : acknowledged;
  const missing = preview.targets.filter((t) => !t.exists);

  return (
    <>
      <p className="modal-lead">
        {preview.targets.length === 1 ? (
          <>
            <strong className="mono">{preview.targets[0]?.path}</strong>
          </>
        ) : (
          <>
            <strong>{formatCount(preview.targets.length)} items</strong> from {share.name}
          </>
        )}
      </p>

      <div className="modal-totals">
        <span>
          <strong>{formatBytes(preview.total_size)}</strong> reclaimed
        </span>
        {preview.total_files > 0 && <span>{formatCount(preview.total_files)} files</span>}
        {preview.total_dirs > 0 && <span>{formatCount(preview.total_dirs)} folders</span>}
      </div>

      <div className="modal-list">
        <table className="mini">
          <thead>
            <tr>
              <th>Path</th>
              <th className="num">Size</th>
              <th className="num">Contents</th>
            </tr>
          </thead>
          <tbody>
            {preview.targets.map((t) => (
              <tr key={t.path}>
                <td className="mono wrap">
                  {t.path}
                  {t.warnings.map((w) => (
                    <div key={w} className="muted small">
                      {w}
                    </div>
                  ))}
                </td>
                <td className="num">{formatBytes(t.size)}</td>
                <td className="num">
                  {t.kind === 'dir'
                    ? `${formatCount(t.files)} files, ${formatCount(t.dirs)} folders`
                    : t.kind}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {missing.length > 0 && (
        <p className="muted">
          {formatCount(missing.length)} of these are no longer on disk and will simply be dropped
          from the results.
        </p>
      )}

      <p className={preview.trash ? 'muted' : 'warn-text'}>
        {preview.trash ? (
          <>
            These move to <code>.sharedirstat-trash</code> inside the share and are purged
            automatically later. They still take up space until then.
          </>
        ) : (
          <>This cannot be undone. There is no trash: the files are unlinked immediately.</>
        )}
      </p>

      {needsTyping ? (
        <label className="confirm-field">
          Type <strong className="mono">{preview.name_to_type}</strong> to confirm
          <input
            type="text"
            value={typed}
            autoCapitalize="off"
            autoCorrect="off"

            autoFocus
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => onTyped(e.target.value)}
            aria-label={`Type ${preview.name_to_type} to confirm`}
          />
        </label>
      ) : (
        <label className="check confirm-field">
          <input
            type="checkbox"
            checked={acknowledged}
            onChange={(e) => setAcknowledged(e.target.checked)}
          />
          I understand what will be removed
        </label>
      )}

      <div className="modal-actions">
        <button type="button" onClick={onClose} disabled={working}>
          Cancel
        </button>
        <button type="button" className="danger-solid" onClick={onConfirm} disabled={!ready || working}>
          {working ? 'Deleting…' : preview.trash ? 'Move to trash' : 'Delete permanently'}
        </button>
      </div>
    </>
  );
}

function ResultBody({ result, onClose }: { result: DeleteResult; onClose: () => void }) {
  const failed = result.results.filter((r) => r.outcome !== 'deleted');
  return (
    <>
      <p className="modal-lead">
        <strong>{formatBytes(result.freed_total)}</strong> reclaimed from{' '}
        {formatCount(result.results.length)}{' '}
        {result.results.length === 1 ? 'item' : 'items'}.
      </p>

      {failed.length > 0 && (
        <>
          <div className="banner error" role="alert">
            {formatCount(failed.length)} could not be fully removed. The affected folders are being
            rescanned so the sizes shown stay honest.
          </div>
          <div className="modal-list">
            <table className="mini">
              <thead>
                <tr>
                  <th>Path</th>
                  <th>Outcome</th>
                  <th>Why</th>
                </tr>
              </thead>
              <tbody>
                {failed.map((r) => (
                  <tr key={r.path}>
                    <td className="mono wrap">{r.path}</td>
                    <td>
                      <span className={`pill outcome-${r.outcome === 'partial' ? 'cancelled' : 'failed'}`}>
                        {r.outcome}
                      </span>
                      {r.removed > 0 && (
                        <div className="muted small">{formatCount(r.removed)} removed</div>
                      )}
                    </td>
                    <td>
                      {r.errno && <code>{r.errno}</code>} <span className="muted">{r.message}</span>
                      {r.errors?.slice(0, 5).map((e) => (
                        <div key={e.path} className="muted small mono wrap">
                          {e.path}
                        </div>
                      ))}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </>
      )}

      <div className="modal-actions">
        <button type="button" className="primary" onClick={onClose} autoFocus>
          Close
        </button>
      </div>
    </>
  );
}
