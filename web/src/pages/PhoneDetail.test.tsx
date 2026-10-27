import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  json,
  ME,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

function phone(over: Record<string, unknown> = {}) {
  return {
    id: 5,
    mac: "805ec0123456",
    serial: "",
    vendor: "yealink",
    model: "T54W",
    label: "Front desk",
    deviceId: 3,
    extensionId: 7,
    extensionNumber: "101",
    templateId: null,
    blf: ["102"],
    enabled: true,
    tokenExposed: true,
    uaMismatch: false,
    bootArmed: false,
    bootReclaimed: false,
    redirectStatus: { state: "registered" },
    firstFetchAt: "2026-10-01T10:00:00Z",
    lastFetchAt: "2026-10-02T10:00:00Z",
    lastFetchIp: "192.0.2.10",
    lastFetchUa: "Yealink SIP-T54W 96.86.0.70",
    lastFetchFile: "805ec0123456.cfg",
    firmwareSeen: "96.86.0.70",
    renderError: false,
    createdAt: "2026-10-01T10:00:00Z",
    updatedAt: "2026-10-01T10:00:00Z",
    ...over,
  };
}

const fetchRow = (n: number, over: Record<string, unknown> = {}) => ({
  at: `2026-10-02T10:0${n}:00Z`,
  ip: `192.0.2.${n}`,
  userAgent: "Yealink SIP-T54W 96.86.0.70",
  path: `/p/****/805ec0123456.cfg?n=${n}`,
  kind: "config",
  result: "served",
  status: 200,
  bytes: 2048,
  ...over,
});

function api(p = phone(), more: Routes = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/phones/5": () => json(p),
    "GET /api/v1/phones/5/pushes?limit=20": () => json({ items: [] }),
    "GET /api/v1/phones/5/fetches?limit=50": () =>
      json({
        items: [fetchRow(1), fetchRow(2, { result: "denied", status: 403 })],
        next: "c-1",
      }),
    "GET /api/v1/phones/5/fetches?before=c-1&limit=50": () =>
      json({ items: [fetchRow(3)], next: "" }),
    ...more,
  });
}

