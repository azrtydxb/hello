import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const CODES = [
  { code: "*72", action: "forward_always", argument: "" },
  { code: "*97", action: "voicemail", argument: "" },
];

const PRESENCE = [
  {
    device: "desk-100",
    extension: "100",
    state: "ringing",
    updatedAt: "2026-10-03T09:00:00Z",
  },
  {
    device: "lobby-200",
    extension: "200",
    state: "idle",
    updatedAt: "2026-10-03T09:01:00Z",
  },
];

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/feature-codes": () => json({ items: CODES }),
    "GET /api/v1/presence": () => json({ items: PRESENCE }),
    ...extra,
  });
}

describe("System", () => {
  it("lists feature codes and saves the edited list", async () => {
    const calls = setup({
      "PUT /api/v1/feature-codes": noContent,
    });
    renderApp("/system");
    await screen.findByLabelText("Argument 1");

    fireEvent.change(screen.getByLabelText("Argument 1"), {
      target: { value: "201" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add code" }));
    fireEvent.change(screen.getByLabelText("Code 3"), {
      target: { value: "*99" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save feature codes" }));

    await screen.findByText("Feature codes saved.");
    expect(calls).toContainEqual({
      method: "PUT",
      url: "/api/v1/feature-codes",
      body: {
        items: [
          { code: "*72", action: "forward_always", argument: "201" },
          { code: "*97", action: "voicemail", argument: "" },
          { code: "*99", action: "voicemail", argument: "" },
        ],
      },
    });
  });

  it("rejects an invalid or duplicated code before sending", async () => {
    const calls = setup();
    renderApp("/system");
    await screen.findByLabelText("Argument 1");

    fireEvent.change(screen.getByLabelText("Code 1"), {
      target: { value: "77" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save feature codes" }));
    const code = screen.getByLabelText("Code 1");
    expect(code).toHaveAttribute("aria-invalid", "true");
    expect(code).toHaveAccessibleDescription(
      "Use * plus 2–4 digits or # (or ##).",
    );
    expect(calls.some((c) => c.method === "PUT")).toBe(false);

    fireEvent.change(screen.getByLabelText("Code 1"), {
      target: { value: "*97" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save feature codes" }));
    expect((await screen.findAllByText("*97 is used twice.")).length).toBe(2);
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("shows a server error that no field matches", async () => {
    setup({
      "PUT /api/v1/feature-codes": () =>
        apiError(409, "conflict", "another admin changed the codes"),
    });
    renderApp("/system");
    await screen.findByLabelText("Argument 1");

    fireEvent.click(screen.getByRole("button", { name: "Save feature codes" }));
    expect(await screen.findByRole("alert")).toHaveTextContent(
      "another admin changed the codes",
    );
  });

  it("lists presence and refreshes it", async () => {
    const calls = setup();
    renderApp("/system");
    const table = await screen.findByRole("table", { name: "Presence" });
    const row = within(table).getByRole("row", { name: /desk-100/ });
    expect(row).toHaveTextContent("100");
    expect(row).toHaveTextContent("ringing");

    await waitFor(
      () =>
        expect(
          calls.filter((c) => c.url === "/api/v1/presence").length,
        ).toBeGreaterThanOrEqual(2),
      { timeout: 7000 },
    );
  });
});
