import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, renderApp } from "../test/api";
import { axisLabels, shortHost } from "./Dashboard";

const NOW = new Date().toISOString();

const VERSION = {
  "GET /api/v1/version": () =>
    json({ version: "0.5.0", commit: "4aad912", configRevision: 42 }),
};

function member(id: string, kind: string, state: string, reason = "") {
  return {
    id,
    kind,
    state,
    reason,
    activeCalls: 0,
    registrations: 0,
    version: "0.5.0",
    configRevision: 42,
    startedAt: NOW,
    heartbeat: NOW,
  };
}

const TRUNK = {
  id: 1,
  name: "carrier-primary",
  mode: "registration",
  maxCalls: 30,
  enabled: true,
};

function cdr(id: number, status: number, from: string, to: string) {
  return {
    id,
    correlationId: `c-${id}`,
    sipCallId: `s-${id}`,
    source: from,
    destination: to,
    originalDestination: to,
    rewrittenDestination: "",
    startTime: "2026-10-05T14:20:10Z",
    endTime: "2026-10-05T14:20:12Z",
    durationMs: 0,
    billableMs: 0,
    sipNode: "hello-sip-1",
    mediaMode: "direct",
    finalStatus: status,
    terminationSide: "system",
    failureReason: "",
    direction: "outbound",
    route: "",
    trunk: "",
  };
}

function routes(degraded: boolean) {
  return {
    ...ME,
    ...VERSION,
    "GET /api/v1/calls": () =>
      json({
        items: [
          {
            id: "corr-1",
            sipCallId: "7b1e9@10.0.0.1",
            from: "1001",
            to: "+442071838750",
            state: "connected",
            node: "hello-sip-1",
            media: "anchored",
            startedAt: NOW,
            answeredAt: NOW,
          },
        ],
      }),
    "GET /api/v1/extensions": () =>
      json({
        items: [{ id: 1, number: "1001", name: "Amira Haddad" }],
      }),
    "GET /api/v1/cluster": () =>
      json({
        members: [
          member("hello-sip-1", "sip", "READY"),
          member(
            "hello-sip-2",
            "sip",
            degraded ? "UNHEALTHY" : "READY",
            "Valkey state timeout",
          ),
          member("hello-control-1", "control", "READY"),
        ],
        postgres: { up: true },
        valkey: { up: true, mode: "single" },
        configRevision: 42,
      }),
    "GET /api/v1/trunks": () => json({ items: [TRUNK] }),
    "GET /api/v1/trunks/status": () =>
      json({
        items: [
          {
            trunkId: 1,
            name: "carrier-primary",
            registration: {
              state: "registered",
              node: "hello-sip-1",
              updatedAt: NOW,
            },
            destinations: [
              {
                destination: "sbc1.carrier-primary.lab:5060",
                up: !degraded,
                lastCode: degraded ? 408 : 200,
                latencyNs: 18_400_000,
                checkedAt: NOW,
              },
              {
                destination: "sbc2.carrier-primary.lab:5060",
                up: true,
                latencyNs: 21_000_000,
                checkedAt: NOW,
              },
            ],
            activeCalls: 4,
          },
        ],
      }),
    "GET /api/v1/registrations": () =>
      json({ items: [{ device: "amira-desk", extension: "1001" }] }),
    "GET /api/v1/devices": () =>
      json({
        items: [
          { id: 1, extensionId: 1, sipUsername: "amira-desk", enabled: true },
          { id: 2, extensionId: 1, sipUsername: "amira-soft", enabled: true },
        ],
      }),
    "GET /api/v1/cdrs/concurrency?range=6h": () =>
      json({
        range: "6h",
        stepSeconds: 900,
        points: [
          { at: "2026-10-05T11:00:00Z", inbound: 1, outbound: 2, internal: 0 },
          { at: "2026-10-05T11:15:00Z", inbound: 3, outbound: 5, internal: 1 },
        ],
        peak: { calls: 9, at: "2026-10-05T11:15:00Z" },
      }),
    "GET /api/v1/cdrs/concurrency?range=1h": () =>
      json({ range: "1h", stepSeconds: 300, points: [], peak: null }),
    "GET /api/v1/cdrs?limit=10&failed=true": () =>
      json({
        items: [
          cdr(4817, 404, "1005", "7777"),
          cdr(4816, 487, "+442079460000", "1011"),
        ],
        next: "",
      }),
    "GET /api/v1/cdrs/4817": () =>
      json({
        ...cdr(4817, 404, "1005", "7777"),
        trace: [],
        explanation: "No route matches 7777",
      }),
  };
}

