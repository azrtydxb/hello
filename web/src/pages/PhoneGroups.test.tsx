import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, noContent, renderApp, type Routes } from "../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const book = (id: number, kind: string, name: string) => ({
  id,
  kind,
  name,
  version: 1,
  entryCount: 0,
  createdAt: STAMP,
  updatedAt: STAMP,
});
const BOOKS = [
  book(1, "company", "Company"),
  book(2, "extensions", "Extensions"),
  book(3, "named", "Warehouse"),
  book(4, "named", "Ops"),
  book(5, "named", "Sales"),
];

const group = (over: Record<string, unknown> = {}) => ({
  id: 9,
  name: "Site A",
  description: "Head office",
  includeExtensions: true,
  includeCompany: true,
  bookIds: [3, 4],
  phoneCount: 1,
  createdAt: STAMP,
  updatedAt: STAMP,
  ...over,
});

const PHONE = {
  id: 5,
  mac: "805ec0123456",
  serial: "",
  vendor: "poly",
  model: "VVX 450",
  label: "Front desk",
  deviceId: 3,
  extensionId: 7,
  extensionNumber: "101",
  templateId: null,
  groupId: 9,
  blf: [],
  enabled: true,
  tokenExposed: false,
  uaMismatch: false,
  bootArmed: false,
  bootReclaimed: false,
  redirectStatus: { state: "manual" },
  firstFetchAt: null,
  lastFetchAt: null,
  lastFetchIp: null,
  lastFetchUa: null,
  lastFetchFile: null,
  firmwareSeen: null,
  renderError: false,
  createdAt: STAMP,
  updatedAt: STAMP,
};

const catalogChannel = (
  id: number,
  kind: string,
  label: string,
  address: string,
  port: number,
) => ({
  id,
  label,
  address,
  port,
  codec: "PCMU",
  kind,
  priority: id,
  enabled: true,
});

const CATALOG = [
  catalogChannel(1, "paging", "Dock", "239.255.0.1", 5004),
  catalogChannel(2, "ptt", "PTT", "224.0.1.116", 5001),
  catalogChannel(3, "ptt", "Floor", "224.0.1.117", 5001),
];

const OPTIONS = {
  allowInDnd: false,
  barge: 0,
  priorityActive: true,
  timeout: 0,
  intercomBarge: false,
};

function api(role = "admin", more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    "GET /api/v1/phone-groups?limit=200": () =>
      json({ items: [group()], next: "" }),
    "GET /api/v1/phonebooks?limit=200": () => json({ items: BOOKS, next: "" }),
    "GET /api/v1/phones": () => json({ items: [PHONE] }),
    "GET /api/v1/multicast-channels": () => json({ items: [] }),
    ...more,
  });
}

async function groupsTable() {
  return screen.findByRole("table", { name: "Phone groups" });
}

