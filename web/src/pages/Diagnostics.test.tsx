import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

const NOW = Date.now();
const ago = (s: number) => new Date(NOW - s * 1000).toISOString();

const DEVICES = [
  { id: 1, extensionId: 10, sipUsername: "desk", enabled: true },
  { id: 2, extensionId: 10, sipUsername: "confroom", enabled: true },
];
const EXTENSIONS = [{ id: 10, number: "1001", name: "Amira" }];
const BINDING = {
  aor: "sip:desk@pbx.test",
  extension: "1001",
  device: "desk",
  contactUri: "sip:desk@10.0.0.9:5060",
  source: "10.0.0.9:5060",
  transport: "udp",
  userAgent: "Yealink T54W",
  path: ["<sip:edge;lr;hflow=REDACTED>"],
  receivedNode: "hello-sip-1",
  expires: new Date(NOW + 3000_000).toISOString(),
  updatedAt: ago(10),
};

const CONFROOM = {
  deviceId: 2,
  device: "confroom",
  extensionId: 10,
  enabled: true,
  aor: "sip:confroom@pbx.test",
  registered: false,
  bindings: [],
  attempts: [
    {
      at: ago(30),
      device: "confroom",
      source: "10.0.0.120:5060",
      ip: "10.0.0.120",
      node: "hello-sip-2",
      userAgent: "Poly Trio C60",
      credentials: true,
      code: 401,
      reason: "Unauthorized",
    },
    {
      at: ago(31),
      device: "confroom",
      source: "10.0.0.120:5060",
      ip: "10.0.0.120",
      node: "hello-sip-2",
      credentials: false,
      code: 401,
      reason: "Unauthorized",
    },
  ],
  attemptsRetentionSeconds: 3600,
  source: {
    ip: "10.0.0.120",
    failures: 2,
    windowEndsAt: ago(-200),
    blocked: false,
  },
  authFailLimit: 10,
  verdict: {
    code: "auth_failed",
    message:
      "Hello rejected the credentials in the last REGISTER (401 Unauthorized after a challenge).",
  },
};

function base(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/devices": () => json({ items: DEVICES }),
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    "GET /api/v1/registrations": () => json({ items: [BINDING] }),
    "GET /api/v1/diagnostics/auth-failures": () =>
      json({
        items: [
          {
            ip: "203.0.113.45",
            failures: 14,
            windowEndsAt: ago(-120),
            blocked: true,
          },
          {
            ip: "10.0.0.120",
            failures: 2,
            windowEndsAt: ago(-200),
            blocked: false,
          },
        ],
        limit: 10,
      }),
    "GET /api/v1/cdrs?limit=25": () => json({ items: [], next: "" }),
    ...extra,
  });
}

