import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import {
  apiError,
  json,
  ME,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const EXT = {
  id: 7,
  number: "101",
  name: "Front desk",
  externalNumber: "",
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const DEVICE = {
  id: 3,
  extensionId: 7,
  sipUsername: "desk-phone",
  enabled: true,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const BINDING = {
  aor: "sip:desk-phone@pbx.test",
  extension: "101",
  device: "desk-phone",
  contactUri: "sip:desk-phone@192.0.2.10:5060",
  source: "192.0.2.10:5060",
  transport: "udp",
  userAgent: "Yealink T54W 96.86.0.70",
  receivedNode: "hello-sip-1",
  expires: "2099-10-01T11:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const FIRST_SECRET = "first-test-secret";
const ROTATED_SECRET = "rotated-test-secret";

function api(initialDevices: unknown[] = [], more: Routes = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: [EXT] }),
    "GET /api/v1/devices": () => json({ items: initialDevices }),
    "POST /api/v1/devices": () =>
      json({ ...DEVICE, secret: FIRST_SECRET, sipDomain: "pbx.test" }, 201),
    "POST /api/v1/devices/3/rotate-secret": () =>
      json({ ...DEVICE, secret: ROTATED_SECRET, sipDomain: "pbx.test" }),
    "PATCH /api/v1/devices/3": () => json({ ...DEVICE, enabled: false }),
    "DELETE /api/v1/devices/3": noContent,
    ...more,
  });
}

async function openNew() {
  const buttons = await screen.findAllByRole("button", { name: "New device" });
  await waitFor(() => expect(buttons[0]).toBeEnabled());
  fireEvent.click(buttons[0]!);
  return screen.getByRole("dialog", { name: "New device" });
}

async function createDevice() {
  const dialog = await openNew();
  fireEvent.change(within(dialog).getByLabelText("Extension"), {
    target: { value: "7" },
  });
  fireEvent.change(within(dialog).getByLabelText("SIP username"), {
    target: { value: "desk-phone" },
  });
  fireEvent.click(
    within(dialog).getByRole("button", { name: "Create device" }),
  );
  return screen.findByRole("dialog", { name: "Device created" });
}

