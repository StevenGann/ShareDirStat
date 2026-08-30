import type { ReactNode } from 'react';

/**
 * The full icon set, hand-drawn on a 16x16 grid. Inline SVG because the CSP
 * is `default-src 'self'` — no icon font, no external sheet — and because a
 * dozen small paths cost less than either. Everything draws in currentColor
 * so the icons follow the text colour of whatever contains them.
 */

interface IconProps {
  className?: string;
}

function icon(children: ReactNode, filled = false) {
  return function Icon({ className }: IconProps) {
    return (
      <svg
        viewBox="0 0 16 16"
        className={className ? `icon ${className}` : 'icon'}
        aria-hidden="true"
        focusable="false"
        fill={filled ? 'currentColor' : 'none'}
        stroke={filled ? 'none' : 'currentColor'}
        strokeWidth={filled ? undefined : 1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
      >
        {children}
      </svg>
    );
  };
}

export const IconChevron = icon(<path d="M6 3.5 11.5 8 6 12.5Z" />, true);
export const IconClose = icon(<path d="M4 4l8 8M12 4l-8 8" />);
export const IconSearch = icon(
  <>
    <circle cx="7" cy="7" r="4.25" />
    <path d="M10.5 10.5 14 14" />
  </>,
);
export const IconKebab = icon(
  <>
    <circle cx="8" cy="3" r="1.4" />
    <circle cx="8" cy="8" r="1.4" />
    <circle cx="8" cy="13" r="1.4" />
  </>,
  true,
);
export const IconDownload = icon(<path d="M8 2v7.5M4.75 6.75 8 10l3.25-3.25M2.5 13.5h11" />);
export const IconFolder = icon(<path d="M1.75 3.5h4.5l1.5 2h6.5v7H1.75Z" />);
export const IconFile = icon(<path d="M4 1.75h5.5l2.5 2.5v10H4ZM9.5 1.75v2.5H12" />);
export const IconWarning = icon(
  <>
    <path d="M8 1.75 15 13.75H1Z" />
    <path d="M8 6.5v3.25" />
    <circle cx="8" cy="11.75" r="0.5" fill="currentColor" stroke="none" />
  </>,
);
export const IconTrash = icon(
  <path d="M2.5 4h11M6 4V2.5h4V4M4 4l.75 10h6.5L12 4M6.5 6.75v4.5M9.5 6.75v4.5" />,
);
export const IconCheck = icon(<path d="M3 8.5 6.5 12 13 4.5" />);
export const IconZoomIn = icon(
  <>
    <circle cx="7" cy="7" r="4.25" />
    <path d="M10.5 10.5 14 14M5.25 7h3.5M7 5.25v3.5" />
  </>,
);
export const IconZoomOut = icon(
  <>
    <circle cx="7" cy="7" r="4.25" />
    <path d="M10.5 10.5 14 14M5.25 7h3.5" />
  </>,
);
export const IconHistory = icon(
  <>
    <circle cx="8" cy="8" r="6" />
    <path d="M8 4.5V8l2.5 1.75" />
  </>,
);
export const IconSettings = icon(
  // Three sliders — the conventional "view options" mark.
  <>
    <path d="M2 4.25h6.5M12 4.25h2M2 8h2M7.5 8H14M2 11.75h8M13.5 11.75H14" />
    <circle cx="10.25" cy="4.25" r="1.6" />
    <circle cx="5.75" cy="8" r="1.6" />
    <circle cx="11.75" cy="11.75" r="1.6" />
  </>,
);
export const IconTreemap = icon(
  <>
    <rect x="1.5" y="1.5" width="7" height="13" />
    <rect x="8.5" y="1.5" width="6" height="8" />
    <rect x="8.5" y="9.5" width="6" height="5" />
  </>,
);
export const IconList = icon(<path d="M5 4h9M5 8h9M5 12h9M2 4h.01M2 8h.01M2 12h.01" />);

/** The brand mark: the treemap itself, in accent tones. */
export function Logo({ className }: IconProps) {
  return (
    <svg
      viewBox="0 0 16 16"
      className={className ? `logo ${className}` : 'logo'}
      aria-hidden="true"
      focusable="false"
      fill="currentColor"
      style={{ color: 'var(--accent)' }}
    >
      <rect x="0" y="0" width="9" height="10" rx="1" />
      <rect x="10" y="0" width="6" height="10" rx="1" opacity="0.55" />
      <rect x="0" y="11" width="6" height="5" rx="1" opacity="0.75" />
      <rect x="7" y="11" width="9" height="5" rx="1" opacity="0.35" />
    </svg>
  );
}
