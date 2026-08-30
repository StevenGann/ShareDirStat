import { useRef } from 'react';
import { IconClose } from './icons';
import { useModalBehavior } from './useModalBehavior';

interface Props {
  title: string;
  onClose: () => void;
  /** 'bottom' rises from the bottom edge; 'full' covers the screen. */
  variant: 'bottom' | 'full';
  children: React.ReactNode;
}

/**
 * The phone-layout dialog surface. Deliberately gesture-free: a close
 * button, Escape and a scrim tap dismiss it — no drag physics to fight the
 * scrollable content inside.
 */
export function Sheet({ title, onClose, variant, children }: Props) {
  const ref = useRef<HTMLDivElement | null>(null);
  const { onKeyDown } = useModalBehavior(ref, { onClose });

  return (
    <div
      className="sheet-backdrop"
      onMouseDown={(e) => {
        if (e.target === e.currentTarget) onClose();
      }}
    >
      <div
        className={`sheet ${variant}`}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        ref={ref}
        tabIndex={-1}
        onKeyDown={onKeyDown}
      >
        <div className="sheet-head">
          <h2>{title}</h2>
          <button type="button" className="icon-button" onClick={onClose} aria-label="Close">
            <IconClose />
          </button>
        </div>
        <div className="sheet-body">{children}</div>
      </div>
    </div>
  );
}