describe("Devices", () => {
  it("shows the secret once, in a dialog, after creating a device", async () => {
    const calls = api();
    renderApp("/devices");

    const dialog = await createDevice();
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(
      within(dialog).getByRole("textbox", {
        name: "SIP secret for desk-phone",
      }),
    ).toHaveTextContent(FIRST_SECRET);
    expect(dialog).toHaveTextContent("Shown once");
    expect(dialog).toHaveTextContent(
      "Enter the username and secret in the phone, with pbx.test as the domain.",
    );
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "Copy" }),
      ).toHaveFocus(),
    );
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/devices",
      body: { extensionId: 7, sipUsername: "desk-phone", enabled: true },
    });

    // The new device is listed with its extension, without the secret.
    const row = screen.getByRole("row", { name: /desk-phone/ });
    expect(row).toHaveTextContent("101");
    expect(row).toHaveTextContent("Front desk");

    fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.body).not.toHaveTextContent(FIRST_SECRET);
  });

  it("says only what it knows when the server sends no SIP domain", async () => {
    api([], {
      "POST /api/v1/devices": () =>
        json({ ...DEVICE, secret: FIRST_SECRET }, 201),
    });
    renderApp("/devices");
    const dialog = await createDevice();
    expect(dialog).toHaveTextContent(
      "Enter the username and secret in the phone.",
    );
  });

  it("closes the secret dialog on Escape", async () => {
    api();
    renderApp("/devices");

    await createDevice();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.body).not.toHaveTextContent(FIRST_SECRET);
  });

  it("does not show the secret again after navigating away and back", async () => {
    api();
    renderApp("/devices");

    await createDevice();
    const nav = screen.getByRole("navigation", { name: "Primary" });
    fireEvent.click(within(nav).getByRole("link", { name: "Extensions" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Extensions" }),
    ).toBeInTheDocument();

    fireEvent.click(within(nav).getByRole("link", { name: "Devices" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Devices" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(document.body).not.toHaveTextContent(FIRST_SECRET);
  });

  it("shows a new secret after confirming a rotation", async () => {
    const calls = api([DEVICE]);
    renderApp("/devices");

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Rotate secret for desk-phone",
      }),
    );
    const confirm = screen.getByRole("dialog", {
      name: "Rotate the secret of desk-phone?",
    });
    expect(calls.some((c) => c.url.endsWith("rotate-secret"))).toBe(false);
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Rotate secret" }),
    );

    const dialog = await screen.findByRole("dialog", {
      name: "Secret rotated",
    });
    expect(dialog).toHaveTextContent(ROTATED_SECRET);
    expect(dialog).toHaveTextContent(
      "desk-phone stops registering until it has the new secret.",
    );
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain(
      "POST /api/v1/devices/3/rotate-secret",
    );

    fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(document.body).not.toHaveTextContent(ROTATED_SECRET);
  });

  it("copies the secret, or explains how when the clipboard is unavailable", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    api();
    renderApp("/devices");

    const dialog = await createDevice();
    fireEvent.click(within(dialog).getByRole("button", { name: "Copy" }));
    expect(await screen.findByText("Secret copied.")).toBeVisible();
    expect(writeText).toHaveBeenCalledWith(FIRST_SECRET);

    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    fireEvent.click(within(dialog).getByRole("button", { name: "Copy" }));
    expect(
      await screen.findByText(/select the secret and copy it/),
    ).toBeVisible();
  });

  it("validates the form before sending", async () => {
    const calls = api();
    renderApp("/devices");

    const dialog = await openNew();
    expect(within(dialog).getByLabelText("Extension")).toHaveFocus();
    const username = within(dialog).getByLabelText("SIP username");
    fireEvent.change(username, { target: { value: "bad name!" } });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create device" }),
    );

    expect(
      within(dialog).getByLabelText("Extension"),
    ).toHaveAccessibleDescription("Choose an extension.");
    expect(username).toHaveAttribute("aria-invalid", "true");
    expect(username).toHaveAccessibleDescription(/1 to 64 letters/);
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("shows the server's error in the new-device dialog", async () => {
    api([], {
      "POST /api/v1/devices": () =>
        apiError(409, "conflict", "sipUsername desk-phone already exists"),
    });
    renderApp("/devices");
    const dialog = await openNew();
    fireEvent.change(within(dialog).getByLabelText("Extension"), {
      target: { value: "7" },
    });
    fireEvent.change(within(dialog).getByLabelText("SIP username"), {
      target: { value: "desk-phone" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create device" }),
    );
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "already exists",
    );
  });

  it("asks for an extension first when there is none", async () => {
    api([], { "GET /api/v1/extensions": () => json({ items: [] }) });
    renderApp("/devices");
    expect(
      await screen.findByRole("link", { name: "Open extensions" }),
    ).toHaveAttribute("href", "/extensions");
  });

  it("shows registration and user agent from live state, — when unknown", async () => {
    api([DEVICE, { ...DEVICE, id: 4, sipUsername: "spare" }], {
      "GET /api/v1/registrations": () =>
        json({ items: [BINDING, { ...BINDING, contactUri: "sip:x@b" }] }),
      "GET /api/v1/presence": () => json({ items: [] }),
    });
    renderApp("/devices");
    const row = await screen.findByRole("row", { name: /desk-phone/ });
    await within(row).findByText("2 contacts");
    expect(row).toHaveTextContent("Yealink T54W 96.86.0.70");
    const spare = screen.getByRole("row", { name: /spare/ });
    expect(spare).toHaveTextContent("No contact");
  });

  it("shows — for registration when live state is unavailable", async () => {
    api([DEVICE], {
      "GET /api/v1/registrations": () =>
        apiError(503, "unavailable", "live state is unavailable"),
    });
    renderApp("/devices");
    const row = await screen.findByRole("row", { name: /desk-phone/ });
    const cells = within(row).getAllByRole("cell");
    expect(cells[2]).toHaveTextContent("—");
    expect(cells[3]).toHaveTextContent("—");
  });

  it("disables and deletes a device after confirming", async () => {
    const calls = api([DEVICE]);
    renderApp("/devices");

    const toggle = await screen.findByRole("switch", {
      name: "Enabled: desk-phone",
    });
    expect(toggle).toBeChecked();
    fireEvent.click(toggle);
    await waitFor(() => expect(toggle).not.toBeChecked());
    expect(screen.getByText("desk-phone disabled.")).toBeVisible();
    expect(calls).toContainEqual({
      method: "PATCH",
      url: "/api/v1/devices/3",
      body: { enabled: false },
    });

    fireEvent.click(
      screen.getByRole("button", { name: "Delete device desk-phone" }),
    );
    const confirm = screen.getByRole("dialog", { name: "Delete desk-phone?" });
    expect(
      within(confirm).getByRole("button", { name: "Delete device" }),
    ).toHaveFocus();
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete device" }),
    );
    expect(
      await screen.findByRole("heading", { name: "No devices yet" }),
    ).toBeVisible();
    expect(screen.getByText("Device desk-phone deleted.")).toBeVisible();
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain(
      "DELETE /api/v1/devices/3",
    );
  });
});
