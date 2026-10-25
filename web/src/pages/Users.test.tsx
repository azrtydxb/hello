import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const PREFS = { notify: true, attach: true, after: "none" };

const user = (over: object) => ({
  id: 2,
  username: "sam",
  displayName: "Sam",
  email: "sam@example.com",
  authMethod: "local",
  role: "user",
  enabled: true,
  mustChangePassword: false,
  extensionId: 7,
  extensionIds: [7],
  voicemailEmail: PREFS,
  createdAt: "2026-10-02T00:00:00Z",
  ...over,
});

const USERS = [
  user({
    id: 1,
    username: "pat",
    displayName: "Pat",
    role: "admin",
    extensionId: null,
    extensionIds: [],
  }),
  user({}),
];

const ext = (id: number, number: string, name: string) => ({
  id,
  number,
  name,
  externalNumber: "",
  createdAt: "2026-10-01T00:00:00Z",
  updatedAt: "2026-10-01T00:00:00Z",
});

function api(role: string, more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "pat", role }),
    "GET /api/v1/users": () => json({ items: USERS }),
    "GET /api/v1/extensions": () =>
      json({
        items: [
          ext(7, "101", "Front desk"),
          ext(8, "102", "Sales"),
          ext(9, "103", "Support"),
        ],
      }),
    ...more,
  });
}

function sidebar() {
  return screen.getByRole("navigation", { name: "Primary" });
}

