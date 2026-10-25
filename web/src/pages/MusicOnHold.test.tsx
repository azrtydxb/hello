import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const CREATED = "2026-10-25T10:00:00Z";

const ANNOUNCEMENTS = [
  { id: 1, name: "jazz", createdAt: CREATED },
  { id: 2, name: "strings", createdAt: CREATED },
];

const CLASS = {
  id: 7,
  name: "lobby",
  mode: "shuffle",
  isDefault: true,
  tracks: [
    { announcementId: 1, name: "jazz" },
    { announcementId: 2, name: "strings" },
  ],
  createdAt: CREATED,
  updatedAt: CREATED,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/announcements": () => json({ items: ANNOUNCEMENTS }),
    "GET /api/v1/moh-classes": () => json({ items: [] }),
    ...extra,
  });
}

describe("Music on hold", () => {
  it("creates a class from ordered announcements", async () => {
    const calls = setup({
      "POST /api/v1/moh-classes": () => json(CLASS, 201),
    });
    renderApp("/moh");
    const [create] = await screen.findAllByRole("button", {
      name: "New class",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "lobby" },
    });
    fireEvent.change(screen.getByLabelText("Play order"), {
      target: { value: "shuffle" },
    });
    fireEvent.click(screen.getByRole("switch", { name: /Default class/ }));
    const pick = await screen.findByLabelText("Add a track");
    await waitFor(() => expect(pick).toBeEnabled());
    for (const id of ["2", "1"]) {
      fireEvent.change(pick, { target: { value: id } });
      fireEvent.click(screen.getByRole("button", { name: "Add" }));
    }
    // Reorder: strings was added first; move jazz above it.
    fireEvent.click(screen.getByRole("button", { name: "Move jazz up" }));
    fireEvent.click(screen.getByRole("button", { name: "Create class" }));

    expect(await screen.findByText("Class lobby created.")).toBeVisible();
    expect(screen.getByRole("listitem", { name: "lobby" })).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/moh-classes",
      body: {
        name: "lobby",
        mode: "shuffle",
        isDefault: true,
        announcementIds: [1, 2],
      },
    });
  });

  it("refuses an empty name without calling the server", async () => {
    const calls = setup();
    renderApp("/moh");
    const [create] = await screen.findAllByRole("button", {
      name: "New class",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.click(screen.getByRole("button", { name: "Create class" }));
    expect(await screen.findByText("Use 1 to 64 characters.")).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("shows a server validation error at its field", async () => {
    setup({
      "POST /api/v1/moh-classes": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid class",
              fields: [{ path: "name", message: "already exists" }],
            },
          },
          400,
        ),
    });
    renderApp("/moh");
    const [create] = await screen.findAllByRole("button", {
      name: "New class",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "lobby" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create class" }));
    expect(await screen.findByText("already exists")).toBeVisible();
  });

  it("saves a changed class with a replace", async () => {
    const calls = setup({
      "GET /api/v1/moh-classes": () => json({ items: [CLASS] }),
      "PUT /api/v1/moh-classes/7": () =>
        json({ ...CLASS, mode: "loop", isDefault: false }),
    });
    renderApp("/moh");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit class lobby" }),
    );
    fireEvent.change(screen.getByLabelText("Play order"), {
      target: { value: "loop" },
    });
    fireEvent.click(screen.getByRole("switch", { name: /Default class/ }));
    fireEvent.click(screen.getByRole("button", { name: "Remove strings" }));
    fireEvent.click(screen.getByRole("button", { name: "Save class" }));
    expect(await screen.findByText("Class lobby saved.")).toBeVisible();
    expect(calls).toContainEqual({
      method: "PUT",
      url: "/api/v1/moh-classes/7",
      body: {
        name: "lobby",
        mode: "loop",
        isDefault: false,
        announcementIds: [1],
      },
    });
  });

  it("deletes a class after confirmation", async () => {
    const calls = setup({
      "GET /api/v1/moh-classes": () => json({ items: [CLASS] }),
      "DELETE /api/v1/moh-classes/7": () => new Response(null, { status: 204 }),
    });
    renderApp("/moh");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete class lobby" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete class" }),
    );
    await screen.findByText("Class lobby deleted.");
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("is read only for a viewer", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }),
      "GET /api/v1/moh-classes": () => json({ items: [CLASS] }),
    });
    renderApp("/moh");
    await screen.findByRole("listitem", { name: "lobby" });
    expect(screen.queryByRole("button", { name: "New class" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Delete class lobby" }),
    ).toBeNull();
  });
});

describe("Music on hold pickers", () => {
  const EXT = {
    id: 1,
    number: "100",
    name: "Reception",
    externalNumber: "",
    mohClassId: null,
    createdAt: CREATED,
    updatedAt: CREATED,
  };

  it("saves an extension's class and clears it with null", async () => {
    const calls = setup({
      "GET /api/v1/extensions": () => json({ items: [EXT] }),
      "GET /api/v1/devices": () => json({ items: [] }),
      "GET /api/v1/moh-classes": () => json({ items: [CLASS] }),
      "PATCH /api/v1/extensions/1": () => json({ ...EXT, mohClassId: 7 }),
    });
    renderApp("/extensions");
    fireEvent.click(
      await screen.findByRole("button", { name: "Open extension 100" }),
    );
    const pick = await screen.findByLabelText("Music on hold");
    await screen.findByRole("option", { name: "lobby (default)" });
    fireEvent.change(pick, { target: { value: "7" } });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PATCH",
        url: "/api/v1/extensions/1",
        body: { mohClassId: 7 },
      }),
    );
  });
});
