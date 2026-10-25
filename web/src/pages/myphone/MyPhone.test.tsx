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
  email: "alice@example.com",
  authMethod: "local",
  mustChangePassword: false,
  extensions: [{ id: 10, number: "101", name: "Alice" }],
  voicemailEmail: { notify: true, attach: true, after: "none" },
  emailDelivery: true,
};

const EXT = {
  id: 10,
  number: "101",
  name: "Alice",
  dnd: false,
  forwardAlways: "",
  forwardBusy: "",
  forwardNoAnswer: "",
  voicemailEnabled: true,
};

const MSG = {
  id: 1000,
  boxId: 100,
  caller: "+3211223344",
  durationMs: 65000,
  heard: false,
  emailStatus: "sent",
  createdAt: STAMP,
};

function api(more: Routes = {}, me: object = ME) {
  return mockApi({
    "GET /api/v1/auth/me": () => json(me),
    "GET /api/v1/me/extensions": () => json({ items: [EXT] }),
    ...more,
  });
}

function sidebar() {
  return screen.getByRole("navigation", { name: "Primary" });
}

describe("My phone (role user)", () => {
  it("shows a slim sidebar and never asks for admin data", async () => {
    const calls = api();
    renderApp("/");
    expect(await screen.findByText("Hello, Alice")).toBeInTheDocument();

    const nav = within(sidebar());
    for (const name of [
      "Address book",
      "Voicemail",
      "Call forwarding",
      "Call history",
      "Profile",
    ]) {
      expect(nav.getByRole("link", { name, hidden: true })).toBeInTheDocument();
    }
    for (const hidden of [
      "Extensions",
      "Devices",
      "Trunks",
      "Cluster",
      "Users",
      "Email",
      "Dashboard",
      "System",
    ]) {
      expect(
        nav.queryByRole("link", { name: new RegExp(`^${hidden}`) }),
      ).toBeNull();
    }
    // Nothing outside /auth/me and /me/ is requested for this role.
    await waitFor(() => expect(calls.length).toBeGreaterThan(0));
    for (const c of calls) {
      expect(
        c.url === "/api/v1/auth/me" ||
          c.url.startsWith("/api/v1/me/") ||
          c.url === "/api/v1/version",
      ).toBe(true);
    }
  });

  it("does not open an admin page for a user", async () => {
    api();
    renderApp("/users");
    // The page itself refuses; no admin list is fetched.
    expect(await screen.findByText("Administrators only")).toBeInTheDocument();
  });

  it("saves the label, DND and forwarding of the user's extension", async () => {
    const calls = api({
      "PATCH /api/v1/me/extensions/10": () =>
        json({ ...EXT, dnd: true, forwardAlways: "102" }),
    });
    renderApp("/me/forwarding");
    fireEvent.click(
      await screen.findByRole("switch", { name: /Do not disturb/ }),
    );
    fireEvent.change(screen.getByLabelText("Forward all calls to"), {
      target: { value: "102" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        url: "/api/v1/me/extensions/10",
        body: {
          name: "Alice",
          dnd: true,
          forwardAlways: "102",
          forwardBusy: "",
          forwardNoAnswer: "",
        },
      }),
    );
    expect(await screen.findByText("Saved.")).toBeInTheDocument();
  });

  it("shows a server validation error next to the field", async () => {
    api({
      "PATCH /api/v1/me/extensions/10": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration",
              fields: [
                {
                  path: "forwardBusy",
                  message:
                    "must be empty or 2-32 of 0-9 * # with an optional leading +",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/me/forwarding");
    fireEvent.change(await screen.findByLabelText("Forward when busy to"), {
      target: { value: "x" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText(/must be empty or 2-32/),
    ).toBeInTheDocument();
  });

  it("explains a user without an extension", async () => {
    api(
      { "GET /api/v1/me/extensions": () => json({ items: [] }) },
      { ...ME, extensions: [] },
    );
    renderApp("/me/forwarding");
    expect(await screen.findByText("No extension linked")).toBeInTheDocument();
  });

  it("lists voicemail with play, download, heard and delete", async () => {
    const calls = api({
      "GET /api/v1/me/extensions/10/voicemail/messages": () =>
        json({ items: [MSG] }),
      "GET /api/v1/me/extensions/10/voicemail": () =>
        json({
          extensionId: 10,
          boxId: 100,
          hasPin: true,
          hasGreeting: false,
          hasUnreachableGreeting: false,
        }),
      "POST /api/v1/me/voicemail/messages/1000/heard": () =>
        json({ id: 1000, heard: true }),
      "DELETE /api/v1/me/voicemail/messages/1000": () => noContent(),
    });
    renderApp("/me/voicemail");
    const table = await screen.findByRole("table", {
      name: "Messages for 101",
    });
    expect(within(table).getByText("+3211223344")).toBeInTheDocument();
    expect(
      within(table).getByRole("link", { name: /Download the message from/ }),
    ).toHaveAttribute(
      "href",
      "/api/v1/me/voicemail/messages/1000/audio?download=1",
    );

    fireEvent.click(
      within(table).getByRole("button", {
        name: /Mark the message from \+3211223344 heard/,
      }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        url: "/api/v1/me/voicemail/messages/1000/heard",
        body: { heard: true },
      }),
    );

    fireEvent.click(
      within(table).getByRole("button", { name: /Delete the message from/ }),
    );
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Delete message",
      }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "DELETE",
        url: "/api/v1/me/voicemail/messages/1000",
        body: undefined,
      }),
    );
    expect(await screen.findByText("No messages")).toBeInTheDocument();
    // Only the user's own routes were used, never /voicemail/ of the admin API.
    expect(calls.some((c) => c.url.startsWith("/api/v1/voicemail"))).toBe(
      false,
    );
  });

  it("changes the PIN and uploads a greeting", async () => {
    const calls = api({
      "GET /api/v1/me/extensions/10/voicemail/messages": () =>
        json({ items: [] }),
      "GET /api/v1/me/extensions/10/voicemail": () =>
        json({
          extensionId: 10,
          boxId: 100,
          hasPin: false,
          hasGreeting: false,
          hasUnreachableGreeting: false,
        }),
      "PUT /api/v1/me/extensions/10/voicemail": () =>
        json({
          extensionId: 10,
          boxId: 100,
          hasPin: true,
          hasGreeting: true,
          hasUnreachableGreeting: false,
        }),
    });
    renderApp("/me/voicemail");
    fireEvent.change(await screen.findByLabelText("PIN"), {
      target: { value: "4321" },
    });
    const file = new File([new Uint8Array(12)], "hello.wav", {
      type: "audio/wav",
    });
    fireEvent.change(screen.getByLabelText("Upload a greeting (WAV)"), {
      target: { files: [file] },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT");
    const form = put?.body as FormData;
    expect(form.get("pin")).toBe("4321");
    expect(form.get("greeting")).toBeInstanceOf(Blob);
    expect(
      await screen.findByText("Voicemail settings saved."),
    ).toBeInTheDocument();
  });

  it("shows the call history with a load-more page", async () => {
    const call = (id: number, src: string) => ({
      id,
      startTime: STAMP,
      durationMs: 1000,
      direction: "outbound",
      kind: "call",
      source: src,
      destination: "+3299",
      answered: true,
    });
    api({
      "GET /api/v1/me/calls?limit=50": () =>
        json({ items: [call(2, "101")], next: "2" }),
      "GET /api/v1/me/calls?limit=50&cursor=2": () =>
        json({ items: [call(1, "101")], next: "" }),
    });
    renderApp("/me/history");
    const table = await screen.findByRole("table", { name: "Your calls" });
    expect(within(table).getAllByText("101")).toHaveLength(1);
    fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    await waitFor(() =>
      expect(
        within(screen.getByRole("table", { name: "Your calls" })).getAllByText(
          "101",
        ),
      ).toHaveLength(2),
    );
    expect(screen.queryByRole("button", { name: "Load more" })).toBeNull();
  });

  it("edits the profile and the voicemail email options", async () => {
    const calls = api({ "PATCH /api/v1/me/profile": () => noContent() });
    renderApp("/me/profile");
    const form = await screen.findByRole("form", { name: "Profile" });
    fireEvent.change(within(form).getByLabelText("Email"), {
      target: { value: "a@new.example" },
    });
    fireEvent.click(
      within(form).getByRole("switch", { name: "Attach the recording" }),
    );
    fireEvent.change(within(form).getByLabelText("After sending"), {
      target: { value: "mark_read" },
    });
    fireEvent.click(within(form).getByRole("button", { name: "Save profile" }));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        url: "/api/v1/me/profile",
        body: {
          displayName: "Alice",
          email: "a@new.example",
          voicemailEmail: { notify: true, attach: false, after: "mark_read" },
        },
      }),
    );
  });

  it("checks the new password before sending it, and maps server errors", async () => {
    const calls = api({
      "POST /api/v1/auth/password": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration",
              fields: [{ path: "currentPassword", message: "is incorrect" }],
            },
          },
          400,
        ),
    });
    renderApp("/me/profile");
    await screen.findByRole("form", { name: "Profile" });
    const set = (label: string, value: string) =>
      fireEvent.change(screen.getByLabelText(label), { target: { value } });
    set("Current password", "old");
    set("New password", "short");
    set("Repeat the new password", "short");
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(
      await screen.findByText("Use at least 10 characters."),
    ).toBeInTheDocument();
    set("New password", "a long enough pass");
    set("Repeat the new password", "something else entirely");
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(
      await screen.findByText("The two passwords differ."),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    set("Repeat the new password", "a long enough pass");
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(await screen.findByText("is incorrect")).toBeInTheDocument();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/auth/password",
      body: { currentPassword: "old", newPassword: "a long enough pass" },
    });
  });
});

