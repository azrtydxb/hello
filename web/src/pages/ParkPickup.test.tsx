import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

const CREATED = "2026-10-25T10:00:00Z";
const ext = (id: number, number: string, name: string) => ({
  id,
  number,
  name,
  createdAt: CREATED,
  updatedAt: CREATED,
});
const EXTENSIONS = [ext(1, "100", "Reception"), ext(2, "200", "Kitchen")];

const LOT = {
  id: 3,
  name: "default",
  slotStart: 701,
  slotEnd: 720,
  returnSecs: 120,
  voicemailSecs: 60,
  enabled: true,
  createdAt: CREATED,
  updatedAt: CREATED,
};

const GROUP = {
  id: 5,
  name: "front",
  members: [{ extensionId: 2, number: "200", name: "Kitchen" }],
  createdAt: CREATED,
  updatedAt: CREATED,
};

const CODES = [
  { code: "*74", action: "park", argument: "" },
  { code: "*75", action: "park_retrieve", argument: "" },
  { code: "*40", action: "pickup_group", argument: "" },
  { code: "*41", action: "pickup_directed", argument: "" },
];

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    "GET /api/v1/feature-codes": () => json({ items: CODES }),
    "GET /api/v1/park-lots": () => json({ items: [] }),
    "GET /api/v1/parked-calls": () => json({ items: [] }),
    "GET /api/v1/pickup-groups": () => json({ items: [] }),
    ...extra,
  });
}

