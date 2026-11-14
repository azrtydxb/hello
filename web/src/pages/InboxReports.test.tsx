import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { json, mockApi, renderApp, type Routes } from "../test/api";
import { duration } from "./InboxReports";

const NOW = new Date("2026-11-15T10:00:00.000Z");

/** The query string the page asks for: the last `days` days, a grouping. */
function q(days: number, groupBy: string): string {
  const from = new Date(NOW.getTime() - days * 86_400_000).toISOString();
  const p = new URLSearchParams({ from, to: NOW.toISOString() });
  if (groupBy) p.set("groupBy", groupBy);
  return p.toString();
}

const dur = (count: number, p50: number) => ({
  count,
  avgSeconds: p50,
  p50Seconds: p50,
  p90Seconds: p50 * 2,
});

const row = (
  key: string,
  label: string,
  over: Record<string, unknown> = {},
) => ({
  key,
  label,
  conversations: 4,
  inboundConversations: 3,
  outboundConversations: 1,
  resolved: 2,
  messagesIn: 9,
  messagesOut: 7,
  firstResponse: dur(3, 240),
  resolution: dur(2, 7200),
  slaFirstResponseBreaches: 1,
  slaResolutionBreaches: 0,
  ...over,
});

const report = (groupBy: string, rows: unknown[]) => ({
  from: "2026-11-08T10:00:00Z",
  to: "2026-11-15T10:00:00Z",
  groupBy,
  timeZone: "UTC",
  sla: { firstResponseSeconds: 900, resolutionSeconds: 86400 },
  totals: row("", "All"),
  rows,
});

function api(role: string, more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    "GET /api/v1/me/inbox/status": () =>
      json({ enabled: true, unread: 0, unassigned: 0, senders: [] }),
    [`GET /api/v1/inbox/reports?${q(7, "channel")}`]: () =>
      json(report("channel", [row("1", "Main line")])),
    "GET /api/v1/messaging/sla": () =>
      json({ firstResponseSeconds: 900, resolutionSeconds: 86400 }),
    ...more,
  });
}

beforeEach(() => {
  vi.useFakeTimers({ toFake: ["Date"] });
  vi.setSystemTime(NOW);
});
afterEach(() => {
  vi.useRealTimers();
});

describe("Inbox reports", () => {
  it("shows the totals and the groups, without customer text", async () => {
    api("operator");
    renderApp("/inbox/reports");
    const table = await screen.findByRole("table", {
      name: /Inbox report by channel/,
    });
    expect(within(table).getByText("Main line")).toBeInTheDocument();
    expect(within(table).getByText("9 / 7")).toBeInTheDocument();
    expect(within(table).getAllByText("4 min").length).toBeGreaterThan(0);
    expect(screen.getByText(/90% within 8 min/)).toBeInTheDocument();
    expect(screen.getByText(/first response over 15 min/)).toBeInTheDocument();
    // The CSV export carries the same period and grouping.
    const csv = screen.getByRole("link", { name: /Export CSV/ });
    expect(csv).toHaveAttribute(
      "href",
      `/api/v1/inbox/reports/export?${q(7, "channel")}`,
    );
    // Targets are an administrator's.
    expect(screen.queryByText("Service-level targets")).toBeNull();
  });

  it("asks again for another period and grouping", async () => {
    const calls = api("operator", {
      [`GET /api/v1/inbox/reports?${q(30, "agent")}`]: () =>
        json(report("agent", [row("2", "Agnes"), row("", "Unassigned")])),
      [`GET /api/v1/inbox/reports?${q(30, "channel")}`]: () =>
        json(report("channel", [])),
    });
    renderApp("/inbox/reports");
    await screen.findByRole("table", { name: /by channel/ });
    fireEvent.change(screen.getByLabelText("Period"), {
      target: { value: "30" },
    });
    expect(
      await screen.findByText("No conversations in this period"),
    ).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Split"), {
      target: { value: "agent" },
    });
    const table = await screen.findByRole("table", { name: /by agent/ });
    expect(within(table).getByText("Unassigned")).toBeInTheDocument();
    expect(
      calls.filter((c) => c.url.startsWith("/api/v1/inbox/reports?")),
    ).toHaveLength(3);
  });

  it("lets an administrator set the targets in minutes", async () => {
    const calls = api("admin", {
      "PUT /api/v1/messaging/sla": () =>
        json({ firstResponseSeconds: 300, resolutionSeconds: 0 }),
    });
    renderApp("/inbox/reports");
    const first = await screen.findByLabelText(
      "First response within (minutes)",
    );
    expect(first).toHaveValue("15");
    fireEvent.change(first, { target: { value: "-1" } });
    fireEvent.click(screen.getByRole("button", { name: "Save targets" }));
    expect(
      await screen.findByText("Enter 0 (off) to 10080 minutes."),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
    fireEvent.change(first, { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Resolved within (minutes)"), {
      target: { value: "0" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save targets" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        firstResponseSeconds: 300,
        resolutionSeconds: 0,
      }),
    );
  });

  it("writes durations for people", () => {
    expect(duration(42)).toBe("42 s");
    expect(duration(600)).toBe("10 min");
    expect(duration(5400)).toBe("1.5 h");
    expect(duration(172800)).toBe("2.0 d");
  });
});
