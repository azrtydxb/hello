import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, renderApp } from "../test/api";

function me(role: string) {
  return { "GET /api/v1/auth/me": () => json({ username: "pat", role }) };
}

const USERS = [
  { id: 1, username: "pat", role: "admin", createdAt: "2026-10-01T00:00:00Z" },
  { id: 2, username: "sam", role: "viewer", createdAt: "2026-10-02T00:00:00Z" },
];

const EXT = {
  id: 7,
  number: "101",
  name: "Front desk",
  externalNumber: "",
  dnd: false,
  voicemail: true,
  recordByDefault: false,
  forwardAlways: "",
  forwardBusy: "",
  forwardNoAnswer: "",
  forwardTimeout: 20,
};

function sidebar() {
  return screen.getByRole("navigation", { name: "Primary" });
}

describe("Users (spec S-23)", () => {
  it("lists users with their roles for an admin, linked from the sidebar", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/users": () => json({ items: USERS }),
    });
    renderApp("/users");

    const table = await screen.findByRole("table", { name: "Users" });
    expect(within(table).getByText("sam")).toBeInTheDocument();
    expect(within(table).getByLabelText("Role of sam")).toHaveValue("viewer");
    expect(
      within(sidebar()).getByRole("link", { name: /Users/ }),
    ).toBeInTheDocument();
  });

  it("changes a role through PATCH /api/v1/users/{id}", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/users": () => json({ items: USERS }),
      "PATCH /api/v1/users/2": () => json({ ...USERS[1], role: "operator" }),
    });
    renderApp("/users");

    fireEvent.change(await screen.findByLabelText("Role of sam"), {
      target: { value: "operator" },
    });
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        url: "/api/v1/users/2",
        body: { role: "operator" },
      }),
    );
    expect(await screen.findByText("sam is now operator.")).toBeInTheDocument();
    expect(screen.getByLabelText("Role of sam")).toHaveValue("operator");
  });

  it("explains a refused last-admin demotion", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/users": () => json({ items: USERS }),
      "PATCH /api/v1/users/1": () =>
        apiError(409, "last_admin", "at least one user must remain admin"),
    });
    renderApp("/users");

    fireEvent.change(await screen.findByLabelText("Role of pat"), {
      target: { value: "viewer" },
    });
    expect(
      await screen.findByText("At least one user must remain administrator."),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Role of pat")).toHaveValue("admin");
  });

  it.each(["viewer", "operator"])(
    "hides the Users page from a %s",
    async (role) => {
      const calls = mockApi(me(role));
      renderApp("/users");

      expect(
        await screen.findByText("Your role cannot manage users."),
      ).toBeInTheDocument();
      expect(screen.queryByRole("table", { name: "Users" })).toBeNull();
      expect(
        within(sidebar()).queryByRole("link", { name: /Users/ }),
      ).toBeNull();
      expect(calls.some((c) => c.url === "/api/v1/users")).toBe(false);
    },
  );

  it("shows a viewer no write actions, and an operator the operator's", async () => {
    const routes = {
      "GET /api/v1/extensions": () => json({ items: [EXT] }),
      "GET /api/v1/devices": () => json({ items: [] }),
    };
    mockApi({ ...me("viewer"), ...routes });
    const { unmount } = renderApp("/extensions");
    expect(await screen.findByText("Front desk")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New extension" })).toBeNull();
    expect(
      within(sidebar()).queryByRole("link", { name: /Route tester/ }),
    ).toBeNull();
    expect(screen.queryByRole("link", { name: /Test a number/ })).toBeNull();
    // Opening an extension shows its details without Save or Delete.
    fireEvent.click(screen.getByText("Front desk"));
    const drawer = await screen.findByRole("dialog");
    expect(within(drawer).queryByRole("button", { name: "Save" })).toBeNull();
    expect(within(drawer).queryByRole("button", { name: "Delete" })).toBeNull();
    unmount();

    mockApi({ ...me("operator"), ...routes });
    renderApp("/extensions");
    expect(
      (await screen.findAllByRole("button", { name: "New extension" })).length,
    ).toBeGreaterThan(0);
    expect(
      within(sidebar()).getByRole("link", { name: /Route tester/ }),
    ).toBeInTheDocument();
  });

  it("shows API tokens on System to an admin only", async () => {
    const routes = {
      "GET /api/v1/feature-codes": () => json({ items: [] }),
      "GET /api/v1/presence": () => json({ items: [] }),
      "GET /api/v1/tokens": () => json({ items: [] }),
    };
    const calls = mockApi({ ...me("operator"), ...routes });
    const { unmount } = renderApp("/system");
    expect(
      await screen.findByRole("heading", { level: 1, name: "System" }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New token" })).toBeNull();
    expect(calls.some((c) => c.url === "/api/v1/tokens")).toBe(false);
    unmount();

    mockApi({ ...me("admin"), ...routes });
    renderApp("/system");
    expect(
      await screen.findByRole("button", { name: "New token" }),
    ).toBeInTheDocument();
  });
});
