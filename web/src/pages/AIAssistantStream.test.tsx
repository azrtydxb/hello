import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, mockApi, renderApp } from "../test/api";

const ME = {
  "GET /api/v1/auth/me": () => json({ username: "pat", role: "operator" }),
  "GET /api/v1/ai/status": json({ enabled: true, reason: null, agents: [] }),
};

const SESSION = {
  id: "s1",
  owner: 1,
  title: "Is 201 registered?",
  createdAt: "2026-10-08T10:00:00Z",
  lastActiveAt: "2026-10-08T10:00:00Z",
};
const USER_MSG = {
  id: 1,
  role: "user",
  content: "Is 201 registered?",
  toolCalls: [],
  citations: [],
  proposalId: null,
  taskId: "t1",
  createdAt: "2026-10-08T10:00:00Z",
};
const ANSWER = {
  id: 2,
  role: "assistant",
  content: "Extension 201 is registered.",
  toolCalls: [
    { operationId: "listRegistrations", status: 200, truncated: false },
  ],
  citations: [0],
  proposalId: null,
  taskId: "t1",
  createdAt: "2026-10-08T10:00:05Z",
};
const DONE = {
  taskId: "t1",
  messageId: 1,
  message: ANSWER,
  proposalId: null,
  usage: { inputTokens: 10, outputTokens: 5, reasoningTokens: 0, calls: 2 },
};

/** A controllable text/event-stream response. */
function sse() {
  const enc = new TextEncoder();
  let ctl!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      ctl = c;
    },
  });
  return {
    response: () =>
      new Response(body, {
        status: 200,
        headers: { "Content-Type": "text/event-stream" },
      }),
    event: (name: string, data: unknown = {}) =>
      act(async () => {
        ctl.enqueue(
          enc.encode(`event: ${name}\ndata: ${JSON.stringify(data)}\n\n`),
        );
        await new Promise((r) => setTimeout(r, 0));
      }),
    raw: (text: string) =>
      act(async () => {
        ctl.enqueue(enc.encode(text));
        await new Promise((r) => setTimeout(r, 0));
      }),
    close: () =>
      act(async () => {
        try {
          ctl.close();
        } catch {
          // The client already left after the final event.
        }
        await new Promise((r) => setTimeout(r, 0));
      }),
  };
}

async function ask(text: string) {
  await screen.findByLabelText("Message");
  fireEvent.change(screen.getByLabelText("Message"), {
    target: { value: text },
  });
  fireEvent.click(screen.getByRole("button", { name: "Send" }));
}

afterEach(() => vi.unstubAllGlobals());

