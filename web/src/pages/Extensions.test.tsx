import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  ME,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const EXT = {
  id: 1,
  number: "100",
  name: "Reception",
  externalNumber: "",
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const DEVICE = {
  id: 3,
  extensionId: 1,
  sipUsername: "reception-1",
  enabled: true,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const BINDING = {
  aor: "sip:reception-1@pbx.test",
  extension: "100",
  device: "reception-1",
  contactUri: "sip:reception-1@192.0.2.10:5060",
  source: "192.0.2.10:5060",
  transport: "udp",
  userAgent: "Yealink T54W",
  receivedNode: "hello-sip-1",
  expires: "2099-10-01T11:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

/** The extension list API; live state is left unmocked (404) unless given. */
function api(extensions: unknown[], more: Routes = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: extensions }),
    "GET /api/v1/devices": () => json({ items: [] }),
    ...more,
  });
}

async function openNew() {
  fireEvent.click(
    (await screen.findAllByRole("button", { name: "New extension" }))[0]!,
  );
  return screen.getByRole("dialog", { name: "New extension" });
}

function fill(dialog: HTMLElement, number: string, name: string) {
  fireEvent.change(within(dialog).getByLabelText("Number"), {
    target: { value: number },
  });
  fireEvent.change(within(dialog).getByLabelText("Name"), {
    target: { value: name },
  });
  fireEvent.click(
    within(dialog).getByRole("button", { name: "Create extension" }),
  );
}

async function openDrawer(number = "100") {
  fireEvent.click(
    await screen.findByRole("button", { name: `Open extension ${number}` }),
  );
  return screen.getByRole("dialog", { name: `Extension ${number}` });
}

