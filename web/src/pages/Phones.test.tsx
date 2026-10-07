import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  json,
  ME,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const EXT = (id: number, number: string, name: string) => ({
  id,
  number,
  name,
  externalNumber: "",
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
});
const EXTENSIONS = [
  EXT(7, "101", "Front desk"),
  EXT(8, "102", "Back office"),
  EXT(9, "103", "Lobby"),
];

const DEVICE = {
  id: 3,
  extensionId: 7,
  sipUsername: "desk-phone",
  enabled: true,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const hoursAgo = (h: number) =>
  new Date(Date.now() - h * 60 * 60 * 1000).toISOString();

function phone(over: Record<string, unknown> = {}) {
  return {
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
    blf: [],
    enabled: true,
    tokenExposed: false,
    uaMismatch: false,
    bootArmed: false,
    bootReclaimed: false,
    redirectStatus: { state: "manual" },
    firstFetchAt: hoursAgo(5),
    lastFetchAt: hoursAgo(1),
    lastFetchIp: "192.0.2.10",
    lastFetchUa: "PolycomVVX-VVX_450-UA/6.4.0",
    lastFetchFile: "805ec0123456.cfg",
    firmwareSeen: "6.4.0.1",
    renderError: false,
    createdAt: "2026-10-01T10:00:00Z",
    updatedAt: "2026-10-01T10:00:00Z",
    ...over,
  };
}

// Token-bearing URLs are assembled at runtime so no literal looks like a secret.
const issuedUrl = (part: string) =>
  ["https://prov.test/p/", "tok", "en-", part, "-", "x9q2"].join("");

function api(phones: unknown[] = [phone()], more: Routes = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/phones": () => json({ items: phones }),
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    "GET /api/v1/devices": () => json({ items: [DEVICE] }),
    "GET /api/v1/prov/templates": () => json({ items: [] }),
    ...more,
  });
}

async function table() {
  return screen.findByRole("table", { name: "Phones" });
}

async function openNew() {
  const buttons = await screen.findAllByRole("button", { name: "New phone" });
  await waitFor(() => expect(buttons[0]).toBeEnabled());
  fireEvent.click(buttons[0]!);
  return screen.getByRole("dialog", { name: "New phone" });
}

function fillNew(dialog: HTMLElement) {
  fireEvent.change(within(dialog).getByLabelText("MAC"), {
    target: { value: "805ec0abcdef" },
  });
  fireEvent.change(within(dialog).getByLabelText("Model"), {
    target: { value: "VVX 450" },
  });
  fireEvent.change(within(dialog).getByLabelText("Vendor"), {
    target: { value: "poly" },
  });
  fireEvent.change(within(dialog).getByLabelText("Extension"), {
    target: { value: "7" },
  });
}

/** The URL is in its dialog; after Done it is nowhere in the document. */
async function expectShownOnce(title: string, url: string) {
  const dialog = await screen.findByRole("dialog", { name: title });
  expect(within(dialog).getByText(url)).toBeInTheDocument();
  expect(within(dialog).getByText("Shown once")).toBeInTheDocument();
  expect(within(dialog).getByRole("button", { name: /^Copy/ })).toHaveFocus();
  fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
  await waitFor(() =>
    expect(screen.queryByRole("dialog", { name: title })).toBeNull(),
  );
  expect(document.body.innerHTML).not.toContain(url);
  // A re-render of the list (another filter and back) does not bring it back.
  fireEvent.click(screen.getByRole("radio", { name: "Flagged" }));
  fireEvent.click(screen.getByRole("radio", { name: "All" }));
  await table();
  expect(document.body.innerHTML).not.toContain(url);
}

