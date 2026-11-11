import { screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../../test/api";

const STAMP = "2026-10-01T10:00:00Z";
const PHONE = {
  id: 5,
  mac: "805ec0123456",
  serial: "",
  vendor: "grandstream",
  model: "WP822",
  label: "",
  deviceId: 3,
  extensionId: 7,
  extensionNumber: "101",
  templateId: null,
  groupId: null,
  blf: [],
  enabled: true,
  tokenExposed: false,
  uaMismatch: false,
  bootArmed: false,
  bootReclaimed: false,
  redirectStatus: { state: "manual" },
  firstFetchAt: null,
  lastFetchAt: null,
  lastFetchIp: null,
  lastFetchUa: null,
  lastFetchFile: null,
  firmwareSeen: null,
  renderError: false,
  createdAt: STAMP,
  updatedAt: STAMP,
};

describe("directory fit warning", () => {
  it("warns when contacts are dropped", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/phones/5": () => json(PHONE),
      "GET /api/v1/phones/5/directory": () =>
        json({
          supported: true,
          delivered: 620,
          capacity: 500,
          dropped: 120,
          vcard: false,
        }),
    });
    renderApp("/phones/5");
    expect(
      await screen.findByText("Some contacts do not fit on this phone"),
    ).toBeTruthy();
    expect(screen.getByText(/The last 120 in the delivery order/)).toBeTruthy();
  });

  it("says nothing when everything fits", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/phones/5": () => json(PHONE),
      "GET /api/v1/phones/5/directory": () =>
        json({
          supported: true,
          delivered: 10,
          capacity: 500,
          dropped: 0,
          vcard: false,
        }),
    });
    renderApp("/phones/5");
    await screen.findByText("Resync and reboot");
    expect(
      screen.queryByText("Some contacts do not fit on this phone"),
    ).toBeNull();
  });
});
