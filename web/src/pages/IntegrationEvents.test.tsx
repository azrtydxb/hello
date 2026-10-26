import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, mockApi, renderApp } from "../test/api";
import { cursorAfter, type EventEntry, type EventType } from "../api/events";
import { mergeEvents } from "./IntegrationEvents";

function me(role: string) {
  return { "GET /api/v1/auth/me": () => json({ username: "pat", role }) };
}

function entry(
  n: number,
  type: EventType,
  ext: string,
  extra: Partial<EventEntry["data"]> = {},
): EventEntry {
  return {
    cursor: `${1000 + n}-0`,
    apiVersion: "1",
    id: `evt_${n}`,
    type,
    createdAt: "2026-10-26T10:00:00Z",
    data: {
      callId: `call-${n}`,
      direction: "internal",
      extension: ext,
      from: { number: "100", extension: "100", name: "Ann" },
      to: { number: ext, extension: ext },
      ...extra,
    },
  };
}

/** A controllable text/event-stream response. */
function sse() {
  const enc = new TextEncoder();
  let ctl!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      ctl = c;
    },
  });
  const push = (text: string) =>
    act(async () => {
      ctl.enqueue(enc.encode(text));
      await new Promise((r) => setTimeout(r, 0));
    });
  return {
    response: () =>
      new Response(body, {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      }),
    open: () => push("retry: 3000\n: stream open\n\n"),
    keepalive: () => push(": keepalive\n\n"),
    event: (e: ReturnType<typeof entry>) => {
      const { cursor, ...ev } = e;
      return push(
        `id: ${cursor}\nevent: ${e.type}\ndata: ${JSON.stringify(ev)}\n\n`,
      );
    },
    close: () =>
      act(async () => {
        try {
          ctl.close();
        } catch {
          // Already closed by the client.
        }
        await new Promise((r) => setTimeout(r, 0));
      }),
  };
}

function streamCalls() {
  return vi
    .mocked(fetch)
    .mock.calls.filter(([url]) =>
      String(url).startsWith("/api/v1/events/stream"),
    );
}

afterEach(() => vi.unstubAllGlobals());

