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
    "GET /api/v1/tokens": () => json({ items: [] }),
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
    const list = await screen.findByRole("list", { name: "Presence" });
    const row = within(list).getByText("desk-100").closest("li")!;
    expect(row).toHaveTextContent("100");
    expect(row).toHaveTextContent("Ringing");
    expect(within(list).getByText("lobby-200").closest("li")).toHaveTextContent(
      "Idle",
    );

    await waitFor(
      () =>
        expect(
          calls.filter((c) => c.url === "/api/v1/presence").length,
        ).toBeGreaterThanOrEqual(2),
      { timeout: 7000 },
    );
  });

  it("creates an API token, shows it once, and revokes one after confirming", async () => {
    const TOKEN = {
      id: 7,
      name: "grafana-read",
      createdAt: "2026-09-12T10:00:00Z",
      lastUsedAt: null,
    };
    const calls = setup({
      "GET /api/v1/tokens": [
        () => json({ items: [TOKEN] }),
        () =>
          json({
            items: [TOKEN, { ...TOKEN, id: 8, name: "provisioning-ci" }],
          }),
        () => json({ items: [{ ...TOKEN, id: 8, name: "provisioning-ci" }] }),
      ],
      "POST /api/v1/tokens": () =>
        json(
          { ...TOKEN, id: 8, name: "provisioning-ci", token: "hlo_secret" },
          201,
        ),
      "DELETE /api/v1/tokens/7": noContent,
    });
    renderApp("/system");
    const list = await screen.findByRole("list", { name: "API tokens" });
    expect(list).toHaveTextContent("grafana-read");
    expect(list).toHaveTextContent("last used never");

    fireEvent.click(screen.getByRole("button", { name: "New token" }));
    const create = await screen.findByRole("dialog", { name: "New API token" });
    fireEvent.click(
      within(create).getByRole("button", { name: "Create token" }),
    );
    expect(within(create).getByLabelText("Name")).toHaveAccessibleDescription(
      "Enter a name.",
    );
    fireEvent.change(within(create).getByLabelText("Name"), {
      target: { value: "provisioning-ci" },
    });
    fireEvent.click(
      within(create).getByRole("button", { name: "Create token" }),
    );

    const shown = await screen.findByRole("dialog", { name: "Token created" });
    expect(within(shown).getByLabelText(/API token/)).toHaveValue("hlo_secret");
    expect(within(shown).getByText("Shown once")).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/tokens",
      body: { name: "provisioning-ci" },
    });
    fireEvent.click(within(shown).getByRole("button", { name: "Done" }));
    expect(screen.queryByText("hlo_secret")).toBeNull();
    await screen.findByText("provisioning-ci");

    fireEvent.click(
      screen.getByRole("button", { name: "Revoke grafana-read" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "Revoke grafana-read?",
    });
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Revoke token" }),
    );
    expect(
      await screen.findByText("Token grafana-read revoked."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/tokens/7",
      body: undefined,
    });
  });
});
