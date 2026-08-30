import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ScanDrawer } from './ScanDrawer';

function stubApi() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (input: RequestInfo | URL) => {
      const url = String(input);
      const body = url.includes('/scans')
        ? { scans: [], running: null }
        : url.includes('/errors')
          ? { errors: [], total: 0, dropped: 0 }
          : { deletions: [] };
      return new Response(JSON.stringify(body), { status: 200 });
    }),
  );
}

afterEach(() => {
  vi.unstubAllGlobals();
});

describe('scan drawer modal behaviour', () => {
  it('closes on Escape without the key reaching the window', async () => {
    stubApi();
    const user = userEvent.setup();
    const onClose = vi.fn();
    const bubbled = vi.fn();
    window.addEventListener('keydown', bubbled);
    try {
      render(<ScanDrawer shareId="media" generation="g1" onClose={onClose} />);
      const drawer = screen.getByRole('dialog', { name: 'Scan history and errors' });
      await waitFor(() => expect(drawer).toHaveFocus());
      await user.keyboard('{Escape}');
      expect(onClose).toHaveBeenCalled();
      expect(bubbled).not.toHaveBeenCalled();
    } finally {
      window.removeEventListener('keydown', bubbled);
    }
  });

  it('traps Tab inside the drawer and returns focus on close', async () => {
    stubApi();
    const user = userEvent.setup();
    const outside = document.createElement('button');
    outside.textContent = 'Scan history';
    document.body.appendChild(outside);
    outside.focus();
    try {
      const view = render(<ScanDrawer shareId="media" generation="g1" onClose={() => {}} />);
      const close = await screen.findByRole('button', { name: 'Close' });
      // The close button is the only focusable control with empty tables, so
      // Tab from it must wrap back to it rather than escaping the dialog.
      close.focus();
      await user.tab();
      expect(close).toHaveFocus();

      view.unmount();
      expect(outside).toHaveFocus();
    } finally {
      outside.remove();
    }
  });
});
