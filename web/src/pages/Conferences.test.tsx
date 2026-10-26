import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-25T10:00:00Z";

const ROOM = {
  id: 7,
  name: "Weekly",
  number: "800",
  hasUserPin: true,
  hasLeaderPin: false,
  maxParticipants: 10,
  waitForLeader: false,
  muteOnEntry: false,
  entryExit: "tone",
  entryAnnouncement: "",
  exitAnnouncement: "",
  record: false,
  enabled: true,
  createdAt: CREATED,
  updatedAt: CREATED,
  live: null,
};

const LIVE_ROOM = {
  ...ROOM,
  live: {
    room: "800",
    node: "hello-sip-2",
    locked: false,
    recording: true,
    updatedAt: CREATED,
    participants: [
      {
        id: "c1",
        number: "101",
        name: "Ann",
        leader: true,
        muted: false,
        mutedByLeader: false,
        waiting: false,
        joinedAt: CREATED,
      },
      {
        id: "c2",
        number: "102",
        name: "Bob",
        leader: false,
        muted: false,
        mutedByLeader: false,
        waiting: false,
        joinedAt: CREATED,
      },
    ],
  },
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/conferences": () => json({ items: [] }),
    ...extra,
  });
}

describe("Conferences", () => {
  it("creates a room with its PINs sent once and never shown", async () => {
    const calls = setup({
      "POST /api/v1/conferences": () => json(ROOM, 201),
    });
    renderApp("/conferences");
    const [create] = await screen.findAllByRole("button", {
      name: "New conference room",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Weekly" },
    });
    fireEvent.change(screen.getByLabelText("Room number"), {
      target: { value: "800" },
    });
    fireEvent.change(screen.getByLabelText("User PIN"), {
      target: { value: "1234" },
    });
    fireEvent.change(screen.getByLabelText("Leader PIN"), {
      target: { value: "987654" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create room" }));

    expect(
      await screen.findByText("Conference room Weekly created."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/conferences",
      body: {
        name: "Weekly",
        number: "800",
        userPin: "1234",
        leaderPin: "987654",
        maxParticipants: 10,
        waitForLeader: false,
        muteOnEntry: false,
        entryExit: "tone",
        entryAnnouncement: "",
        exitAnnouncement: "",
        record: false,
        enabled: true,
      },
    });
  });

  it("shows validation errors at their fields and sends nothing", async () => {
    const calls = setup();
    renderApp("/conferences");
    const [create] = await screen.findAllByRole("button", {
      name: "New conference room",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Room number"), {
      target: { value: "8" },
    });
    fireEvent.change(screen.getByLabelText("Seats"), {
      target: { value: "99" },
    });
    fireEvent.change(screen.getByLabelText("User PIN"), {
      target: { value: "12" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create room" }));

    expect(
      await screen.findByText("Use 2 to 10 digits, for example 800."),
    ).toBeVisible();
    expect(screen.getByText("Use a whole number from 2 to 50.")).toBeVisible();
    expect(screen.getByText("Use 4 to 10 digits.")).toBeVisible();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(0);
  });

  it("maps a server validation error onto its field", async () => {
    setup({
      "POST /api/v1/conferences": () =>
        json(
          {
            error: {
              code: "validation",
              message: "invalid",
              fields: [
                { path: "number", message: "is already an extension number" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/conferences");
    const [create] = await screen.findAllByRole("button", {
      name: "New conference room",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Clash" },
    });
    fireEvent.change(screen.getByLabelText("Room number"), {
      target: { value: "100" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create room" }));
    expect(
      await screen.findByText("is already an extension number"),
    ).toBeVisible();
  });

  it("keeps a stored PIN unless it is replaced or removed, and never shows it", async () => {
    const calls = setup({
      "GET /api/v1/conferences": () => json({ items: [ROOM] }),
      "PUT /api/v1/conferences/7": () => json({ ...ROOM, hasUserPin: false }),
    });
    renderApp("/conferences");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Edit conference room Weekly",
      }),
    );
    const pin = screen.getByLabelText("User PIN") as HTMLInputElement;
    expect(pin.value).toBe("");
    expect(pin.type).toBe("password");
    expect(screen.getByText(/A PIN is set/)).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Save room" }));
    await screen.findByText("Conference room Weekly saved.");
    const keep = calls.find((c) => c.method === "PUT");
    expect(keep?.body).not.toHaveProperty("userPin");

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Edit conference room Weekly",
      }),
    );
    fireEvent.click(screen.getByLabelText("Remove the user pin"));
    fireEvent.click(screen.getByRole("button", { name: "Save room" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PUT")).toHaveLength(2),
    );
    expect(calls.filter((c) => c.method === "PUT")[1]?.body).toMatchObject({
      userPin: "",
    });
  });

  it("lists who is in a room and mutes, kicks and locks", async () => {
    const calls = setup({
      "GET /api/v1/conferences": () => json({ items: [LIVE_ROOM] }),
      "POST /api/v1/conferences/7/control": () =>
        json({ queued: true, node: "hello-sip-2", commandId: "x" }, 202),
    });
    renderApp("/conferences");
    const people = await screen.findByRole("list", {
      name: "Participants in Weekly",
    });
    expect(within(people).getByText("Leader")).toBeVisible();
    expect(screen.getByText("Recording")).toBeVisible();

    fireEvent.click(
      screen.getByRole("button", { name: "Mute 102 Bob in Weekly" }),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Kick 102 Bob from Weekly" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Lock room Weekly" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.url.endsWith("/control"))).toHaveLength(3),
    );
    const bodies = calls
      .filter((c) => c.url.endsWith("/control"))
      .map((c) => c.body);
    expect(bodies).toEqual([
      { action: "mute", participant: "c2" },
      { action: "kick", participant: "c2" },
      { action: "lock" },
    ]);
  });

  it("says why a control command failed", async () => {
    setup({
      "GET /api/v1/conferences": () => json({ items: [LIVE_ROOM] }),
      "POST /api/v1/conferences/7/control": () =>
        apiError(409, "conflict", "that participant is no longer in the room"),
    });
    renderApp("/conferences");
    fireEvent.click(
      await screen.findByRole("button", { name: "Kick 102 Bob from Weekly" }),
    );
    expect(
      await screen.findByText(/that participant is no longer in the room/),
    ).toBeVisible();
  });

  it("deletes a room after confirmation", async () => {
    const calls = setup({
      "GET /api/v1/conferences": () => json({ items: [ROOM] }),
      "DELETE /api/v1/conferences/7": () => noContent(),
    });
    renderApp("/conferences");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Delete conference room Weekly",
      }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Delete room" }));
    expect(
      await screen.findByText("Conference room Weekly deleted."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/conferences/7",
      body: undefined,
    });
  });

  it("is read only for a viewer", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }),
      "GET /api/v1/conferences": () => json({ items: [LIVE_ROOM] }),
    });
    renderApp("/conferences");
    await screen.findByRole("list", { name: "Participants in Weekly" });
    expect(
      screen.queryByRole("button", { name: /New conference room/ }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Edit conference room/ }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: /Delete conference room/ }),
    ).toBeNull();
    expect(screen.queryByRole("button", { name: /Kick/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Mute/ })).toBeNull();
    expect(screen.queryByRole("button", { name: /Lock room/ })).toBeNull();
  });
});
