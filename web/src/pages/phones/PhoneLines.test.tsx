import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../../test/api";
import { freeLineNumbers, hasAccountSharing } from "./PhoneLines";

const line = (n: number, ext: string, over: Record<string, unknown> = {}) => ({
  line: n,
  deviceId: 10 + n,
  extensionId: 100 + n,
  extensionNumber: ext,
  extensionName: `Ext ${ext}`,
  sipUsername: `${ext}-123456`,
  provisioned: true,
  ...over,
});

function phone(over: Record<string, unknown> = {}) {
  return {
    id: 5,
    mac: "805ec0123456",
    serial: "",
    vendor: "yealink",
    model: "T54W",
    label: "Front desk",
    deviceId: 11,
    extensionId: 101,
    extensionNumber: "101",
    templateId: null,
    blf: [],
    enabled: true,
    tokenExposed: false,
    uaMismatch: false,
    bootArmed: false,
    bootReclaimed: false,
    redirectStatus: { state: "registered" },
    firstFetchAt: null,
    lastFetchAt: null,
    lastFetchIp: null,
    lastFetchUa: null,
    lastFetchFile: null,
    firmwareSeen: null,
    renderError: false,
    createdAt: "2026-10-01T10:00:00Z",
    updatedAt: "2026-10-01T10:00:00Z",
    maxLines: 6,
    lines: [line(1, "101")],
    ...over,
  };
}

const extensions = [
  { id: 101, number: "101", name: "Ext 101", externalNumber: "" },
  { id: 102, number: "102", name: "Ext 102", externalNumber: "" },
];

describe("lines helpers", () => {
  it("lists the line numbers still free up to the model's count", () => {
    expect(freeLineNumbers({ maxLines: 3, lines: [line(1, "101")] })).toEqual([
      2, 3,
    ]);
    expect(
      freeLineNumbers({ maxLines: 3, lines: [line(1, "101"), line(2, "102")] }),
    ).toEqual([3]);
    expect(freeLineNumbers({ maxLines: 1, lines: [line(1, "101")] })).toEqual(
      [],
    );
    expect(freeLineNumbers({})).toEqual([]);
  });

  it("offers account sharing to the WP8x6 only", () => {
    for (const model of ["WP816", "WP826", "wp 836"]) {
      expect(hasAccountSharing({ vendor: "grandstream", model })).toBe(true);
    }
    for (const model of ["WP822", "WP856", "GRP2614"]) {
      expect(hasAccountSharing({ vendor: "grandstream", model })).toBe(false);
    }
    expect(hasAccountSharing({ vendor: "yealink", model: "WP836" })).toBe(
      false,
    );
  });
});

describe("Phone lines", () => {
  function api(p = phone()) {
    return mockApi({
      ...ME,
      "GET /api/v1/phones/5": () => json(p),
      "GET /api/v1/phones/5/pushes?limit=20": () => json({ items: [] }),
      "GET /api/v1/phones/5/fetches?limit=50": () =>
        json({ items: [], next: "" }),
      "GET /api/v1/extensions": () => json({ items: extensions }),
      "PUT /api/v1/phones/5/lines/2": () =>
        json(phone({ lines: [line(1, "101"), line(2, "102")] })),
      "DELETE /api/v1/phones/5/lines/2": () => json(phone()),
    });
  }

  it("binds line 2 to another extension and shows it", async () => {
    const calls = api();
    renderApp("/phones/5");
    await screen.findByRole("heading", { level: 2, name: "Lines" });
    const extension = await screen.findByLabelText("Extension");
    // The extension line 1 already uses is not offered.
    await waitFor(() =>
      expect(
        within(extension).queryByRole("option", { name: "101 Ext 101" }),
      ).toBeNull(),
    );
    fireEvent.change(extension, { target: { value: "102" } });
    fireEvent.click(screen.getByRole("button", { name: "Bind line" }));
    const table = await screen.findByRole("table", { name: "Phone lines" });
    expect(await within(table).findByText("Line 2")).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.url).toBe("/api/v1/phones/5/lines/2");
    expect(put?.body).toEqual({ extensionId: 102 });
  });

  it("unbinds a further line, not line 1", async () => {
    const calls = api(phone({ lines: [line(1, "101"), line(2, "102")] }));
    renderApp("/phones/5");
    const table = await screen.findByRole("table", { name: "Phone lines" });
    expect(
      within(table).queryByRole("button", { name: "Unbind line 1" }),
    ).toBeNull();
    fireEvent.click(
      within(table).getByRole("button", { name: "Unbind line 2" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
    expect(calls.find((c) => c.method === "DELETE")?.url).toBe(
      "/api/v1/phones/5/lines/2",
    );
  });

  it("says a held Grandstream line is not written", async () => {
    api(
      phone({
        vendor: "grandstream",
        model: "WP836",
        maxLines: 3,
        lines: [line(1, "101"), line(2, "102", { provisioned: false })],
      }),
    );
    renderApp("/phones/5");
    expect(await screen.findByText("Held, not written")).toBeInTheDocument();
    expect(
      screen.getByText("Some lines are held, not written"),
    ).toBeInTheDocument();
  });

  it("shows the sharing role for a WP8x6 only", async () => {
    api(phone({ vendor: "grandstream", model: "WP836", sharingRole: "guest" }));
    renderApp("/phones/5");
    expect(
      await screen.findByRole("heading", { level: 2, name: "Account sharing" }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Role")).toHaveValue("guest");
  });

  it("hides the sharing role for another phone", async () => {
    api();
    renderApp("/phones/5");
    await screen.findByRole("heading", { level: 2, name: "Lines" });
    expect(
      screen.queryByRole("heading", { level: 2, name: "Account sharing" }),
    ).toBeNull();
  });
});
