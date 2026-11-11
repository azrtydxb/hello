import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const T = "2026-10-26T10:00:00Z";

const ENTRY = {
  id: 1,
  pattern: "+4479*",
  match: "prefix",
  action: "block",
  trunkId: null,
  trunkName: "",
  reason: "UK spam",
  announcementId: null,
  enabled: true,
  createdAt: T,
  updatedAt: T,
};

const ALLOW = {
  ...ENTRY,
  id: 2,
  pattern: "+447911123456",
  match: "exact",
  action: "allow",
  trunkId: 3,
  trunkName: "carrier-a",
  reason: "our supplier",
};

const BASE = {
  "GET /api/v1/trunks": () => json({ items: [{ id: 3, name: "carrier-a" }] }),
  "GET /api/v1/announcements": () =>
    json({ items: [{ id: 9, name: "sorry" }] }),
};

function page(items: unknown[], extra: Record<string, unknown> = {}) {
  return () => json({ items, next: "", total: items.length, ...extra });
}

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    ...BASE,
    "GET /api/v1/blocklist?limit=200": page([ENTRY, ALLOW]),
    ...extra,
  });
}

describe("Blocklist", () => {
  it("lists the entries with their action, scope and (to an admin) reason", async () => {
    setup();
    renderApp("/blocklist");
    const table = await screen.findByRole("table", {
      name: "Blocklist entries",
    });
    const rows = within(table).getAllByRole("row");
    expect(within(rows[1]!).getByText("+4479*")).toBeVisible();
    expect(within(rows[1]!).getByText("Block")).toBeVisible();
    expect(within(rows[1]!).getByText("All trunks")).toBeVisible();
    expect(within(rows[1]!).getByText("UK spam")).toBeVisible();
    expect(within(rows[2]!).getByText("Allow")).toBeVisible();
    expect(within(rows[2]!).getByText("carrier-a")).toBeVisible();
    expect(screen.getByText("Showing 2 of 2 entries.")).toBeVisible();
  });

  it("hides the reason column from an operator", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "ops", role: "operator" }),
      "GET /api/v1/blocklist?limit=200": page([{ ...ENTRY, reason: "" }]),
    });
    renderApp("/blocklist");
    const table = await screen.findByRole("table", {
      name: "Blocklist entries",
    });
    expect(within(table).queryByText("Reason")).toBeNull();
    expect(
      within(table).getByRole("button", { name: "Edit +4479*" }),
    ).toBeVisible();
  });

  it("shows no write buttons to a viewer", async () => {
    setup({
      "GET /api/v1/auth/me": () => json({ username: "view", role: "viewer" }),
    });
    renderApp("/blocklist");
    await screen.findByRole("table", { name: "Blocklist entries" });
    expect(screen.queryByRole("button", { name: "Add entry" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit +4479*" })).toBeNull();
  });

  it("explains an empty blocklist", async () => {
    setup({ "GET /api/v1/blocklist?limit=200": page([]) });
    renderApp("/blocklist");
    expect(await screen.findByText("The blocklist is empty")).toBeVisible();
  });

  it("searches by number through the API", async () => {
    const calls = setup({
      "GET /api/v1/blocklist?q=4479&limit=200": page([ENTRY]),
    });
    renderApp("/blocklist");
    await screen.findByRole("table", { name: "Blocklist entries" });
    fireEvent.change(screen.getByLabelText("Search"), {
      target: { value: "4479" },
    });
    await waitFor(() =>
      expect(calls.some((c) => c.url.includes("q=4479"))).toBe(true),
    );
  });

  it("adds an entry scoped to a trunk with an announcement", async () => {
    const calls = setup({
      "POST /api/v1/blocklist": () => json({ ...ENTRY, id: 5 }, 201),
    });
    renderApp("/blocklist");
    await screen.findByRole("table", { name: "Blocklist entries" });
    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    const dialog = screen.getByRole("dialog", {
      name: "Add a blocklist entry",
    });
    fireEvent.change(within(dialog).getByLabelText("Number"), {
      target: { value: "+44" },
    });
    fireEvent.change(within(dialog).getByLabelText("Match"), {
      target: { value: "prefix" },
    });
    fireEvent.change(within(dialog).getByLabelText("Applies to"), {
      target: { value: "3" },
    });
    fireEvent.change(within(dialog).getByLabelText("Announcement"), {
      target: { value: "9" },
    });
    fireEvent.change(within(dialog).getByLabelText("Reason"), {
      target: { value: "spam" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add entry" }));
    expect(await screen.findByText("Blocklist entry added.")).toBeVisible();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      pattern: "+44",
      match: "prefix",
      action: "block",
      trunkId: 3,
      announcementId: 9,
      enabled: true,
      reason: "spam",
    });
  });

  it("shows the server's complaint about a number", async () => {
    setup({
      "POST /api/v1/blocklist": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration: pattern: is already listed",
              fields: [{ path: "pattern", message: "is already listed" }],
            },
          },
          400,
        ),
    });
    renderApp("/blocklist");
    await screen.findByRole("table", { name: "Blocklist entries" });
    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    const dialog = screen.getByRole("dialog", {
      name: "Add a blocklist entry",
    });
    fireEvent.change(within(dialog).getByLabelText("Number"), {
      target: { value: "+4479" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add entry" }));
    expect(
      await within(dialog).findAllByText(/is already listed/),
    ).not.toHaveLength(0);
  });

  it("asks for a number before saving", async () => {
    const calls = setup();
    renderApp("/blocklist");
    await screen.findByRole("table", { name: "Blocklist entries" });
    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    const dialog = screen.getByRole("dialog", {
      name: "Add a blocklist entry",
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add entry" }));
    expect(
      await within(dialog).findByText("Enter a number or a prefix."),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("deletes an entry after confirmation", async () => {
    const calls = setup({
      "DELETE /api/v1/blocklist/1": () => noContent(),
    });
    renderApp("/blocklist");
    const table = await screen.findByRole("table", {
      name: "Blocklist entries",
    });
    fireEvent.click(
      within(table).getByRole("button", { name: "Delete +4479*" }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete entry" }),
    );
    expect(await screen.findByText("Blocklist entry deleted.")).toBeVisible();
    expect(
      calls.some((c) => c.method === "DELETE" && c.url.endsWith("/1")),
    ).toBe(true);
  });

  it("checks a CSV first, lists the bad rows, and imports only a clean file", async () => {
    const bad = {
      ok: false,
      dryRun: true,
      rows: 2,
      created: 0,
      updated: 0,
      errors: [{ row: 3, column: "number", reason: "is not a number" }],
      warnings: [],
    };
    const good = { ...bad, ok: true, errors: [] };
    const calls = setup({
      "POST /api/v1/blocklist/import?dry_run=true": [
        () => json(bad),
        () => json(good),
      ],
      "POST /api/v1/blocklist/import?dry_run=false": () =>
        json({ ...good, dryRun: false, created: 2 }),
    });
    renderApp("/blocklist");
    await screen.findByRole("table", { name: "Blocklist entries" });
    fireEvent.click(screen.getByRole("button", { name: "Import CSV" }));
    const dialog = screen.getByRole("dialog", {
      name: "Import blocklist entries",
    });
    const importBtn = within(dialog).getByRole("button", { name: "Import" });
    expect(importBtn).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("CSV"), {
      target: { value: "number\n+44*\nabc\n" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Check" }));
    expect(await within(dialog).findByText("is not a number")).toBeVisible();
    expect(importBtn).toBeDisabled();
    // Fixing the file means checking again.
    fireEvent.change(within(dialog).getByLabelText("CSV"), {
      target: { value: "number\n+44*\n+33*\n" },
    });
    expect(importBtn).toBeDisabled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Check" }));
    expect(await within(dialog).findByText("Ready to import")).toBeVisible();
    fireEvent.click(importBtn);
    expect(
      await screen.findByText("Imported 2 entries (2 new, 0 updated)."),
    ).toBeVisible();
    const post = calls.filter((c) => c.method === "POST");
    expect(post).toHaveLength(3);
    expect((post[2]!.body as FormData).get("file")).toBeInstanceOf(Blob);
  });

  it("reports a failed load", async () => {
    setup({
      "GET /api/v1/blocklist?limit=200": () =>
        apiError(503, "unavailable", "the blocklist is not configured"),
    });
    renderApp("/blocklist");
    expect(
      await screen.findByText("Could not load the blocklist"),
    ).toBeVisible();
    expect(screen.getByText("the blocklist is not configured")).toBeVisible();
  });
});