describe("Phone groups", () => {
  it("lists each group with the books it delivers and its phone count", async () => {
    api();
    renderApp("/phones/groups");
    const table = await groupsTable();
    const row = within(table).getByText("Site A").closest("tr")!;
    expect(within(row).getByText("Head office")).toBeInTheDocument();
    expect(within(row).getByText("Extensions")).toBeInTheDocument();
    expect(within(row).getByText("Company")).toBeInTheDocument();
    expect(within(row).getByText("Warehouse")).toBeInTheDocument();
    expect(within(row).getByText("Ops")).toBeInTheDocument();
    expect(within(row).getByText("1")).toBeInTheDocument();
    // The Phones section's tabs link to it.
    expect(screen.getByRole("link", { name: "Groups" })).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("shows an empty state that explains the default", async () => {
    api("admin", {
      "GET /api/v1/phone-groups?limit=200": () => json({ items: [], next: "" }),
    });
    renderApp("/phones/groups");
    expect(await screen.findByText("No phone groups yet")).toBeInTheDocument();
  });

  it("reorders the group's books and saves their order", async () => {
    const calls = api("admin", {
      "PUT /api/v1/phone-groups/9": () => json(group()),
      "PUT /api/v1/phone-groups/9/books": () =>
        json(group({ bookIds: [4, 3, 5] })),
    });
    renderApp("/phones/groups");
    await groupsTable();
    fireEvent.click(screen.getByRole("button", { name: "Edit Site A" }));
    const dialog = screen.getByRole("dialog", { name: "Edit Site A" });
    const list = within(dialog).getByRole("list", {
      name: "Named books, in delivery order",
    });
    expect(
      within(list)
        .getAllByRole("listitem")
        .map((li) => li.textContent?.replace(/\s+/g, " ").trim()),
    ).toEqual(["Warehouse", "Ops"]);
    expect(
      within(dialog).getByRole("button", { name: "Move Warehouse up" }),
    ).toBeDisabled();

    fireEvent.click(
      within(dialog).getByRole("button", { name: "Move Ops up" }),
    );
    expect(
      within(list)
        .getAllByRole("listitem")
        .map((li) => li.textContent?.replace(/\s+/g, " ").trim()),
    ).toEqual(["Ops", "Warehouse"]);

    // Only named books not yet assigned can be added.
    const add = within(dialog).getByLabelText("Add a book");
    expect(
      within(add)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["Choose…", "Sales"]);
    fireEvent.change(add, { target: { value: "5" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add book" }));

    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Edit Site A" })).toBeNull(),
    );
    const put = calls.find((c) => c.url === "/api/v1/phone-groups/9/books");
    expect(put!.body).toEqual({ bookIds: [4, 3, 5] });
    expect(calls.find((c) => c.url === "/api/v1/phone-groups/9")!.body).toEqual(
      {
        name: "Site A",
        description: "Head office",
        includeExtensions: true,
        includeCompany: true,
      },
    );
  });

  it("saves catalog multicast channels and a PTT channel", async () => {
    const PTT = {
      enabled: true,
      channelId: 2,
      address: "224.0.1.117:5001",
      codec: "PCMU",
      defaultChannel: 1,
      acceptWhileBusy: false,
      channels: [
        { channel: 1, label: "Channel 1", transmit: true, subscribe: true },
      ],
    };
    const calls = api("admin", {
      "GET /api/v1/multicast-channels": () => json({ items: CATALOG }),
      "PUT /api/v1/phone-groups/9": () => json(group()),
      "PUT /api/v1/phone-groups/9/multicast": () =>
        json(group({ multicast: [{ ...CATALOG[0], role: "both" }] })),
      "PUT /api/v1/phone-groups/9/ptt": () =>
        json(group({ multicast: [{ ...CATALOG[0], role: "both" }], ptt: PTT })),
    });
    renderApp("/phones/groups");
    await groupsTable();
    fireEvent.click(screen.getByRole("button", { name: "Edit Site A" }));
    const dialog = screen.getByRole("dialog", { name: "Edit Site A" });

    const add = await within(dialog).findByRole("button", {
      name: "Add channel",
    });
    fireEvent.click(add);
    const pick = within(dialog).getByRole("combobox", { name: "Channel 1" });
    expect(
      Array.from(pick.querySelectorAll("option")).map((o) => o.textContent),
    ).toEqual(["Dock (239.255.0.1:5004, PCMU)"]);
    // PTT is on by default; pick the catalog's second PTT channel.
    expect(
      within(dialog).getByRole("switch", { name: /^Push-to-talk/ }),
    ).toBeChecked();
    expect(within(dialog).getByTestId("ptt-effective")).toHaveTextContent(
      "224.0.1.116:5001",
    );
    fireEvent.change(within(dialog).getByLabelText("PTT channel"), {
      target: { value: "3" },
    });
    expect(within(dialog).getByTestId("ptt-effective")).toHaveTextContent(
      "224.0.1.117:5001",
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls).toContainEqual({
      method: "PUT",
      url: "/api/v1/phone-groups/9/multicast",
      body: { channels: [{ channelId: 1, role: "both" }], options: OPTIONS },
    });
    expect(calls).toContainEqual({
      method: "PUT",
      url: "/api/v1/phone-groups/9/ptt",
      body: {
        enabled: true,
        channelId: 3,
        defaultChannel: 1,
        priorityChannel: 24,
        emergencyChannel: 25,
        acceptWhileBusy: false,
        groupPaging: false,
        defaultGroup: 26,
        priorityGroup: 49,
        emergencyGroup: 50,
        channels: [
          { channel: 1, label: "Channel 1", transmit: true, subscribe: true },
        ],
      },
    });
  });

  it("saves group paging channels and the paging options", async () => {
    const calls = api("admin", {
      "GET /api/v1/multicast-channels": () => json({ items: CATALOG }),
      "PUT /api/v1/phone-groups/9": () => json(group()),
      "PUT /api/v1/phone-groups/9/multicast": () => json(group()),
      "PUT /api/v1/phone-groups/9/ptt": () => json(group()),
    });
    renderApp("/phones/groups");
    await groupsTable();
    fireEvent.click(screen.getByRole("button", { name: "Edit Site A" }));
    const dialog = screen.getByRole("dialog", { name: "Edit Site A" });

    // Intercom barging is off by default.
    const barging = await within(dialog).findByRole("switch", {
      name: /^Intercom barging/,
    });
    expect(barging).not.toBeChecked();
    fireEvent.click(barging);
    fireEvent.click(
      within(dialog).getByRole("switch", {
        name: /^Play pages in Do Not Disturb/,
      }),
    );
    fireEvent.change(within(dialog).getByLabelText("Paging barge"), {
      target: { value: "3" },
    });
    fireEvent.change(within(dialog).getByLabelText(/^Paging timeout/), {
      target: { value: "60" },
    });
    expect(within(dialog).queryByLabelText(/^Default group/)).toBeNull();
    fireEvent.click(
      within(dialog).getByRole("switch", { name: /^Group paging/ }),
    );
    fireEvent.change(within(dialog).getByLabelText(/^Priority channel/), {
      target: { value: "20" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Add group channel" }),
    );
    expect(
      within(dialog).getByRole("group", { name: "Group channel 26" }),
    ).toBeVisible();
    fireEvent.change(within(dialog).getByLabelText("Channel name 2"), {
      target: { value: "Floor" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    expect(calls).toContainEqual({
      method: "PUT",
      url: "/api/v1/phone-groups/9/multicast",
      body: {
        channels: [],
        options: {
          allowInDnd: true,
          barge: 3,
          priorityActive: true,
          timeout: 60,
          intercomBarge: true,
        },
      },
    });
    expect(calls).toContainEqual({
      method: "PUT",
      url: "/api/v1/phone-groups/9/ptt",
      body: {
        enabled: true,
        channelId: null,
        defaultChannel: 1,
        priorityChannel: 20,
        emergencyChannel: 25,
        acceptWhileBusy: false,
        groupPaging: true,
        defaultGroup: 26,
        priorityGroup: 49,
        emergencyGroup: 50,
        channels: [
          { channel: 1, label: "Channel 1", transmit: true, subscribe: true },
          { channel: 26, label: "Floor", transmit: true, subscribe: true },
        ],
      },
    });
  });

  it("shows a multicast validation error at its field", async () => {
    api("admin", {
      "GET /api/v1/multicast-channels": () => json({ items: CATALOG }),
      "PUT /api/v1/phone-groups/9": () => json(group()),
      "PUT /api/v1/phone-groups/9/multicast": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid",
              fields: [
                {
                  path: "channels[0].channelId",
                  message: "is a push-to-talk channel",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/phones/groups");
    await groupsTable();
    fireEvent.click(screen.getByRole("button", { name: "Edit Site A" }));
    const dialog = screen.getByRole("dialog", { name: "Edit Site A" });
    fireEvent.click(
      await within(dialog).findByRole("button", { name: "Add channel" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    expect(
      await within(dialog).findByText("is a push-to-talk channel"),
    ).toBeVisible();
    expect(screen.getByRole("dialog", { name: "Edit Site A" })).toBeVisible();
  });

  it("creates a group with the extensions book switched off", async () => {
    const calls = api("admin", {
      "POST /api/v1/phone-groups": () =>
        json(
          group({
            id: 10,
            name: "Warehouse site",
            bookIds: [],
            includeExtensions: false,
          }),
          201,
        ),
    });
    renderApp("/phones/groups");
    await groupsTable();
    fireEvent.click(screen.getByRole("button", { name: "New group" }));
    const dialog = screen.getByRole("dialog", { name: "New phone group" });
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "Warehouse site" },
    });
    fireEvent.click(
      within(dialog).getByRole("switch", { name: /Extensions book/ }),
    );
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create group" }),
    );
    expect(await screen.findByText("Warehouse site")).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      name: "Warehouse site",
      description: "",
      includeExtensions: false,
      includeCompany: true,
    });
    // No books chosen: the order is not written.
    expect(calls.some((c) => c.url.endsWith("/books"))).toBe(false);
  });

  it("shows a name conflict at its field", async () => {
    api("admin", {
      "POST /api/v1/phone-groups": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid group",
              fields: [
                { path: "name", message: "a group with this name exists" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/phones/groups");
    await groupsTable();
    fireEvent.click(screen.getByRole("button", { name: "New group" }));
    const dialog = screen.getByRole("dialog", { name: "New phone group" });
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "site a" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create group" }),
    );
    expect(
      await within(dialog).findByText("a group with this name exists"),
    ).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Name")).toBeInvalid();
  });

  it("lists the member phones in the group's dialog", async () => {
    api();
    renderApp("/phones/groups");
    await groupsTable();
    fireEvent.click(screen.getByRole("button", { name: "Edit Site A" }));
    const dialog = screen.getByRole("dialog", { name: "Edit Site A" });
    const members = within(dialog).getByRole("list", { name: "Member phones" });
    expect(
      within(members).getByRole("link", { name: /80:5e:c0:12:34:56/i }),
    ).toHaveAttribute("href", "/phones/5");
  });

  it("deletes a group after confirmation", async () => {
    const calls = api("admin", {
      "DELETE /api/v1/phone-groups/9": () => noContent(),
    });
    renderApp("/phones/groups");
    await groupsTable();
    fireEvent.click(screen.getByRole("button", { name: "Delete Site A" }));
    const confirm = await screen.findByRole("dialog", {
      name: "Delete phone group Site A?",
    });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete group" }),
    );
    await waitFor(() => expect(screen.queryByText("Site A")).toBeNull());
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("lets an operator look but not create, save or delete", async () => {
    api("operator");
    renderApp("/phones/groups");
    await groupsTable();
    expect(screen.queryByRole("button", { name: "New group" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete Site A" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Edit Site A" }));
    const dialog = screen.getByRole("dialog", { name: "Edit Site A" });
    expect(within(dialog).queryByRole("button", { name: "Save" })).toBeNull();
  });
});

describe("Phone group resync and reboot", () => {
  it("resyncs the group and reboots it only after confirmation", async () => {
    const calls = api("operator", {
      "POST /api/v1/phone-groups/9/resync": () =>
        json({ queued: 1, skipped: 0 }, 202),
      "POST /api/v1/phone-groups/9/reboot": () =>
        json({ queued: 1, skipped: 1 }, 202),
    });
    renderApp("/phones/groups");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Resync every phone in Site A",
      }),
    );
    expect(
      await screen.findByText(/Site A: 1 phone queued to resync/),
    ).toBeInTheDocument();

    fireEvent.click(
      screen.getByRole("button", { name: "Reboot every phone in Site A" }),
    );
    expect(calls.some((c) => c.url.endsWith("/reboot"))).toBe(false);
    const confirm = screen.getByRole("dialog", {
      name: "Reboot every phone in Site A?",
    });
    fireEvent.click(within(confirm).getByRole("button", { name: "Reboot" }));
    expect(
      await screen.findByText(/1 phone queued to reboot; 1 skipped/),
    ).toBeInTheDocument();
  });

  it("hides the push buttons from a viewer", async () => {
    api("viewer");
    renderApp("/phones/groups");
    await screen.findByText("Site A");
    expect(
      screen.queryByRole("button", { name: "Resync every phone in Site A" }),
    ).toBeNull();
  });
});

describe("Phone group on the phone form", () => {
  it("offers the groups and saves the chosen one", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/me": () => json({ username: "u", role: "admin" }),
      "GET /api/v1/phones": () =>
        json({ items: [{ ...PHONE, groupId: null }] }),
      "GET /api/v1/extensions": () =>
        json({
          items: [
            {
              id: 7,
              number: "101",
              name: "Front desk",
              externalNumber: "",
              createdAt: STAMP,
              updatedAt: STAMP,
            },
          ],
        }),
      "GET /api/v1/devices": () => json({ items: [] }),
      "GET /api/v1/prov/templates": () => json({ items: [] }),
      "GET /api/v1/phone-groups?limit=200": () =>
        json({ items: [group()], next: "" }),
      "PATCH /api/v1/phones/5": () => json({ ...PHONE, groupId: 9 }),
    });
    renderApp("/phones");
    fireEvent.click(await screen.findByRole("button", { name: /^Edit / }));
    const dialog = await screen.findByRole("dialog", { name: /^Edit / });
    const select = within(dialog).getByLabelText("Phone group");
    expect(
      within(select)
        .getAllByRole("option")
        .map((o) => o.textContent),
    ).toEqual(["No group", "Site A"]);
    fireEvent.change(select, { target: { value: "9" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({
      groupId: 9,
    });
  });
});
