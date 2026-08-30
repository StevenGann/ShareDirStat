import { useCallback, useEffect } from 'react';

/** What counts as focusable inside a dialog. Extend this selector when a new
 *  kind of interactive element is added to any modal content. */
const FOCUSABLE =
  'a[href], button:not([disabled]), input:not([disabled]), select, textarea, [tabindex]:not([tabindex="-1"])';

/**
 * The behaviours that make role="dialog" actually modal, extracted from
 * DeleteDialog so the drawers get the same treatment: initial focus, a Tab
 * trap, Escape-to-close, and focus handed back on unmount.
 *
 * The element `ref` points at must have tabIndex={-1}; spread the returned
 * onKeyDown onto it.
 */
export function useModalBehavior(
  ref: React.RefObject<HTMLElement | null>,
  {
    onClose,
    closable = true,
    focusKey,
  }: {
    onClose: () => void;
    /** When false, Escape is ignored (e.g. while a delete is in flight). */
    closable?: boolean;
    /** Re-runs the initial-focus rule when this changes (dialog phases). */
    focusKey?: unknown;
  },
): { onKeyDown: (e: React.KeyboardEvent) => void } {
  // Focus returns where it came from when the dialog goes away.
  useEffect(() => {
    const previous = document.activeElement;
    return () => {
      if (previous instanceof HTMLElement && previous.isConnected) previous.focus();
    };
  }, []);

  // Focus the dialog so Escape works and screen readers announce it -- but
  // only when nothing inside it has already claimed focus. React applies
  // `autoFocus` during commit and this effect runs afterwards, so focusing
  // unconditionally stole focus back from the typed-confirmation input: the
  // user was told to type the folder name and their keystrokes went nowhere.
  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (el.contains(document.activeElement) && document.activeElement !== el) return;
    el.focus();
  }, [ref, focusKey]);

  // A modal that does not trap Tab is only decoratively modal: focus walks
  // out onto the tree and the toolbar behind it, where the user can start a
  // scan or change the basis while a confirmation is open.
  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      if (e.key === 'Escape') {
        if (!closable) return;
        e.preventDefault();
        // App also listens for Escape on window and would close the results
        // pane underneath the dialog the user was only trying to dismiss.
        e.stopPropagation();
        onClose();
        return;
      }
      if (e.key !== 'Tab') return;
      const dialog = ref.current;
      if (!dialog) return;
      const focusable = dialog.querySelectorAll<HTMLElement>(FOCUSABLE);
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
    },
    [ref, onClose, closable],
  );

  return { onKeyDown };
}
