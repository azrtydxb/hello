import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, renderApp } from "../test/api";
import { callsPerNode } from "./Calls";
import { shortCallId, stepKind } from "./calls/common";

const STARTED = new Date(Date.now() - 65_000).toISOString();

function call(
  id: string,
  from: string,
  to: string,
  state: string,
  node: string,
) {
  return {
    id,
    sipCallId: `${id}abcdef@10.89.53.101`,
    from,
    to,
    state,
    node,
    media: "anchored",
    startedAt: STARTED,
    ...(state === "connected" ? { answeredAt: STARTED } : {}),
  };
}

describe("Active calls", () => {
  it("lists live calls with names, states, nodes and per-node counts", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/calls": () =>
        json({
          items: [
            call("c1", "1001", "+442071838750", "connected", "hello-sip-1"),
            call("c2", "1004", "1011", "ringing", "hello-sip-1"),
          ],
        }),
      "GET /api/v1/extensions": () =>
        json({
          items: [
            { id: 1, number: "1001", name: "Amira Haddad" },
            { id: 2, number: "1011", name: "Support desk" },
          ],
        }),
      "GET /api/v1/cluster": () =>
        json({
          members: [
            { id: "hello-sip-1", kind: "sip", state: "READY" },
            { id: "hello-sip-2", kind: "sip", state: "UNHEALTHY" },
          ],
          postgres: { up: true },
          valkey: { up: true, mode: "single" },
          configRevision: 1,
        }),
    });
    renderApp("/calls");

    const table = await screen.findByRole("table", { name: "Active calls" });
    expect(await within(table).findByText("Amira Haddad")).toBeVisible();
    expect(within(table).getByText("Support desk")).toBeVisible();
    expect(within(table).getByText("Answered")).toHaveClass("az-badge--good");
    expect(within(table).getByText("Ringing")).toBeVisible();
    expect(within(table).getAllByText("Anchored")).toHaveLength(2);
    expect(within(table).getAllByText(/^1:[0-5][0-9]$/).length).toBeGreaterThan(
      0,
    );
    expect(within(table).getByText("c1ab…@10.89.53.101")).toHaveAttribute(
      "title",
      "c1abcdef@10.89.53.101",
    );

    const nodes = await screen.findByRole("list", {
      name: "Calls per SIP node",
    });
    const items = within(nodes).getAllByRole("listitem");
    expect(items.map((n) => n.textContent)).toEqual([
      "hello-sip-1, READY, calls:2",
      "hello-sip-2, UNHEALTHY, calls:0",
    ]);
    expect(screen.getByText("LIVE · 5 S")).toBeVisible();
  });

  it("says when no call is in progress", async () => {
    mockApi({ ...ME, "GET /api/v1/calls": () => json({ items: [] }) });
    renderApp("/calls");

    expect(await screen.findByText("No calls in progress")).toBeVisible();
    expect(screen.queryByRole("table")).toBeNull();
  });

  it("explains a live-state outage", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/calls": () =>
        apiError(503, "unavailable", "live state (Valkey) is unavailable"),
    });
    renderApp("/calls");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load active calls");
    expect(alert).toHaveTextContent("live state (Valkey) is unavailable");
    expect(screen.getByText("PAUSED")).toBeVisible();
  });
});

describe("calls helpers", () => {
  it("counts calls on nodes outside the cluster too", () => {
    const counts = callsPerNode(
      [call("a", "1", "2", "ringing", "hello-sip-9")],
      [],
    );
    expect(counts).toEqual([{ id: "hello-sip-9", calls: 1 }]);
  });

  it("shortens a long Call-ID and keeps a short one", () => {
    expect(shortCallId("7b1e9f00@10.0.0.1")).toBe("7b1e…@10.0.0.1");
    expect(shortCallId("abc@h")).toBe("abc@h");
  });

  it("classifies routing trace steps", () => {
    expect(stepKind('Route "Intl" matched (prefix 00)', false, false)).toBe(
      "hit",
    );
    expect(stepKind('Route "X" skipped: disabled', false, false)).toBe("miss");
    expect(
      stepKind(
        "carrier-primary (sbc1:5060) -> 503 Service Unavailable",
        false,
        false,
      ),
    ).toBe("warn");
    expect(stepKind("Caller ID 1001 (extension number)", false, false)).toBe(
      "info",
    );
    expect(stepKind("Caller ID 1001 (extension number)", true, true)).toBe(
      "fail",
    );
  });
});