describe("Dashboard", () => {
  it("shows the version, headline numbers, trunks, calls and failures from the API", async () => {
    mockApi(routes(false));
    renderApp("/");

    expect(await screen.findByText("4aad912")).toBeVisible();
    expect(
      screen.getByText(/hello-control v0\.5\.0 · commit/),
    ).toHaveTextContent("configuration revision 42");

    // Registered devices: 1 of 2, one enabled device without a contact.
    expect(await screen.findByText("1 device has no contact")).toBeVisible();
    // Trunk destinations: both up; 4 of 30 channels in use.
    expect(
      await screen.findByText("4 of 30 trunk channels in use"),
    ).toBeVisible();
    // SIP nodes: both ready; the control node named in the sub line.
    expect(await screen.findByText("1 control node ready")).toBeVisible();
    expect(screen.getByText("1 answered · 0 ringing")).toBeVisible();
    const values = [...document.querySelectorAll(".az-stat__value")].map(
      (v) => v.textContent,
    );
    expect(values).toEqual(["1", "1/ 2", "2/ 2", "2/ 2"]);

    // Trunk card: registered, calls meter, destination latencies.
    expect(screen.getByText("Registered")).toBeVisible();
    expect(screen.getByText("4 of 30")).toBeVisible();
    const dests = screen.getByRole("list", {
      name: "carrier-primary destinations",
    });
    expect(dests).toHaveTextContent("sbc118.4 ms");
    expect(dests).toHaveTextContent("sbc221.0 ms");

    // Active calls with the extension's name; a row opens Active calls.
    const table = screen.getByRole("table", { name: "Active calls" });
    expect(within(table).getByText("Amira Haddad")).toBeVisible();
    expect(within(table).getByText("Answered")).toBeVisible();

    // Recent failures: the explanation, and no 487 (the caller gave up).
    const failures = await screen.findByRole("list", {
      name: "Recent failures",
    });
    expect(failures).toHaveTextContent("No route matches 7777");
    expect(failures).not.toHaveTextContent("+442079460000");
    expect(within(failures).getByRole("link")).toHaveAttribute(
      "href",
      "/history/4817",
    );

    expect(await screen.findByText(/^Peak 9 at/)).toBeVisible();
    // Nothing is degraded, so there are no health alerts.
    expect(screen.queryByText(/is UNHEALTHY/)).toBeNull();
    expect(screen.queryByText(/is down/)).toBeNull();
  });

  it("raises the health alerts when a SIP node or a trunk destination is down", async () => {
    mockApi(routes(true));
    renderApp("/");

    const node = await screen.findByText("hello-sip-2 is UNHEALTHY");
    expect(node.closest(".az-alert")).toHaveTextContent("Valkey state timeout");
    const dest = await screen.findByText("carrier-primary · sbc1 is down");
    expect(dest.closest(".az-alert")).toHaveTextContent(
      "OPTIONS to sbc1.carrier-primary.lab:5060 failed (408).",
    );
    expect(screen.getByText("Degraded")).toBeVisible();
    expect(screen.getByText("sbc1 down")).toBeVisible();
    expect(screen.getByText("hello-sip-2 unhealthy")).toBeVisible();

    fireEvent.click(
      within(dest.closest(".az-alert") as HTMLElement).getByRole("link", {
        name: "Open trunks",
      }),
    );
    expect(screen.getByTestId("location")).toHaveTextContent(/^\/trunks$/);
  });

  it("switches the chart range", async () => {
    const calls = mockApi(routes(false));
    renderApp("/");

    await screen.findByText(/^Peak 9 at/);
    fireEvent.click(screen.getByRole("radio", { name: "1h" }));
    expect(
      await screen.findByText(
        "No recorded calls in this window · from call records",
      ),
    ).toBeVisible();
    expect(calls.map((c) => c.url)).toContain(
      "/api/v1/cdrs/concurrency?range=1h",
    );
  });

  it("shows unknown numbers as a dash and says what failed", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/version": () => apiError(503, "unavailable", "down"),
      "GET /api/v1/calls": () =>
        apiError(503, "unavailable", "live state (Valkey) is unavailable"),
    });
    renderApp("/");

    expect(
      await screen.findByText("Could not reach the control plane."),
    ).toBeVisible();
    const alert = await screen.findByText("Could not load active calls");
    expect(alert.closest(".az-alert")).toHaveTextContent(
      "live state (Valkey) is unavailable",
    );
    // Every headline number is unknown, never a made-up zero.
    const values = [...document.querySelectorAll(".az-stat__value")].map(
      (v) => v.textContent,
    );
    expect(values).toEqual(["—", "—", "—", "—"]);
  });
});

describe("Dashboard helpers", () => {
  it("shortens a destination host, keeping IP addresses whole", () => {
    expect(shortHost("sbc1.carrier-primary.lab:5060")).toBe("sbc1");
    expect(shortHost("10.0.0.5:5060")).toBe("10.0.0.5");
  });

  it("spreads at most five axis labels over the samples", () => {
    const points = Array.from({ length: 25 }, (_, i) => ({
      at: new Date(2026, 9, 5, 9, i * 15).toISOString(),
    }));
    expect(axisLabels(points)).toEqual([
      "09:00",
      "10:30",
      "12:00",
      "13:30",
      "15:00",
    ]);
  });

  it("opens the New extension modal from the header link", async () => {
    mockApi({
      ...routes(false),
      "GET /api/v1/extensions": () => json({ items: [] }),
    });
    renderApp("/");

    const link = await screen.findByRole("link", { name: "New extension" });
    expect(link).toHaveAttribute("href", "/extensions?new=1");
    fireEvent.click(link);
    expect(
      await screen.findByRole("dialog", { name: "New extension" }),
    ).toBeVisible();
  });
});
