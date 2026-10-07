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
});