describe("Diagnostics", () => {
  it("explains why a device is not registered from its REGISTER attempts", async () => {
    base({
      "GET /api/v1/diagnostics/devices/2": () => json(CONFROOM),
    });
    renderApp("/diagnostics?tab=reg");

    // The first enabled device without a contact is selected.
    const select = await screen.findByLabelText("Device");
    await waitFor(() => expect(select).toHaveValue("2"));
    expect(
      within(select)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["desk · 1001", "confroom · 1001"]);

    const why = await screen.findByRole("alert");
    expect(why).toHaveTextContent("Why it is not registered");
    expect(why).toHaveTextContent("Hello rejected the credentials");
    expect(
      within(why).getByRole("link", { name: "Open devices" }),
    ).toHaveAttribute("href", "/devices");
    expect(screen.getByText("Not registered")).toBeVisible();
    expect(screen.getByText("sip:confroom@pbx.test")).toBeVisible();
    expect(screen.getByText("Poly Trio C60")).toBeVisible();
    expect(screen.getByText("2 of 10 before throttling")).toBeVisible();
    expect(screen.getByText("1001 · Amira")).toBeVisible();

    const attempts = screen.getByRole("table", {
      name: "Recent REGISTER attempts",
    });
    const rows = within(attempts).getAllByRole("row").slice(1);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent(
      "REGISTER (credentials)10.0.0.120:5060hello-sip-2401 Unauthorized",
    );
    expect(rows[1]).toHaveTextContent("REGISTER10.0.0.120:5060");
  });

  it("shows a registered device's binding and switches device through the URL", async () => {
    base({
      "GET /api/v1/diagnostics/devices/2": () => json(CONFROOM),
      "GET /api/v1/diagnostics/devices/1": () =>
        json({
          ...CONFROOM,
          deviceId: 1,
          device: "desk",
          aor: "sip:desk@pbx.test",
          registered: true,
          bindings: [BINDING],
          attempts: [],
          source: null,
          verdict: null,
        }),
    });
    renderApp("/diagnostics?tab=reg");
    const select = await screen.findByLabelText("Device");
    await waitFor(() => expect(select).toHaveValue("2"));
    fireEvent.change(select, { target: { value: "1" } });

    expect(await screen.findByText("Registered")).toBeVisible();
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/diagnostics?tab=reg&device=1",
    );
    expect(screen.getByText("sip:desk@10.0.0.9:5060")).toBeVisible();
    expect(screen.getByText("10.0.0.9:5060 · UDP")).toBeVisible();
    expect(screen.getByText("<sip:edge;lr;hflow=REDACTED>")).toBeVisible();
    expect(screen.getByText("Yealink T54W")).toBeVisible();
    expect(screen.queryByText("Why it is not registered")).toBeNull();
    expect(
      screen.getByText("No REGISTER received in the last 1 h."),
    ).toBeVisible();
  });

  it("lists blocked sources and unblocks one", async () => {
    const calls = base({
      "GET /api/v1/diagnostics/devices/2": () => json(CONFROOM),
      "DELETE /api/v1/diagnostics/auth-failures/203.0.113.45": noContent,
    });
    renderApp("/diagnostics?tab=reg");
    const table = await screen.findByRole("table", { name: "Blocked sources" });
    // Only blocked sources are listed.
    expect(within(table).getAllByRole("row")).toHaveLength(2);
    expect(table).toHaveTextContent("203.0.113.4514 in the window");
    fireEvent.click(
      within(table).getByRole("button", { name: "Unblock 203.0.113.45" }),
    );
    expect(await screen.findByText("203.0.113.45 unblocked.")).toBeVisible();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/diagnostics/auth-failures/203.0.113.45",
      body: undefined,
    });
  });

  it("shows trunk probes with their configured priority and weight", async () => {
    base({
      "GET /api/v1/trunks": () =>
        json({
          items: [
            {
              id: 4,
              name: "carrier-primary",
              destinations: [
                {
                  host: "sbc1.carrier.lab",
                  port: 5060,
                  priority: 0,
                  weight: 1,
                },
                { host: "sbc2.carrier.lab", port: 0, priority: 1, weight: 1 },
              ],
            },
          ],
        }),
      "GET /api/v1/trunks/status": () =>
        json({
          items: [
            {
              trunkId: 4,
              name: "carrier-primary",
              activeCalls: 0,
              destinations: [
                {
                  destination: "sbc1.carrier.lab:5060",
                  up: false,
                  lastCode: 408,
                  checkedAt: ago(11),
                },
                {
                  destination: "sbc2.carrier.lab:5060",
                  up: true,
                  lastCode: 200,
                  latencyNs: 18_400_000,
                  checkedAt: ago(4),
                },
              ],
            },
          ],
        }),
    });
    renderApp("/diagnostics?tab=probes");
    const table = await screen.findByRole("table", { name: "Trunk probes" });
    const rows = within(table).getAllByRole("row").slice(1);
    expect(rows[0]).toHaveTextContent(
      "sbc1.carrier.lab:5060priority 0 · weight 1",
    );
    expect(rows[0]).toHaveTextContent("Down · 408—");
    expect(rows[1]).toHaveTextContent("Up · 20018.4 ms");
    // An SRV destination (port 0) still matches on host:5060.
    expect(rows[1]).toHaveTextContent("priority 1 · weight 1");
  });

  it("runs health checks from the live views and counts the failing ones", async () => {
    base({
      "GET /api/v1/cluster": () =>
        json({
          members: [
            {
              id: "hello-sip-1",
              kind: "sip",
              state: "READY",
              activeCalls: 0,
              registrations: 1,
              version: "0.5.0",
              configRevision: 42,
              revisionLag: 0,
              startedAt: ago(100),
              heartbeat: ago(2),
            },
            {
              id: "hello-sip-2",
              kind: "sip",
              state: "UNHEALTHY",
              reason: "valkey timeout",
              activeCalls: 0,
              registrations: 0,
              version: "0.5.0",
              configRevision: 41,
              revisionLag: 1,
              startedAt: ago(100),
              heartbeat: ago(14),
            },
          ],
          postgres: { up: true },
          valkey: { up: true, mode: "single" },
          configRevision: 42,
        }),
    });
    renderApp("/diagnostics?tab=health");
    const list = await screen.findByRole("list", { name: "Health checks" });
    await waitFor(() =>
      expect(list).toHaveTextContent("SIP nodes ready: 1 of 2"),
    );
    expect(list).toHaveTextContent(
      "hello-sip-2 is UNHEALTHY (valkey timeout).",
    );
    expect(list).toHaveTextContent("Configuration revision drift");
    expect(list).toHaveTextContent("Registrations: 1 of 2 enabled devices");
    expect(list).toHaveTextContent("Not registered: confroom.");
    expect(list).toHaveTextContent("Failed-auth throttling: 1 source blocked");
    // Failing (warn or bad) checks: SIP nodes, revision drift, throttling.
    expect(
      screen.getByRole("tab", { name: /Health checks/ }),
    ).toHaveTextContent("Health checks3");

    fireEvent.click(within(list).getByRole("button", { name: "Inspect" }));
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/diagnostics?tab=reg",
    );
  });

  it("shows a call's routing trace and links to its record", async () => {
    base({
      "GET /api/v1/cdrs?limit=25": () =>
        json({
          items: [
            {
              id: 4818,
              correlationId: "c1",
              sipCallId: "abc@10.0.0.9",
              source: "1001",
              destination: "+442071838750",
              originalDestination: "00442071838750",
              startTime: ago(60),
              endTime: ago(30),
              sipNode: "hello-sip-1",
              finalStatus: 503,
              direction: "outbound",
            },
          ],
          next: "",
        }),
      "GET /api/v1/cdrs/4818": () =>
        json({
          id: 4818,
          sipCallId: "abc@10.0.0.9",
          source: "1001",
          destination: "+442071838750",
          originalDestination: "00442071838750",
          rewrittenDestination: "+442071838750",
          route: "International",
          trunk: "carrier-primary",
          sipNode: "hello-sip-1",
          finalStatus: 503,
          explanation: "All trunks failed",
          trace: [
            { n: 2, text: "Trunk carrier-primary answered 503" },
            { n: 1, text: "Route International matched" },
          ],
        }),
    });
    renderApp("/diagnostics");
    expect(
      await screen.findByRole("heading", { name: /Call 4818/ }),
    ).toBeVisible();
    const steps = screen.getByRole("list", { name: "Trace of call 4818" });
    expect(
      within(steps)
        .getAllByRole("listitem")
        .map((li) => li.textContent),
    ).toEqual([
      "1Route International matched",
      "2Trunk carrier-primary answered 503",
    ]);
    expect(screen.getByText("All trunks failed")).toBeVisible();
    expect(screen.getByText("00442071838750 → +442071838750")).toBeVisible();
    expect(screen.getByRole("link", { name: "Call record" })).toHaveAttribute(
      "href",
      "/history/4818",
    );

    fireEvent.change(screen.getByLabelText("Search calls"), {
      target: { value: "nomatch" },
    });
    expect(screen.getByText("No call matches")).toBeVisible();
  });

  it("selects the call named by ?call=, even one not in the recent list", async () => {
    const calls = base({
      "GET /api/v1/cdrs?limit=25": () =>
        json({
          items: [
            {
              id: 4819,
              source: "1002",
              destination: "1003",
              startTime: ago(20),
              endTime: ago(10),
              finalStatus: 200,
              direction: "internal",
            },
          ],
          next: "",
        }),
      "GET /api/v1/cdrs/77": () =>
        json({
          id: 77,
          source: "101",
          destination: "0501234567",
          originalDestination: "0501234567",
          finalStatus: 503,
          trace: [{ n: 1, text: "Route UAE Mobile matched" }],
        }),
    });
    renderApp("/diagnostics?tab=trace&call=77");

    expect(
      await screen.findByRole("list", { name: "Trace of call 77" }),
    ).toHaveTextContent("Route UAE Mobile matched");
    expect(calls.map((c) => c.url)).not.toContain("/api/v1/cdrs/4819");

    // Picking another call puts it in the URL.
    fireEvent.click(await screen.findByRole("button", { name: /Call 4819/ }));
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/diagnostics?tab=trace&call=4819",
    );
  });
});