describe("Phone detail", () => {
  it("shows the phone, its flags and redirect state", async () => {
    api(phone({ vendor: "fanvil", redirectStatus: { state: "manual" } }));
    renderApp("/phones/5");
    expect(
      await screen.findByRole("heading", { level: 1, name: "Front desk" }),
    ).toBeInTheDocument();
    expect(screen.getByText("80:5e:c0:12:34:56")).toBeInTheDocument();
    expect(screen.getByText("Token exposed")).toBeInTheDocument();
    expect(screen.getByText(/FDPS portal/)).toBeInTheDocument();
  });

  it("pages the fetch log with Load older", async () => {
    const calls = api();
    renderApp("/phones/5");
    const log = await screen.findByRole("table", { name: "Fetch log" });
    expect(within(log).getByText("192.0.2.1")).toBeInTheDocument();
    expect(within(log).getByText("denied")).toHaveClass("az-badge--bad");
    expect(within(log).getByText("served")).toHaveClass("az-badge--good");
    expect(within(log).queryByText("192.0.2.3")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Load older" }));
    expect(
      await within(screen.getByRole("table", { name: "Fetch log" })).findByText(
        "192.0.2.3",
      ),
    ).toBeInTheDocument();
    expect(screen.getByText("192.0.2.1")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Load older" })).toBeNull();
    expect(
      calls.filter((c) => c.url.startsWith("/api/v1/phones/5/fetches")),
    ).toHaveLength(2);
  });

  it("shows the syslog lines of the phone and an empty state", async () => {
    api(phone(), {
      "GET /api/v1/phones/5/syslog?limit=200": () =>
        json({
          items: [
            {
              at: "2026-10-02T10:05:00Z",
              source: "192.0.2.10",
              severity: 3,
              message: "sip: registration failed",
            },
            {
              at: "2026-10-02T10:04:00Z",
              source: "192.0.2.10",
              severity: 6,
              message: "sip: registered",
            },
          ],
          cap: 10000,
          retentionSeconds: 86400,
        }),
    });
    renderApp("/phones/5");
    await screen.findByRole("table", { name: "Fetch log" });
    fireEvent.click(screen.getByRole("tab", { name: "Syslog" }));
    const table = await screen.findByRole("table", { name: "Syslog" });
    expect(within(table).getByText("sip: registration failed")).toBeVisible();
    expect(within(table).getByText("error")).toHaveClass("az-badge--bad");
    expect(within(table).getByText("info")).toHaveClass("az-badge--neutral");
    expect(screen.getByText(/kept for 24 h/)).toBeInTheDocument();
  });

  it("filters the syslog by severity", async () => {
    const line = (severity: number, message: string) => ({
      at: "2026-10-02T10:05:00Z",
      source: "192.0.2.10",
      severity,
      message,
    });
    api(phone(), {
      "GET /api/v1/phones/5/syslog?limit=200": () =>
        json({
          items: [
            line(3, "boom"),
            line(4, "careful"),
            line(6, "chatty"),
            line(7, "noisy"),
            line(-1, "[hello] 4 lines dropped"),
          ],
          cap: 10000,
          retentionSeconds: 86400,
        }),
    });
    renderApp("/phones/5");
    await screen.findByRole("table", { name: "Fetch log" });
    fireEvent.click(screen.getByRole("tab", { name: "Syslog" }));
    await screen.findByText("noisy");
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "4" } });
    expect(screen.getByText("boom")).toBeInTheDocument();
    expect(screen.getByText("careful")).toBeInTheDocument();
    expect(screen.queryByText("chatty")).toBeNull();
    expect(screen.queryByText("noisy")).toBeNull();
    expect(screen.getByText(/lines dropped/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Show"), { target: { value: "3" } });
    expect(screen.queryByText("careful")).toBeNull();
  });

  it("explains an empty syslog", async () => {
    api(phone(), {
      "GET /api/v1/phones/5/syslog?limit=200": () =>
        json({ items: [], cap: 10000, retentionSeconds: 86400 }),
    });
    renderApp("/phones/5");
    await screen.findByRole("table", { name: "Fetch log" });
    fireEvent.click(screen.getByRole("tab", { name: "Syslog" }));
    expect(await screen.findByText(/No syslog lines yet/)).toBeInTheDocument();
  });

  it("previews a file, encoding its name in the URL", async () => {
    const calls = api(phone(), {
      "GET /api/v1/phones/5/preview?file=a+b%26c%3D1.cfg": () =>
        new Response("#!version:1.0.0.1\naccount.1.password = ****\n"),
    });
    renderApp("/phones/5");
    await screen.findByRole("table", { name: "Fetch log" });
    fireEvent.click(screen.getByRole("tab", { name: "Preview" }));
    const name = screen.getByLabelText("File name");
    expect(name).toHaveValue("805ec0123456.cfg");
    expect(screen.getByText(/masked by the server/)).toBeInTheDocument();
    fireEvent.change(name, { target: { value: "a b&c=1.cfg" } });
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));
    expect(
      await screen.findByText(/account\.1\.password = \*\*\*\*/),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.url.includes("/preview?"))).toBe(true);
    expect(calls.find((c) => c.url.includes("/preview?"))!.url).toBe(
      "/api/v1/phones/5/preview?file=a+b%26c%3D1.cfg",
    );
  });

  it.each([
    ["grandstream", "cfg805ec0123456.xml"],
    ["snom", "805EC0123456"],
    ["generic", ""],
  ])("prefills the %s preview file name", async (vendor, file) => {
    api(phone({ vendor }));
    renderApp("/phones/5");
    await screen.findByRole("heading", { level: 1, name: "Front desk" });
    fireEvent.click(screen.getByRole("tab", { name: "Preview" }));
    expect(screen.getByLabelText("File name")).toHaveValue(file);
  });

  it("deletes the phone and returns to the inventory", async () => {
    api(phone(), {
      "DELETE /api/v1/phones/5": noContent,
      "GET /api/v1/phones": () => json({ items: [] }),
      "GET /api/v1/extensions": () => json({ items: [] }),
      "GET /api/v1/devices": () => json({ items: [] }),
      "GET /api/v1/prov/templates": () => json({ items: [] }),
    });
    renderApp("/phones/5");
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const confirm = screen.getByRole("dialog", { name: "Delete Front desk?" });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete phone" }),
    );
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(/^\/phones$/),
    );
  });

  it("queues a resync and shows the result the phone gave", async () => {
    const push = (status: string) => ({
      id: 9,
      phoneId: 5,
      kind: "resync",
      source: "manual",
      actor: "alice",
      status,
      detail: "200",
      contacts: 1,
      requestedAt: "2026-10-02T11:00:00Z",
      finishedAt: status === "pending" ? null : "2026-10-02T11:00:01Z",
    });
    const calls = api(phone(), {
      "POST /api/v1/phones/5/resync": () => json(push("pending"), 202),
      "GET /api/v1/phones/5/pushes?limit=20": [
        () => json({ items: [] }),
        () => json({ items: [push("ok")] }),
      ],
    });
    renderApp("/phones/5");
    fireEvent.click(await screen.findByRole("button", { name: "Resync" }));
    expect(
      await screen.findByText("Resync: Acknowledged (200)"),
    ).toBeInTheDocument();
    expect(
      calls.some((c) => c.method === "POST" && c.url.endsWith("/resync")),
    ).toBe(true);
  });

  it("asks before rebooting, and reports a timeout as not acknowledged", async () => {
    const reboot = {
      id: 10,
      phoneId: 5,
      kind: "reboot",
      source: "manual",
      actor: "alice",
      status: "timeout",
      detail: "no answer",
      contacts: 1,
      requestedAt: "2026-10-02T11:00:00Z",
      finishedAt: "2026-10-02T11:00:09Z",
    };
    const calls = api(phone(), {
      "POST /api/v1/phones/5/reboot": () =>
        json({ ...reboot, status: "pending" }, 202),
      "GET /api/v1/phones/5/pushes?limit=20": [
        () => json({ items: [] }),
        () => json({ items: [reboot] }),
      ],
    });
    renderApp("/phones/5");
    fireEvent.click(await screen.findByRole("button", { name: "Reboot" }));
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    const confirm = screen.getByRole("dialog", { name: "Reboot Front desk?" });
    fireEvent.click(within(confirm).getByRole("button", { name: "Reboot" }));
    expect(
      await screen.findByText("Reboot: Push not acknowledged"),
    ).toBeInTheDocument();
  });

  it("does not offer a push to a phone with no device", async () => {
    api(phone({ deviceId: null }));
    renderApp("/phones/5");
    expect(
      await screen.findByRole("button", { name: "Resync" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: "Reboot" })).toBeDisabled();
  });

  it("overrides the PTT channel of a WP836 and shows where it talks", async () => {
    const ptt = (id: number, label: string, address: string) => ({
      id,
      label,
      address,
      port: 5001,
      codec: "PCMU",
      kind: "ptt",
      priority: id,
      enabled: true,
    });
    const wp = phone({ vendor: "grandstream", model: "WP836", groupId: null });
    const calls = api(wp, {
      "GET /api/v1/multicast-channels": () =>
        json({
          items: [
            ptt(1, "PTT", "224.0.1.116"),
            ptt(2, "Floor", "224.0.1.117"),
            { ...ptt(3, "Dock", "239.255.0.1"), kind: "paging", port: 5004 },
          ],
        }),
      "PATCH /api/v1/phones/5": () => json({ ...wp, pttChannelId: 2 }),
    });
    renderApp("/phones/5");
    const effective = await screen.findByTestId("phone-ptt-effective");
    await waitFor(() =>
      expect(effective).toHaveTextContent("224.0.1.116:5001"),
    );
    const pick = screen.getByLabelText("PTT channel");
    expect(
      Array.from(pick.querySelectorAll("option")).map((o) => o.textContent),
    ).toEqual([
      "Group's channel (or the catalog default)",
      "PTT (224.0.1.116:5001, PCMU)",
      "Floor (224.0.1.117:5001, PCMU)",
    ]);
    fireEvent.change(pick, { target: { value: "2" } });
    expect(effective).toHaveTextContent("224.0.1.117:5001");
    fireEvent.click(screen.getByRole("button", { name: "Save PTT channel" }));
    expect(await screen.findByText(/PTT channel saved/)).toBeInTheDocument();
    expect(calls).toContainEqual({
      method: "PATCH",
      url: "/api/v1/phones/5",
      body: { pttChannelId: 2 },
    });
  });

  it("offers no PTT channel to a phone without a PTT key", async () => {
    api(phone());
    renderApp("/phones/5");
    await screen.findByRole("heading", { level: 1, name: "Front desk" });
    expect(screen.queryByLabelText("PTT channel")).toBeNull();
  });
});
