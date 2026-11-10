import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

const WHEN = "2026-10-27T10:00:00Z";

const ROWS = [
  {
    id: 12,
    queueId: 7,
    number: "0471234567",
    callerName: "Ann",
    status: "waiting",
    attempts: 1,
    positionKey: Date.parse("2026-10-27T09:40:00Z"),
    nextAttemptAt: "2026-10-27T10:02:00Z",
    claimedBy: "",
    agent: "",
    result: "busy",
    createdAt: WHEN,
    expiresAt: "2026-10-27T12:00:00Z",
  },
  {
    id: 11,
    queueId: 7,
    number: "100",
    callerName: "",
    status: "connected",
    attempts: 1,
    positionKey: Date.parse("2026-10-27T09:30:00Z"),
    nextAttemptAt: WHEN,
    claimedBy: "",
    agent: "200",
    result: "connected",
    createdAt: WHEN,
    expiresAt: "2026-10-27T12:00:00Z",
    finishedAt: WHEN,
  },
];

describe("QueueCallbacks", () => {
  it("lists the requests and cancels an open one", async () => {
    let rows = ROWS;
    const calls = mockApi({
      ...ME,
      "GET /api/v1/queues/7/callbacks": () => json({ items: rows }),
      "DELETE /api/v1/queues/7/callbacks/12": () => {
        rows = [{ ...ROWS[0]!, status: "cancelled" }, ROWS[1]!];
        return noContent();
      },
    });
    renderApp("/queues/7/callbacks");
    const table = await screen.findByRole("table", {
      name: "Callback requests",
    });
    expect(within(table).getByText("0471234567")).toBeVisible();
    expect(within(table).getByText("Waiting")).toBeVisible();
    expect(within(table).getByText("Connected")).toBeVisible();
    expect(within(table).getByText("Agent 200")).toBeVisible();
    // Only the open request can be cancelled.
    expect(
      screen.queryByRole("button", { name: "Cancel callback to 100" }),
    ).toBeNull();
    fireEvent.click(
      screen.getByRole("button", { name: "Cancel callback to 0471234567" }),
    );
    expect(
      await screen.findByText("Callback to 0471234567 cancelled."),
    ).toBeVisible();
    await waitFor(() => expect(screen.getByText("Cancelled")).toBeVisible());
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("says so when there are no requests", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/queues/7/callbacks": () => json({ items: [] }),
    });
    renderApp("/queues/7/callbacks");
    expect(await screen.findByText("No callback requests")).toBeVisible();
  });
});
