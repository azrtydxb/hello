import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, renderApp } from "../test/api";

const queue = {
  id: 7,
  name: "support",
  strategy: "longest_idle",
  capacity: 3,
  waiting: [
    { conversationId: 41, channel: "sms", position: 1, waitSeconds: 190 },
    { conversationId: 42, channel: "webchat", position: 2, waitSeconds: 20 },
  ],
  waitingTotal: 2,
  assigned: 4,
  oldestWaitSeconds: 190,
  agents: [
    {
      userId: 4,
      name: "Agnes",
      extension: "101",
      dynamic: true,
      open: 3,
      answered: 12,
      state: "at_capacity",
    },
    {
      userId: 5,
      name: "Olga",
      extension: "102",
      dynamic: false,
      open: 1,
      answered: 9,
      state: "available",
    },
  ],
};

describe("Text queues", () => {
  it("shows who waits and who answers, with no customer text", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => json({ username: "otto", role: "operator" }),
      "GET /api/v1/me/inbox/status": () =>
        json({ enabled: true, unread: 0, unassigned: 2 }),
      "GET /api/v1/inbox/queues": () => json({ items: [queue] }),
    });
    renderApp("/inbox/queues");
    const waiting = await screen.findByRole("table", {
      name: "Waiting in support",
    });
    expect(within(waiting).getByText("3 min")).toBeInTheDocument();
    expect(within(waiting).getByText("#41")).toBeInTheDocument();
    const agents = screen.getByRole("table", { name: "Agents of support" });
    expect(within(agents).getByText("At capacity")).toBeInTheDocument();
    expect(within(agents).getByText("3 of 3")).toBeInTheDocument();
    expect(within(agents).getByText("Available")).toBeInTheDocument();
    expect(
      screen.getByText(/2 waiting, the longest for 3 min/),
    ).toBeInTheDocument();
  });

  it("says when no queue takes texts", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => json({ username: "otto", role: "operator" }),
      "GET /api/v1/me/inbox/status": () =>
        json({ enabled: true, unread: 0, unassigned: 0 }),
      "GET /api/v1/inbox/queues": () => json({ items: [] }),
    });
    renderApp("/inbox/queues");
    expect(await screen.findByText("No queue takes texts")).toBeInTheDocument();
  });
});
