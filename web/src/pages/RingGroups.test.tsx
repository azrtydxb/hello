import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-01T10:00:00Z";

const EXT100 = {
  id: 1,
  number: "100",
  name: "Reception",
  createdAt: CREATED,
  updatedAt: CREATED,
};
const EXT200 = {
  id: 2,
  number: "200",
  name: "Support",
  createdAt: CREATED,
  updatedAt: CREATED,
};

const GROUP = {
  id: 5,
  name: "Sales",
  strategy: "ring-all",
  hunt: false,
  ringTimeout: 30,
  memberDelay: 0,
  ignoreDnd: false,
  failureKind: "none",
  failureTarget: "",
  members: [
    { extensionId: 1, position: 1, weight: 1, delay: 0 },
    { extensionId: 2, position: 2, weight: 1, delay: 0 },
  ],
  createdAt: CREATED,
  updatedAt: CREATED,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: [EXT100, EXT200] }),
    "GET /api/v1/ring-groups": () => json({ items: [] }),
    ...extra,
  });
}

describe("RingGroups", () => {
  it("persists the chosen strategy and failure destination on create", async () => {
    const calls = setup({
      "POST /api/v1/ring-groups": () =>
        json(
          {
            ...GROUP,
            id: 6,
            name: "Support",
            strategy: "sequential",
            failureKind: "external",
            failureTarget: "+97142000100",
            members: [{ extensionId: 1, position: 1, weight: 1, delay: 0 }],
          },
          201,
        ),
    });
    renderApp("/ring-groups");
    const [create] = await screen.findAllByRole("button", {
      name: "New ring group",
    });
    // Enabled once the list the new group lands in has loaded.
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Support" },
    });
    fireEvent.change(screen.getByLabelText("Strategy"), {
      target: { value: "sequential" },
    });
    fireEvent.change(screen.getByLabelText("When no one answers"), {
      target: { value: "external" },
    });
    fireEvent.change(screen.getByLabelText("External number"), {
      target: { value: "+97142000100" },
    });
    // The member select stays disabled until the extensions request lands,
    // which can lose the race against the form on a loaded CI runner.
    const pick = await screen.findByLabelText("Add member");
    await waitFor(() => expect(pick).toBeEnabled());
    fireEvent.change(pick, { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: /^Add$/ }));
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));

    // The toast says the create landed; the list then holds the group.
    expect(
      await screen.findByText("Ring group Support created."),
    ).toBeVisible();
    expect(screen.getByRole("listitem", { name: "Support" })).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/ring-groups",
      body: {
        name: "Support",
        strategy: "sequential",
        hunt: false,
        ringTimeout: 30,
        memberDelay: 0,
        ignoreDnd: false,
        failureKind: "external",
        failureTarget: "+97142000100",
        members: [{ extensionId: 1, position: 1, weight: 1, delay: 0 }],
      },
    });
  });

  it("persists a changed strategy on edit", async () => {
    const calls = setup({
      "GET /api/v1/ring-groups": () => json({ items: [GROUP] }),
      "PATCH /api/v1/ring-groups/5": () =>
        json({ ...GROUP, strategy: "weighted" }),
    });
    renderApp("/ring-groups");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit ring group Sales" }),
    );

    fireEvent.change(screen.getByLabelText("Strategy"), {
      target: { value: "weighted" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save group" }));

    await screen.findByText("Ring group Sales saved.");
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.body).toMatchObject({ strategy: "weighted" });
  });

  it("sends member positions after a reorder", async () => {
    const calls = setup({
      "GET /api/v1/ring-groups": () => json({ items: [GROUP] }),
      "PATCH /api/v1/ring-groups/5": () => json(GROUP),
    });
    renderApp("/ring-groups");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit ring group Sales" }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Ring 100 later" }));
    fireEvent.click(screen.getByRole("button", { name: "Save group" }));

    await screen.findByText("Ring group Sales saved.");
    expect(calls.find((c) => c.method === "PATCH")?.body).toMatchObject({
      members: [
        { extensionId: 2, position: 1 },
        { extensionId: 1, position: 2 },
      ],
    });
  });

  it("shows server field errors on the matching fields", async () => {
    setup({
      "GET /api/v1/ring-groups": () => json({ items: [GROUP] }),
      "PATCH /api/v1/ring-groups/5": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid group",
              fields: [{ path: "name", message: "the name Sales is reserved" }],
            },
          },
          400,
        ),
    });
    renderApp("/ring-groups");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit ring group Sales" }),
    );

    fireEvent.click(screen.getByRole("button", { name: "Save group" }));
    const name = await screen.findByLabelText("Name");
    await waitFor(() => expect(name).toHaveAttribute("aria-invalid", "true"));
    expect(name).toHaveAccessibleDescription("the name Sales is reserved");
  });

  it("deletes a group after the in-page confirmation", async () => {
    const calls = setup({
      "GET /api/v1/ring-groups": () => json({ items: [GROUP] }),
      "DELETE /api/v1/ring-groups/5": noContent,
    });
    renderApp("/ring-groups");
    const card = await screen.findByRole("listitem", { name: "Sales" });

    fireEvent.click(
      within(card).getByRole("button", { name: "Delete ring group Sales" }),
    );
    const dialog = screen.getByRole("dialog", {
      name: "Delete ring group Sales?",
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete group" }),
    );
    await screen.findByText("No ring groups yet");
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/ring-groups/5",
      body: undefined,
    });
  });

  it("shows each group as a card: strategy, timing, members in order, failure", async () => {
    setup({
      "GET /api/v1/ring-groups": () =>
        json({
          items: [
            {
              ...GROUP,
              strategy: "sequential",
              hunt: true,
              ringTimeout: 60,
              failureKind: "voicemail",
              failureTarget: "100",
              members: [
                { extensionId: 2, position: 2, weight: 1, delay: 15 },
                { extensionId: 1, position: 1, weight: 1, delay: 0 },
                { extensionId: 9, position: 3, weight: 1, delay: 0 },
              ],
            },
          ],
        }),
    });
    renderApp("/ring-groups");

    const card = await screen.findByRole("listitem", { name: "Sales" });
    expect(within(card).getByText("Sequential")).toBeVisible();
    expect(
      within(card).getByText("Hunt · rings 60 s · respects DND"),
    ).toBeVisible();
    const members = within(card).getByRole("list", {
      name: "Members of Sales, in ring order",
    });
    await waitFor(() =>
      expect(
        within(members)
          .getAllByRole("listitem")
          .map((li) => li.textContent),
      ).toEqual(["1R100 Reception—", "2S200 Support15 s", "3?#9 —"]),
    );
    expect(card).toHaveTextContent("If nobody answers: Voicemail box 100");
  });
});
