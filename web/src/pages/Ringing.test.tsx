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
      customTones: true,
    },
    {
      vendor: "poly",
      label: "Poly",
      slots: 23,
      tones: tones(["ringer1", "ringer2", "silent"]),
      customTones: false,
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

const CHIME = {
  id: 4,
  name: "chime",
  sizeBytes: 16044,
  sha256: "a".repeat(64),
  sampleRate: 8000,
  durationMs: 1000,
  createdAt: "2026-10-27T10:00:00Z",
  usedBy: 0,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/ring-tags": () =>
      json({ items: [tag("internal", 1), tag("doorbell", 1)] }),
    "GET /api/v1/ring-map": () => json(VIEW),
    "GET /api/v1/ringtones": () => json({ items: [CHIME] }),
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

  it("offers a library tone on a vendor that is provisioned one", async () => {
    const calls = setup({
      "PUT /api/v1/ring-map": () => json({ global: [], groups: [] }),
    });
    await openMap();
    const yealink = screen.getByLabelText("Yealink tone for internal");
    await waitFor(() => expect(yealink).toHaveTextContent("Custom: chime"));
    expect(
      screen.getByLabelText("Poly tone for internal"),
    ).not.toHaveTextContent("Custom: chime");
    fireEvent.change(yealink, { target: { value: "custom:chime" } });
    fireEvent.click(screen.getByRole("button", { name: "Save ring map" }));
    expect(await screen.findByText(/Ring map saved/)).toBeVisible();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      entries: [
        { tag: "internal", vendor: "yealink", tone: "custom:chime" },
        { tag: "doorbell", vendor: "yealink", tone: "3" },
      ],
    });
  });
});

describe("Ringing: the tone library", () => {
  async function openTones() {
    renderApp("/ringing");
    fireEvent.click(await screen.findByRole("tab", { name: "Tones" }));
    return screen.findByRole("list", { name: "Tones" });
  }

  it("lists the tones with their size and use", async () => {
    setup({
      "GET /api/v1/ringtones": () => json({ items: [{ ...CHIME, usedBy: 2 }] }),
    });
    const list = await openTones();
    expect(list).toHaveTextContent("chime");
    expect(list).toHaveTextContent("1.0 s · 8 kHz · 16 KiB");
    expect(list).toHaveTextContent("used by 2 ring map choices");
  });

  it("uploads a tone and refuses a bad name before sending", async () => {
    const calls = setup({
      "POST /api/v1/ringtones": () =>
        json({ ...CHIME, id: 5, name: "bell" }, 201),
    });
    await openTones();
    fireEvent.change(screen.getByLabelText("Tone name"), {
      target: { value: "Bad Name" },
    });
    fireEvent.change(screen.getByLabelText("WAV file"), {
      target: {
        files: [new File(["RIFF"], "bell.wav", { type: "audio/wav" })],
      },
    });
    fireEvent.click(screen.getByRole("button", { name: "Upload tone" }));
    expect(
      await screen.findByText(
        "Use 1 to 32 lowercase letters, digits or dashes.",
      ),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(screen.getByLabelText("Tone name"), {
      target: { value: "bell" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Upload tone" }));
    expect(await screen.findByText("Tone bell added.")).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(true);
    expect(screen.getByLabelText("Tone bell")).toBeVisible();
  });

  it("shows the API's reason for a refused file", async () => {
    setup({
      "POST /api/v1/ringtones": () =>
        json(
          {
            error: {
              code: "validation_failed",
              message: "invalid",
              fields: [{ path: "file", message: "the audio must be mono" }],
            },
          },
          400,
        ),
    });
    await openTones();
    fireEvent.change(screen.getByLabelText("Tone name"), {
      target: { value: "wide" },
    });
    fireEvent.change(screen.getByLabelText("WAV file"), {
      target: { files: [new File(["RIFF"], "w.wav")] },
    });
    fireEvent.click(screen.getByRole("button", { name: "Upload tone" }));
    expect(
      await screen.findAllByText("the audio must be mono"),
    ).not.toHaveLength(0);
  });

  it("deletes a tone after confirmation", async () => {
    const calls = setup({
      "DELETE /api/v1/ringtones/4": () => new Response(null, { status: 204 }),
    });
    await openTones();
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete tone chime" }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Delete tone" }));
    expect(await screen.findByText("Tone chime deleted.")).toBeVisible();
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("offers a viewer no upload or delete", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }),
    });
    await openTones();
    expect(screen.queryByRole("button", { name: "Upload tone" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Delete tone chime" }),
    ).toBeNull();
  });
});
