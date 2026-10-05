import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { LIVE_REFRESH_MS } from "../usePolling";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";
import { geometry } from "./Cluster";

const NOW = Date.now();
const ago = (s: number) => new Date(NOW - s * 1000).toISOString();

function member(id: string, over: Record<string, unknown> = {}) {
  return {
    id,
    kind: "sip",
    state: "READY",
    sipAddr: "10.0.0.11:5060",
    transports: ["udp"],
    activeCalls: 12,
    registrations: 241,
    version: "0.3.0",
    configRevision: 1284,
    revisionLag: 0,
    startedAt: ago(3600),
    heartbeat: ago(3),
    ...over,
  };
}

function cluster(members: unknown[]) {
  return json({
    members,
    postgres: { up: true },
    valkey: { up: true, mode: "sentinel", primary: "10.0.0.21:6379" },
    configRevision: 1284,
  });
}

const DRAIN_1 = "POST /api/v1/cluster/nodes/hello-sip-1/drain";
const LAST_READY = () =>
  json(
    {
      error: {
        code: "conflict",
        message: "draining hello-sip-1 would leave no READY SIP node",
      },
    },
    409,
  );

/** The topology box of a service, by its title. */
const box = (title: string) =>
  screen.getByRole("button", { name: (n) => n.startsWith(`${title}:`) });

async function openBox(title: string) {
  fireEvent.click(
    await screen.findByRole("button", {
      name: (n) => n.startsWith(`${title}:`),
    }),
  );
  return screen.findByRole("dialog", { name: title });
}

