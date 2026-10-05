import { act, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { apiError, json, mockApi } from "../test/api";
import { Calls } from "./Calls";
import { Registrations } from "./Registrations";

const BINDING = {
  aor: "sip:desk-phone@pbx.test",
  extension: "101",
  device: "desk-phone",
  contactUri: "sip:desk-phone@192.0.2.10:5060",
  source: "192.0.2.10:5060",
  transport: "udp",
  userAgent: "TestPhone/1.0",
  receivedNode: "hello-sip-1",
  expires: "2026-10-01T11:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const CALL = {
  id: "corr-1",
  sipCallId: "abc@host",
  from: "101",
  to: "102",
  state: "ringing",
  node: "hello-sip-2",
  media: "direct",
  startedAt: "2026-10-01T10:00:00Z",
  ha: "owned",
};

async function flush(ms = 0) {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
}

describe("live pages", () => {
  beforeEach(() => {
    vi.useFakeTimers();
  });
  afterEach(() => {
    vi.useRealTimers();
  });

  it("Registrations refreshes every 5 seconds", async () => {
    vi.setSystemTime(new Date("2026-10-01T10:06:55Z"));
    const calls = mockApi({
      "GET /api/v1/registrations": [
        () => json({ items: [] }),
        () => json({ items: [BINDING] }),
        () => apiError(503, "unavailable", "live state is unavailable"),
      ],
    });
    render(<Registrations />);
    await flush();
    expect(
      screen.getByRole("heading", { name: "No devices are registered" }),
    ).toBeVisible();
    expect(screen.getByText("LIVE · 5 S")).toBeVisible();

    await flush(4900);
    expect(calls).toHaveLength(1);
    await flush(100);
    expect(calls).toHaveLength(2);
    expect(screen.getByRole("columnheader", { name: "AOR" })).toBeVisible();
    const row = screen.getByRole("row", { name: /desk-phone@pbx.test/ });
    expect(
      within(row)
        .getAllByRole("cell")
        .map((c) => c.textContent),
    ).toEqual([
      "sip:desk-phone@pbx.testdesk-phone",
      "sip:desk-phone@192.0.2.10:5060",
      "192.0.2.10:5060",
      "UDP",
      "TestPhone/1.0",
      "hello-sip-1",
      "53 min",
    ]);

    // A failed refresh keeps the last list and says so.
    await flush(5000);
    expect(screen.getByRole("alert")).toHaveTextContent(
      /Could not load registrations.*Showing the last list received/,
    );
    expect(
      screen.getByRole("row", { name: /desk-phone@pbx.test/ }),
    ).toBeVisible();
    expect(screen.queryByText("LIVE · 5 S")).toBeNull();
  });

  it("Active Calls refreshes every 5 seconds and stops on unmount", async () => {
    const calls = mockApi({
      "GET /api/v1/calls": [
        () => json({ items: [CALL] }),
        () => json({ items: [] }),
      ],
    });
    const { unmount } = render(<Calls />);
    await flush();
    expect(screen.getByText("Ringing")).toBeVisible();

    await flush(5000);
    expect(screen.getByText("No calls in progress")).toBeVisible();

    unmount();
    await flush(15000);
    expect(calls.filter((c) => c.url === "/api/v1/calls")).toHaveLength(2);
  });

  it("Active Calls badges a taken-over call and marks the others owned", async () => {
    mockApi({
      "GET /api/v1/calls": json({
        items: [
          { ...CALL, id: "corr-owned", from: "101", state: "connected" },
          {
            ...CALL,
            id: "corr-taken",
            from: "103",
            state: "connected",
            node: "hello-sip-1",
            ha: "taken-over",
          },
        ],
      }),
    });
    render(<Calls />);
    await flush();
    expect(screen.getByRole("columnheader", { name: "HA" })).toBeVisible();
    const taken = screen.getByRole("row", { name: /103/ });
    expect(within(taken).getByText("Taken over")).toHaveClass("az-badge");
    const owned = screen.getByRole("row", { name: /101/ });
    expect(within(owned).getByText("Owned")).toBeVisible();
    expect(within(owned).queryByText("Taken over")).toBeNull();
  });
});
