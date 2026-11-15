import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../../test/api";

const NOW = Date.now();
const ago = (s: number) => new Date(NOW - s * 1000).toISOString();

const STATUS = {
  generatedAt: ago(0),
  clients: {
    enrolled: { web: 3, mobile: 0 },
    registered: { desk: 4, web: 1, mobile: 0 },
    byTransport: { udp: 0, tcp: 0, tls: 0, ws: 0, wss: 1 },
  },
  registrations: [
    {
      deviceId: 31,
      device: "w31",
      kind: "web",
      label: "Chrome on laptop",
      extension: "101",
      transport: "wss",
      source: "10.0.0.5:51000",
      userAgent: "Hello softphone",
      node: "hello-sip-1",
      expires: ago(-600),
    },
  ],
  nodes: [
    {
      node: "hello-sip-1",
      port: 21100,
      hosts: ["192.168.10.101"],
      legs: 2,
      counters: {
        ice: { connected: 7, restarted: 1, failed: 2 },
        dtlsHandshakes: 7,
        dtlsFailures: { timeout: 1, fingerprint: 1, failed: 0 },
        srtpAuthFailures: { from_caller: 3, from_callee: 0 },
      },
      updatedAt: ago(4),
    },
  ],
  legs: [
    {
      call: "abc@10.0.0.5",
      side: "caller",
      ringing: false,
      ice: "connected",
      dtls: "connected",
      dtlsMs: 42,
      iceRestarts: 0,
      remote: "198.51.100.7:49200",
      video: true,
      srtpAuthFailures: 0,
      since: ago(60),
      node: "hello-sip-1",
      from: "101",
      to: "102",
      turn: true,
    },
    {
      call: "def@10.0.0.6",
      side: "callee",
      ringing: false,
      ice: "checking",
      dtls: "failed",
      dtlsReason: "timeout",
      iceRestarts: 0,
      video: false,
      srtpAuthFailures: 0,
      since: ago(10),
      node: "hello-sip-1",
      turn: false,
    },
  ],
  turn: {
    configured: true,
    ttlSeconds: 3600,
    stun: ["stun:192.168.10.121:3478"],
    servers: [
      {
        url: "turn:192.168.10.121:3478?transport=udp",
        transport: "udp",
        method: "stun",
        reachable: true,
        latencyMs: 3,
        addrs: ["192.168.10.121"],
        checkedAt: ago(5),
      },
      {
        url: "turns:192.168.10.121:5349?transport=tcp",
        transport: "tls",
        method: "connect",
        reachable: false,
        latencyMs: 2000,
        error: "no answer before the timeout",
        addrs: ["192.168.10.121"],
        checkedAt: ago(5),
      },
    ],
    relayedLegs: 1,
  },
  push: {
    configured: false,
    detail: "The push gateway is not built yet.",
  },
  candidates: {
    since: ago(86400),
    reports: 5,
    local: { host: 3, srflx: 1, prflx: 0, relay: 1, unknown: 0 },
    remote: { host: 5, srflx: 0, prflx: 0, relay: 0, unknown: 0 },
    relayed: 1,
  },
  authFailures: [
    {
      deviceId: 33,
      device: "w33",
      label: "Edge at home",
      kind: "web",
      at: ago(30),
      code: 403,
      reason: "Forbidden",
      source: "203.0.113.9:6000",
      node: "hello-sip-2",
      credentials: true,
    },
  ],
  lastErrors: [
    {
      at: ago(30),
      kind: "register",
      reason: "forbidden",
      detail: "Forbidden",
      node: "hello-sip-2",
      device: "w33",
    },
    {
      at: ago(90),
      kind: "dtls",
      reason: "timeout",
      detail: "the DTLS handshake did not finish in time",
      node: "hello-sip-1",
      call: "def@10.0.0.6",
    },
  ],
  unavailable: [],
};

function base(status: unknown = STATUS) {
  return mockApi({
    ...ME,
    "GET /api/v1/webrtc/status": () => json(status),
    "GET /api/v1/devices": () => json({ items: [] }),
    "GET /api/v1/extensions": () => json({ items: [] }),
    "GET /api/v1/registrations": () => json({ items: [] }),
    "GET /api/v1/diagnostics/auth-failures": () =>
      json({ items: [], limit: 10 }),
    "GET /api/v1/cdrs?limit=25": () => json({ items: [], next: "" }),
  });
}

