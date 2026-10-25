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

const contact = (id: number, first: string, last: string, number: string) => ({
  id,
  bookId: 9,
  firstName: first,
  lastName: last,
  company: "",
  numbers: [{ label: "work", number }],
  note: "",
  createdAt: STAMP,
  updatedAt: STAMP,
});

function api(role: string, more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    "GET /api/v1/me/contacts?limit=200": () =>
      json({
        items: [contact(10, "Ann", "Smith", "+32 2 555 01 02")],
        next: "",
      }),
    ...more,
  });
}

describe("MyContacts", () => {
  it("lets a viewer keep their own contacts", async () => {
    const calls = api("viewer", {
      "POST /api/v1/me/contacts": () =>
        json(contact(11, "Cy", "Young", "1004"), 201),
      "DELETE /api/v1/me/contacts/10": () => noContent(),
    });
    renderApp("/me/contacts");
    const table = await screen.findByRole("table", {
      name: "Entries of My contacts",
    });
    expect(within(table).getByText("Ann Smith")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "Export CSV" })).toHaveAttribute(
      "href",
      "/api/v1/me/contacts/export",
    );

    // A viewer may add, unlike on the shared Phonebooks page.
    fireEvent.click(screen.getByRole("button", { name: "Add entry" }));
    const dialog = screen.getByRole("dialog", { name: "Add entry" });
    expect(within(dialog).getByText("In My contacts.")).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("First name"), {
      target: { value: "Cy" },
    });
    fireEvent.change(within(dialog).getByLabelText("Last name"), {
      target: { value: "Young" },
    });
    fireEvent.change(within(dialog).getByLabelText("Number 1"), {
      target: { value: "1004" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Add entry" }));
    expect(await screen.findByText("Cy Young")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")).toMatchObject({
      url: "/api/v1/me/contacts",
      body: {
        firstName: "Cy",
        lastName: "Young",
        numbers: [{ label: "work", number: "1004" }],
      },
    });

    fireEvent.click(screen.getByRole("button", { name: "Delete Ann Smith" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete entry" }),
    );
    await waitFor(() => expect(screen.queryByText("Ann Smith")).toBeNull());
  });

  it("imports through the user's own route after a clean check", async () => {
    const result = (dryRun: boolean) =>
      json({
        ok: true,
        dryRun,
        mode: "append",
        rows: 1,
        imported: dryRun ? 0 : 1,
        errors: [],
        warnings: [],
      });
    const calls = api("viewer", {
      "POST /api/v1/me/contacts/import?dry_run=true&mode=append": () =>
        result(true),
      "POST /api/v1/me/contacts/import?dry_run=false&mode=append": () =>
        result(false),
    });
    renderApp("/me/contacts");
    await screen.findByRole("table", { name: "Entries of My contacts" });
    fireEvent.click(screen.getByRole("button", { name: "Import CSV" }));
    const dialog = screen.getByRole("dialog", {
      name: "Import into My contacts",
    });
    fireEvent.change(within(dialog).getByLabelText("CSV"), {
      target: { value: "last_name,work\nB,1001\n" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Check" }));
    expect(
      await within(dialog).findByText("Ready to import"),
    ).toBeInTheDocument();
    const importBtn = within(dialog).getByRole("button", { name: "Import" });
    await waitFor(() => expect(importBtn).toBeEnabled());
    fireEvent.click(importBtn);
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Import into My contacts" }),
      ).toBeNull(),
    );
    expect(calls.filter((c) => c.method === "POST").map((c) => c.url)).toEqual([
      "/api/v1/me/contacts/import?dry_run=true&mode=append",
      "/api/v1/me/contacts/import?dry_run=false&mode=append",
    ]);
  });

  it("explains a user without a linked extension", async () => {
    api("viewer", {
      "GET /api/v1/me/contacts?limit=200": () =>
        apiError(403, "no_extension", "your account is not linked"),
    });
    renderApp("/me/contacts");
    expect(
      await screen.findByText("Your account is not linked to an extension"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add entry" })).toBeNull();
  });

  it("shows another failure as an error, not as an empty book", async () => {
    api("operator", {
      "GET /api/v1/me/contacts?limit=200": () =>
        apiError(500, "internal", "boom"),
    });
    renderApp("/me/contacts");
    expect(
      await screen.findByText("Could not load your contacts"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Add entry" })).toBeNull();
  });

  it("is the user's address book in the sidebar", async () => {
    mockApi({
      "GET /api/v1/auth/me": () =>
        json({
          username: "u",
          role: "user",
          extensions: [{ id: 1, number: "101", name: "Desk" }],
        }),
      "GET /api/v1/me/contacts?limit=200": () => json({ items: [], next: "" }),
    });
    renderApp("/me/contacts");
    expect(
      await screen.findByRole("link", { name: "Address book" }),
    ).toHaveAttribute("href", "/me/contacts");
  });

  it("lets a user with the self-service role keep contacts", async () => {
    api("user", {
      "POST /api/v1/me/contacts": () =>
        json(contact(11, "Cy", "Young", "1004"), 201),
    });
    renderApp("/me/contacts");
    await screen.findByRole("table", { name: "Entries of My contacts" });
    expect(
      screen.getByRole("button", { name: "Add entry" }),
    ).toBeInTheDocument();
  });
});
