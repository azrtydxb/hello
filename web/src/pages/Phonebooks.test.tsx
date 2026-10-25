import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const COMPANY = {
  id: 1,
  kind: "company",
  name: "Company",
  version: 3,
  entryCount: 2,
  createdAt: STAMP,
  updatedAt: STAMP,
};
const EXTENSIONS_BOOK = {
  ...COMPANY,
  id: 2,
  kind: "extensions",
  name: "Extensions",
  entryCount: 2,
};
const NAMED = {
  ...COMPANY,
  id: 3,
  kind: "named",
  name: "Warehouse",
  entryCount: 0,
};

const entry = (id: number, first: string, last: string, number: string) => ({
  id,
  bookId: 1,
  firstName: first,
  lastName: last,
  company: "",
  numbers: [{ label: "work", number }],
  note: "",
  createdAt: STAMP,
  updatedAt: STAMP,
});

const EXT = (id: number, number: string, name: string, hidden = false) => ({
  id,
  number,
  name,
  externalNumber: "",
  directoryHidden: hidden,
  createdAt: STAMP,
  updatedAt: STAMP,
});

function api(role = "admin", more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    "GET /api/v1/phonebooks?limit=200": () =>
      json({ items: [COMPANY, EXTENSIONS_BOOK, NAMED], next: "" }),
    "GET /api/v1/phonebooks/1/entries?limit=200": () =>
      json({
        items: [
          entry(10, "Ann", "Smith", "+32 2 555 01 02"),
          entry(11, "Bob", "Adams", "1003"),
        ],
        next: "",
      }),
    ...more,
  });
}

async function entriesTable() {
  return screen.findByRole("table", { name: "Entries of Company" });
}

