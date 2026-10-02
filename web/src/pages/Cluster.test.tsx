import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

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

const rowOf = (id: string) =>
  screen.getByRole("row", { name: (name) => name.includes(id) });

describe("Cluster", () => {
  it("shows each node's state, load, version, revision lag and heartbeat, and the dependencies", async () => {
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
            heartbeat: ago(7),
          }),
          member("hello-control-1", {
            kind: "control",
            sipAddr: undefined,
            transports: undefined,
            activeCalls: 0,
            registrations: 0,
          }),
        ]),
    });
    renderApp("/cluster");

    const one = await screen.findByRole("row", { name: /hello-sip-1/ });
    const cells = (row: HTMLElement) =>
      within(row)
        .getAllByRole("cell")
        .map((c) => c.textContent);
    expect(cells(one).slice(0, 8)).toEqual([
      "SIP",
      "Ready",
      "10.0.0.11:5060 (udp)",
      "12",
      "241",
      "0.3.0",
      "current (rev 1284)",
      "3 s ago",
    ]);
    expect(cells(rowOf("hello-sip-2")).slice(0, 8)).toEqual([
      "SIP",
      "Draining — drain requested",
      "10.0.0.12:5060 (udp)",
      "2",
      "96",
      "0.2.9",
      "3 behind (rev 1281)",
      "7 s ago",
    ]);
    expect(within(rowOf("hello-control-1")).getByText("Control")).toBeVisible();
    for (const h of [
      "State",
      "Calls",
      "Registrations",
      "Version",
      "Revision lag",
      "Heartbeat",
    ]) {
      expect(screen.getByRole("columnheader", { name: h })).toBeVisible();
    }

    const deps = screen.getByRole("table", { name: "Dependencies" });
    expect(
      within(deps).getByRole("row", { name: /PostgreSQL/ }),
    ).toHaveTextContent("PostgreSQLUp");
    expect(within(deps).getByRole("row", { name: /Valkey/ })).toHaveTextContent(
      "ValkeyUpSentinel, primary 10.0.0.21:6379",
    );
    expect(screen.getByText("Configuration revision:")).toHaveTextContent(
      "Configuration revision: 1284",
    );
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
    renderApp("/cluster");
    await screen.findByRole("row", { name: /hello-sip-1/ });
    expect(
      await screen.findByText("— valkey unreachable", {}, { timeout: 6000 }),
    ).toBeVisible();
    expect(calls.filter((c) => c.url === "/api/v1/cluster").length).toBe(2);
  }, 10000);

  it("drains a node only after an in-page confirmation", async () => {
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
    renderApp("/cluster");

    fireEvent.click(
      await screen.findByRole("button", { name: "Drain hello-sip-1" }),
    );
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    const confirm = screen.getByRole("group", { name: "Drain hello-sip-1?" });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Drain node" }),
    );

    expect(await screen.findByText("hello-sip-1 is draining.")).toBeVisible();
    expect(calls.filter((c) => c.method === "POST").map((c) => c.url)).toEqual([
      "/api/v1/cluster/nodes/hello-sip-1/drain",
    ]);
    // The page re-reads the cluster right away.
    await waitFor(() =>
      expect(within(rowOf("hello-sip-1")).getByText("Draining")).toBeVisible(),
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

    fireEvent.click(
      await screen.findByRole("button", { name: "Drain hello-sip-1" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Drain node" }));

    const warn = await screen.findByRole("group", {
      name: "Drain hello-sip-1 anyway?",
    });
    expect(within(warn).getByRole("alert")).toHaveTextContent(
      "draining hello-sip-1 would leave no READY SIP node",
    );
    const posts = () =>
      calls.filter((c) => c.method === "POST").map((c) => c.url);
    expect(posts()).toEqual(["/api/v1/cluster/nodes/hello-sip-1/drain"]);
    // The safe choice has focus.
    expect(
      within(warn).getByRole("button", { name: "Keep hello-sip-1 in service" }),
    ).toHaveFocus();

    fireEvent.click(within(warn).getByRole("button", { name: "Drain anyway" }));
    expect(await screen.findByText("hello-sip-1 is draining.")).toBeVisible();
    expect(posts()).toEqual([
      "/api/v1/cluster/nodes/hello-sip-1/drain",
      "/api/v1/cluster/nodes/hello-sip-1/drain?force=true",
    ]);
  });

  it("never forces a drain when the warning is declined", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/cluster": () => cluster([member("hello-sip-1")]),
      [DRAIN_1]: LAST_READY,
    });
    renderApp("/cluster");

    fireEvent.click(
      await screen.findByRole("button", { name: "Drain hello-sip-1" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Drain node" }));
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Keep hello-sip-1 in service",
      }),
    );
    expect(
      screen.getByRole("button", { name: "Drain hello-sip-1" }),
    ).toHaveFocus();
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

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "No SIP node is READY.",
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Undrain hello-sip-1" }),
    );
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(screen.getByRole("button", { name: "Undrain node" }));

    expect(
      await screen.findByText("hello-sip-1 is returning to service."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/cluster/nodes/hello-sip-1/drain",
      body: undefined,
    });
    await waitFor(() =>
      expect(within(rowOf("hello-sip-1")).getByText("Ready")).toBeVisible(),
    );
    expect(screen.queryByText("No SIP node is READY.")).toBeNull();
  });
});
