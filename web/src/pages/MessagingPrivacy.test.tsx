import { fireEvent, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { json, mockApi, renderApp, type Routes } from "../test/api";

const NOW = new Date("2026-11-15T10:00:00.000Z");

function range(days: number, more: Record<string, string> = {}): string {
  const p = new URLSearchParams({
    from: new Date(NOW.getTime() - days * 86_400_000).toISOString(),
    to: new Date(NOW.getTime() + 60_000).toISOString(),
    ...more,
  });
  return p.toString();
}

const view = (id: number, actor: string, userName: string, conv: number) => ({
  id,
  at: "2026-11-14T09:00:00Z",
  actor,
  userId: id,
  userName,
  conversationId: conv,
  via: "",
});

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "a", role: "admin" }),
    "GET /api/v1/messaging/redactions": () =>
      json({
        items: [
          {
            provider: "telnyx",
            pending: 0,
            done: 0,
            failed: 0,
            unsupported: 2,
          },
          {
            provider: "twilio",
            pending: 1,
            done: 5,
            failed: 0,
            unsupported: 0,
          },
        ],
      }),
    [`GET /api/v1/inbox/audit/supervisor-views?${range(30)}`]: () =>
      json({
        items: [view(12, "user:otto", "Otto", 7)],
        nextBefore: 12,
        totals: [
          { actor: "user:otto", userName: "Otto", views: 2, conversations: 2 },
        ],
      }),
    [`GET /api/v1/inbox/audit/supervisor-views?${range(30, { before: "12" })}`]:
      () =>
        json({
          items: [view(9, "user:otto", "Otto", 5)],
          nextBefore: 0,
          totals: [],
        }),
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

describe("Messaging privacy", () => {
  it("offers the export of a customer only for a number, with notes on request", async () => {
    api();
    renderApp("/messaging/privacy");
    const input = await screen.findByLabelText("Customer's number");
    expect(
      screen.getByRole("button", { name: /Download export/ }),
    ).toBeDisabled();
    fireEvent.change(input, { target: { value: "+32 475 12 34 56" } });
    const link = screen.getByRole("link", { name: /Download export/ });
    expect(link).toHaveAttribute(
      "href",
      "/api/v1/inbox/export?peer=%2B32+475+12+34+56",
    );
    fireEvent.click(screen.getByLabelText("Include internal notes"));
    expect(
      screen.getByRole("link", { name: /Download export/ }),
    ).toHaveAttribute(
      "href",
      "/api/v1/inbox/export?peer=%2B32+475+12+34+56&notes=true",
    );
    fireEvent.change(input, { target: { value: "<script>" } });
    expect(
      screen.getByText("Enter a phone number or visitor:<id>."),
    ).toBeInTheDocument();
  });

  it("shows the deletions at the carriers", async () => {
    api();
    renderApp("/messaging/privacy");
    const table = await screen.findByRole("table", {
      name: "Deletions at the carriers",
    });
    const rows = within(table).getAllByRole("row");
    expect(rows[1]).toHaveTextContent("telnyx");
    expect(rows[2]).toHaveTextContent("twilio");
  });

  it("lists the supervisor views with totals and pages back", async () => {
    const calls = api();
    renderApp("/messaging/privacy");
    const totals = await screen.findByRole("table", {
      name: "Supervisor views per person",
    });
    expect(within(totals).getByText("Otto")).toBeInTheDocument();
    const list = screen.getByRole("table", { name: "Supervisor views" });
    expect(within(list).getByText("#7")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show older views" }));
    expect(await within(list).findByText("#5")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Show older views" }),
    ).toBeNull();
    expect(
      calls.filter((c) => c.url.includes("supervisor-views")),
    ).toHaveLength(2);
  });
});
