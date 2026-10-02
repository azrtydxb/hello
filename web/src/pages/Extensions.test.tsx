import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const EXT = {
  id: 1,
  number: "100",
  name: "Reception",
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

function fill(number: string, name: string) {
  fireEvent.change(screen.getByLabelText("Number"), {
    target: { value: number },
  });
  fireEvent.change(screen.getByLabelText("Name"), { target: { value: name } });
  fireEvent.click(screen.getByRole("button", { name: "Create extension" }));
}

describe("Extensions", () => {
  it.each(["1", "12345678901", "12a", " 101", ""])(
    "rejects number %j before sending",
    async (number) => {
      const calls = mockApi({
        ...ME,
        "GET /api/v1/extensions": () => json({ items: [] }),
      });
      renderApp("/extensions");
      await screen.findByText("No extensions yet.");

      fill(number, "Desk");

      const input = screen.getByLabelText("Number");
      expect(input).toHaveAttribute("aria-invalid", "true");
      expect(input).toHaveAccessibleDescription(
        "The number must be 2 to 10 digits (0–9 only).",
      );
      expect(calls.some((c) => c.method === "POST")).toBe(false);
    },
  );

  it("creates an extension with a valid number and lists it", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/extensions": () => json({ items: [] }),
      "POST /api/v1/extensions": () =>
        json({ ...EXT, id: 2, number: "0123456789", name: "Desk" }, 201),
    });
    renderApp("/extensions");
    await screen.findByText("No extensions yet.");

    fill("0123456789", "Desk");

    expect(
      await screen.findByRole("row", { name: /0123456789/ }),
    ).toHaveTextContent("Desk");
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/extensions",
      body: { number: "0123456789", name: "Desk" },
    });
    expect(screen.getByLabelText("Number")).toHaveValue("");
    expect(screen.getByLabelText("Number")).not.toHaveAttribute("aria-invalid");
  });

  it("shows the server's error message on conflict", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/extensions": () => json({ items: [EXT] }),
      "POST /api/v1/extensions": () =>
        apiError(409, "conflict", "extension 100 already exists"),
    });
    renderApp("/extensions");
    await screen.findByRole("row", { name: /Reception/ });

    fill("100", "Again");

    expect(await screen.findByRole("alert")).toHaveTextContent(
      "extension 100 already exists",
    );
  });

  it("edits the name and deletes with an in-page confirmation", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/extensions": () => json({ items: [EXT] }),
      "PATCH /api/v1/extensions/1": () => json({ ...EXT, name: "Lobby" }),
      "DELETE /api/v1/extensions/1": noContent,
    });
    renderApp("/extensions");

    fireEvent.click(
      await screen.findByRole("button", { name: "Edit extension 100" }),
    );
    fireEvent.change(screen.getByLabelText("Name for extension 100"), {
      target: { value: "Lobby" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await screen.findByRole("button", { name: "Edit extension 100" });
    const row = screen.getByRole("row", { name: /Lobby/ });
    expect(calls).toContainEqual({
      method: "PATCH",
      url: "/api/v1/extensions/1",
      body: { name: "Lobby" },
    });

    fireEvent.click(
      within(row).getByRole("button", { name: "Delete extension 100" }),
    );
    const confirm = screen.getByRole("group", {
      name: "Delete 100 and its devices?",
    });
    expect(
      within(confirm).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete extension" }),
    );
    expect(await screen.findByText("No extensions yet.")).toBeVisible();
  });
  it("sets an external number on create and edit, validating it", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/extensions": () =>
        json({ items: [{ ...EXT, externalNumber: "" }] }),
      "POST /api/v1/extensions": () =>
        json(
          {
            ...EXT,
            id: 2,
            number: "102",
            name: "Desk",
            externalNumber: "+97142000102",
          },
          201,
        ),
      "PATCH /api/v1/extensions/1": () =>
        apiError(400, "bad_request", "invalid extension"),
    });
    renderApp("/extensions");
    await screen.findByRole("row", { name: /Reception/ });

    const external = screen.getByLabelText("External number");
    fireEvent.change(external, { target: { value: "12-34" } });
    fill("102", "Desk");
    expect(external).toHaveAttribute("aria-invalid", "true");
    expect(calls.some((c) => c.method === "POST")).toBe(false);

    fireEvent.change(external, { target: { value: "+97142000102" } });
    fireEvent.click(screen.getByRole("button", { name: "Create extension" }));
    expect(await screen.findByText("+97142000102")).toBeVisible();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      number: "102",
      name: "Desk",
      externalNumber: "+97142000102",
    });

    fireEvent.click(screen.getByRole("button", { name: "Edit extension 100" }));
    fireEvent.change(
      screen.getByLabelText("External number for extension 100"),
      {
        target: { value: "+97142000100" },
      },
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(await screen.findByText(/invalid extension/)).toBeVisible();
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      externalNumber: "+97142000100",
    });
  });
});
