import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-26T10:00:00Z";
const ext = (id: number, number: string, name: string, box: number | null) => ({
  id,
  number,
  name,
  voicemailBoxId: box,
  createdAt: CREATED,
  updatedAt: CREATED,
});
const EXTENSIONS = [
  ext(1, "100", "Reception", 11),
  ext(2, "200", "Kitchen", 12),
  ext(3, "300", "No box", null),
];

const GROUP = {
  id: 5,
  number: "700",
  name: "Sales",
  enabled: true,
  maxSeconds: 180,
  members: [{ extensionId: 2, number: "200", name: "Kitchen" }],
  createdAt: CREATED,
  updatedAt: CREATED,
};

const CODES = [{ code: "*63", action: "voicemail_broadcast", argument: "" }];

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    "GET /api/v1/feature-codes": () => json({ items: CODES }),
    "GET /api/v1/voicemail-groups": () => json({ items: [] }),
    ...extra,
  });
}

/** Opens the new-group drawer once the list has loaded. */
async function openNew() {
  const [create] = await screen.findAllByRole("button", {
    name: "New voicemail group",
  });
  await waitFor(() => expect(create).toBeEnabled());
  fireEvent.click(create!);
}

describe("Voicemail groups", () => {
  it("lists the groups with the broadcast code in the description", async () => {
    setup({ "GET /api/v1/voicemail-groups": () => json({ items: [GROUP] }) });
    renderApp("/voicemail/groups");
    expect(
      await screen.findByRole("listitem", { name: "Sales" }),
    ).toBeVisible();
    expect(screen.getByText("700")).toBeVisible();
    expect(screen.getByText(/messages up to 180 s/)).toBeVisible();
    await waitFor(() =>
      expect(
        screen.getByText(/dial \*63 and the group's number/),
      ).toBeVisible(),
    );
  });

  it("shows the empty state", async () => {
    setup();
    renderApp("/voicemail/groups");
    expect(await screen.findByText("No voicemail groups")).toBeVisible();
  });

  it("says why the list could not load", async () => {
    setup({
      "GET /api/v1/voicemail-groups": () =>
        apiError(500, "internal", "database unavailable"),
    });
    renderApp("/voicemail/groups");
    expect(await screen.findByText("database unavailable")).toBeVisible();
  });

  it("creates a group from the extensions that have a box", async () => {
    const calls = setup({
      "POST /api/v1/voicemail-groups": () => json(GROUP, 201),
    });
    renderApp("/voicemail/groups");
    await openNew();
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Sales" },
    });
    fireEvent.change(screen.getByLabelText("Number"), {
      target: { value: "700" },
    });
    const pick = await screen.findByLabelText("Add to members");
    await waitFor(() => expect(pick).toBeEnabled());
    // An extension without a box is not offered.
    expect(screen.queryByRole("option", { name: "300 · No box" })).toBeNull();
    fireEvent.change(pick, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(
      await screen.findByText("Voicemail group Sales created."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/voicemail-groups",
      body: {
        number: "700",
        name: "Sales",
        enabled: true,
        maxSeconds: 180,
        members: [2],
      },
    });
  });

  it("checks the fields before it calls the server", async () => {
    const calls = setup();
    renderApp("/voicemail/groups");
    await openNew();
    fireEvent.change(screen.getByLabelText("Number"), {
      target: { value: "7" },
    });
    fireEvent.change(screen.getByLabelText("Longest message (s)"), {
      target: { value: "601" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(await screen.findByText("Use 2 to 16 digits.")).toBeVisible();
    expect(screen.getByText("Use 10 to 600 seconds.")).toBeVisible();
    expect(screen.getByText("Use 1 to 64 characters.")).toBeVisible();
    expect(screen.getByText("Choose at least one member.")).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("shows the server's reason when a save is refused", async () => {
    setup({
      "POST /api/v1/voicemail-groups": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid voicemail group",
              fields: [
                { path: "number", message: "is already an extension number" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/voicemail/groups");
    await openNew();
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Sales" },
    });
    fireEvent.change(screen.getByLabelText("Number"), {
      target: { value: "100" },
    });
    const pick = await screen.findByLabelText("Add to members");
    await waitFor(() => expect(pick).toBeEnabled());
    fireEvent.change(pick, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(
      await screen.findByText("is already an extension number"),
    ).toBeVisible();
  });

  it("replaces and deletes a group", async () => {
    const calls = setup({
      "GET /api/v1/voicemail-groups": () => json({ items: [GROUP] }),
      "PUT /api/v1/voicemail-groups/5": () =>
        json({ ...GROUP, name: "Support", maxSeconds: 300 }),
      "DELETE /api/v1/voicemail-groups/5": () => noContent(),
    });
    renderApp("/voicemail/groups");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit voicemail group Sales" }),
    );
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Support" },
    });
    fireEvent.change(screen.getByLabelText("Longest message (s)"), {
      target: { value: "300" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save group" }));
    await screen.findByText("Voicemail group Support saved.");
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      number: "700",
      name: "Support",
      enabled: true,
      maxSeconds: 300,
      members: [2],
    });
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Delete voicemail group Support",
      }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete group" }),
    );
    await screen.findByText("Voicemail group Support deleted.");
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("keeps a group that a menu still uses", async () => {
    setup({
      "GET /api/v1/voicemail-groups": () => json({ items: [GROUP] }),
      "DELETE /api/v1/voicemail-groups/5": () =>
        apiError(409, "in_use", "voicemail group 700 is used by IVR menu main"),
    });
    renderApp("/voicemail/groups");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Delete voicemail group Sales",
      }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete group" }),
    );
    expect(
      await screen.findByText(/voicemail group 700 is used by IVR menu main/),
    ).toBeVisible();
    expect(screen.getByRole("listitem", { name: "Sales" })).toBeVisible();
  });

  it("is read only for a viewer", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }),
      "GET /api/v1/voicemail-groups": () => json({ items: [GROUP] }),
    });
    renderApp("/voicemail/groups");
    await screen.findByRole("listitem", { name: "Sales" });
    expect(
      screen.queryByRole("button", { name: "New voicemail group" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Delete voicemail group Sales" }),
    ).toBeNull();
  });

  it("is read only for an operator, since writes need an administrator", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "o", role: "operator" }),
      "GET /api/v1/voicemail-groups": () => json({ items: [GROUP] }),
    });
    renderApp("/voicemail/groups");
    await screen.findByRole("listitem", { name: "Sales" });
    expect(
      screen.queryByRole("button", { name: "Edit voicemail group Sales" }),
    ).toBeNull();
  });
});