describe("Extensions", () => {
  it("shows the empty state with the page header", async () => {
    api([]);
    renderApp("/extensions");
    expect(
      await screen.findByRole("heading", { name: "No extensions yet" }),
    ).toBeVisible();
    expect(
      screen.getByRole("heading", { level: 1, name: "Extensions" }),
    ).toBeVisible();
    expect(
      screen.getByText("Dialable numbers, their devices and call features."),
    ).toBeVisible();
  });

  it("shows an error when the list cannot load", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/extensions": () => apiError(500, "internal", "db down"),
      "GET /api/v1/devices": () => json({ items: [] }),
    });
    renderApp("/extensions");
    expect(await screen.findByRole("alert")).toHaveTextContent(
      /Could not load extensions.*db down/,
    );
  });

  it.each(["1", "12345678901", "12a", " 101", ""])(
    "rejects number %j before sending",
    async (number) => {
      const calls = api([]);
      renderApp("/extensions");
      const dialog = await openNew();

      fill(dialog, number, "Desk");

      const input = within(dialog).getByLabelText("Number");
      expect(input).toHaveAttribute("aria-invalid", "true");
      expect(input).toHaveAccessibleDescription(
        "The number must be 2 to 10 digits (0–9 only).",
      );
      expect(calls.some((c) => c.method === "POST")).toBe(false);
    },
  );

  it("creates an extension in the modal, lists it and confirms with a toast", async () => {
    const calls = api([], {
      "POST /api/v1/extensions": () =>
        json(
          {
            ...EXT,
            id: 2,
            number: "0123456789",
            name: "Desk",
            externalNumber: "+97142000102",
          },
          201,
        ),
    });
    renderApp("/extensions");
    const dialog = await openNew();
    expect(within(dialog).getByLabelText("Number")).toHaveFocus();

    fireEvent.change(within(dialog).getByLabelText("External number"), {
      target: { value: "+97142000102" },
    });
    fill(dialog, "0123456789", "Desk");

    expect(
      await screen.findByRole("row", { name: /0123456789/ }),
    ).toHaveTextContent("Desk");
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByText("Extension 0123456789 created.")).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/extensions",
      body: {
        number: "0123456789",
        name: "Desk",
        externalNumber: "+97142000102",
      },
    });
  });

  it("validates the external number and shows server errors in the modal", async () => {
    const calls = api([EXT], {
      "POST /api/v1/extensions": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid extension",
              fields: [
                {
                  path: "externalNumber",
                  message: "+97142000100 is already used by 100",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/extensions");
    const dialog = await openNew();
    const external = within(dialog).getByLabelText("External number");

    fireEvent.change(external, { target: { value: "12-34" } });
    fill(dialog, "102", "Desk");
    expect(external).toHaveAttribute("aria-invalid", "true");
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.change(external, { target: { value: "+97142000100" } });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create extension" }),
    );
    await waitFor(() =>
      expect(external).toHaveAccessibleDescription(
        "+97142000100 is already used by 100",
      ),
    );
    expect(within(dialog).getByRole("alert")).toHaveTextContent(
      "invalid extension",
    );
  });

  it("lists features, and shows — for live state it could not read", async () => {
    api(
      [
        {
          ...EXT,
          externalNumber: "+97142000100",
          forwardNoAnswer: "1100",
          forwardBusy: "1011",
          voicemailEnabled: false,
          recordDefault: true,
        },
      ],
      { "GET /api/v1/devices": () => json({ items: [DEVICE] }) },
    );
    renderApp("/extensions");
    const row = await screen.findByRole("row", { name: /Reception/ });
    const cells = within(row).getAllByRole("cell");
    expect(cells.map((c) => c.textContent)).toEqual([
      "100",
      "Reception",
      "+97142000100",
      "1 · — registered",
      "—",
      "Busy → 1011 · No answer → 1100",
      "Off",
      "Default on",
    ]);
  });

  it("derives devices and presence from registrations and presence", async () => {
    api(
      [
        EXT,
        { ...EXT, id: 2, number: "101", name: "Jonas", dnd: true },
        { ...EXT, id: 4, number: "102", name: "Conference" },
        { ...EXT, id: 5, number: "103", name: "Priya" },
      ],
      {
        "GET /api/v1/devices": () =>
          json({
            items: [
              DEVICE,
              { ...DEVICE, id: 4, sipUsername: "reception-2" },
              { ...DEVICE, id: 5, extensionId: 4, sipUsername: "confroom-1" },
              { ...DEVICE, id: 6, extensionId: 5, sipUsername: "priya-desk" },
            ],
          }),
        "GET /api/v1/registrations": () =>
          json({
            items: [
              BINDING,
              { ...BINDING, device: "priya-desk", extension: "103" },
            ],
          }),
        "GET /api/v1/presence": () =>
          json({
            items: [
              {
                device: "reception-1",
                extension: "100",
                state: "on-call",
                updatedAt: "2026-10-01T10:00:00Z",
              },
            ],
          }),
      },
    );
    renderApp("/extensions");

    const reception = await screen.findByRole("row", { name: /Reception/ });
    await within(reception).findByText("On call");
    expect(reception).toHaveTextContent("2 · 1 registered");
    expect(screen.getByRole("row", { name: /Jonas/ })).toHaveTextContent(
      /None.*DND/,
    );
    expect(screen.getByRole("row", { name: /Conference/ })).toHaveTextContent(
      /1 · 0 registered.*Not registered/,
    );
    expect(screen.getByRole("row", { name: /Priya/ })).toHaveTextContent(
      /1 · 1 registered.*Idle/,
    );
  });

  it("filters by number or name", async () => {
    api([EXT, { ...EXT, id: 2, number: "101", name: "Jonas" }]);
    renderApp("/extensions");
    await screen.findByRole("row", { name: /Jonas/ });
    expect(screen.getByText("2 of 2")).toBeVisible();

    const search = screen.getByRole("searchbox", {
      name: "Search number or name",
    });
    fireEvent.change(search, { target: { value: "jon" } });
    expect(screen.queryByRole("row", { name: /Reception/ })).toBeNull();
    expect(screen.getByText("1 of 2")).toBeVisible();

    fireEvent.change(search, { target: { value: "999" } });
    expect(
      screen.getByRole("heading", { name: "No extensions match" }),
    ).toBeVisible();
  });

  it("edits call features and forwarding in the drawer", async () => {
    const calls = api(
      [{ ...EXT, dnd: false, voicemailEnabled: true, recordDefault: false }],
      {
        "GET /api/v1/devices": () => json({ items: [DEVICE] }),
        "GET /api/v1/registrations": () => json({ items: [BINDING] }),
        "GET /api/v1/presence": () => json({ items: [] }),
        "PATCH /api/v1/extensions/1": () =>
          json({
            ...EXT,
            name: "Lobby",
            dnd: true,
            voicemailEnabled: false,
            recordDefault: true,
            forwardAlways: "201",
            forwardBusy: "+97142000100",
          }),
      },
    );
    renderApp("/extensions");
    const drawer = await openDrawer();
    expect(drawer).toHaveAttribute("aria-modal", "true");
    expect(within(drawer).getByLabelText("Number")).toHaveFocus();
    // The extension's devices, with the user agent of the registered one.
    expect(await within(drawer).findByText("Yealink T54W")).toBeInTheDocument();
    expect(within(drawer).getByText("reception-1")).toBeVisible();

    fireEvent.change(within(drawer).getByLabelText("Name"), {
      target: { value: "Lobby" },
    });
    fireEvent.click(
      within(drawer).getByRole("switch", { name: /Do not disturb/ }),
    );
    fireEvent.click(within(drawer).getByRole("switch", { name: /^Voicemail/ }));
    fireEvent.click(
      within(drawer).getByRole("switch", { name: /Record calls by default/ }),
    );
    fireEvent.change(within(drawer).getByLabelText("Always"), {
      target: { value: "201" },
    });
    fireEvent.change(within(drawer).getByLabelText("When busy"), {
      target: { value: "+97142000100" },
    });
    fireEvent.click(within(drawer).getByRole("button", { name: "Save" }));

    expect(await screen.findByText("Extension 100 saved.")).toBeVisible();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(calls).toContainEqual({
      method: "PATCH",
      url: "/api/v1/extensions/1",
      body: {
        name: "Lobby",
        forwardAlways: "201",
        forwardBusy: "+97142000100",
        dnd: true,
        voicemailEnabled: false,
        recordDefault: true,
      },
    });
    expect(screen.getByRole("row", { name: /Lobby/ })).toHaveTextContent(
      "Always → 201 · Busy → +97142000100",
    );
  });

  it("validates forwarding targets before sending", async () => {
    const calls = api([EXT]);
    renderApp("/extensions");
    const drawer = await openDrawer();

    fireEvent.change(within(drawer).getByLabelText("No answer"), {
      target: { value: "desk" },
    });
    fireEvent.click(within(drawer).getByRole("button", { name: "Save" }));

    const field = within(drawer).getByLabelText("No answer");
    expect(field).toHaveAttribute("aria-invalid", "true");
    expect(field).toHaveAccessibleDescription(
      "Empty = off; or 2 to 20 digits, optionally starting with +.",
    );
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
  });

  it("shows server field errors on the drawer's fields", async () => {
    api([EXT], {
      "PATCH /api/v1/extensions/1": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid extension",
              fields: [{ path: "number", message: "number 101 is taken" }],
            },
          },
          400,
        ),
    });
    renderApp("/extensions");
    const drawer = await openDrawer();
    const number = within(drawer).getByLabelText("Number");
    fireEvent.change(number, { target: { value: "101" } });
    fireEvent.click(within(drawer).getByRole("button", { name: "Save" }));

    await waitFor(() => expect(number).toHaveAttribute("aria-invalid", "true"));
    expect(number).toHaveAccessibleDescription("number 101 is taken");
    expect(within(drawer).getByRole("alert")).toHaveTextContent(
      "invalid extension",
    );
  });

  it("deletes from the drawer after confirming", async () => {
    const calls = api([EXT], { "DELETE /api/v1/extensions/1": noContent });
    renderApp("/extensions");
    const drawer = await openDrawer();

    fireEvent.click(within(drawer).getByRole("button", { name: "Delete" }));
    expect(
      within(drawer).getByText("Delete 100 and its devices?"),
    ).toBeVisible();
    expect(
      within(drawer).getByRole("button", { name: "Delete extension" }),
    ).toHaveFocus();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Delete extension" }),
    );

    expect(
      await screen.findByText("Extension 100 and its devices deleted."),
    ).toBeVisible();
    expect(
      screen.getByRole("heading", { name: "No extensions yet" }),
    ).toBeVisible();
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain(
      "DELETE /api/v1/extensions/1",
    );
  });

  it("closes the drawer on Escape without saving", async () => {
    const calls = api([EXT]);
    renderApp("/extensions");
    const drawer = await openDrawer();
    fireEvent.change(within(drawer).getByLabelText("Name"), {
      target: { value: "Changed" },
    });
    fireEvent.keyDown(window, { key: "Escape" });
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(calls.some((c) => c.method === "PATCH")).toBe(false);
  });
});
