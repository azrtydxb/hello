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

const GROUP = {
  id: 7,
  name: "kitchen",
  code: "*60",
  priority: "normal",
  enabled: true,
  members: [{ extensionId: 2, number: "200", name: "Kitchen" }],
  callers: [],
  createdAt: CREATED,
  updatedAt: CREATED,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    "GET /api/v1/paging-groups": () => json({ items: [] }),
    "GET /api/v1/multicast-channels": () => json({ items: [] }),
    ...extra,
  });
}

describe("Paging", () => {
  it("creates a group with its code, priority and chosen members", async () => {
    const calls = setup({
      "POST /api/v1/paging-groups": () => json(GROUP, 201),
    });
    renderApp("/paging");
    const [create] = await screen.findAllByRole("button", {
      name: "New paging group",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "kitchen" },
    });
    fireEvent.change(screen.getByLabelText("Page code"), {
      target: { value: "*60" },
    });
    const pick = await screen.findByLabelText("Add to members");
    await waitFor(() => expect(pick).toBeEnabled());
    fireEvent.change(pick, { target: { value: "2" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Add" })[0]!);
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));

    expect(
      await screen.findByText("Paging group kitchen created."),
    ).toBeVisible();
    expect(screen.getByRole("listitem", { name: "kitchen" })).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/paging-groups",
      body: {
        name: "kitchen",
        code: "*60",
        priority: "normal",
        enabled: true,
        members: [2],
        callers: [],
      },
    });
  });

  it("tests a group by paging one member and shows the headers sent", async () => {
    const calls = setup({
      "GET /api/v1/paging-groups": () => json({ items: [GROUP] }),
      "POST /api/v1/paging-groups/7/test": () =>
        json({
          id: "t1",
          extension: "200",
          result: "answered",
          node: "hello-sip-1",
          contacts: [
            {
              device: "kitchen-1",
              userAgent: "Yealink SIP-T54W",
              result: "answered",
              headers: ["Alert-Info: Ring Answer"],
            },
          ],
        }),
    });
    renderApp("/paging");
    fireEvent.click(
      await screen.findByRole("button", { name: "Test paging group kitchen" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Page now" }));

    expect(
      await screen.findByText("Answered and played the tone"),
    ).toBeVisible();
    expect(screen.getByText(/Alert-Info: Ring Answer/)).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/paging-groups/7/test",
      body: { extension: "200" },
    });
  });

  it("shows why a test could not run", async () => {
    setup({
      "GET /api/v1/paging-groups": () => json({ items: [GROUP] }),
      "POST /api/v1/paging-groups/7/test": () =>
        json(
          { error: { code: "timeout", message: "no hello-sip node reported" } },
          504,
        ),
    });
    renderApp("/paging");
    fireEvent.click(
      await screen.findByRole("button", { name: "Test paging group kitchen" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Page now" }));
    expect(
      await screen.findByText(/Could not run the test: .*no hello-sip node/),
    ).toBeVisible();
  });

  it("creates a multicast-only group from a catalog paging channel", async () => {
    const channel = (
      id: number,
      kind: string,
      codec: string,
      label: string,
      enabled = true,
    ) => ({
      id,
      label,
      address: `239.255.0.${id}`,
      port: 5004,
      codec,
      kind,
      priority: 1,
      enabled,
    });
    const calls = setup({
      "GET /api/v1/multicast-channels": () =>
        json({
          items: [
            channel(1, "paging", "PCMU", "Dock"),
            channel(2, "paging", "PCMU", "Off", false),
            channel(3, "paging", "G722", "Wideband"),
            channel(4, "ptt", "PCMU", "Talk"),
          ],
        }),
      "POST /api/v1/paging-groups": () =>
        json(
          {
            ...GROUP,
            members: [],
            multicastChannelId: 1,
            multicastChannel: {
              id: 1,
              label: "Dock",
              address: "239.255.0.1",
              port: 5004,
              codec: "PCMU",
              enabled: true,
            },
          },
          201,
        ),
    });
    renderApp("/paging");
    const [create] = await screen.findAllByRole("button", {
      name: "New paging group",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "kitchen" },
    });
    fireEvent.change(screen.getByLabelText("Page code"), {
      target: { value: "*60" },
    });
    // No member and no channel: refused before anything is sent.
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(
      await screen.findByText(/Choose at least one member, or a multicast/),
    ).toBeVisible();

    const pick = screen.getByLabelText(/Also page a multicast channel/);
    const labels = Array.from(pick.querySelectorAll("option")).map(
      (o) => o.textContent,
    );
    expect(labels).toEqual(["None", "Dock (239.255.0.1:5004, PCMU)"]);
    fireEvent.change(pick, { target: { value: "1" } });
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(
      await screen.findByText("Paging group kitchen created."),
    ).toBeVisible();
    expect(screen.getByText(/Also multicast to Dock/)).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/paging-groups",
      body: {
        name: "kitchen",
        code: "*60",
        priority: "normal",
        enabled: true,
        members: [],
        callers: [],
        multicastChannelId: 1,
      },
    });
  });

  it("manages the multicast channel catalog", async () => {
    const DOCK = {
      id: 1,
      label: "Dock",
      address: "239.255.0.1",
      port: 5004,
      codec: "PCMU",
      kind: "paging",
      priority: 1,
      enabled: true,
    };
    const calls = setup({
      "GET /api/v1/multicast-channels": () => json({ items: [DOCK] }),
      "POST /api/v1/multicast-channels": [
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid multicast channel",
              fields: [{ path: "address", message: "already used" }],
            },
          },
          400,
        ),
        () =>
          json(
            {
              ...DOCK,
              id: 2,
              label: "Talk",
              address: "224.0.1.117",
              port: 5001,
              kind: "ptt",
            },
            201,
          ),
      ],
      "DELETE /api/v1/multicast-channels/1": () => noContent(),
    });
    renderApp("/paging");
    const table = await screen.findByRole("table", {
      name: "Multicast channels",
    });
    expect(await within(table).findByText("239.255.0.1:5004")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "New channel" }));
    const dialog = screen.getByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "Talk" },
    });
    fireEvent.change(within(dialog).getByLabelText("Kind"), {
      target: { value: "ptt" },
    });
    fireEvent.change(within(dialog).getByLabelText(/Multicast address/), {
      target: { value: "224.0.1.117" },
    });
    fireEvent.change(within(dialog).getByLabelText(/Port/), {
      target: { value: "5001" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create channel" }),
    );
    expect(await within(dialog).findByText("already used")).toBeVisible();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create channel" }),
    );
    expect(
      await screen.findByText("Multicast channel Talk created."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/multicast-channels",
      body: {
        label: "Talk",
        address: "224.0.1.117",
        port: 5001,
        codec: "PCMU",
        priority: 1,
        kind: "ptt",
        enabled: true,
      },
    });
    expect(within(table).getByText("Push-to-talk")).toBeVisible();

    fireEvent.click(screen.getByRole("button", { name: "Delete Dock" }));
    fireEvent.click(screen.getByRole("button", { name: "Delete channel" }));
    expect(
      await screen.findByText("Multicast channel Dock deleted."),
    ).toBeVisible();
    expect(within(table).queryByText("239.255.0.1:5004")).toBeNull();
  });

  it("shows a server validation error at its field", async () => {
    setup({
      "POST /api/v1/paging-groups": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid paging group",
              fields: [
                { path: "code", message: "collides with feature code *80" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/paging");
    const [create] = await screen.findAllByRole("button", {
      name: "New paging group",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "clash" },
    });
    fireEvent.change(screen.getByLabelText("Page code"), {
      target: { value: "*80" },
    });
    const pick = await screen.findByLabelText("Add to members");
    await waitFor(() => expect(pick).toBeEnabled());
    fireEvent.change(pick, { target: { value: "1" } });
    fireEvent.click(screen.getAllByRole("button", { name: "Add" })[0]!);
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));

    expect(
      await screen.findByText("collides with feature code *80"),
    ).toBeVisible();
  });

  it("refuses an invalid code before calling the API", async () => {
    const calls = setup();
    renderApp("/paging");
    const [create] = await screen.findAllByRole("button", {
      name: "New paging group",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "kitchen" },
    });
    fireEvent.change(screen.getByLabelText("Page code"), {
      target: { value: "60" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create group" }));
    expect(
      await screen.findByText(
        "Use * followed by 2 to 4 digits, for example *60.",
      ),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("replaces a group with PUT on edit and deletes it", async () => {
    const calls = setup({
      "GET /api/v1/paging-groups": () => json({ items: [GROUP] }),
      "PUT /api/v1/paging-groups/7": () =>
        json({ ...GROUP, priority: "emergency" }),
      "DELETE /api/v1/paging-groups/7": () => noContent(),
    });
    renderApp("/paging");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit paging group kitchen" }),
    );
    fireEvent.change(screen.getByLabelText("Priority"), {
      target: { value: "emergency" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save group" }));
    await screen.findByText("Paging group kitchen saved.");
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toMatchObject({ priority: "emergency", members: [2] });

    fireEvent.click(
      await screen.findByRole("button", {
        name: "Delete paging group kitchen",
      }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete group" }),
    );
    await screen.findByText("Paging group kitchen deleted.");
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("is read only for a viewer", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "v", role: "viewer" }),
      "GET /api/v1/paging-groups": () => json({ items: [GROUP] }),
    });
    renderApp("/paging");
    await screen.findByRole("listitem", { name: "kitchen" });
    expect(
      screen.queryByRole("button", { name: "New paging group" }),
    ).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Delete paging group kitchen" }),
    ).toBeNull();
  });
});
