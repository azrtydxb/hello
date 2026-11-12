import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const TIME = "2026-11-09T10:00:00Z";
const base = {
  parentId: null,
  country: "",
  state: "",
  city: "",
  street: "",
  houseNumber: "",
  postalCode: "",
  latitude: null,
  longitude: null,
  radiusM: null,
  createdAt: TIME,
  updatedAt: TIME,
};
const HQ = {
  ...base,
  id: 1,
  kind: "site",
  name: "HQ",
  country: "NL",
  city: "Amsterdam",
  street: "Damrak",
  houseNumber: "1",
};
const FLOOR = { ...base, id: 2, kind: "floor", name: "3", parentId: 1 };
const ROOM = {
  ...base,
  id: 3,
  kind: "room",
  name: "Server room",
  parentId: 2,
  latitude: 52.3,
  longitude: 4.9,
};

const VIEWER = {
  "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }),
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/locations": () => json({ items: [HQ, FLOOR, ROOM] }),
    "GET /api/v1/location-assignments": () =>
      json({
        items: [
          { kind: "group", id: 7, name: "Warehouse", locationId: 1 },
          { kind: "extension", id: 11, name: "101 (Sales)", locationId: null },
          { kind: "phone", id: 21, name: "805ec0aabb01", locationId: 3 },
        ],
      }),
    "GET /api/v1/emergency": () => json({ notifyGroup: "" }),
    "GET /api/v1/phone-groups?limit=200": () =>
      json({ items: [{ id: 7, name: "Warehouse" }], nextCursor: "" }),
    "GET /api/v1/phone-groups/7/emergency": () =>
      json({ e911Enabled: false, alarmTarget: "" }),
    "GET /api/v1/ring-groups": () =>
      json({
        items: [
          { id: 5, name: "700" },
          { id: 6, name: "900" },
        ],
      }),
    "GET /api/v1/extensions": () =>
      json({ items: [{ id: 11, number: "101", name: "Sales" }] }),
    ...extra,
  });
}

async function newSite() {
  const button = await screen.findByRole("button", { name: "New site" });
  await waitFor(() => expect(button).toBeEnabled());
  fireEvent.click(button);
}

