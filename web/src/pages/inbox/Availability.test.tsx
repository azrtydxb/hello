import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  apiError,
  json,
  mockApi,
  renderApp,
  type Routes,
} from "../../test/api";

class FakeEventSource {
  addEventListener() {}
  close() {}
}

const member = (over: Record<string, unknown> = {}) => ({
  member: true,
  needsLogin: true,
  loggedIn: true,
  paused: false,
  reason: "",
  extensions: [
    {
      number: "101",
      name: "Agnes",
      queues: ["support"],
      dynamic: true,
      loggedIn: true,
      paused: false,
    },
  ],
  ...over,
});

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "agnes", role: "user" }),
    "GET /api/v1/me/inbox/status": () =>
      json({ enabled: true, unread: 0, unassigned: 0 }),
    "GET /api/v1/me/inbox/agents": () => json({ items: [] }),
    "GET /api/v1/me/inbox/conversations?view=all": () =>
      json({ items: [], next: "" }),
    ...more,
  });
}

beforeEach(() => {
  vi.stubGlobal("EventSource", FakeEventSource);
});
afterEach(() => vi.unstubAllGlobals());

describe("Agent availability", () => {
  it("pauses and resumes through the call queues' state", async () => {
    const calls = api({
      "GET /api/v1/me/inbox/agent": () => json(member()),
      "POST /api/v1/me/inbox/agent": [
        () => json(member({ paused: true, reason: "pause" })),
        () => json(member()),
      ],
    });
    renderApp("/me/inbox");
    expect(await screen.findByText("Available")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Pause" }));
    expect(await screen.findByText("Paused: pause")).toBeInTheDocument();
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        paused: true,
      }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Resume" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "POST").at(-1)?.body).toEqual({
        paused: false,
      }),
    );
  });

  it("logs a dynamic agent in", async () => {
    const calls = api({
      "GET /api/v1/me/inbox/agent": () => json(member({ loggedIn: false })),
      "POST /api/v1/me/inbox/agent": () => json(member()),
    });
    renderApp("/me/inbox");
    expect(await screen.findByText("Logged out")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Pause" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Log in" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        loggedIn: true,
      }),
    );
  });

  it("says why the server refuses", async () => {
    api({
      "GET /api/v1/me/inbox/agent": () => json(member()),
      "POST /api/v1/me/inbox/agent": () =>
        apiError(503, "unavailable", "live state is unavailable"),
    });
    renderApp("/me/inbox");
    fireEvent.click(await screen.findByRole("button", { name: "Pause" }));
    expect(
      await screen.findByText("live state is unavailable"),
    ).toBeInTheDocument();
  });

  it("shows nothing to someone who answers no text queue", async () => {
    api({
      "GET /api/v1/me/inbox/agent": () =>
        json({
          member: false,
          needsLogin: false,
          loggedIn: false,
          paused: false,
          reason: "",
          extensions: [],
        }),
    });
    renderApp("/me/inbox");
    await screen.findByRole("heading", { level: 1, name: "My inbox" });
    await screen.findByRole("list", { name: "Conversations" });
    expect(screen.queryByRole("group", { name: "Availability" })).toBeNull();
  });
});