describe("Phonebooks", () => {
  it("lists the company book's entries and filters them by search", async () => {
    api();
    renderApp("/phonebooks");
    const table = await entriesTable();
    expect(within(table).getByText("Ann Smith")).toBeInTheDocument();
    expect(within(table).getByText("Bob Adams")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Search entries"), {
      target: { value: "1003" },
    });
    expect(within(table).queryByText("Ann Smith")).toBeNull();
    expect(within(table).getByText("Bob Adams")).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText("Search entries"), {
      target: { value: "zzz" },
    });
    expect(
      screen.getByText("No entries match the search."),
    ).toBeInTheDocument();
  });

  it("adds an entry and shows a server field error at its field", async () => {
    const calls = api("operator", {
      "POST /api/v1/phonebooks/1/entries": [
        () =>
          json(
            {
              error: {
                code: "bad_request",
                message: "invalid entry",
                fields: [
                  {
                    path: "numbers[0].number",
                    message: "must be 1-32 printable characters",
                  },
                ],
              },
            },
            400,
          ),
        () => json(entry(12, "Cy", "Young", "1004"), 201),
      ],
    });
    renderApp("/phonebooks");
    await entriesTable();
    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    const dialog = screen.getByRole("dialog", { name: "Add entry" });
    fireEvent.change(within(dialog).getByLabelText("First name"), {
      target: { value: "Cy" },
    });
    fireEvent.change(within(dialog).getByLabelText("Last name"), {
      target: { value: "Young" },
    });
    fireEvent.change(within(dialog).getByLabelText("Number 1"), {
      target: { value: "bad" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add entry" }));
    expect(
      await within(dialog).findByText("must be 1-32 printable characters"),
    ).toBeInTheDocument();
    expect(within(dialog).getByLabelText("Number 1")).toBeInvalid();

    fireEvent.change(within(dialog).getByLabelText("Number 1"), {
      target: { value: "1004" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add entry" }));
    await waitFor(() =>
      expect(screen.queryByRole("dialog", { name: "Add entry" })).toBeNull(),
    );
    expect(await screen.findByText("Cy Young")).toBeInTheDocument();
    const posts = calls.filter((c) => c.method === "POST");
    expect(posts[1]!.body).toEqual({
      firstName: "Cy",
      lastName: "Young",
      company: "",
      note: "",
      numbers: [{ label: "work", number: "1004" }],
    });
  });

  it("edits and deletes an entry", async () => {
    const calls = api("operator", {
      "PUT /api/v1/phonebooks/1/entries/11": () =>
        json({ ...entry(11, "Bob", "Adams", "1099") }),
      "DELETE /api/v1/phonebooks/1/entries/10": () => noContent(),
    });
    renderApp("/phonebooks");
    await entriesTable();
    fireEvent.click(screen.getByRole("button", { name: "Edit Bob Adams" }));
    const dialog = screen.getByRole("dialog", { name: "Edit Bob Adams" });
    fireEvent.change(within(dialog).getByLabelText("Number 1"), {
      target: { value: "1099" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    expect(await screen.findByText("1099")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")!.body).toMatchObject({
      numbers: [{ label: "work", number: "1099" }],
    });

    fireEvent.click(screen.getByRole("button", { name: "Delete Ann Smith" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete entry" }),
    );
    await waitFor(() => expect(screen.queryByText("Ann Smith")).toBeNull());
  });

  it("shows CSV dry-run errors per row and imports only after a clean check", async () => {
    let dry = 0;
    const calls = api("operator", {
      "POST /api/v1/phonebooks/1/import?dry_run=true&mode=append": () => {
        dry += 1;
        return dry === 1
          ? json({
              ok: false,
              dryRun: true,
              mode: "append",
              rows: 2,
              imported: 0,
              errors: [
                { row: 3, column: "work", reason: "must be 1-32 characters" },
                { row: 4, column: "last_name", reason: "a name is required" },
              ],
              warnings: [],
            })
          : json({
              ok: true,
              dryRun: true,
              mode: "append",
              rows: 2,
              imported: 0,
              errors: [],
              warnings: [{ row: 2, reason: "number looks like a short code" }],
            });
      },
      "POST /api/v1/phonebooks/1/import?dry_run=false&mode=append": () =>
        json({
          ok: true,
          dryRun: false,
          mode: "append",
          rows: 2,
          imported: 2,
          errors: [],
          warnings: [],
        }),
      "GET /api/v1/phonebooks/1/entries?limit=200": () =>
        json({ items: [entry(10, "Ann", "Smith", "1001")], next: "" }),
    });
    renderApp("/phonebooks");
    await entriesTable();
    fireEvent.click(screen.getByRole("button", { name: "Import CSV" }));
    const dialog = screen.getByRole("dialog", { name: "Import into Company" });
    const importBtn = within(dialog).getByRole("button", { name: "Import" });
    expect(importBtn).toBeDisabled();
    fireEvent.change(within(dialog).getByLabelText("CSV"), {
      target: { value: "first_name,last_name,work\nA,B,1\n" },
    });
    // Not importable before a check, whatever the text.
    expect(importBtn).toBeDisabled();
    fireEvent.click(within(dialog).getByRole("button", { name: "Check" }));

    const errs = await within(dialog).findByRole("table", {
      name: "Rows with errors",
    });
    expect(
      within(errs).getByText("must be 1-32 characters"),
    ).toBeInTheDocument();
    expect(within(errs).getByText("a name is required")).toBeInTheDocument();
    expect(within(errs).getByText("3")).toBeInTheDocument();
    expect(within(errs).getByText("last_name")).toBeInTheDocument();
    expect(importBtn).toBeDisabled();

    // Editing the text invalidates the check; a clean check enables Import.
    fireEvent.change(within(dialog).getByLabelText("CSV"), {
      target: { value: "first_name,last_name,work\nA,B,1001\n" },
    });
    expect(
      within(dialog).queryByRole("table", { name: "Rows with errors" }),
    ).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Check" }));
    expect(
      await within(dialog).findByText("Ready to import"),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByText(/number looks like a short code/),
    ).toBeInTheDocument();
    await waitFor(() => expect(importBtn).toBeEnabled());
    fireEvent.click(importBtn);
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Import into Company" }),
      ).toBeNull(),
    );
    const posts = calls.filter((c) => c.method === "POST");
    expect(posts.map((c) => c.url)).toEqual([
      "/api/v1/phonebooks/1/import?dry_run=true&mode=append",
      "/api/v1/phonebooks/1/import?dry_run=true&mode=append",
      "/api/v1/phonebooks/1/import?dry_run=false&mode=append",
    ]);
    const form = posts[2]!.body as FormData;
    expect(await (form.get("file") as Blob).text()).toBe(
      "first_name,last_name,work\nA,B,1001\n",
    );
  });

  it("warns that replace mode deletes the current entries", async () => {
    api("operator");
    renderApp("/phonebooks");
    await entriesTable();
    fireEvent.click(screen.getByRole("button", { name: "Import CSV" }));
    const dialog = screen.getByRole("dialog", { name: "Import into Company" });
    fireEvent.change(within(dialog).getByLabelText("Mode"), {
      target: { value: "replace" },
    });
    expect(
      within(dialog).getByText("Replace deletes the current entries"),
    ).toBeInTheDocument();
  });

  it("links the CSV export of the shown book", async () => {
    api("operator");
    renderApp("/phonebooks");
    await entriesTable();
    expect(screen.getByRole("link", { name: "Export CSV" })).toHaveAttribute(
      "href",
      "/api/v1/phonebooks/1/export",
    );
  });

  it("makes the extensions book read-only with a hide toggle per extension", async () => {
    const calls = api("operator", {
      "GET /api/v1/extensions": () =>
        json({
          items: [
            EXT(7, "101", "Front desk"),
            EXT(8, "102", "Hidden one", true),
          ],
        }),
      "PATCH /api/v1/extensions/7": () =>
        json(EXT(7, "101", "Front desk", true)),
    });
    renderApp("/phonebooks");
    await entriesTable();
    fireEvent.change(screen.getByLabelText("Book"), { target: { value: "2" } });
    const table = await screen.findByRole("table", { name: "Extensions book" });
    expect(screen.getByText("Built from the extensions")).toBeInTheDocument();
    for (const name of ["Add entry", "Import CSV", "Rename", "Delete book"]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
    expect(screen.queryByRole("link", { name: "Export CSV" })).toBeNull();
    expect(within(table).queryByRole("button", { name: /^Edit/ })).toBeNull();

    const toggle = within(table).getByRole("switch", {
      name: "Show 101 in the directory",
    });
    expect(toggle).toBeChecked();
    expect(
      within(table).getByRole("switch", { name: "Show 102 in the directory" }),
    ).not.toBeChecked();
    fireEvent.click(toggle);
    await waitFor(() => expect(toggle).not.toBeChecked());
    expect(calls.find((c) => c.method === "PATCH")!.body).toEqual({
      directoryHidden: true,
    });
  });

  it("creates, renames and deletes a named book (administrator)", async () => {
    const calls = api("admin", {
      "POST /api/v1/phonebooks": () =>
        json({ ...NAMED, id: 4, name: "Sales", entryCount: 0 }, 201),
      "GET /api/v1/phonebooks/4/entries?limit=200": () =>
        json({ items: [], next: "" }),
      "PUT /api/v1/phonebooks/4": () =>
        json({ ...NAMED, id: 4, name: "Sales EU", entryCount: 0 }),
      "DELETE /api/v1/phonebooks/4": () => noContent(),
    });
    renderApp("/phonebooks");
    await entriesTable();
    fireEvent.click(screen.getByRole("button", { name: "New book" }));
    const dialog = screen.getByRole("dialog", { name: "New phonebook" });
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "Sales" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Create book" }),
    );
    expect(await screen.findByText("Sales is empty")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Rename" }));
    const rename = screen.getByRole("dialog", { name: "Rename phonebook" });
    fireEvent.change(within(rename).getByLabelText("Name"), {
      target: { value: "Sales EU" },
    });
    fireEvent.click(within(rename).getByRole("button", { name: "Save" }));
    expect(await screen.findByText("Sales EU is empty")).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Delete book" }));
    const confirm = await screen.findByRole("dialog", {
      name: "Delete phonebook Sales EU?",
    });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete book" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
    expect(await entriesTable()).toBeInTheDocument();
  });

  it("hides every write control from a viewer", async () => {
    api("viewer");
    renderApp("/phonebooks");
    const table = await entriesTable();
    expect(within(table).getByText("Ann Smith")).toBeInTheDocument();
    for (const name of [
      "New book",
      "Add entry",
      "Import CSV",
      "Edit Ann Smith",
      "Delete Ann Smith",
    ]) {
      expect(screen.queryByRole("button", { name })).toBeNull();
    }
    expect(screen.queryByRole("link", { name: "Export CSV" })).toBeNull();
  });

  it("reports a failed load", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => json({ username: "u", role: "admin" }),
      "GET /api/v1/phonebooks?limit=200": () =>
        apiError(503, "unavailable", "address books are not configured"),
    });
    renderApp("/phonebooks");
    expect(
      await screen.findByText("address books are not configured"),
    ).toBeInTheDocument();
  });
});
