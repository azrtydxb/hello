import { useEffect, useRef } from "react";
import { INBOX_EVENTS, INBOX_STREAM } from "../../api/inbox";

/** One live event: only ids; the page reads the rest through the API. */
export interface InboxEvent {
  type: (typeof INBOX_EVENTS)[number];
  conversationId: number;
  messageId: number;
}

/**
 * Follows the caller's inbox stream and calls `onEvent` for each event. The
 * browser reconnects by itself and sends Last-Event-ID, so an event is not
 * missed across a dropped connection. Without EventSource (an old browser)
 * nothing is followed and the page's own refresh covers it. `onOpen` runs
 * every time the stream (re)connects, so the page can refresh what it may
 * have missed meanwhile.
 */
export function useInboxStream(
  onEvent: (e: InboxEvent) => void,
  onOpen?: () => void,
) {
  const handler = useRef(onEvent);
  const opened = useRef(onOpen);
  useEffect(() => {
    handler.current = onEvent;
    opened.current = onOpen;
  });
  useEffect(() => {
    if (typeof EventSource === "undefined") return;
    const source = new EventSource(INBOX_STREAM, { withCredentials: true });
    source.onopen = () => opened.current?.();
    for (const type of INBOX_EVENTS) {
      source.addEventListener(type, (ev) => {
        try {
          const d = JSON.parse((ev as MessageEvent<string>).data) as {
            conversationId: number;
            messageId: number;
          };
          handler.current({
            type,
            conversationId: d.conversationId,
            messageId: d.messageId,
          });
        } catch {
          // A frame that is not ours is ignored.
        }
      });
    }
    return () => source.close();
  }, []);
}
