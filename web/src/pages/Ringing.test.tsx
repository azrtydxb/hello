import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, renderApp } from "../test/api";

const tag = (name: string, slot: number) => ({
  name,
  builtin: true,
  bellcoreSlot: slot,
  description: "",
  usedBy: 0,
});

const tones = (ids: string[]) =>
  ids.map((id) => ({ id, label: `Tone ${id}`, silent: id === "silent" }));

const VIEW = {
  vendors: [
    {
      vendor: "yealink",
      label: "Yealink",
      slots: 10,
      tones: tones(["1", "2", "3"]),
    },
    {
      vendor: "poly",
      label: "Poly",
      slots: 23,
      tones: tones(["ringer1", "ringer2", "silent"]),
    },
  ],
  defaults: [
    { tag: "internal", vendor: "yealink", tone: "1" },
    { tag: "internal", vendor: "poly", tone: "ringer2" },
    { tag: "doorbell", vendor: "yealink", tone: "1" },
    { tag: "doorbell", vendor: "poly", tone: "ringer2" },
  ],
  global: [{ tag: "doorbell", vendor: "yealink", tone: "3" }],
  groups: [],
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/ring-tags": () =>
      json({ items: [tag("internal", 1), tag("doorbell", 1)] }),
    "GET /api/v1/ring-map": () => json(VIEW),
    "GET /api/v1/phone-groups?limit=200": () =>
      json({ items: [{ id: 7, name: "Warehouse" }], nextCursor: "" }),
    ...extra,
  });
}

async function openMap() {
  renderApp("/ringing");
  fireEvent.click(await screen.findByRole("tab", { name: "Ring map" }));
  return screen.findByRole("table", { name: "Ring map" });
}

describe("Ringing: the ring map", () => {
  it("shows the saved choice and the default of every other cell", async () => {
    setup();
    await openMap();
    expect(screen.getByLabelText("Yealink tone for doorbell")).toHaveValue("3");
    const internal = screen.getByLabelText("Yealink tone for internal");
    expect(internal).toHaveValue("");
    expect(internal).toHaveTextContent("Default: Tone 1");
  });

  it("saves the chosen tones and nothing left on default", async () => {
    const calls = setup({
      "PUT /api/v1/ring-map": () => json({ global: [], groups: [] }),
    });
    await openMap();
    fireEvent.change(screen.getByLabelText("Poly tone for internal"), {
      target: { value: "silent" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save ring map" }));
    expect(await screen.findByText(/Ring map saved/)).toBeVisible();
    expect(calls.find((c) => c.method === "PUT")).toEqual({
      method: "PUT",
      url: "/api/v1/ring-map",
      body: {
        entries: [
          { tag: "doorbell", vendor: "yealink", tone: "3" },
          { tag: "internal", vendor: "poly", tone: "silent" },
        ],
      },
    });
  });

  it("saves a phone group's override on its own endpoint", async () => {
    const calls = setup({
      "PUT /api/v1/ring-map/groups/7": () => json({ global: [], groups: [] }),
    });
    await openMap();
    await waitFor(() =>
      expect(screen.getByLabelText("Applies to")).toHaveTextContent(
        "Warehouse",
      ),
    );
    fireEvent.change(screen.getByLabelText("Applies to"), {
      target: { value: "7" },
    });
    // The group starts empty and follows the all-phones choice.
    const cell = screen.getByLabelText("Yealink tone for doorbell");
    expect(cell).toHaveValue("");
    expect(cell).toHaveTextContent("Same as all phones: Tone 3");
    fireEvent.change(cell, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Save ring map" }));
    expect(await screen.findByText(/Group ring map saved/)).toBeVisible();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      entries: [{ tag: "doorbell", vendor: "yealink", tone: "2" }],
    });
  });

  it("shows a refused tone at its cell", async () => {
    setup({
      "PUT /api/v1/ring-map": () =>
        json(
          {
            error: {
              code: "validation_failed",
              message: "invalid",
              fields: [
                { path: "entries[1].tone", message: "not a ringer of poly" },
              ],
            },
          },
          400,
        ),
    });
    await openMap();
    fireEvent.change(screen.getByLabelText("Poly tone for internal"), {
      target: { value: "ringer1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save ring map" }));
    expect(await screen.findByText("not a ringer of poly")).toBeVisible();
  });

  it("offers a viewer no way to change the map", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }),
    });
    await openMap();
    expect(screen.queryByRole("button", { name: "Save ring map" })).toBeNull();
    expect(screen.getByLabelText("Yealink tone for doorbell")).toBeDisabled();
  });

  it("reports a failed load", async () => {
    setup({ "GET /api/v1/ring-map": () => apiError(500, "internal", "boom") });
    renderApp("/ringing");
    fireEvent.click(await screen.findByRole("tab", { name: "Ring map" }));
    expect(
      await screen.findByText("Could not load the ring map"),
    ).toBeVisible();
  });
});
