import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

const EXT = {
  id: 7,
  number: "101",
  name: "Front desk",
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

const FIRST_SECRET = "first-test-secret";
const ROTATED_SECRET = "rotated-test-secret";

function api(initialDevices: unknown[] = []) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: [EXT] }),
    "GET /api/v1/devices": () => json({ items: initialDevices }),
    "POST /api/v1/devices": () =>
      json({ ...DEVICE, secret: FIRST_SECRET }, 201),
    "POST /api/v1/devices/3/rotate-secret": () =>
      json({ ...DEVICE, secret: ROTATED_SECRET }),
    "PATCH /api/v1/devices/3": () => json({ ...DEVICE, enabled: false }),
    "DELETE /api/v1/devices/3": noContent,
  });
}

async function createDevice() {
  fireEvent.change(await screen.findByLabelText("Extension"), {
    target: { value: "7" },
  });
  fireEvent.change(screen.getByLabelText("SIP username"), {
    target: { value: "desk-phone" },
  });
  fireEvent.click(screen.getByRole("button", { name: "Create device" }));
  return screen.findByRole("dialog", { name: "Device created" });
}

describe("Devices", () => {
  it("shows the secret once, in a dialog, after creating a device", async () => {
    const calls = api();
    renderApp("/devices");

    const dialog = await createDevice();
    expect(dialog).toHaveAttribute("aria-modal", "true");
    expect(within(dialog).getByLabelText("SIP secret")).toHaveValue(
      FIRST_SECRET,
    );
    expect(dialog).toHaveTextContent(/you will not see this again/i);
    // Focus moves into the dialog in an effect, which on a loaded runner can
    // land after findByRole has already returned the dialog.
    await waitFor(() =>
      expect(
        within(dialog).getByRole("button", { name: "Copy secret" }),
      ).toHaveFocus(),
    );
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/devices",
      body: { extensionId: 7, sipUsername: "desk-phone", enabled: true },
    });

    // The new device is listed with its extension number, without the secret.
    const row = screen.getByRole("row", { name: /desk-phone/ });
    expect(row).toHaveTextContent("101");

    fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByDisplayValue(FIRST_SECRET)).toBeNull();
    expect(document.body).not.toHaveTextContent(FIRST_SECRET);
  });

  it("closes the secret dialog on Escape", async () => {
    api();
    renderApp("/devices");

    const dialog = await createDevice();
    fireEvent.keyDown(within(dialog).getByLabelText("SIP secret"), {
      key: "Escape",
    });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByDisplayValue(FIRST_SECRET)).toBeNull();
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
    expect(screen.queryByDisplayValue(FIRST_SECRET)).toBeNull();

    fireEvent.click(within(nav).getByRole("link", { name: "Devices" }));
    expect(await screen.findByLabelText("SIP username")).toBeInTheDocument();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByDisplayValue(FIRST_SECRET)).toBeNull();
    expect(document.body).not.toHaveTextContent(FIRST_SECRET);
  });

  it("shows a new secret after rotating", async () => {
    const calls = api([DEVICE]);
    renderApp("/devices");

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Rotate secret for desk-phone",
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Rotate" }));

    const dialog = await screen.findByRole("dialog", {
      name: "Secret rotated",
    });
    expect(within(dialog).getByLabelText("SIP secret")).toHaveValue(
      ROTATED_SECRET,
    );
    expect(document.body).not.toHaveTextContent(FIRST_SECRET);
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain(
      "POST /api/v1/devices/3/rotate-secret",
    );

    fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    expect(screen.queryByDisplayValue(ROTATED_SECRET)).toBeNull();
  });

  it("copies the secret, or explains how when the clipboard is unavailable", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText } });
    api();
    renderApp("/devices");

    const dialog = await createDevice();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Copy secret" }),
    );
    expect(
      await within(dialog).findByText("Copied to the clipboard."),
    ).toBeVisible();
    expect(writeText).toHaveBeenCalledWith(FIRST_SECRET);

    vi.stubGlobal("navigator", { ...navigator, clipboard: undefined });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Copy secret" }),
    );
    expect(
      await within(dialog).findByText(/select the secret and copy it manually/),
    ).toBeVisible();
  });

  it("validates the SIP username before sending", async () => {
    const calls = api();
    renderApp("/devices");

    fireEvent.change(await screen.findByLabelText("Extension"), {
      target: { value: "7" },
    });
    const username = screen.getByLabelText("SIP username");
    fireEvent.change(username, { target: { value: "bad name!" } });
    fireEvent.click(screen.getByRole("button", { name: "Create device" }));

    expect(username).toHaveAttribute("aria-invalid", "true");
    expect(username).toHaveAccessibleDescription(/1 to 64 letters/);
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("disables and deletes a device with an in-page confirmation", async () => {
    const calls = api([DEVICE]);
    renderApp("/devices");

    fireEvent.click(
      await screen.findByRole("button", { name: "Disable desk-phone" }),
    );
    expect(
      await screen.findByRole("button", { name: "Enable desk-phone" }),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "PATCH",
      url: "/api/v1/devices/3",
      body: { enabled: false },
    });

    fireEvent.click(screen.getByRole("button", { name: "Delete desk-phone" }));
    expect(
      screen.getByRole("group", { name: "Delete desk-phone?" }),
    ).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Delete device" }));
    expect(await screen.findByText("No devices yet.")).toBeVisible();
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain(
      "DELETE /api/v1/devices/3",
    );
  });
});