describe("Cluster", () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it("draws every node with its state and load, and the dependencies", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cluster": () =>
        cluster([
          member("hello-sip-1"),
          member("hello-sip-2", {
            state: "DRAINING",
            reason: "drain requested",
            sipAddr: "10.0.0.12:5060",
            activeCalls: 2,
            registrations: 96,
            version: "0.2.9",
            configRevision: 1281,
            revisionLag: 3,
            heartbeat: ago(7),
          }),
          member("hello-control-1", {
            kind: "control",
            sipAddr: undefined,
            httpAddr: "10.0.0.20:8081",
            transports: undefined,
            activeCalls: 0,
            registrations: 0,
          }),
        ]),
    });
    renderApp("/cluster");

    const one = await screen.findByRole("button", {
      name: "hello-sip-1: Ready",
    });
    expect(one).toHaveTextContent("10.0.0.11:5060 · 0.3.0");
    expect(one).toHaveTextContent("Revision current (rev 1284)");
    expect(one).toHaveTextContent("Calls12");
    expect(one).toHaveTextContent("Regs241");
    const two = box("hello-sip-2");
    expect(two).toHaveAccessibleName("hello-sip-2: Draining");
    expect(two).toHaveTextContent("drain requested");
    expect(box("hello-control")).toHaveTextContent("hello-control-1Ready");
    expect(box("valkey")).toHaveAccessibleName("valkey: Up");
    expect(box("valkey")).toHaveTextContent("Primary 10.0.0.21:6379");
    expect(box("postgres")).toHaveAccessibleName("postgres: Up");
    expect(box("kamailio")).toHaveAccessibleName("kamailio: Not monitored");
    expect(
      screen.getByText(/Configuration revision 1284\./),
    ).toBeInTheDocument();

    const dialog = await openBox("hello-sip-2");
    expect(within(dialog).getByText("SIP node · 0.2.9")).toBeVisible();
    expect(within(dialog).getByText("Draining")).toBeVisible();
    expect(within(dialog).getByText("drain requested")).toBeVisible();
    expect(within(dialog).getByText("10.0.0.12:5060 (udp)")).toBeVisible();
    expect(within(dialog).getByText("3 behind (rev 1281)")).toBeVisible();
    expect(within(dialog).getByText(/^\d+ s ago$/)).toBeVisible();
    // Focus moves into the dialog, and Close returns it to the box.
    await waitFor(() =>
      expect(
        within(dialog).getAllByRole("button", { name: "Close" })[0],
      ).toHaveFocus(),
    );
    fireEvent.click(
      within(dialog).getAllByRole("button", { name: "Close" })[1]!,
    );
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(box("hello-sip-2")).toHaveFocus();
  });

  it("shows live views around the cluster, and — for what it cannot read", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cluster": () => cluster([member("hello-sip-1")]),
      "GET /api/v1/devices": () =>
        json({
          items: [
            { id: 1, extensionId: 1, sipUsername: "desk", enabled: true },
            { id: 2, extensionId: 1, sipUsername: "lobby", enabled: true },
          ],
        }),
      "GET /api/v1/registrations": () =>
        json({ items: [{ device: "desk", aor: "sip:desk@pbx.test" }] }),
      "GET /api/v1/calls": () => json({ items: [] }),
      "GET /api/v1/trunks/status": () =>
        json({
          items: [
            {
              trunkId: 4,
              name: "carrier-primary",
              activeCalls: 3,
              destinations: [
                {
                  destination: "sbc1:5060",
                  up: false,
                  lastCode: 408,
                  checkedAt: ago(5),
                },
                { destination: "sbc2:5060", up: true, checkedAt: ago(5) },
              ],
            },
          ],
        }),
      "GET /api/v1/trunks": () =>
        json({
          items: [
            { id: 4, name: "carrier-primary", maxCalls: 30, destinations: [] },
          ],
        }),
    });
    renderApp("/cluster");

    await waitFor(() =>
      expect(box("Phones")).toHaveAccessibleName("Phones: 1 reg"),
    );
    expect(box("Phones")).toHaveTextContent("Devices2Registered1Calls0");
    expect(box("Carriers")).toHaveAccessibleName("Carriers: Degraded");
    expect(box("Carriers")).toHaveTextContent("carrier-primary3 / 30");
    expect(box("Carriers")).toHaveTextContent("1 of 2 destinations up");

    const dialog = await openBox("Phones");
    expect(within(dialog).getByText("lobby")).toBeVisible();
  });

  it("shows unknown revision while PostgreSQL is down, and the Valkey error", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cluster": () =>
        json({
          members: [member("hello-sip-1", { revisionLag: undefined })],
          postgres: { up: false, error: "unavailable" },
          valkey: { up: false, mode: "sentinel", error: "members unavailable" },
          configRevision: null,
        }),
    });
    renderApp("/cluster");

    expect(
      await screen.findByRole("button", { name: "hello-sip-1: Ready" }),
    ).toHaveTextContent("Revision — (rev 1284)");
    expect(screen.getByText(/Configuration revision —\./)).toBeInTheDocument();
    expect(box("postgres")).toHaveAccessibleName("postgres: Down");
    expect(box("postgres")).toHaveTextContent("unavailable");
    expect(box("valkey")).toHaveAccessibleName("valkey: Down");
    expect(box("valkey")).toHaveTextContent("members unavailable");
  });

  it("refreshes every 5 seconds", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/cluster": [
        () => cluster([member("hello-sip-1")]),
        () =>
          cluster([
            member("hello-sip-1", {
              state: "UNHEALTHY",
              reason: "valkey unreachable",
            }),
          ]),
      ],
    });
    // The poll's timer is driven by the test, not by the wall clock.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    renderApp("/cluster");
    await screen.findByRole("button", { name: "hello-sip-1: Ready" });

    await act(() => vi.advanceTimersByTimeAsync(LIVE_REFRESH_MS));
    expect(
      await screen.findByRole("button", { name: "hello-sip-1: Unhealthy" }),
    ).toHaveTextContent("valkey unreachable");
    expect(
      calls.filter((c) => c.url === "/api/v1/cluster").length,
    ).toBeGreaterThanOrEqual(2);
  });

  it("drains a node only after an in-dialog confirmation", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/cluster": [
        () => cluster([member("hello-sip-1"), member("hello-sip-2")]),
        () =>
          cluster([
            member("hello-sip-1", { state: "DRAINING" }),
            member("hello-sip-2"),
          ]),
      ],
      [DRAIN_1]: noContent,
    });
    // No poll fires on its own, so a second read is the re-read after drain.
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    renderApp("/cluster");

    const dialog = await openBox("hello-sip-1");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Drain hello-sip-1" }),
    );
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    const confirm = within(dialog).getByRole("group", {
      name: "Drain hello-sip-1?",
    });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Drain node" }),
    );

    expect(await screen.findByText("hello-sip-1 is draining.")).toBeVisible();
    expect(calls.filter((c) => c.method === "POST").map((c) => c.url)).toEqual([
      "/api/v1/cluster/nodes/hello-sip-1/drain",
    ]);
    // The page re-reads the cluster right away, not at the next poll.
    await waitFor(() =>
      expect(box("hello-sip-1")).toHaveAccessibleName("hello-sip-1: Draining"),
    );
  });

  it("warns when draining the last READY node and sends force=true only after a second confirmation", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/cluster": () =>
        cluster([
          member("hello-sip-1"),
          member("hello-sip-2", { state: "DRAINING" }),
        ]),
      [DRAIN_1]: LAST_READY,
      [`${DRAIN_1}?force=true`]: noContent,
    });
    renderApp("/cluster");

    const dialog = await openBox("hello-sip-1");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Drain hello-sip-1" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Drain node" }));

    const warn = await within(dialog).findByRole("group", {
      name: "Drain hello-sip-1 anyway?",
    });
    expect(within(warn).getByRole("alert")).toHaveTextContent(
      "draining hello-sip-1 would leave no READY SIP node",
    );
    const posts = () =>
      calls.filter((c) => c.method === "POST").map((c) => c.url);
    expect(posts()).toEqual(["/api/v1/cluster/nodes/hello-sip-1/drain"]);
    // The safe choice has focus.
    await waitFor(() =>
      expect(
        within(warn).getByRole("button", {
          name: "Keep hello-sip-1 in service",
        }),
      ).toHaveFocus(),
    );

    fireEvent.click(within(warn).getByRole("button", { name: "Drain anyway" }));
    expect(await screen.findByText("hello-sip-1 is draining.")).toBeVisible();
    expect(posts()).toEqual([
      "/api/v1/cluster/nodes/hello-sip-1/drain",
      "/api/v1/cluster/nodes/hello-sip-1/drain?force=true",
    ]);
  });

  it("never forces a drain when the warning is declined, and Escape cancels without closing", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/cluster": () => cluster([member("hello-sip-1")]),
      [DRAIN_1]: LAST_READY,
    });
    renderApp("/cluster");

    const dialog = await openBox("hello-sip-1");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Drain hello-sip-1" }),
    );
    fireEvent.keyDown(
      within(dialog).getByRole("button", { name: "Drain node" }),
      { key: "Escape" },
    );
    expect(screen.getByRole("dialog", { name: "hello-sip-1" })).toBeVisible();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Drain hello-sip-1" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Drain node" }));
    fireEvent.click(
      await within(dialog).findByRole("button", {
        name: "Keep hello-sip-1 in service",
      }),
    );
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "Drain hello-sip-1" }),
      ).toHaveFocus(),
    );
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(1);
    expect(calls.some((c) => c.url.includes("force=true"))).toBe(false);
  });

  it("undrains a draining node after confirmation", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/cluster": [
        () =>
          cluster([
            member("hello-sip-1", {
              state: "DRAINING",
              reason: "drain requested",
            }),
          ]),
        () => cluster([member("hello-sip-1")]),
      ],
      "DELETE /api/v1/cluster/nodes/hello-sip-1/drain": noContent,
    });
    renderApp("/cluster");

    expect(
      (await screen.findAllByRole("alert")).some((a) =>
        a.textContent?.includes("No SIP node is READY."),
      ),
    ).toBe(true);
    const dialog = await openBox("hello-sip-1");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Undrain hello-sip-1" }),
    );
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Undrain node" }),
    );

    expect(
      await screen.findByText("hello-sip-1 is returning to service."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/cluster/nodes/hello-sip-1/drain",
      body: undefined,
    });
    await waitFor(() =>
      expect(box("hello-sip-1")).toHaveAccessibleName("hello-sip-1: Ready"),
    );
    expect(screen.queryByText("No SIP node is READY.")).toBeNull();
  });

  it("lays out any number of SIP nodes without overlap", () => {
    for (const n of [0, 1, 2, 5]) {
      const g = geometry(n);
      expect(g.sip).toHaveLength(n);
      g.sip.forEach((y, i) => {
        if (i > 0) expect(y).toBeGreaterThanOrEqual(g.sip[i - 1]! + 150);
      });
      expect(g.control).toBeGreaterThanOrEqual((g.sip.at(-1) ?? 0) + 150);
      expect(g.height).toBeGreaterThanOrEqual(g.control + 150);
      expect(g.minio).toBeGreaterThan(g.pg + 132);
    }
  });
});
