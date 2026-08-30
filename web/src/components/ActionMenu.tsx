import { useEffect, useLayoutEffect, useRef, useState } from 'react';
import type { ActionItem } from '../actions';

interface Props {
  items: ActionItem[];
  /** Viewport coordinates the menu opens at (pointer or trigger position). */
  x: number;
  y: number;
  label: string;
  onClose: () => void;
}

/**
 * The context menu (FR-UI-10): opened by right-click on desktop and
 * long-press on touch, replicating the detail-bar actions. One instance is
 * owned by App and positioned at the pointer.
 */
export function ActionMenu({ items, x, y, label, onClose }: Props) {
  const ref = useRef<HTMLDivElement | null>(null);
  const [pos, setPos] = useState({ x, y });

  // Clamp into the viewport once the size is known.
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const r = el.getBoundingClientRect();
    setPos({
      x: Math.max(Math.min(x, window.innerWidth - r.width - 4), 4),
      y: Math.max(Math.min(y, window.innerHeight - r.height - 4), 4),
    });
  }, [x, y]);

  // Focus the first enabled item; hand focus back where it came from.
  useEffect(() => {
    const previous = document.activeElement as HTMLElement | null;
    const first = ref.current?.querySelector<HTMLElement>('[role="menuitem"]:not(:disabled)');
    (first ?? ref.current)?.focus();
    return () => previous?.focus();
  }, []);

  // Any press outside dismisses. pointerdown, not click, so the menu is gone
  // before the outside element reacts.
  useEffect(() => {
    const onDown = (e: PointerEvent) => {
      if (!ref.current?.contains(e.target as globalThis.Node)) onClose();
    };
    window.addEventListener('pointerdown', onDown);
    return () => window.removeEventListener('pointerdown', onDown);
  }, [onClose]);

  const onKeyDown = (e: React.KeyboardEvent) => {
    if (e.key === 'Escape') {
      e.preventDefault();
      // App's window-level Escape would also close the results pane below.
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key === 'Tab') {
      onClose();
      return;
    }
    if (e.key !== 'ArrowDown' && e.key !== 'ArrowUp') return;
    e.preventDefault();
    const focusable = Array.from(
      ref.current?.querySelectorAll<HTMLElement>('[role="menuitem"]:not(:disabled)') ?? [],
    );
    if (focusable.length === 0) return;
    const at = focusable.indexOf(document.activeElement as HTMLElement);
    const delta = e.key === 'ArrowDown' ? 1 : -1;
    const next = focusable[(at + delta + focusable.length) % focusable.length];
    next?.focus();
  };

  return (
    <div
      ref={ref}
      className="action-menu"
      role="menu"
      aria-label={label}
      tabIndex={-1}
      style={{ left: pos.x, top: pos.y }}
      onKeyDown={onKeyDown}
      onContextMenu={(e) => e.preventDefault()}
    >
      {items.map((it) =>
        it.href && !it.disabled ? (
          <a key={it.id} role="menuitem" href={it.href} title={it.title} onClick={onClose}>
            {it.label}
          </a>
        ) : (
          <button
            key={it.id}
            type="button"
            role="menuitem"
            className={it.danger ? 'danger' : undefined}
            disabled={it.disabled}
            title={it.title}
            onClick={() => {
              it.run?.();
              onClose();
            }}
          >
            {it.label}
          </button>
        ),
      )}
    </div>
  );
}
