import { act, render, screen } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { json, mockApi } from "../test/api";
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
    const calls = mockApi({
      "GET /api/v1/registrations": [
        () => json({ items: [] }),
        () => json({ items: [BINDING] }),
      ],
    });
    render(<Registrations />);
    await flush();
    expect(screen.getByText("No devices are registered.")).toBeVisible();

    await flush(4900);
    expect(calls).toHaveLength(1);
    await flush(100);
    expect(calls).toHaveLength(2);
    expect(screen.getByRole("columnheader", { name: "Device" })).toBeVisible();
    expect(screen.getByRole("rowheader", { name: "101" })).toBeVisible();
    expect(screen.getByText("TestPhone/1.0")).toBeVisible();
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
});