describe("Diagnostics Clients tab", () => {
  it("shows clients, live WebRTC legs, TURN, candidate types and errors", async () => {
    base();
    renderApp("/diagnostics?tab=clients");

    expect(
      await screen.findByRole("tab", { name: "Clients", selected: true }),
    ).toBeVisible();
    expect(await screen.findByText("1 registered · 3 enrolled")).toBeVisible();
    expect(screen.getByText("WSS 1")).toBeVisible();
    expect(screen.getByText("2 · 1 through TURN")).toBeVisible();
    expect(screen.getByText("1 of 2 reachable")).toBeVisible();
    expect(
      screen.getByText("The push gateway is not built yet."),
    ).toBeVisible();

    const legs = screen.getByRole("table", { name: "Live WebRTC legs" });
    const [, first, second] = within(legs).getAllByRole("row");
    if (!first || !second) throw new Error("two leg rows expected");
    expect(first).toHaveTextContent("101 → 102");
    expect(first).toHaveTextContent("abc@10.0.0.5");
    expect(first).toHaveTextContent("Caller · video");
    expect(first).toHaveTextContent("connected · 42 ms");
    expect(first).toHaveTextContent("198.51.100.7:49200");
    expect(within(first).getByText("TURN")).toBeVisible();
    expect(second).toHaveTextContent("checking");
    expect(second).toHaveTextContent("failed · timeout");

    const regs = screen.getByRole("table", { name: "Registered clients" });
    expect(regs).toHaveTextContent("Chrome on laptop");
    expect(regs).toHaveTextContent("WSS");

    const turn = screen.getByRole("table", { name: "TURN servers" });
    expect(turn).toHaveTextContent("Reachable · stun");
    expect(turn).toHaveTextContent("Unreachable · connect");
    expect(turn).toHaveTextContent("no answer before the timeout");

    const cand = screen.getByRole("table", { name: "ICE candidate types" });
    const relay = within(cand).getByText("Relay (TURN)").closest("tr");
    expect(relay).toHaveTextContent("1");
    expect(screen.getByText("1 of 5 calls went through TURN.")).toBeVisible();

    const errors = screen.getByRole("list", { name: "Last client errors" });
    expect(within(errors).getAllByRole("listitem")).toHaveLength(2);
    expect(errors).toHaveTextContent(
      "the DTLS handshake did not finish in time",
    );

    const nodes = screen.getByRole("table", {
      name: "SIP nodes with WebRTC media",
    });
    expect(nodes).toHaveTextContent("7 / 2");
    expect(nodes).toHaveTextContent("UDP 21100 · 192.168.10.101");
  });

  it("opens a failing device on the Registrations tab", async () => {
    base();
    renderApp("/diagnostics?tab=clients");
    fireEvent.click(
      await screen.findByRole("button", { name: "Diagnose Edge at home" }),
    );
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/diagnostics?tab=reg&device=33",
    );
  });

  it("warns when no web client is registered and names missing sources", async () => {
    base({
      ...STATUS,
      clients: {
        ...STATUS.clients,
        registered: { desk: 1, web: 0, mobile: 0 },
      },
      registrations: [],
      legs: [],
      nodes: [],
      turn: { ...STATUS.turn, configured: false, servers: [], relayedLegs: 0 },
      candidates: null,
      authFailures: [],
      lastErrors: [],
      unavailable: ["webrtc", "quality"],
    });
    renderApp("/diagnostics?tab=clients");
    expect(
      await screen.findByText("No web client is registered"),
    ).toBeVisible();
    expect(
      screen.getByText(
        /the SIP nodes' WebRTC state \(Valkey\), the clients' call reports/,
      ),
    ).toBeVisible();
    expect(screen.getByText("No browser or app is in a call.")).toBeVisible();
    expect(screen.getByText(/TURN is not configured/)).toBeVisible();
    expect(screen.getByText("The call reports are unavailable.")).toBeVisible();
    expect(screen.getByText(/No SIP node reports WebRTC media/)).toBeVisible();
  });
});
