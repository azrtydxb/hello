import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const STAMP = "2026-11-09T10:00:00Z";
const SECRET = "ab".repeat(32);

const HOOK = {
  id: 4,
  name: "Gate bell",
  action: "ring_door",
  ringGroupId: 5,
  ringGroup: "800",
  rateLimit: 11,
  enabled: true,
  lastTriggeredAt: null,
  createdAt: STAMP,
  updatedAt: STAMP,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}, me = ME) {
  return mockApi({
    ...me,
    "GET /api/v1/door/webhooks": () => json({ items: [HOOK] }),
    "GET /api/v1/ring-groups": () =>
      json({
        items: [
          { id: 5, name: "800", isDoor: true, members: [] },
          { id: 6, name: "810", isDoor: false, members: [] },
        ],
      }),
    "GET /api/v1/paging-groups": () =>
      json({
        items: [{ id: 9, name: "Hall", code: "*60", members: [], callers: [] }],
      }),
    "GET /api/v1/announcements": () =>
      json({ items: [{ id: 2, name: "chime", createdAt: STAMP }] }),
    ...extra,
  });
}

describe("Door webhooks", () => {
  it("lists the webhooks without any secret", async () => {
    setup();
    renderApp("/door");
    expect(await screen.findByText("Gate bell")).toBeVisible();
    expect(screen.getByText("Rings Door group 800")).toBeVisible();
    expect(screen.getByText(/never pressed/)).toBeVisible();
    expect(document.body.textContent).not.toContain(SECRET);
  });

  it("shows a validation error at its field and sends nothing", async () => {
    const calls = setup();
    renderApp("/door");
    // New webhook opens once the list has loaded, or the load wipes the
    // webhook the page adds.
    await screen.findByText("Gate bell");
    fireEvent.click(screen.getAllByRole("button", { name: "New webhook" })[0]!);
    const drawer = screen.getByRole("dialog", { name: "New webhook" });
    fireEvent.change(within(drawer).getByLabelText("Name"), {
      target: { value: "a/b" },
    });
    fireEvent.change(within(drawer).getByLabelText("Triggers a minute"), {
      target: { value: "0" },
    });
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Create webhook" }),
    );
    expect(
      await within(drawer).findByText(/1-64 of A-Z a-z 0-9/),
    ).toBeVisible();
    expect(within(drawer).getByText(/from 1 to 600/)).toBeVisible();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);

    // A chime needs its paging group.
    fireEvent.change(within(drawer).getByLabelText("When pressed"), {
      target: { value: "chime_page" },
    });
    fireEvent.change(within(drawer).getByLabelText("Name"), {
      target: { value: "Hall chime" },
    });
    fireEvent.change(within(drawer).getByLabelText("Triggers a minute"), {
      target: { value: "5" },
    });
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Create webhook" }),
    );
    expect(
      await within(drawer).findByText("Choose the paging group to chime."),
    ).toBeVisible();
  });

  it("creates a webhook and shows its secret once", async () => {
    const calls = setup({
      "POST /api/v1/door/webhooks": () =>
        json({ ...HOOK, id: 7, name: "Back gate", secret: SECRET }, 201),
    });
    renderApp("/door");
    // New webhook opens once the list has loaded, or the load wipes the
    // webhook the page adds.
    await screen.findByText("Gate bell");
    fireEvent.click(screen.getAllByRole("button", { name: "New webhook" })[0]!);
    const drawer = screen.getByRole("dialog", { name: "New webhook" });
    fireEvent.change(within(drawer).getByLabelText("Name"), {
      target: { value: "Back gate" },
    });
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Create webhook" }),
    );
    expect(
      await screen.findByText("Secret for Back gate (shown once)"),
    ).toBeVisible();
    expect(screen.getByText(SECRET)).toBeVisible();
    expect(
      screen.getByText(/POST .*\/api\/v1\/door\/webhooks\/7\/trigger/),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/door/webhooks",
      body: {
        name: "Back gate",
        action: "ring_door",
        ringGroupId: null,
        pagingGroupId: null,
        announcementId: null,
        rateLimit: 11,
        enabled: true,
      },
    });

    // Dismissing the notice removes the secret from the page for good.
    fireEvent.click(screen.getByRole("button", { name: "I have copied it" }));
    await waitFor(() =>
      expect(document.body.textContent).not.toContain(SECRET),
    );
    expect(screen.getByText("Back gate")).toBeVisible();
  });

  it("shows the server's field error", async () => {
    setup({
      "POST /api/v1/door/webhooks": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration",
              fields: [
                { path: "ringGroupId", message: "must be a Door group" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/door");
    // New webhook opens once the list has loaded, or the load wipes the
    // webhook the page adds.
    await screen.findByText("Gate bell");
    fireEvent.click(screen.getAllByRole("button", { name: "New webhook" })[0]!);
    const drawer = screen.getByRole("dialog", { name: "New webhook" });
    fireEvent.change(within(drawer).getByLabelText("Name"), {
      target: { value: "Back gate" },
    });
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Create webhook" }),
    );
    expect(
      await within(drawer).findByText("must be a Door group"),
    ).toBeVisible();
  });

  it("replaces the secret after a confirmation and shows the new one once", async () => {
    const calls = setup({
      "POST /api/v1/door/webhooks/4/rotate-secret": () =>
        json({ ...HOOK, secret: SECRET }),
    });
    renderApp("/door");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Replace the secret of Gate bell",
      }),
    );
    expect(calls.filter((c) => c.url.endsWith("rotate-secret"))).toHaveLength(
      0,
    );
    fireEvent.click(screen.getByRole("button", { name: "Replace secret" }));
    expect(await screen.findByText(SECRET)).toBeVisible();
  });

  it("deletes a webhook after a confirmation", async () => {
    setup({ "DELETE /api/v1/door/webhooks/4": () => noContent() });
    renderApp("/door");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete Gate bell" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete webhook" }));
    await waitFor(() => expect(screen.queryByText("Gate bell")).toBeNull());
  });

  it("gives a viewer no write control", async () => {
    setup(
      {},
      { "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }) },
    );
    renderApp("/door");
    expect(await screen.findByText("Gate bell")).toBeVisible();
    for (const name of [
      /New webhook/,
      /^Edit/,
      /Replace the secret/,
      /^Delete/,
    ]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
  });

  it("says when the webhooks cannot be loaded", async () => {
    setup({
      "GET /api/v1/door/webhooks": () => apiError(500, "internal", "boom"),
    });
    renderApp("/door");
    expect(
      await screen.findByText("Could not load the webhooks"),
    ).toBeVisible();
  });
});