describe("Users", () => {
  it("lists users with role, extensions and state, linked from the sidebar", async () => {
    api("admin");
    renderApp("/users");
    const table = await screen.findByRole("table", { name: "Users" });
    const row = within(table).getByText("sam").closest("tr") as HTMLElement;
    expect(within(row).getByText("user")).toBeInTheDocument();
    expect(within(row).getByText("101")).toBeInTheDocument();
    expect(within(row).getByText("Active")).toBeInTheDocument();
    expect(
      within(sidebar()).getByRole("link", { name: /Users/, hidden: true }),
    ).toBeInTheDocument();
  });

  it("creates a user with an initial password and linked extensions", async () => {
    const created = user({
      id: 3,
      username: "erin@example.com",
      displayName: "Erin",
      email: "erin@example.com",
      extensionId: 8,
      extensionIds: [8, 9],
      mustChangePassword: true,
    });
    const calls = api("admin", {
      "POST /api/v1/users": () => json(created, 201),
    });
    renderApp("/users");
    fireEvent.click(await screen.findByRole("button", { name: "Add user" }));
    const dialog = screen.getByRole("dialog", { name: "Add user" });
    fireEvent.change(within(dialog).getByLabelText("Login"), {
      target: { value: "erin@example.com" },
    });
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "Erin" },
    });
    fireEvent.change(within(dialog).getByLabelText("Email"), {
      target: { value: "erin@example.com" },
    });
    fireEvent.change(within(dialog).getByLabelText("Initial password"), {
      target: { value: "initial password 1" },
    });
    // Extension 101 is taken by sam.
    expect(within(dialog).getByLabelText(/101 Front desk/)).toBeDisabled();
    fireEvent.click(within(dialog).getByLabelText(/102 Sales/));
    fireEvent.click(within(dialog).getByLabelText(/103 Support/));
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create user" }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        url: "/api/v1/users",
        body: {
          username: "erin@example.com",
          displayName: "Erin",
          email: "erin@example.com",
          role: "user",
          password: "initial password 1",
          mustChangePassword: true,
          extensionIds: [8, 9],
          voicemailEmail: PREFS,
        },
      }),
    );
    const table = await screen.findByRole("table", { name: "Users" });
    const row = within(table)
      .getByRole("button", { name: "Edit erin@example.com" })
      .closest("tr") as HTMLElement;
    expect(within(row).getByText("Must change password")).toBeInTheDocument();
  });

  it("generates an initial password", async () => {
    api("admin");
    renderApp("/users");
    fireEvent.click(await screen.findByRole("button", { name: "Add user" }));
    const dialog = screen.getByRole("dialog", { name: "Add user" });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Generate a password" }),
    );
    const pw = (
      within(dialog).getByLabelText("Initial password") as HTMLInputElement
    ).value;
    expect(pw).toHaveLength(16);
  });

  it("shows field errors from the server in the dialog", async () => {
    api("admin", {
      "POST /api/v1/users": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration",
              fields: [
                { path: "password", message: "must be 10-128 characters" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/users");
    fireEvent.click(await screen.findByRole("button", { name: "Add user" }));
    const dialog = screen.getByRole("dialog", { name: "Add user" });
    fireEvent.change(within(dialog).getByLabelText("Login"), {
      target: { value: "x" },
    });
    fireEvent.change(within(dialog).getByLabelText("Initial password"), {
      target: { value: "short" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create user" }),
    );
    expect(
      await within(dialog).findByText("must be 10-128 characters"),
    ).toBeInTheDocument();
  });

  it("edits a user: role, extensions, voicemail email and a password reset", async () => {
    const calls = api("admin", {
      "PATCH /api/v1/users/2": () =>
        json(
          user({
            role: "viewer",
            extensionIds: [7, 8],
            mustChangePassword: true,
          }),
        ),
    });
    renderApp("/users");
    fireEvent.click(await screen.findByRole("button", { name: "Edit sam" }));
    const dialog = screen.getByRole("dialog", { name: "Edit sam" });
    expect(within(dialog).getByLabelText("Login")).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("Role"), {
      target: { value: "viewer" },
    });
    fireEvent.click(within(dialog).getByLabelText(/102 Sales/));
    fireEvent.click(
      within(dialog).getByRole("switch", { name: "Attach the recording" }),
    );
    fireEvent.change(within(dialog).getByLabelText("After sending"), {
      target: { value: "delete" },
    });
    fireEvent.change(within(dialog).getByLabelText("Reset password"), {
      target: { value: "reset password 99" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        url: "/api/v1/users/2",
        body: {
          displayName: "Sam",
          email: "sam@example.com",
          role: "viewer",
          extensionIds: [7, 8],
          voicemailEmail: { notify: true, attach: false, after: "delete" },
          password: "reset password 99",
          mustChangePassword: true,
        },
      }),
    );
  });

  it("does not send a password when none was typed", async () => {
    const calls = api("admin", {
      "PATCH /api/v1/users/2": () => json(user({})),
    });
    renderApp("/users");
    fireEvent.click(await screen.findByRole("button", { name: "Edit sam" }));
    fireEvent.click(
      within(screen.getByRole("dialog", { name: "Edit sam" })).getByRole(
        "button",
        { name: "Save" },
      ),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    const body = calls.find((c) => c.method === "PATCH")?.body as Record<
      string,
      unknown
    >;
    expect(body).not.toHaveProperty("password");
    expect(body).not.toHaveProperty("mustChangePassword");
  });

  it("disables and re-enables a user, but offers neither for yourself", async () => {
    const calls = api("admin", {
      "PATCH /api/v1/users/2": [
        () => json(user({ enabled: false })),
        () => json(user({ enabled: true })),
      ],
    });
    renderApp("/users");
    expect(
      await screen.findByRole("button", { name: "Edit pat" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Disable pat" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete pat" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Disable sam" }));
    expect(await screen.findByText("Disabled")).toBeInTheDocument();
    expect(calls).toContainEqual({
      method: "PATCH",
      url: "/api/v1/users/2",
      body: { enabled: false },
    });
    fireEvent.click(screen.getByRole("button", { name: "Enable sam" }));
    expect(
      await screen.findByText("Active", { selector: "td *" }),
    ).toBeInTheDocument();
  });

  it("deletes a user after confirming", async () => {
    const calls = api("admin", { "DELETE /api/v1/users/2": () => noContent() });
    renderApp("/users");
    fireEvent.click(await screen.findByRole("button", { name: "Delete sam" }));
    fireEvent.click(
      within(screen.getByRole("dialog")).getByRole("button", {
        name: "Delete user",
      }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "DELETE",
        url: "/api/v1/users/2",
        body: undefined,
      }),
    );
    await waitFor(() => expect(screen.queryByText("sam")).toBeNull());
  });

  it("explains a refused change", async () => {
    api("admin", {
      "PATCH /api/v1/users/2": () => apiError(409, "last_admin", "no"),
    });
    renderApp("/users");
    fireEvent.click(await screen.findByRole("button", { name: "Disable sam" }));
    expect(
      await screen.findByText(
        "At least one enabled user must remain administrator.",
      ),
    ).toBeInTheDocument();
  });

  it("is for administrators only", async () => {
    const calls = api("operator");
    renderApp("/users");
    expect(
      await screen.findByText("Your role cannot manage users."),
    ).toBeInTheDocument();
    expect(within(sidebar()).queryByRole("link", { name: /Users/ })).toBeNull();
    expect(calls.some((c) => c.url === "/api/v1/users")).toBe(false);
  });
});
