import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { json, mockApi, renderApp } from "../test/api";

const me = (role: string) => ({
  "GET /api/v1/auth/me": () => json({ username: "pat", role }),
});
const ON = {
  "GET /api/v1/ai/status": json({ enabled: true, reason: null, agents: [] }),
};

const SESSION = {
  id: "s1",
  owner: 1,
  title: "Why did calls fail?",
  createdAt: "2026-10-08T10:00:00Z",
  lastActiveAt: "2026-10-08T10:00:00Z",
};
const USER_MSG = {
  id: 1,
  role: "user",
  content: "Why did calls fail?",
  toolCalls: null,
  citations: null,
  proposalId: null,
  taskId: "t1",
  createdAt: "2026-10-08T10:00:00Z",
};
const ANSWER = {
  id: 2,
  role: "assistant",
  content:
    "Look at <img src=x onerror=alert(1)> and [click](http://evil.test)\nsecond line",
  toolCalls: [
    { operationId: "listCdrs", status: 200, truncated: true },
    { operationId: "getCluster", status: 200, truncated: false },
  ],
  citations: [0],
  proposalId: "p1",
  taskId: "t1",
  createdAt: "2026-10-08T10:00:05Z",
};
const PROPOSAL = {
  id: "p1",
  source: "assistant",
  sessionId: "s1",
  findingId: null,
  title: "Raise the ring timeout",
  rationale: "Longer ring",
  status: "open",
  configRevision: 1,
  actions: [],
  hasDelete: false,
  createdAt: "2026-10-08T10:00:00Z",
  updatedAt: "2026-10-08T10:00:00Z",
};

describe("AI assistant", () => {
  beforeEach(() => vi.useFakeTimers({ shouldAdvanceTime: true }));
  afterEach(() => vi.useRealTimers());

  it("renders answers as plain text, lists data used, and shows the proposal inline", async () => {
    mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": json({
        session: SESSION,
        messages: [USER_MSG, ANSWER],
        task: null,
      }),
      "GET /api/v1/ai/proposals/p1": json(PROPOSAL),
    });
    const { container } = renderApp("/ai/assistant/s1");
    expect(await screen.findByText(/second line/)).toBeVisible();
    expect(container.querySelector("main img")).toBeNull();
    expect(container.querySelector("main a[href^='http']")).toBeNull();
    expect(screen.getByText(/\[click\]\(http:\/\/evil.test\)/)).toBeVisible();
    expect(
      screen.getByText(/listCdrs \(200\) — result truncated/),
    ).toBeVisible();
    expect(screen.queryByText(/getCluster \(200\)/)).toBeNull();
    expect(
      await screen.findByRole("article", {
        name: "Proposal: Raise the ring timeout",
      }),
    ).toBeVisible();
  });

  it("posts a message, polls the task every 2 s and shows the answer", async () => {
    let done = false;
    const calls = mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/sessions": json({ items: [SESSION] }),
      "GET /api/v1/ai/sessions/s1": () =>
        json({
          session: SESSION,
          messages: done
            ? [USER_MSG, { ...ANSWER, proposalId: null }]
            : [USER_MSG],
          task: null,
        }),
      "POST /api/v1/ai/sessions/s1/messages": json(
        { taskId: "t1", messageId: 1 },
        202,
      ),
      "GET /api/v1/ai/tasks/t1": () => {
        const status = done ? "succeeded" : "running";
        return json({
          id: "t1",
          kind: "message",
          status,
          sessionId: "s1",
          errorCode: null,
          errorMessage: null,
          createdAt: "2026-10-08T10:00:00Z",
        });
      },
    });
    renderApp("/ai/assistant/s1");
    await screen.findByText("Message");
    fireEvent.change(screen.getByLabelText("Message"), {
      target: { value: "Why did calls fail?" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/s1/messages"))).toBe(true),
    );
    expect(calls.find((c) => c.url.endsWith("/s1/messages"))?.body).toEqual({
      content: "Why did calls fail?",
    });
    expect(
      await screen
        .findByText("The assistant is working…", {}, { timeout: 100 })
        .catch(() => null),
    ).toBeDefined();

    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(calls.filter((c) => c.url === "/api/v1/ai/tasks/t1")).toHaveLength(
      1,
    );
    done = true;
    await act(() => vi.advanceTimersByTimeAsync(2000));
    expect(calls.filter((c) => c.url === "/api/v1/ai/tasks/t1")).toHaveLength(
      2,
    );
    expect(await screen.findByText(/second line/)).toBeVisible();
  });

  it("limits a message to 4000 characters", async () => {
    mockApi({
      ...me("operator"),
      ...ON,
      "GET /api/v1/ai/sessions": json({ items: [] }),
    });
    renderApp("/ai/assistant");
    const box = await screen.findByLabelText("Message");
    expect(box).toHaveAttribute("maxlength", "4000");
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
  });

  it("hides chat from a viewer", async () => {
    mockApi({ ...me("viewer"), ...ON });
    renderApp("/ai/assistant");
    expect(
      await screen.findByText("The assistant is for operators"),
    ).toBeVisible();
    expect(screen.queryByLabelText("Message")).toBeNull();
    expect(screen.queryByRole("button", { name: "Send" })).toBeNull();
  });

  it("shows the reason while AI is off", async () => {
    mockApi({
      ...me("operator"),
      "GET /api/v1/ai/status": json({
        enabled: false,
        reason: "incomplete_configuration",
      }),
    });
    renderApp("/ai/assistant");
    expect(await screen.findByText("The AI agent is off")).toBeVisible();
    expect(screen.queryByLabelText("Message")).toBeNull();
  });
});
