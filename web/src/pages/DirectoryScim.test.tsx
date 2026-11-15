import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, noContent, renderApp } from "../test/api";
import type {
  Connector,
  ScimStatus,
  ScimToken,
  ScimTokenCreated,
} from "../api/dirsync";
import { PBX_EXTENSION } from "./DirectoryScim";
import { sourceText } from "./DirectorySync";

const AT = "2026-11-14T09:00:00Z";
const BASE = "https://pbx.example.com/scim/v2";

function scimConnector(over: Partial<Connector> = {}): Connector {
  return {
    id: 7,
    kind: "scim",
    name: "Entra",
    enabled: true,
    mode: "authoritative",
    scheduleMinutes: 0,
    config: { adoptExisting: false } as Connector["config"],
    mappings: {
      username: { sources: ["userName"], transforms: [] },
      email: {
        sources: ["emails[primary]", "emails[work]", "emails"],
        transforms: [],
      },
      displayName: { sources: ["displayName"], transforms: [] },
      extension: { sources: [PBX_EXTENSION], transforms: [] },
      active: { attribute: "active", mode: "bool" },
    },
    groupRules: { roles: [], ringGroups: [] },
    options: {
      absenceRuns: 0,
      disableDevices: null,
      allowAdminFromDirectory: false,
      runTimeoutMinutes: 0,
      userCap: 0,
      loginMethod: "oidc",
      oidcProvider: "entra",
    },
    autoProvision: {
      enabled: false,
      rangeStart: "",
      rangeEnd: "",
      device: true,
      voicemail: true,
      groups: [],
    },
    hasCredential: false,
    status: "ok",
    lastRunAt: AT,
    lastSuccessAt: AT,
    lastErrorCode: "",
    lastErrorDetail: "",
    consecutiveFailures: 0,
    linkedUsers: 3,
    lastFullRunAt: null,
    pausedAt: null,
    createdAt: AT,
    updatedAt: AT,
    ...over,
  };
}

function status(over: Partial<ScimStatus> = {}): ScimStatus {
  return {
    connectorId: 7,
    baseUrl: BASE,
    enabled: true,
    pausedAt: null,
    pausedRunId: null,
    guard: { disables: 1, limit: 5, linked: 3 },
    resources: { users: 4, activeUsers: 3, deletedUsers: 1, groups: 2 },
    lastRequestAt: AT,
    activeTokens: 1,
    adoptExisting: false,
    ...over,
  };
}

function token(id: number, over: Partial<ScimToken> = {}): ScimToken {
  return {
    id,
    connectorId: 7,
    hint: "hello_scim_Ab12",
    createdBy: "user:admin",
    createdAt: AT,
    lastUsedAt: AT,
    revokedAt: null,
    ...over,
  };
}

function setup(
  extra: Parameters<typeof mockApi>[0] = {},
  items = [scimConnector()],
) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "pat", role: "admin" }),
    "GET /api/v1/directory/connectors": () => json({ items }),
    "GET /api/v1/directory/connectors/7/runs": () => json({ items: [] }),
    "GET /api/v1/directory/connectors/7/scim": () => json(status()),
    "GET /api/v1/directory/connectors/7/scim-tokens": () =>
      json({ items: [token(2)] }),
    ...extra,
  });
}

async function openPanel() {
  renderApp("/integrations/directory");
  fireEvent.click(
    await screen.findByRole("button", { name: "Show runs of Entra" }),
  );
  return screen.findByRole("heading", { name: "SCIM of Entra" });
}

