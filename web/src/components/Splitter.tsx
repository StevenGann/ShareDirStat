import { useCallback, useRef } from 'react';

interface Props {
  orientation: 'vertical' | 'horizontal';
  /** Current size of the pane before the splitter, in CSS pixels. */
  value: number;
  min: number;
  max: number;
  onChange: (next: number) => void;
  label: string;
}

/** Pixels a keyboard nudge moves the divider. */
const STEP = 16;

/**
 * A draggable pane divider. `orientation` describes the divider itself: a
 * vertical divider sits between two side-by-side panes and is dragged left
 * and right.
 */
export function Splitter({ orientation, value, min, max, onChange, label }: Props) {
  const start = useRef<{ pos: number; value: number } | null>(null);
  const vertical = orientation === 'vertical';

  const clamp = useCallback((v: number) => Math.min(Math.max(v, min), max), [min, max]);

  const onPointerDown = useCallback(
    (e: React.PointerEvent<HTMLDivElement>) => {
      e.currentTarget.setPointerCapture(e.pointerId);
      start.current = { pos: vertical ? e.clientX : e.clientY, value };
    },
    [vertical, value],
  );

  const onPointerMove = useCallback(
    (e: React.PointerEvent) => {
      const s = start.current;
      if (!s) return;
      const delta = (vertical ? e.clientX : e.clientY) - s.pos;
      onChange(clamp(s.value + delta));
    },
    [vertical, onChange, clamp],
  );

  const onPointerUp = useCallback((e: React.PointerEvent<HTMLDivElement>) => {
    start.current = null;
    e.currentTarget.releasePointerCapture(e.pointerId);
  }, []);

  const onKeyDown = useCallback(
    (e: React.KeyboardEvent) => {
      const back = vertical ? 'ArrowLeft' : 'ArrowUp';
      const forward = vertical ? 'ArrowRight' : 'ArrowDown';
      if (e.key === back) {
        e.preventDefault();
        onChange(clamp(value - STEP));
      } else if (e.key === forward) {
        e.preventDefault();
        onChange(clamp(value + STEP));
      } else if (e.key === 'Home') {
        e.preventDefault();
        onChange(min);
      } else if (e.key === 'End') {
        e.preventDefault();
        onChange(max);
      }
    },
    [vertical, value, onChange, clamp, min, max],
  );

  return (
    <div
      className={`splitter ${orientation}`}
      role="separator"
      aria-orientation={vertical ? 'vertical' : 'horizontal'}
      aria-label={label}
      aria-valuenow={Math.round(value)}
      aria-valuemin={min}
      aria-valuemax={max}
      tabIndex={0}
      onPointerDown={onPointerDown}
      onPointerMove={onPointerMove}
      onPointerUp={onPointerUp}
      onKeyDown={onKeyDown}
    />
  );
}