describe("Locations: places", () => {
  it("shows the sites with the floors and rooms inside them", async () => {
    setup();
    renderApp("/locations");
    const tree = await screen.findByRole("list", { name: "Locations" });
    expect(within(tree).getByText("HQ")).toBeVisible();
    expect(within(tree).getByText("Damrak 1, Amsterdam, NL")).toBeVisible();
    const floor = within(tree).getByRole("list", { name: "Inside HQ" });
    expect(within(floor).getByText("3")).toBeVisible();
    expect(
      within(screen.getByRole("list", { name: "Inside 3" })).getByText(
        "Server room",
      ),
    ).toBeVisible();
  });

  it("shows a validation error at its field and sends nothing", async () => {
    const calls = setup();
    renderApp("/locations");
    await newSite();
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Branch" },
    });
    fireEvent.change(screen.getByLabelText("Country"), {
      target: { value: "nl" },
    });
    fireEvent.change(screen.getByLabelText("Latitude"), {
      target: { value: "95" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create location" }));
    expect(
      await screen.findByText(
        "Use a two-letter country code in capitals, for example NL.",
      ),
    ).toBeVisible();
    expect(screen.getByText("Use degrees from -90 to 90.")).toBeVisible();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("shows the server's field error at its field", async () => {
    setup({
      "POST /api/v1/locations": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration",
              fields: [
                { path: "name", message: "another location has this name" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/locations");
    await newSite();
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "HQ" },
    });
    fireEvent.change(screen.getByLabelText("City"), {
      target: { value: "Utrecht" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create location" }));
    const error = await screen.findByText("another location has this name");
    expect(error.closest(".az-field")).toContainElement(
      screen.getByLabelText("Name"),
    );
  });

  it("creates a floor inside a site", async () => {
    const calls = setup({
      "POST /api/v1/locations": () =>
        json({ ...base, id: 9, kind: "floor", name: "4", parentId: 1 }, 201),
    });
    renderApp("/locations");
    fireEvent.click(
      await screen.findByRole("button", { name: "Add a floor to HQ" }),
    );
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "4" } });
    fireEvent.click(screen.getByRole("button", { name: "Create location" }));
    expect(await screen.findByText("Location 4 created.")).toBeVisible();
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      kind: "floor",
      name: "4",
      parentId: 1,
      country: "",
    });
  });

  it("names what is in the way of a deletion", async () => {
    setup({
      "DELETE /api/v1/locations/1": () =>
        apiError(
          409,
          "conflict",
          "the location has 1 floors or rooms inside it",
        ),
    });
    renderApp("/locations");
    fireEvent.click(await screen.findByRole("button", { name: "Delete HQ" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete location" }),
    );
    expect(
      await screen.findByText(/the location has 1 floors or rooms inside it/),
    ).toBeVisible();
  });

  it("offers a viewer no write control", async () => {
    setup(VIEWER);
    renderApp("/locations");
    await screen.findByRole("list", { name: "Locations" });
    for (const name of [
      "New site",
      "Add a floor to HQ",
      "Edit HQ",
      "Delete HQ",
    ]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
  });
});

describe("Locations: assignments", () => {
  async function open() {
    renderApp("/locations");
    fireEvent.click(await screen.findByRole("tab", { name: "Assignments" }));
    return screen.findByRole("table", { name: "Location assignments" });
  }

  it("shows each assignment by its path and assigns one", async () => {
    const calls = setup({
      "PUT /api/v1/location-assignments/extension/11": () => noContent(),
    });
    await open();
    expect(
      screen.getByLabelText<HTMLSelectElement>("Location of phone 805ec0aabb01")
        .selectedOptions[0]?.textContent,
    ).toBe("HQ / 3 / Server room");
    fireEvent.change(
      screen.getByLabelText("Location of extension 101 (Sales)"),
      { target: { value: "2" } },
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        locationId: 2,
      }),
    );
    expect(
      await screen.findByText("Extension 101 (Sales) saved."),
    ).toBeVisible();
  });

  it("shows a refusal at the row", async () => {
    setup({
      "PUT /api/v1/location-assignments/group/7": () =>
        apiError(400, "bad_request", "locationId: no such location"),
    });
    await open();
    fireEvent.change(
      screen.getByLabelText("Location of phone group Warehouse"),
      {
        target: { value: "" },
      },
    );
    expect(
      await screen.findByText("locationId: no such location"),
    ).toBeVisible();
  });

  it("offers a viewer no way to assign", async () => {
    setup(VIEWER);
    await open();
    expect(
      screen.getByLabelText("Location of phone group Warehouse"),
    ).toBeDisabled();
  });
});

describe("Locations: emergency", () => {
  async function open() {
    renderApp("/locations");
    fireEvent.click(await screen.findByRole("tab", { name: "Emergency" }));
    return screen.findByLabelText("Alarm destination of Warehouse");
  }

  it("sets a group's E911 switch and alarm destination", async () => {
    const calls = setup({
      "PUT /api/v1/phone-groups/7/emergency": () =>
        json({ e911Enabled: true, alarmTarget: "101" }),
    });
    const alarm = await open();
    const save = screen.getByRole("button", { name: "Save Warehouse" });
    expect(save).toBeDisabled();
    fireEvent.change(alarm, { target: { value: "101" } });
    fireEvent.click(screen.getByRole("switch", { name: /E911 and HELD/ }));
    fireEvent.click(save);
    expect(
      await screen.findByText("Phone group Warehouse saved."),
    ).toBeVisible();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      e911Enabled: true,
      alarmTarget: "101",
    });
  });

  it("shows a refusal at the alarm destination", async () => {
    setup({
      "PUT /api/v1/phone-groups/7/emergency": () =>
        apiError(
          400,
          "bad_request",
          "alarmTarget: must be an extension or a ring group",
        ),
    });
    const alarm = await open();
    fireEvent.change(alarm, { target: { value: "101" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Warehouse" }));
    const msg = await screen.findByText(
      "alarmTarget: must be an extension or a ring group",
    );
    expect(msg.closest(".az-field")).toContainElement(alarm);
  });

  it("saves the notify group", async () => {
    const calls = setup({
      "PUT /api/v1/emergency": () => json({ notifyGroup: "900" }),
    });
    await open();
    fireEvent.change(screen.getByLabelText("Ring group"), {
      target: { value: "900" },
    });
    fireEvent.click(
      screen.getByRole("button", { name: "Save emergency settings" }),
    );
    expect(await screen.findByText("Emergency settings saved.")).toBeVisible();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      notifyGroup: "900",
    });
  });

  it("offers an operator no way to change emergency settings", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "o", role: "operator" }),
    });
    const alarm = await open();
    expect(alarm).toBeDisabled();
    expect(
      screen.getByRole("switch", { name: /E911 and HELD/ }),
    ).toBeDisabled();
    expect(screen.queryByRole("button", { name: "Save Warehouse" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Save emergency settings" }),
    ).toBeNull();
  });
});
