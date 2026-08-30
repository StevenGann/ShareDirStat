import { useEffect, useRef } from 'react';
import { eventsUrl, type EventName } from './api';

type Handler = (name: EventName, data: unknown) => void;

/**
 * Subscribes to the server's event stream for the lifetime of the component.
 * EventSource reconnects on its own and replays from Last-Event-ID, so the
 * hook only has to route messages.
 */
export function useEvents(onEvent: Handler) {
  const handler = useRef(onEvent);

  // Keep the latest callback without resubscribing: the stream must survive
  // re-renders, but must always dispatch to the current handler.
  useEffect(() => {
    handler.current = onEvent;
  }, [onEvent]);

  useEffect(() => {
    const names: EventName[] = [
      'scan.started',
      'scan.progress',
      'scan.completed',
      'scan.cancelled',
      'scan.failed',
      'scan.paused',
      'scan.resumed',
      'share.state',
      'node.deleted',
    ];
    const source = new EventSource(eventsUrl());
    const listeners = names.map((name) => {
      const fn = (ev: MessageEvent<string>) => {
        try {
          handler.current(name, JSON.parse(ev.data));
        } catch {
          /* a malformed event must not take down the stream */
        }
      };
      source.addEventListener(name, fn as EventListener);
      return [name, fn] as const;
    });
    return () => {
      for (const [name, fn] of listeners) source.removeEventListener(name, fn as EventListener);
      source.close();
    };
  }, []);
}