describe("Events live tail (spec integrations P1)", () => {
  it("shows the stored events newest first and follows the stream from the newest cursor", async () => {
    const stream = sse();
    mockApi({
      ...me("viewer"),
      "GET /api/v1/events?order=desc&limit=50": () =>
        json({
          items: [
            entry(2, "call.answered", "200"),
            entry(1, "call.ringing", "200"),
          ],
          nextCursor: "",
        }),
      "GET /api/v1/events/stream": () => stream.response(),
    });
    renderApp("/integrations/events");

    const table = await screen.findByRole("table", { name: "Call events" });
    await waitFor(() => expect(streamCalls()).toHaveLength(1));
    // The stream resumes after the newest stored event: no gap, no repeat.
    expect(streamCalls()[0]?.[1]?.headers).toMatchObject({
      Accept: "text/event-stream",
      "Last-Event-ID": "1002-0",
    });
    let rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(within(rows[0]!).getByText("call.answered")).toBeVisible();
    expect(within(rows[1]!).getByText("call.ringing")).toBeVisible();

    await stream.open();
    await screen.findByText("LIVE");
    await stream.event(
      entry(3, "call.missed", "201", {
        reason: "no_answer",
        disposition: "no_answer",
      }),
    );
    await stream.event(entry(2, "call.answered", "200")); // delivered twice
    rows = within(table).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(3);
    expect(within(rows[0]!).getByText("call.missed")).toBeVisible();
    expect(within(rows[0]!).getByText(/reason no_answer/)).toBeVisible();
    await stream.close();
  });

  it("filters by type and extension and restarts the stream for the new filter", async () => {
    const streams = [sse(), sse()];
    let n = 0;
    mockApi({
      ...me("operator"),
      "GET /api/v1/events?order=desc&limit=50": json({
        items: [entry(1, "call.ringing", "200")],
        nextCursor: "",
      }),
      "GET /api/v1/events?types=call.missed&order=desc&limit=50": json({
        items: [],
        nextCursor: "",
      }),
      "GET /api/v1/events/stream": () => streams[n++]!.response(),
      "GET /api/v1/events/stream?types=call.missed": () =>
        streams[1]!.response(),
    });
    renderApp("/integrations/events");
    await screen.findByRole("table", { name: "Call events" });
    fireEvent.change(screen.getByLabelText("Event type"), {
      target: { value: "call.missed" },
    });
    await screen.findByText("No events yet");
    await waitFor(() =>
      expect(
        streamCalls().some(
          ([u]) => String(u) === "/api/v1/events/stream?types=call.missed",
        ),
      ).toBe(true),
    );
    fireEvent.change(screen.getByLabelText("Extension"), {
      target: { value: "12" },
    });
    fireEvent.change(screen.getByLabelText("Extension"), {
      target: { value: "1x" },
    });
    expect(screen.getByText("2 to 10 digits")).toBeVisible();
  });

  it("pauses without losing its place and resumes from the last cursor", async () => {
    const first = sse();
    const second = sse();
    let n = 0;
    mockApi({
      ...me("viewer"),
      "GET /api/v1/events?order=desc&limit=50": json({
        items: [],
        nextCursor: "",
      }),
      "GET /api/v1/events/stream": () => [first, second][n++]!.response(),
    });
    renderApp("/integrations/events");
    await waitFor(() => expect(streamCalls()).toHaveLength(1));
    await first.open();
    await first.event(entry(5, "call.ringing", "200"));
    const table = await screen.findByRole("table", { name: "Call events" });
    await within(table).findByText("call.ringing");

    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    await screen.findByRole("button", { name: "Resume" });
    expect(within(table).getByText("call.ringing")).toBeVisible(); // the tail stays
    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    await waitFor(() => expect(streamCalls()).toHaveLength(2));
    expect(streamCalls()[1]?.[1]?.headers).toMatchObject({
      "Last-Event-ID": "1005-0",
    });
    // Resuming does not reload the stored events.
    const historyCalls = vi
      .mocked(fetch)
      .mock.calls.filter(([u]) => String(u).startsWith("/api/v1/events?"));
    expect(historyCalls).toHaveLength(1);
    await second.open();
    await second.event(entry(6, "call.ended", "200"));
    expect(await within(table).findByText("call.ended")).toBeVisible();
  });

  it("reconnects with the last cursor after the server closes the stream", async () => {
    const first = sse();
    const second = sse();
    let n = 0;
    mockApi({
      ...me("viewer"),
      "GET /api/v1/events?order=desc&limit=50": json({
        items: [],
        nextCursor: "",
      }),
      "GET /api/v1/events/stream": () => [first, second][n++]!.response(),
    });
    renderApp("/integrations/events");
    await waitFor(() => expect(streamCalls()).toHaveLength(1));
    await first.open();
    await first.event(entry(9, "call.ringing", "200"));
    await first.close();
    await waitFor(() => expect(streamCalls()).toHaveLength(2), {
      timeout: 4000,
    });
    expect(streamCalls()[1]?.[1]?.headers).toMatchObject({
      "Last-Event-ID": "1009-0",
    });
  });

  it("says why the stream was refused", async () => {
    mockApi({
      ...me("viewer"),
      "GET /api/v1/events?order=desc&limit=50": json({
        items: [],
        nextCursor: "",
      }),
      "GET /api/v1/events/stream": apiError(
        429,
        "rate_limited",
        "too many open event streams",
      ),
    });
    renderApp("/integrations/events");
    expect(
      await screen.findByText("too many open event streams"),
    ).toBeVisible();
  });

  it("is in the sidebar for a viewer and not for a self-service user", async () => {
    mockApi({
      ...me("viewer"),
      "GET /api/v1/events?order=desc&limit=50": json({
        items: [],
        nextCursor: "",
      }),
      "GET /api/v1/events/stream": () => sse().response(),
    });
    renderApp("/integrations/events");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(within(nav).getByRole("link", { name: /Events/ })).toBeVisible();
  });
});

describe("event cursors", () => {
  it("order numerically, not as text", () => {
    expect(cursorAfter("10-0", "9-0")).toBe(true);
    expect(cursorAfter("9-0", "10-0")).toBe(false);
    expect(cursorAfter("5-2", "5-1")).toBe(true);
    expect(cursorAfter("5-1", "5-1")).toBe(false);
    expect(cursorAfter("1-0", "")).toBe(true);
  });

  it("merge keeps one row per event id, newest first, bounded", () => {
    const a = entry(1, "call.ringing", "200");
    const b = entry(2, "call.ended", "200");
    const tail = mergeEvents([], [a, b]);
    expect(tail.map((e) => e.id)).toEqual(["evt_2", "evt_1"]);
    expect(
      mergeEvents(tail, [b, entry(3, "call.missed", "200")]).map((e) => e.id),
    ).toEqual(["evt_3", "evt_2", "evt_1"]);
    const many = Array.from({ length: 400 }, (_, i) =>
      entry(i, "call.ringing", "200"),
    );
    expect(mergeEvents([], many)).toHaveLength(300);
  });
});
