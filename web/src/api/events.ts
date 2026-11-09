// Call events (docs/integrations.md, "Call events"): the stored history and
// the live server-sent event stream. The stream is read with fetch rather
// than EventSource so that keepalive comments are seen (a stalled stream is
// told apart from a quiet one) and a refusal (429, 503) reads as an error.
import { request, responseJson } from "../api";

/** The event types hello-sip emits. */
export const EVENT_TYPES = [
  "call.ringing",
  "call.answered",
  "call.ended",
  "call.missed",
  "call.recording.ready",
  "call.completed",
] as const;
export type EventType = (typeof EVENT_TYPES)[number];

/** One end of a call. */
export interface EventParty {
  number?: string;
  name?: string;
  extension?: string;
}

/** The payload of a call event; fields that do not apply are absent. */
export interface EventData {
  callId: string;
  direction?: "internal" | "inbound" | "outbound";
  /** The extension the event is about. */
  extension?: string;
  from: EventParty;
  to: EventParty;
  trunk?: string;
  startedAt?: string;
  answeredAt?: string;
  endedAt?: string;
  durationMs?: number;
  billableMs?: number;
  disposition?: string;
  reason?: string;
  originateId?: string;
  cdrId?: number;
  recordingId?: number;
}

/** A call event as delivered (the id is the dedupe key). */
export interface CallEvent {
  apiVersion: string;
  id: string;
  type: EventType;
  createdAt: string;
  data: EventData;
}

/** An event with its cursor, which orders events and resumes a stream. */
export interface EventEntry extends CallEvent {
  cursor: string;
}

export interface EventFilter {
  /** Event types or a prefix pattern such as "call.*"; empty is all. */
  types?: readonly string[];
  extension?: string;
}

function filterQuery(f: EventFilter): URLSearchParams {
  const q = new URLSearchParams();
  if (f.types && f.types.length > 0) q.set("types", f.types.join(","));
  if (f.extension) q.set("extension", f.extension);
  return q;
}

/** GET /api/v1/events: the newest events first (or after a cursor, oldest first). */
export async function listEvents(
  opts: EventFilter & {
    order?: "asc" | "desc";
    after?: string;
    before?: string;
    limit?: number;
  },
  signal?: AbortSignal,
): Promise<{ items: EventEntry[]; nextCursor: string }> {
  const q = filterQuery(opts);
  if (opts.order) q.set("order", opts.order);
  if (opts.after) q.set("after", opts.after);
  if (opts.before) q.set("before", opts.before);
  if (opts.limit !== undefined) q.set("limit", String(opts.limit));
  const qs = q.toString();
  return request("GET", `/api/v1/events${qs ? `?${qs}` : ""}`, { signal });
}

/** What a stream reports while it runs. */
export interface StreamHandlers {
  /** The server accepted the stream. */
  onOpen?: () => void;
  onEvent: (e: EventEntry) => void;
  /** Any frame arrived, a keepalive comment included. */
  onActivity?: () => void;
}

/**
 * Opens GET /api/v1/events/stream and reports its events until it ends.
 * Resolves when the server closes the stream (it does so when a client lags;
 * reconnect with the last cursor); rejects with ApiError when the server
 * refuses it (429 too many streams, 503) and with an AbortError when `signal`
 * aborts.
 */
export async function streamEvents(
  opts: EventFilter & { lastEventId?: string },
  h: StreamHandlers,
  signal: AbortSignal,
): Promise<void> {
  const q = filterQuery(opts);
  const qs = q.toString();
  const path = `/api/v1/events/stream${qs ? `?${qs}` : ""}`;
  const headers: Record<string, string> = { Accept: "text/event-stream" };
  if (opts.lastEventId) headers["Last-Event-ID"] = opts.lastEventId;
  const res = await fetch(path, {
    method: "GET",
    headers,
    credentials: "same-origin",
    signal,
  });
  const type = res.headers.get("Content-Type") ?? "";
  if (!res.ok || !type.startsWith("text/event-stream") || !res.body) {
    await responseJson<unknown>(res, "GET", path);
    throw new Error(`GET ${path} did not answer with an event stream`);
  }
  h.onOpen?.();
  const reader = res.body.getReader();
  const stop = () => void reader.cancel().catch(() => undefined);
  signal.addEventListener("abort", stop);
  const decoder = new TextDecoder();
  let buf = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      buf += decoder.decode(value, { stream: !done }).replace(/\r\n?/g, "\n");
      for (let i = buf.indexOf("\n\n"); i >= 0; i = buf.indexOf("\n\n")) {
        const block = buf.slice(0, i);
        buf = buf.slice(i + 2);
        h.onActivity?.();
        let id = "";
        const data: string[] = [];
        for (const line of block.split("\n")) {
          if (line.startsWith(":")) continue; // a comment: keepalive
          const colon = line.indexOf(":");
          const field = colon < 0 ? line : line.slice(0, colon);
          const val = colon < 0 ? "" : line.slice(colon + 1).replace(/^ /, "");
          if (field === "id") id = val;
          else if (field === "data") data.push(val);
        }
        if (data.length === 0) continue;
        try {
          const ev = JSON.parse(data.join("\n")) as CallEvent;
          h.onEvent({ ...ev, cursor: id });
        } catch {
          // A frame this version cannot read is skipped, not fatal.
        }
      }
      if (done) return;
    }
  } finally {
    signal.removeEventListener("abort", stop);
    stop();
  }
}

/** Whether cursor a is after cursor b (stream ids are "milliseconds-sequence"). */
export function cursorAfter(a: string, b: string): boolean {
  if (b === "") return a !== "";
  const [am = 0, as = 0] = a.split("-").map(Number);
  const [bm = 0, bs = 0] = b.split("-").map(Number);
  return am !== bm ? am > bm : as > bs;
}
