import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const CREATED = "2026-10-25T10:00:00Z";
const EXTENSIONS = [
  {
    id: 1,
    number: "100",
    name: "Reception",
    createdAt: CREATED,
    updatedAt: CREATED,
  },
  {
    id: 2,
    number: "200",
    name: "Kitchen",
    createdAt: CREATED,
    updatedAt: CREATED,
  },
];
const ANNOUNCEMENTS = [
  { id: 5, name: "greeting", createdAt: CREATED },
  { id: 6, name: "oops", createdAt: CREATED },
];

const MENU = {
  id: 9,
  name: "main",
  number: "800",
  enabled: true,
  greetingId: 5,
  greetingName: "greeting",
  invalidPromptId: 6,
  invalidPromptName: "oops",
  timeoutPromptId: null,
  timeoutPromptName: "",
  digitTimeout: 5,
  interDigitTimeout: 3,
  maxRetries: 3,
  allowDirectDial: true,
  promptLanguage: "",
  timeout: { kind: "hangup", target: "" },
  invalid: { kind: "extension", target: "100" },
  options: [{ digit: "1", kind: "extension", target: "200" }],
  createdAt: CREATED,
  updatedAt: CREATED,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    "GET /api/v1/announcements": () => json({ items: ANNOUNCEMENTS }),
    "GET /api/v1/ring-groups": () => json({ items: [] }),
    "GET /api/v1/paging-groups": () => json({ items: [] }),
    "GET /api/v1/ivr-menus": () => json({ items: [] }),
    ...extra,
  });
}

describe("Ivr", () => {
  it("creates a menu with a greeting, a key and exits", async () => {
    const calls = setup({
      "POST /api/v1/ivr-menus": () => json(MENU, 201),
    });
    renderApp("/ivr");
    const [create] = await screen.findAllByRole("button", {
      name: "New IVR menu",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "main" },
    });
    const greeting = screen.getByLabelText("Greeting");
    await waitFor(() =>
      expect(greeting.querySelectorAll("option").length).toBe(3),
    );
    fireEvent.change(greeting, { target: { value: "5" } });
    expect(
      screen.getByRole("button", { name: "Preview greeting" }),
    ).toBeEnabled();
    fireEvent.click(screen.getByRole("button", { name: "Add key" }));
    fireEvent.change(screen.getByLabelText("Goes to"), {
      target: { value: "extension" },
    });
    fireEvent.change(screen.getByLabelText("Extension"), {
      target: { value: "200" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create menu" }));

    expect(await screen.findByText("IVR menu main created.")).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/ivr-menus",
      body: {
        name: "main",
        number: "",
        enabled: true,
        greetingId: 5,
        invalidPromptId: null,
        timeoutPromptId: null,
        digitTimeout: 5,
        interDigitTimeout: 3,
        maxRetries: 3,
        allowDirectDial: false,
        promptLanguage: "",
        timeout: { kind: "hangup", target: "" },
        invalid: { kind: "hangup", target: "" },
        options: [{ digit: "1", kind: "extension", target: "200" }],
      },
    });
  });

  it("sends a key to the dial-by-name directory with the menu's language", async () => {
    const calls = setup({
      "POST /api/v1/ivr-menus": () => json(MENU, 201),
    });
    renderApp("/ivr");
    const [create] = await screen.findAllByRole("button", {
      name: "New IVR menu",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "main" },
    });
    const greeting = screen.getByLabelText("Greeting");
    await waitFor(() =>
      expect(greeting.querySelectorAll("option").length).toBe(3),
    );
    fireEvent.change(greeting, { target: { value: "5" } });
    fireEvent.change(screen.getByLabelText("Prompt language"), {
      target: { value: "nl" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add key" }));
    fireEvent.change(screen.getByLabelText("Goes to"), {
      target: { value: "directory" },
    });
    // The directory names nothing, so no target is asked for.
    expect(screen.queryByLabelText("Target")).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Create menu" }));
    expect(await screen.findByText("IVR menu main created.")).toBeVisible();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({
      promptLanguage: "nl",
      options: [{ digit: "1", kind: "directory", target: "" }],
    });
  });

  it("refuses a menu without a greeting", async () => {
    const calls = setup();
    renderApp("/ivr");
    const [create] = await screen.findAllByRole("button", {
      name: "New IVR menu",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "main" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create menu" }));
    expect(await screen.findByText("Choose the greeting.")).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("lists menus with their keys and deletes one", async () => {
    const calls = setup({
      "GET /api/v1/ivr-menus": () => json({ items: [MENU] }),
      "DELETE /api/v1/ivr-menus/9": () => new Response(null, { status: 204 }),
    });
    renderApp("/ivr");
    const card = await screen.findByRole("listitem", { name: "main" });
    expect(card).toHaveTextContent("Dial 800");
    expect(card).toHaveTextContent("Extension 200");
    fireEvent.click(
      screen.getByRole("button", { name: "Delete IVR menu main" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete menu" }));
    expect(await screen.findByText("IVR menu main deleted.")).toBeVisible();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/ivr-menus/9",
      body: undefined,
    });
  });

  it("shows a server validation error at its field", async () => {
    setup({
      "GET /api/v1/ivr-menus": () => json({ items: [MENU] }),
      "PUT /api/v1/ivr-menus/9": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid IVR menu",
              fields: [
                { path: "number", message: "is already an extension number" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/ivr");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit IVR menu main" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save menu" }));
    expect(
      await screen.findByText("is already an extension number"),
    ).toBeVisible();
  });
});
