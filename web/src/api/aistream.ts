// Streamed assistant answers (docs/ai-agent.md, "Streaming answers"): POST
// .../messages with Accept: text/event-stream answers with server-sent
// events. EventSource cannot POST, so the stream is read with fetch.
import { responseJson } from "../api";
import type { AIMessage } from "./aiagent";

/** Tokens one answer cost. */
export interface StreamUsage {
  inputTokens: number;
  outputTokens: number;
  reasoningTokens: number;
  calls: number;
}

/** One event of a streamed answer; `done` and `error` end the stream. */
export type StreamEvent =
  | { type: "started"; taskId: string; messageId: number }
  | { type: "thinking" }
  | { type: "tool"; operationId: string; label: string }
  | { type: "delta"; text: string }
  /** The text streamed so far is void; more follows. */
  | { type: "reset" }
  | {
      type: "done";
      taskId: string;
      messageId: number;
      message: AIMessage;
      proposalId: string | null;
      usage: StreamUsage;
    }
  | { type: "error"; code: string; message: string };

/** How a stream ended: with its terminal event, or the server did not stream. */
export type StreamOutcome =
  | { kind: "done"; event: Extract<StreamEvent, { type: "done" }> }
  | { kind: "error"; event: Extract<StreamEvent, { type: "error" }> }
  /** The server answered 202 as it does without Accept: poll the task. */
  | { kind: "accepted"; taskId: string; messageId: number }
  /** The connection ended without a terminal event. */
  | { kind: "dropped" };

/** Parses the data of one SSE frame into an event, or null if it is not one. */
function parseEvent(name: string, data: string): StreamEvent | null {
  let payload: Record<string, unknown>;
  try {
    payload = JSON.parse(data || "{}") as Record<string, unknown>;
  } catch {
    return null;
  }
  switch (name) {
    case "started":
    case "thinking":
    case "tool":
    case "delta":
    case "reset":
    case "done":
    case "error":
      return { type: name, ...payload } as StreamEvent;
    default:
      return null;
  }
}

/** Splits an SSE byte stream into frames (blank-line separated), parsed. */
async function* frames(
  body: ReadableStream<Uint8Array>,
  signal?: AbortSignal,
): AsyncGenerator<StreamEvent> {
  const reader = body.getReader();
  // fetch aborts its own body; this ends the read whatever the fetch does.
  const stop = () => void reader.cancel().catch(() => undefined);
  signal?.addEventListener("abort", stop);
  const decoder = new TextDecoder();
  let buf = "";
  try {
    for (;;) {
      const { done, value } = await reader.read();
      buf += decoder.decode(value, { stream: !done }).replace(/\r\n?/g, "\n");
      for (let i = buf.indexOf("\n\n"); i >= 0; i = buf.indexOf("\n\n")) {
        const block = buf.slice(0, i);
        buf = buf.slice(i + 2);
        let name = "message";
        const data: string[] = [];
        for (const line of block.split("\n")) {
          if (line.startsWith(":")) continue; // a comment: keepalive
          const colon = line.indexOf(":");
          const field = colon < 0 ? line : line.slice(0, colon);
          const val = colon < 0 ? "" : line.slice(colon + 1).replace(/^ /, "");
          if (field === "event") name = val;
          else if (field === "data") data.push(val);
        }
        const ev = parseEvent(name, data.join("\n"));
        if (ev) yield ev;
      }
      if (done) return;
    }
  } finally {
    // Stops the download when the consumer stops early (abort, navigation).
    signal?.removeEventListener("abort", stop);
    stop();
  }
}

/**
 * Posts a message and reads the answer as it is generated: `onEvent` gets
 * every event in order. Resolves with how the stream ended; rejects with
 * ApiError when the server refuses before streaming (409 task_running, 429
 * ai_busy, ...) and with an AbortError when `signal` aborts, which also
 * stops the work on the server.
 */
export async function streamMessage(
  sessionId: string,
  content: string,
  onEvent: (e: StreamEvent) => void,
  signal?: AbortSignal,
): Promise<StreamOutcome> {
  const path = `/api/v1/ai/sessions/${encodeURIComponent(sessionId)}/messages`;
  const res = await fetch(path, {
    method: "POST",
    headers: {
      Accept: "text/event-stream",
      "Content-Type": "application/json",
    },
    body: JSON.stringify({ content }),
    credentials: "same-origin",
    signal,
  });
  const type = res.headers.get("Content-Type") ?? "";
  if (!res.ok || !type.startsWith("text/event-stream") || !res.body) {
    // A refusal, or a server that does not stream and answered 202.
    const body = await responseJson<{ taskId: string; messageId: number }>(
      res,
      "POST",
      path,
    );
    return { kind: "accepted", taskId: body.taskId, messageId: body.messageId };
  }
  for await (const ev of frames(res.body, signal)) {
    onEvent(ev);
    if (ev.type === "done") return { kind: "done", event: ev };
    if (ev.type === "error") return { kind: "error", event: ev };
  }
  signal?.throwIfAborted();
  return { kind: "dropped" };
}