describe("Directory: SCIM connectors (spec integrations P10)", () => {
  it("lists a SCIM connector as pushed, with its pause", async () => {
    expect(sourceText(scimConnector())).toMatch(/SCIM/);
    setup({}, [scimConnector({ pausedAt: AT })]);
    renderApp("/integrations/directory");
    const table = await screen.findByRole("table", {
      name: "Directory connectors",
    });
    expect(within(table).getByText("pushed")).toBeVisible();
    expect(within(table).getByText("paused")).toBeVisible();
    // A SCIM connector is tested without a password.
    expect(
      within(table).getByRole("button", {
        name: "Test the connection of Entra",
      }),
    ).toBeEnabled();
  });

  it("creates a SCIM connector with its mapping and adoption switch", async () => {
    const calls = setup({
      "POST /api/v1/directory/connectors": () =>
        json(scimConnector({ id: 8, name: "Okta" }), 201),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: /New SCIM connector/ }),
    );
    const form = await screen.findByRole("form", {
      name: "New SCIM connector",
    });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "Okta" },
    });
    fireEvent.click(
      within(form).getByRole("switch", {
        name: /Take over matching local users/,
      }),
    );
    fireEvent.change(within(form).getByLabelText("Role rules"), {
      target: { value: "grp-sales operator" },
    });
    fireEvent.submit(form);
    expect(
      await within(form).findByText(
        "Choose how these users sign in: they have no local password.",
      ),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(within(form).getByLabelText("How these users sign in"), {
      target: { value: "oidc:google" },
    });
    fireEvent.submit(form);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST")).toBe(true),
    );
    const body = calls.find((c) => c.method === "POST")?.body as Record<
      string,
      unknown
    >;
    expect(body.kind).toBe("scim");
    expect(body.config).toEqual({ adoptExisting: true });
    expect(body.bindPassword).toBeUndefined();
    expect(body.scheduleMinutes).toBeUndefined();
    expect(body.options).toMatchObject({
      loginMethod: "oidc",
      oidcProvider: "google",
    });
    expect((body.mappings as { extension: unknown }).extension).toEqual({
      sources: [PBX_EXTENSION],
      transforms: [],
    });
    expect(body.groupRules).toEqual({
      roles: [{ groupId: "grp-sales", role: "operator" }],
      ringGroups: [],
    });
  });

  it("edits a SCIM connector in the SCIM form", async () => {
    setup();
    renderApp("/integrations/directory");
    fireEvent.click(await screen.findByRole("button", { name: "Edit Entra" }));
    const form = await screen.findByRole("form", { name: "Edit Entra" });
    expect(within(form).queryByLabelText("Bind password")).toBeNull();
    expect(within(form).getByLabelText("Extension")).toHaveValue(PBX_EXTENSION);
    expect(within(form).getByLabelText("How these users sign in")).toHaveValue(
      "oidc:entra",
    );
  });

  it("shows the base URL, what was pushed and the tokens", async () => {
    setup();
    await openPanel();
    expect(await screen.findByText(BASE)).toBeVisible();
    expect(
      screen.getByText("4 users (3 active, 1 deleted), 2 groups"),
    ).toBeVisible();
    expect(screen.getByText("1 of 5 allowed")).toBeVisible();
    const tokens = await screen.findByRole("table", {
      name: "SCIM tokens of Entra",
    });
    expect(within(tokens).getByText("hello_scim_Ab12…")).toBeVisible();
    expect(within(tokens).getByText("active")).toBeVisible();
  });

  it("issues a token and shows it once", async () => {
    const issued: ScimTokenCreated = {
      ...token(3, { lastUsedAt: null, hint: "hello_scim_Zz99" }),
      token: "hello_scim_Zz99-one-time-secret-value",
      baseUrl: BASE,
    };
    const calls = setup({
      "POST /api/v1/directory/connectors/7/scim-token": () => json(issued, 201),
    });
    await openPanel();
    fireEvent.click(await screen.findByRole("button", { name: "Issue token" }));
    const dialog = await screen.findByRole("dialog", { name: "SCIM token" });
    expect(
      within(dialog).getByText("hello_scim_Zz99-one-time-secret-value"),
    ).toBeVisible();
    expect(within(dialog).getByText(BASE)).toBeVisible();
    expect(within(dialog).getByText("Shown once")).toBeVisible();
    expect(
      calls.filter((c) => c.method === "POST" && c.url.endsWith("/scim-token")),
    ).toHaveLength(1);
  });

  it("refuses a third token in the console", async () => {
    setup({
      "GET /api/v1/directory/connectors/7/scim-tokens": () =>
        json({ items: [token(2), token(3)] }),
    });
    await openPanel();
    await screen.findByRole("table", { name: "SCIM tokens of Entra" });
    await waitFor(() =>
      expect(
        screen.getByRole("button", { name: "Issue token" }),
      ).toBeDisabled(),
    );
  });

  it("revokes a token after confirmation", async () => {
    const calls = setup({
      "DELETE /api/v1/directory/scim-tokens/2": () => noContent(),
    });
    await openPanel();
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Revoke token hello_scim_Ab12",
      }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Revoke token" }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "DELETE" &&
            c.url === "/api/v1/directory/scim-tokens/2",
        ),
      ).toBe(true),
    );
  });

  it("resumes a connector the guard paused", async () => {
    const calls = setup({
      "GET /api/v1/directory/connectors/7/scim": () =>
        json(
          status({
            pausedAt: AT,
            pausedRunId: 41,
            guard: { disables: 5, limit: 5, linked: 9 },
          }),
        ),
      "POST /api/v1/directory/runs/41/confirm": () =>
        json({ id: 41, status: "succeeded" }),
    });
    await openPanel();
    expect(await screen.findByText("SCIM writes are paused")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Resume SCIM" }));
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" &&
            c.url === "/api/v1/directory/runs/41/confirm",
        ),
      ).toBe(true),
    );
  });

  it("says when SCIM is not reachable without a public URL", async () => {
    setup({
      "GET /api/v1/directory/connectors/7/scim": () =>
        json(status({ baseUrl: "" })),
    });
    await openPanel();
    expect(await screen.findByText("SCIM is not reachable")).toBeVisible();
  });
});