describe("Park and pickup", () => {
  it("shows the configured codes and the calls parked now", async () => {
    setup({
      "GET /api/v1/parked-calls": () =>
        json({
          items: [
            {
              slot: "701",
              lot: "default",
              node: "sip-1",
              callId: "c1",
              correlationId: "x1",
              parkedBy: "100",
              caller: "200",
              callerName: "Kitchen",
              parkedAt: new Date().toISOString(),
              returnAt: new Date(Date.now() + 90_000).toISOString(),
              phase: "parked",
            },
            {
              slot: "702",
              lot: "default",
              node: "sip-1",
              callId: "c2",
              correlationId: "x2",
              parkedBy: "100",
              caller: "201",
              callerName: "",
              parkedAt: new Date().toISOString(),
              returnAt: new Date().toISOString(),
              phase: "returning",
            },
          ],
        }),
    });
    renderApp("/park");
    const list = await screen.findByRole("list", { name: "Parked calls" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("701");
    expect(rows[0]).toHaveTextContent("parked by 100");
    expect(rows[0]).toHaveTextContent(/Returns in \d+ s/);
    expect(rows[1]).toHaveTextContent("Ringing the parker");
    expect(
      await screen.findByText(/Park a call during a call with \*74/),
    ).toBeVisible();
    expect(screen.getByText(/or \*41 and their extension/)).toBeVisible();
  });

  it("says when nothing is parked and when the live view is down", async () => {
    setup({
      "GET /api/v1/parked-calls": () =>
        json(
          {
            error: { code: "unavailable", message: "live state is down" },
          },
          503,
        ),
    });
    renderApp("/park");
    expect(
      await screen.findByText("Could not load the parked calls"),
    ).toBeVisible();
  });

  it("creates a park lot with its range and timers", async () => {
    const calls = setup({
      "POST /api/v1/park-lots": () => json(LOT, 201),
    });
    renderApp("/park");
    expect(await screen.findByText("No call is parked.")).toBeVisible();
    const [create] = await screen.findAllByRole("button", {
      name: "New park lot",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "default" },
    });
    fireEvent.change(screen.getByLabelText("First slot"), {
      target: { value: "701" },
    });
    fireEvent.change(screen.getByLabelText("Last slot"), {
      target: { value: "720" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create lot" }));
    expect(await screen.findByText("Park lot default created.")).toBeVisible();
    expect(screen.getByRole("listitem", { name: "default" })).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/park-lots",
      body: {
        name: "default",
        slotStart: 701,
        slotEnd: 720,
        returnSecs: 120,
        voicemailSecs: 60,
        enabled: true,
      },
    });
  });

  it("refuses a bad range before calling the API, and shows a server overlap at its field", async () => {
    const calls = setup({
      "POST /api/v1/park-lots": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid park lot",
              fields: [
                {
                  path: "slotStart",
                  message: "slots 701-720 overlap extension 705",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/park");
    const [create] = await screen.findAllByRole("button", {
      name: "New park lot",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "clash" },
    });
    fireEvent.change(screen.getByLabelText("First slot"), {
      target: { value: "720" },
    });
    fireEvent.change(screen.getByLabelText("Last slot"), {
      target: { value: "701" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create lot" }));
    expect(
      await screen.findByText(/Use a range of at most 100 slots/),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.change(screen.getByLabelText("First slot"), {
      target: { value: "701" },
    });
    fireEvent.change(screen.getByLabelText("Last slot"), {
      target: { value: "720" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create lot" }));
    expect(
      await screen.findByText("slots 701-720 overlap extension 705"),
    ).toBeVisible();
  });

  it("replaces a lot with PUT and deletes it", async () => {
    const calls = setup({
      "GET /api/v1/park-lots": () => json({ items: [LOT] }),
      "PUT /api/v1/park-lots/3": () => json({ ...LOT, returnSecs: 90 }),
      "DELETE /api/v1/park-lots/3": () => noContent(),
    });
    renderApp("/park");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit park lot default" }),
    );
    fireEvent.change(screen.getByLabelText("Return to the parker after (s)"), {
      target: { value: "90" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save lot" }));
    await screen.findByText("Park lot default saved.");
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
      slotStart: 701,
      slotEnd: 720,
      returnSecs: 90,
    });
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete park lot default" }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Delete lot" }));
    await screen.findByText("Park lot default deleted.");
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("creates a pickup group from the chosen extensions", async () => {
    const calls = setup({
      "POST /api/v1/pickup-groups": () => json(GROUP, 201),
    });
    renderApp("/park");
    const [create] = await screen.findAllByRole("button", {
      name: "New pickup group",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "front" },
    });
    // No member yet: refused before anything is sent.
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(await screen.findByText("Choose at least one member.")).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    const pick = await screen.findByLabelText("Add to members");
    await waitFor(() => expect(pick).toBeEnabled());
    fireEvent.change(pick, { target: { value: "2" } });
    fireEvent.click(screen.getByRole("button", { name: "Add" }));
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(
      await screen.findByText("Pickup group front created."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/pickup-groups",
      body: { name: "front", members: [2] },
    });
  });

  it("replaces and deletes a pickup group", async () => {
    const calls = setup({
      "GET /api/v1/pickup-groups": () => json({ items: [GROUP] }),
      "PUT /api/v1/pickup-groups/5": () => json({ ...GROUP, name: "back" }),
      "DELETE /api/v1/pickup-groups/5": () => noContent(),
    });
    renderApp("/park");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit pickup group front" }),
    );
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "back" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save group" }));
    await screen.findByText("Pickup group back saved.");
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      name: "back",
      members: [2],
    });
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete pickup group back" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete group" }),
    );
    await screen.findByText("Pickup group back deleted.");
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("is read only for a viewer", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }),
      "GET /api/v1/park-lots": () => json({ items: [LOT] }),
      "GET /api/v1/pickup-groups": () => json({ items: [GROUP] }),
    });
    renderApp("/park");
    await screen.findByRole("listitem", { name: "default" });
    await screen.findByRole("listitem", { name: "front" });
    expect(screen.queryByRole("button", { name: "New park lot" })).toBeNull();
    expect(
      screen.queryByRole("button", { name: "New pickup group" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Delete park lot default" }),
    ).toBeNull();
  });
});