describe("AI assistant streaming", () => {
  it("types, shows the tool step, grows the answer and swaps in the stored message", async () => {
    const stream = sse();
    let stored = false;
    mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": () =>
        json({
          session: SESSION,
          messages: stored ? [USER_MSG, ANSWER] : [],
          task: null,
        }),
      "POST /api/v1/ai/sessions/s1/messages": () => stream.response(),
    });
    const { container } = renderApp("/ai/assistant/s1");
    await ask("Is 201 registered?");

    // The question and the indicator show at once.
    expect(await screen.findByText("The assistant is working…")).toBeVisible();
    expect(
      within(container.querySelector<HTMLElement>(".aix-thread")!).getByText(
        "Is 201 registered?",
      ),
    ).toBeVisible();
    const typing = container.querySelector(".aix-typing");
    expect(typing).toHaveAttribute("role", "status");
    expect(typing).toHaveAttribute("aria-live", "polite");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    expect(screen.getByRole("button", { name: "Stop" })).toBeEnabled();
    const post = vi
      .mocked(fetch)
      .mock.calls.find(([url]) => String(url).endsWith("/s1/messages"));
    expect(post?.[1]?.headers).toMatchObject({ Accept: "text/event-stream" });

    await stream.event("started", { taskId: "t1", messageId: 1 });
    await stream.event("thinking");
    expect(await screen.findByText("Thinking…")).toBeVisible();
    await stream.event("tool", {
      operationId: "listRegistrations",
      label: "Looking up registrations…",
    });
    expect(await screen.findByText("Looking up registrations…")).toBeVisible();

    // The first token replaces the indicator; the answer grows.
    await stream.event("delta", { text: "Extension 201 " });
    expect(container.querySelector(".aix-typing")).toBeNull();
    const live = container.querySelector(".aix-msg--live");
    expect(live).toHaveTextContent("Extension 201");
    expect(live).toHaveAttribute("aria-live", "off");
    await stream.event("delta", { text: "is registered." });
    expect(container.querySelector(".aix-msg--live")).toHaveTextContent(
      "Extension 201 is registered.",
    );

    // The final event swaps in the stored message with its sources.
    stored = true;
    await stream.event("done", DONE);
    await stream.close();
    await waitFor(() =>
      expect(container.querySelector(".aix-msg--live")).toBeNull(),
    );
    expect(screen.getByText("Extension 201 is registered.")).toBeVisible();
    expect(screen.getByText(/listRegistrations \(200\)/)).toBeVisible();
    expect(screen.getAllByText("Is 201 registered?").length).toBeGreaterThan(0);
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
    expect(screen.getByLabelText("Message")).toHaveValue("");
  });

  it("reads events split across chunks and ignores keepalive comments", async () => {
    const stream = sse();
    mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [],
        task: null,
      }),
      "POST /api/v1/ai/sessions/s1/messages": () => stream.response(),
    });
    const { container } = renderApp("/ai/assistant/s1");
    await ask("hi");
    await screen.findByText("The assistant is working…");
    await stream.raw(": keepalive\n\nevent: del");
    await stream.raw('ta\r\ndata: {"text":"Half');
    expect(container.querySelector(".aix-msg--live")).toBeNull();
    await stream.raw(' and half"}\r\n\r\n');
    expect(container.querySelector(".aix-msg--live")).toHaveTextContent(
      "Half and half",
    );
  });

  it("shows the indicator again when the streamed text is voided", async () => {
    const stream = sse();
    mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [],
        task: null,
      }),
      "POST /api/v1/ai/sessions/s1/messages": () => stream.response(),
    });
    const { container } = renderApp("/ai/assistant/s1");
    await ask("hi");
    await stream.event("delta", { text: "A draft that is rejected" });
    expect(container.querySelector(".aix-msg--live")).not.toBeNull();
    await stream.event("reset");
    expect(container.querySelector(".aix-msg--live")).toBeNull();
    expect(await screen.findByText("Thinking…")).toBeVisible();
  });

  it("renders streamed text as plain text", async () => {
    const stream = sse();
    mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [],
        task: null,
      }),
      "POST /api/v1/ai/sessions/s1/messages": () => stream.response(),
    });
    const { container } = renderApp("/ai/assistant/s1");
    await ask("hi");
    await stream.event("delta", {
      text: "<img src=x onerror=alert(1)> [x](http://evil.test)",
    });
    expect(container.querySelector("main img")).toBeNull();
    expect(container.querySelector("main a[href^='http']")).toBeNull();
    expect(screen.getByText(/\[x\]\(http:\/\/evil.test\)/)).toBeVisible();
  });

  it("shows an error event as the assistant's failure and keeps the question", async () => {
    const stream = sse();
    mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [USER_MSG],
        task: null,
      }),
      "POST /api/v1/ai/sessions/s1/messages": () => stream.response(),
    });
    const { container } = renderApp("/ai/assistant/s1");
    await ask("Is 201 registered?");
    await stream.event("delta", { text: "Half an ans" });
    await stream.event("error", {
      code: "provider_error",
      message: "the model endpoint failed",
    });
    expect(
      await screen.findByText("The assistant could not answer"),
    ).toBeVisible();
    expect(
      screen.getByText("provider_error: the model endpoint failed"),
    ).toBeVisible();
    expect(container.querySelector(".aix-msg--live")).toBeNull();
    expect(container.querySelector(".aix-typing")).toBeNull();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled(); // empty draft
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
    // The stored question is shown again from the server.
    await waitFor(() =>
      expect(screen.getAllByText("Is 201 registered?").length).toBeGreaterThan(
        0,
      ),
    );
  });

  it("reports a refusal before the stream, such as a task still running", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [],
        task: null,
      }),
      "POST /api/v1/ai/sessions/s1/messages": apiError(
        409,
        "task_running",
        "a message is still being answered in this session",
      ),
    });
    const { container } = renderApp("/ai/assistant/s1");
    await ask("again");
    expect(
      await screen.findByText(/still being answered in this session/),
    ).toBeVisible();
    expect(container.querySelector(".aix-typing")).toBeNull();
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  });

  it("stops on request: aborts the request, drops the partial text, keeps the question", async () => {
    const stream = sse();
    const calls = mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [USER_MSG],
        task: null,
      }),
      "POST /api/v1/ai/sessions/s1/messages": () => stream.response(),
    });
    const { container } = renderApp("/ai/assistant/s1");
    await ask("Is 201 registered?");
    await stream.event("delta", { text: "Partial answ" });
    expect(container.querySelector(".aix-msg--live")).not.toBeNull();
    const signal = vi
      .mocked(fetch)
      .mock.calls.find(([url]) =>
        String(url).endsWith("/s1/messages"),
      )?.[1]?.signal;
    const loads = calls.filter(
      (c) => c.url === "/api/v1/ai/sessions/s1",
    ).length;

    fireEvent.click(screen.getByRole("button", { name: "Stop" }));
    expect(signal?.aborted).toBe(true);
    expect(await screen.findByText(/Stopped\./)).toBeVisible();
    expect(container.querySelector(".aix-msg--live")).toBeNull();
    expect(container.querySelector(".aix-typing")).toBeNull();
    await waitFor(() =>
      expect(
        calls.filter((c) => c.url === "/api/v1/ai/sessions/s1").length,
      ).toBeGreaterThan(loads),
    );
    expect(screen.queryByRole("button", { name: "Stop" })).toBeNull();
  });

  it("aborts the request when the page is left", async () => {
    const stream = sse();
    mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [],
        task: null,
      }),
      "POST /api/v1/ai/sessions/s1/messages": () => stream.response(),
    });
    const { unmount } = renderApp("/ai/assistant/s1");
    await ask("hi");
    await stream.event("started", { taskId: "t1", messageId: 1 });
    const signal = vi
      .mocked(fetch)
      .mock.calls.find(([url]) =>
        String(url).endsWith("/s1/messages"),
      )?.[1]?.signal;
    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("starts a new chat without remounting mid-stream, then opens it", async () => {
    const stream = sse();
    mockApi({
      ...ME,
      "GET /api/v1/ai/sessions": json({ items: [] }),
      "POST /api/v1/ai/sessions": json(SESSION, 201),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [USER_MSG, ANSWER],
        task: null,
      }),
      "POST /api/v1/ai/sessions/s1/messages": () => stream.response(),
    });
    renderApp("/ai/assistant");
    await ask("Is 201 registered?");
    await stream.event("delta", { text: "Extension 201 " });
    // Still the new-chat page: the stream was not cut by a navigation.
    expect(screen.getByTestId("location")).toHaveTextContent(
      /^\/ai\/assistant$/,
    );
    expect(screen.getByText(/Extension 201/)).toBeVisible();
    await stream.event("done", DONE);
    await stream.close();
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "/ai/assistant/s1",
      ),
    );
    expect(await screen.findByText(/listRegistrations \(200\)/)).toBeVisible();
  });
});