describe("forced password change", () => {
  it("blocks the app until a new password is chosen", async () => {
    let must = true;
    const calls = mockApi({
      "GET /api/v1/auth/me": () => json({ ...ME, mustChangePassword: must }),
      "GET /api/v1/me/extensions": () => json({ items: [EXT] }),
      "POST /api/v1/auth/password": () => {
        must = false;
        return noContent();
      },
    });
    renderApp("/me/forwarding");
    expect(
      await screen.findByRole("heading", { name: "Choose a new password" }),
    ).toBeInTheDocument();
    // The app behind it is not rendered or loaded.
    expect(screen.queryByRole("navigation", { name: "Primary" })).toBeNull();
    expect(calls.some((c) => c.url.startsWith("/api/v1/me/extensions"))).toBe(
      false,
    );

    fireEvent.change(screen.getByLabelText("Current password"), {
      target: { value: "initial-password" },
    });
    fireEvent.change(screen.getByLabelText("New password"), {
      target: { value: "my own long password" },
    });
    fireEvent.change(screen.getByLabelText("Repeat the new password"), {
      target: { value: "my own long password" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(
      await screen.findByRole("navigation", { name: "Primary" }),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("heading", { name: "Choose a new password" }),
    ).toBeNull();
  });

  it("does not trap an unauthenticated 403 as a password prompt elsewhere", async () => {
    api({ "GET /api/v1/me/extensions": () => apiError(403, "no_user", "no") });
    renderApp("/me/forwarding");
    expect(
      await screen.findByText("Could not load your extension"),
    ).toBeInTheDocument();
  });
});
