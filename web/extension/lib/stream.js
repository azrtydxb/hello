// The call-event stream: GET /api/v1/events/stream with the bearer token,
// resumed with Last-Event-ID after any drop.
import { createSseParser } from "./sse.js";

/**
 * Reads the stream until it ends. Resolves with the last event id seen;
 * rejects when the server refuses the stream or the network fails.
 */
export async function readStream(
  session,
  { types, lastEventId, signal, onEvent, onOpen },
) {
  const q = new URLSearchParams();
  if (types?.length) q.set("types", types.join(","));
  const headers = { Accept: "text/event-stream" };
  if (lastEventId) headers["Last-Event-ID"] = lastEventId;
  const res = await session.fetch(
    `/api/v1/events/stream${q.size ? `?${q}` : ""}`,
    { headers, signal },
  );
  if (
    !res.ok ||
    !(res.headers.get("Content-Type") ?? "").startsWith("text/event-stream") ||
    !res.body
  ) {
    const e = new Error(`the event stream answered ${res.status}`);
    e.status = res.status;
    throw e;
  }
  onOpen?.();
  const reader = res.body.getReader();
  const decoder = new TextDecoder();
  const parser = createSseParser();
  let last = lastEventId ?? "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      for (const f of parser.push(decoder.decode(value, { stream: !done }))) {
        let ev;
        try {
          ev = JSON.parse(f.data);
        } catch {
          continue;
        }
        if (f.id) last = f.id;
        onEvent(ev, f.id);
      }
      if (done) return last;
    }
  } finally {
    await reader.cancel().catch(() => undefined);
  }
}

/** Seconds to wait before reconnect attempt n (1, 2, 4 … 30). */
export function backoff(n) {
  return Math.min(30, 2 ** Math.max(0, n - 1));
}
