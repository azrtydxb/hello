import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const ME = {
  username: "alice",
  role: "user",
  displayName: "Alice",
  authMethod: "local",
  extensions: [{ id: 10, number: "101", name: "Alice" }],
  voicemailEmail: { notify: true, attach: true, after: "none" },
  emailDelivery: true,
  ringWeb: true,
  ringMobile: true,
};

const DEVICE = {
  id: 4,
  kind: "web",
  label: "Firefox on the desk",
  platform: "Firefox 130",
  appVersion: "",
  extensionId: 10,
  extensionNumber: "101",
  sipUsername: "w4",
  lastSeenAt: STAMP,
  createdAt: STAMP,
  registered: true,
};

const PASSWORD = "derived-test-password";

function api(devices: unknown[] = [DEVICE], more: Routes = {}, me = ME) {
  return mockApi({
    "GET /api/v1/auth/me": () => json(me),
    "GET /api/v1/me/extensions": () => json({ items: [] }),
    "GET /api/v1/me/devices": () => json({ items: devices }),
    ...more,
  });
}

describe("My web devices", () => {
  it("lists the user's devices with their state", async () => {
    api();
    renderApp("/me/devices");
    const row = (await screen.findByText("Firefox on the desk")).closest("tr")!;
    expect(within(row).getByText("101")).toBeInTheDocument();
    expect(within(row).getByText("Registered")).toBeInTheDocument();
  });

  it("says so when there is none", async () => {
    api([]);
    renderApp("/me/devices");
    expect(await screen.findByText("No web devices yet")).toBeInTheDocument();
  });

  it("adds a device and shows its password once", async () => {
    const calls = api([], {
      "POST /api/v1/me/devices": () =>
        json(
          {
            deviceId: 4,
            sipUsername: "w4.1893456000",
            sipPassword: PASSWORD,
            realm: "pbx.test",
            wssUrl: "",
            expiresAt: "2030-01-01T00:00:00Z",
            device: DEVICE,
            ice: { iceServers: [], expiresAt: STAMP, ttlSeconds: 3600 },
          },
          201,
        ),
    });
    renderApp("/me/devices");
    const form = await screen.findByRole("form", { name: "Add a web device" });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "Work laptop" },
    });
    fireEvent.click(
      within(form).getByRole("button", { name: "Add web device" }),
    );
    expect(
      await screen.findByText("Firefox on the desk added"),
    ).toBeInTheDocument();
    expect(screen.getByText(PASSWORD)).toBeInTheDocument();
    expect(screen.getByText("w4.1893456000")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({ label: "Work laptop" });
    // Closing the dialog drops the password from the page.
    fireEvent.click(screen.getByRole("button", { name: "Done" }));
    await waitFor(() => expect(screen.queryByText(PASSWORD)).toBeNull());
  });

  it("explains the device limit", async () => {
    api([], {
      "POST /api/v1/me/devices": () =>
        apiError(409, "device_limit", "you have the most devices allowed"),
    });
    renderApp("/me/devices");
    const form = await screen.findByRole("form", { name: "Add a web device" });
    fireEvent.click(
      within(form).getByRole("button", { name: "Add web device" }),
    );
    expect(
      await screen.findByText(/most devices allowed. Revoke one first/),
    ).toBeInTheDocument();
  });

  it("revokes a device after confirmation", async () => {
    const calls = api([DEVICE], { "DELETE /api/v1/me/devices/4": noContent });
    renderApp("/me/devices");
    fireEvent.click(
      await screen.findByRole("button", { name: "Revoke Firefox on the desk" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Revoke device" }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "DELETE",
        url: "/api/v1/me/devices/4",
        body: undefined,
      }),
    );
  });

  it("turns ringing of web devices off", async () => {
    const calls = api([], { "PATCH /api/v1/me/profile": noContent });
    renderApp("/me/devices");
    fireEvent.click(
      await screen.findByRole("switch", { name: /Ring my web devices/ }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        url: "/api/v1/me/profile",
        body: { ringWeb: false },
      }),
    );
  });
});
