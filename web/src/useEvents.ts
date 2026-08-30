import { useEffect, useRef } from 'react';
import { eventsUrl, type EventName } from './api';

type Handler = (name: EventName, data: unknown) => void;

const NAMES: EventName[] = [
  'scan.started',
  'scan.progress',
  'scan.completed',
  'scan.cancelled',
  'scan.failed',
  'scan.paused',
  'scan.resumed',
  'share.state',
  'node.deleted',
  // The server sends this when it drops a slow subscriber or is shutting
  // down, precisely so the client knows to resynchronise. Leaving it out of
  // this list meant the signal was delivered and discarded.
  'reconnect',
];

/** Backoff bounds for reopening a stream the browser gave up on. */
const RETRY_MIN_MS = 1000;
const RETRY_MAX_MS = 30000;

/**
 * Subscribes to the server's event stream for the lifetime of the component.
 *
 * EventSource reconnects on its own after a transient network drop and
 * replays from Last-Event-ID, but it does *not* retry when the response was
 * not a 2xx: a 503 (the server's 100-stream cap, or a boot before the broker
 * is up) puts it in CLOSED for good. Without our own retry the UI silently
 * stops receiving updates until the page is reloaded, showing stale sizes and
 * a scan that never appears to finish, with nothing on screen to say so.
 */
export function useEvents(onEvent: Handler) {
  const handler = useRef(onEvent);

  // Keep the latest callback without resubscribing: the stream must survive
  // re-renders, but must always dispatch to the current handler.
  useEffect(() => {
    handler.current = onEvent;
  }, [onEvent]);

  useEffect(() => {
    let source: EventSource | null = null;
    let retry: number | undefined;
    let delay = RETRY_MIN_MS;
    let stopped = false;

    const open = () => {
      if (stopped) return;
      const es = new EventSource(eventsUrl());
      source = es;

      for (const name of NAMES) {
        es.addEventListener(name, (ev) => {
          try {
            handler.current(name, JSON.parse((ev as MessageEvent<string>).data));
          } catch {
            /* a malformed event must not take down the stream */
          }
        });
      }

      es.onopen = () => {
        delay = RETRY_MIN_MS;
      };

      es.onerror = () => {
        // CONNECTING means the browser is retrying by itself; leave it alone.
        if (es.readyState !== EventSource.CLOSED || stopped) return;
        es.close();
        retry = window.setTimeout(open, delay);
        delay = Math.min(delay * 2, RETRY_MAX_MS);
      };
    };

    open();
    return () => {
      stopped = true;
      window.clearTimeout(retry);
      source?.close();
    };
  }, []);
}
