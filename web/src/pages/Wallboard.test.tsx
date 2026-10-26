import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";
import { mmss } from "./Wallboard";

const STATUS = {
  items: [
    {
      name: "sales",
      number: "600",
      enabled: true,
      strategy: "ring_all",
      waiting: [
        {
          callId: "c1",
          caller: "0501234567",
          callerName: "",
          position: 1,
          waitSeconds: 95,
          offering: "100",
          node: "sip-1",
        },
        {
          callId: "c2",
          caller: "0507654321",
          callerName: "",
          position: 2,
          waitSeconds: 20,
          offering: "",
          node: "sip-2",
        },
      ],
      longestWaitSeconds: 95,
      avgHandleSeconds: 120,
      staffed: 1,
      available: 0,
      agents: [
        {
          extension: "100",
          name: "Reception",
          dynamic: true,
          penalty: 0,
          loggedIn: true,
          paused: false,
          pauseReason: "",
          state: "ringing",
          callsAnswered: 4,
          wrapSeconds: 0,
        },
        {
          extension: "200",
          name: "Kitchen",
          dynamic: true,
          penalty: 1,
          loggedIn: false,
          paused: false,
          pauseReason: "",
          state: "logged_out",
          callsAnswered: 0,
          wrapSeconds: 0,
        },
      ],
    },
  ],
};

describe("mmss", () => {
  it("formats seconds as minutes and seconds", () => {
    expect(mmss(0)).toBe("0:00");
    expect(mmss(95)).toBe("1:35");
    expect(mmss(3725)).toBe("1:02:05");
    expect(mmss(-3)).toBe("0:00");
  });
});

describe("Wallboard", () => {
  it("shows the waiting callers, the longest wait and the agents", async () => {
    mockApi({ ...ME, "GET /api/v1/queues/status": () => json(STATUS) });
    renderApp("/wallboard");
    const panel = await screen.findByRole("region", { name: "Queue sales" });
    expect(within(panel).getAllByText("1:35")).toHaveLength(2); // the longest wait and the first caller
    const waiting = within(panel).getByRole("table", {
      name: "Callers waiting in sales",
    });
    expect(within(waiting).getAllByRole("row")).toHaveLength(3);
    expect(within(waiting).getByText("0507654321")).toBeVisible();
    const agents = within(panel).getByRole("table", {
      name: "Agents of sales",
    });
    expect(within(agents).getByText("Ringing")).toBeVisible();
    expect(within(agents).getByText("Logged out")).toBeVisible();
  });

  it("logs an agent in from the wallboard", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/queues/status": () => json(STATUS),
      "PUT /api/v1/queue-agents/200": () => json({ extension: "200" }),
    });
    renderApp("/wallboard");
    fireEvent.click(
      await screen.findByRole("button", { name: "Log in agent 200" }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PUT",
        url: "/api/v1/queue-agents/200",
        body: { loggedIn: true },
      }),
    );
  });

  it("says when there are no queues", async () => {
    mockApi({ ...ME, "GET /api/v1/queues/status": () => json({ items: [] }) });
    renderApp("/wallboard");
    expect(await screen.findByText("No queues")).toBeVisible();
  });
});