describe("Phones inventory", () => {
  it("lists phones with their flags and redirect state", async () => {
    api([
      phone({
        tokenExposed: true,
        uaMismatch: true,
        bootReclaimed: true,
        renderError: true,
        bootArmed: true,
        redirectStatus: { state: "failed", reason: "drift" },
      }),
    ]);
    renderApp("/phones");
    const t = await table();
    expect(
      screen.getByRole("heading", { level: 1, name: "Phones" }),
    ).toBeInTheDocument();
    const link = within(t).getByRole("link", { name: "80:5e:c0:12:34:56" });
    expect(link).toHaveAttribute("href", "/phones/5");
    for (const flag of [
      "Token exposed",
      "UA mismatch",
      "Boot reclaimed",
      "Render error",
      "Armed",
    ]) {
      expect(within(t).getByText(flag)).toBeInTheDocument();
    }
    expect(within(t).getByText("Failed: drift")).toBeInTheDocument();
    expect(within(t).getByText("desk-phone")).toBeInTheDocument();
    expect(within(t).getByText("6.4.0.1")).toBeInTheDocument();
  });

  it("filters never fetched, stale and flagged phones", async () => {
    api([
      phone({ id: 1, mac: "000000000001", lastFetchAt: null }),
      phone({ id: 2, mac: "000000000002", lastFetchAt: hoursAgo(49) }),
      phone({ id: 3, mac: "000000000003", lastFetchAt: hoursAgo(47) }),
      phone({ id: 4, mac: "000000000004", uaMismatch: true }),
    ]);
    renderApp("/phones");
    await table();
    const shown = () =>
      within(screen.getByRole("table", { name: "Phones" }))
        .getAllByRole("link")
        .map((a) => a.textContent);
    expect(shown()).toHaveLength(4);
    fireEvent.click(screen.getByRole("radio", { name: "Never fetched" }));
    expect(shown()).toEqual(["00:00:00:00:00:01"]);
    fireEvent.click(screen.getByRole("radio", { name: "Stale" }));
    expect(shown()).toEqual(["00:00:00:00:00:02"]);
    fireEvent.click(screen.getByRole("radio", { name: "Flagged" }));
    expect(shown()).toEqual(["00:00:00:00:00:04"]);
  });

  it("shows the provisioning URL once after create, with the manual step", async () => {
    const url = issuedUrl("create");
    const created = phone({ id: 6, mac: "805ec0abcdef", label: "" });
    const calls = api([phone()], {
      "POST /api/v1/phones": () =>
        json({ ...created, provisioningUrl: url }, 201),
    });
    renderApp("/phones");
    await table();
    const dialog = await openNew();
    fillNew(dialog);
    expect(within(dialog).queryByText(/Binding rotates/)).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Add phone" }));
    const shown = await screen.findByRole("dialog", { name: "Phone added" });
    expect(within(shown).getByText(/Poly ZT portal/)).toBeInTheDocument();
    expect(within(shown).queryByText(/device's secret was rotated/)).toBeNull();
    await expectShownOnce("Phone added", url);
    expect(
      within(await table()).getByRole("link", { name: "80:5e:c0:ab:cd:ef" }),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toEqual({
      mac: "805ec0abcdef",
      vendor: "poly",
      model: "VVX 450",
      label: "",
      extensionId: 7,
      blf: [],
      enabled: true,
    });
  });

  it("warns that binding an existing device rotates its secret, and sends deviceId", async () => {
    const url = issuedUrl("bind");
    const calls = api([], {
      "POST /api/v1/phones": () =>
        json(
          {
            ...phone({ id: 6, mac: "805ec0abcdef" }),
            provisioningUrl: url,
            secretRotated: true,
          },
          201,
        ),
    });
    renderApp("/phones");
    const dialog = await openNew();
    fillNew(dialog);
    const device = within(dialog).getByLabelText("Device");
    expect(device).toHaveValue("new");
    expect(within(dialog).queryByText(/Binding rotates/)).toBeNull();
    fireEvent.change(device, { target: { value: "3" } });
    expect(
      within(dialog).getByText(
        "Binding rotates the device secret; re-provision or re-type any phone already using it.",
      ),
    ).toBeInTheDocument();
    fireEvent.change(device, { target: { value: "new" } });
    expect(within(dialog).queryByText(/Binding rotates/)).toBeNull();
    fireEvent.change(device, { target: { value: "3" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add phone" }));
    const shown = await screen.findByRole("dialog", { name: "Phone added" });
    expect(
      within(shown).getByText(/device's secret was rotated/),
    ).toBeInTheDocument();
    const post = calls.find((c) => c.method === "POST")!;
    expect(post.body).toMatchObject({ deviceId: 3, extensionId: 7 });
    await expectShownOnce("Phone added", url);
  });

  it("shows the new URL once after an immediate token rotation", async () => {
    const url = issuedUrl("rotate");
    const calls = api([phone()], {
      "POST /api/v1/phones/5/rotate-token?immediate=true": () =>
        json({ ...phone(), provisioningUrl: url }),
    });
    renderApp("/phones");
    const t = await table();
    fireEvent.click(
      within(t).getByRole("button", {
        name: "Rotate token for 80:5e:c0:12:34:56",
      }),
    );
    const confirm = screen.getByRole("dialog", {
      name: "Rotate the token of Front desk?",
    });
    expect(within(confirm).getByText(/rolling/)).toBeInTheDocument();
    fireEvent.click(
      within(confirm).getByRole("checkbox", {
        name: /Revoke the old token now/,
      }),
    );
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Rotate token" }),
    );
    await expectShownOnce("Token rotated", url);
    expect(calls.some((c) => c.url.endsWith("?immediate=true"))).toBe(true);
  });

  it("shows the new URL once after a re-arm", async () => {
    const url = issuedUrl("rearm");
    api([phone()], {
      "POST /api/v1/phones/5/rearm": () =>
        json({ ...phone({ bootArmed: true }), provisioningUrl: url }),
    });
    renderApp("/phones");
    const t = await table();
    fireEvent.click(
      within(t).getByRole("button", { name: "Re-arm 80:5e:c0:12:34:56" }),
    );
    const confirm = screen.getByRole("dialog", { name: "Re-arm Front desk?" });
    expect(
      within(confirm).getByText(/revokes the current token at once/),
    ).toBeInTheDocument();
    fireEvent.click(within(confirm).getByRole("button", { name: "Re-arm" }));
    await expectShownOnce("Phone re-armed", url);
    expect(within(await table()).getByText("Armed")).toBeInTheDocument();
  });

  it("sends only the changed fields when editing, and shows field errors", async () => {
    const calls = api([phone()], {
      "PATCH /api/v1/phones/5": [
        json(
          {
            error: {
              code: "invalid",
              message: "invalid phone",
              fields: [{ path: "label", message: "Label is too long." }],
            },
          },
          400,
        ),
        json(phone({ label: "Lobby", blf: ["103", "102"] })),
      ],
    });
    renderApp("/phones");
    const t = await table();
    fireEvent.click(
      within(t).getByRole("button", { name: "Edit 80:5e:c0:12:34:56" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Edit Front desk" });
    expect(within(dialog).queryByLabelText("MAC")).toBeNull();
    fireEvent.change(within(dialog).getByLabelText("Label"), {
      target: { value: "Lobby" },
    });
    const add = within(dialog).getByLabelText("Add BLF key");
    fireEvent.change(add, { target: { value: "102" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add key" }));
    fireEvent.change(add, { target: { value: "103" } });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add key" }));
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Move 103 up" }),
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    expect(await within(dialog).findByText("Label is too long.")).toBeVisible();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: /^Edit/ })).toBeNull(),
    );
    const patches = calls.filter((c) => c.method === "PATCH");
    expect(patches).toHaveLength(2);
    expect(patches[1]!.body).toEqual({ label: "Lobby", blf: ["103", "102"] });
  });

  it("deletes a phone after confirmation", async () => {
    const calls = api([phone()], { "DELETE /api/v1/phones/5": noContent });
    renderApp("/phones");
    const t = await table();
    fireEvent.click(
      within(t).getByRole("button", { name: "Delete 80:5e:c0:12:34:56" }),
    );
    const confirm = screen.getByRole("dialog", { name: "Delete Front desk?" });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete phone" }),
    );
    expect(await screen.findByText("No phones yet")).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });
});

describe("Phones CSV import", () => {
  const CSV =
    "805ec0000001,yealink,T54W,101\n805ec0000001,yealink,T54W,101,Dup\n";

  async function openImport() {
    await table();
    fireEvent.click(screen.getByRole("button", { name: "Import CSV" }));
    const dialog = screen.getByRole("dialog", { name: "Import phones" });
    fireEvent.change(within(dialog).getByLabelText("CSV"), {
      target: { value: CSV },
    });
    return dialog;
  }

  it("shows each dry-run row's own errors and keeps Import disabled", async () => {
    const calls = api([phone()], {
      "POST /api/v1/phones/import?dryRun=true": () =>
        json({
          ok: false,
          rows: [
            { line: 1, mac: "805ec0000001", errors: [] },
            {
              line: 2,
              mac: "805ec0000001",
              errors: ["duplicate MAC", { message: "label taken" }],
            },
          ],
        }),
    });
    renderApp("/phones");
    const dialog = await openImport();
    const importButton = within(dialog).getByRole("button", { name: "Import" });
    expect(importButton).toBeDisabled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Check" }));
    const report = await within(dialog).findByRole("table", {
      name: "Check result",
    });
    const row = (line: string) =>
      within(report).getByRole("cell", { name: line }).closest("tr")!;
    expect(within(row("1")).getByText("OK")).toBeInTheDocument();
    expect(within(row("1")).queryByText(/duplicate MAC/)).toBeNull();
    expect(
      within(row("2")).getByText("duplicate MAC; label taken"),
    ).toBeInTheDocument();
    expect(within(row("2")).queryByText("OK")).toBeNull();
    expect(importButton).toBeDisabled();
    expect(calls.some((c) => c.url.includes("dryRun=false"))).toBe(false);
  });

  it("imports only after a clean dry run of the same text", async () => {
    const report = {
      ok: true,
      rows: [
        { line: 1, mac: "805ec0000001", errors: [] },
        { line: 2, mac: "805ec0000002", errors: [] },
      ],
    };
    const calls = api([phone()], {
      "POST /api/v1/phones/import?dryRun=true": () => json(report),
      "POST /api/v1/phones/import?dryRun=false": () =>
        json({ ...report, created: 2 }),
    });
    renderApp("/phones");
    const dialog = await openImport();
    const importButton = within(dialog).getByRole("button", { name: "Import" });
    fireEvent.click(within(dialog).getByRole("button", { name: "Check" }));
    await waitFor(() => expect(importButton).toBeEnabled());
    // Editing the text after the check needs another check.
    fireEvent.change(within(dialog).getByLabelText("CSV"), {
      target: { value: `${CSV}x` },
    });
    expect(importButton).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("CSV"), {
      target: { value: CSV },
    });
    expect(importButton).toBeEnabled();
    fireEvent.click(importButton);
    expect(await screen.findByText("2 phones imported")).toBeInTheDocument();
    expect(screen.queryByRole("dialog", { name: "Import phones" })).toBeNull();
    const apply = calls.find((c) => c.url.includes("dryRun=false"))!;
    expect(apply.body).toBeInstanceOf(Blob);
    expect(await (apply.body as Blob).text()).toBe(CSV);
    await waitFor(() =>
      expect(
        calls.filter((c) => c.method === "GET" && c.url === "/api/v1/phones"),
      ).toHaveLength(2),
    );
  });

  it("shows a server error as an alert", async () => {
    api([phone()], {
      "POST /api/v1/phones/import?dryRun=true": () =>
        json({ error: { code: "bad_csv", message: "missing header" } }, 400),
    });
    renderApp("/phones");
    const dialog = await openImport();
    fireEvent.click(within(dialog).getByRole("button", { name: "Check" }));
    expect(await within(dialog).findByRole("alert")).toHaveTextContent(
      "missing header",
    );
  });
});

describe("Phone admin password", () => {
  const secret = ["adm", "in-", "pw", "-7k"].join("");

  function detailApi() {
    return api([phone()], {
      "GET /api/v1/phones/5": () => json(phone()),
      "GET /api/v1/phones/5/fetches?limit=50": () =>
        json({ items: [], next: "" }),
      "POST /api/v1/phones/5/admin-password/reveal": () =>
        json({ adminPassword: secret }),
      "POST /api/v1/phones/5/admin-password/rotate": noContent,
    });
  }

  it("is shown only after an explicit reveal", async () => {
    const calls = detailApi();
    renderApp("/phones/5");
    const reveal = await screen.findByRole("button", {
      name: "Reveal admin password",
    });
    await screen.findByText("The phone has not fetched anything yet.");
    expect(document.body.innerHTML).not.toContain(secret);
    expect(calls.some((c) => c.url.includes("admin-password"))).toBe(false);
    fireEvent.click(reveal);
    const dialog = await screen.findByRole("dialog", {
      name: "Admin password",
    });
    expect(within(dialog).getByText(secret)).toBeInTheDocument();
    expect(
      calls.filter((c) => c.url.endsWith("/admin-password/reveal")),
    ).toHaveLength(1);
    fireEvent.click(within(dialog).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(document.body.innerHTML).not.toContain(secret));
  });

  it("rotates after confirmation", async () => {
    const calls = detailApi();
    renderApp("/phones/5");
    fireEvent.click(
      await screen.findByRole("button", { name: "Rotate admin password" }),
    );
    const confirm = screen.getByRole("dialog", {
      name: "Rotate the admin password of Front desk?",
    });
    expect(calls.some((c) => c.url.endsWith("/rotate"))).toBe(false);
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Rotate admin password" }),
    );
    expect(
      await screen.findByText(/Admin password rotated/),
    ).toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain(secret);
  });
});
