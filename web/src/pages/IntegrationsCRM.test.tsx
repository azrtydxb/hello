import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, mockApi, noContent, renderApp } from "../test/api";
import type { CRMConnector, CRMLog } from "../api/crm";

function me(role: string) {
  return { "GET /api/v1/auth/me": () => json({ username: "pat", role }) };
}

function connector(extra: Partial<CRMConnector> = {}): CRMConnector {
  return {
    id: 1,
    kind: "hubspot",
    name: "HubSpot",
    enabled: true,
    config: { clientId: "cid-1", region: "na" },
    hasClientSecret: true,
    recordings: false,
    linkTtlHours: 720,
    logMissed: true,
    fallbackOwner: "",
    createdAt: "2026-11-01T10:00:00Z",
    capabilities: {
      lookup: true,
      logCalls: true,
      recordings: true,
      clickToCall: true,
      screenPop: true,
      ticket: false,
    },
    connection: {
      status: "connected",
      externalAccount: "4711",
      grantedScopes: ["crm.objects.contacts.read"],
      updatedAt: "2026-11-02T10:00:00Z",
    },
    redirectUri: "https://hello.example/api/v1/integrations/crm/oauth/callback",
    ...extra,
  };
}

function log(
  id: number,
  status: CRMLog["status"],
  extra: Partial<CRMLog> = {},
): CRMLog {
  return {
    id,
    connectorId: 1,
    callId: `call-${id}`,
    direction: "inbound",
    status,
    externalId: "",
    attempts: 0,
    nextAttemptAt: "2026-11-10T10:00:00Z",
    lastError: "",
    eventType: "call.completed",
    ownerExtension: "101",
    createdAt: "2026-11-10T09:00:00Z",
    updatedAt: "2026-11-10T09:00:00Z",
    ...extra,
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("CRM connectors (spec integrations P6)", () => {
  it("is linked from the sidebar for administrators only", async () => {
    mockApi({
      ...me("operator"),
      "GET /api/v1/integrations/crm": () => json({ items: [] }),
    });
    const view = renderApp("/integrations/events");
    await screen.findByRole("link", { name: "Events" });
    expect(screen.queryByRole("link", { name: "CRM" })).toBeNull();
    view.unmount();
    vi.unstubAllGlobals();
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [] }),
    });
    renderApp("/integrations/events");
    expect(await screen.findByRole("link", { name: "CRM" })).toHaveAttribute(
      "href",
      "/integrations/crm",
    );
  });

  it("shows the connection state and the HubSpot app values to register", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [connector()] }),
    });
    renderApp("/integrations/crm");
    const panel = await screen.findByRole("article", {
      name: "HubSpot connector",
    });
    expect(within(panel).getByText("Connected")).toBeVisible();
    expect(within(panel).getByText("4711")).toBeVisible();
    expect(
      within(panel).getAllByText(
        /hello\.example\/api\/v1\/integrations\/crm\/oauth\/callback/,
      ).length,
    ).toBeGreaterThan(0);
    expect(
      within(panel).getByText(/crm\.objects\.owners\.read/),
    ).toBeInTheDocument();
  });

  it("warns when the connection needs signing in again", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () =>
        json({
          items: [
            connector({
              connection: {
                status: "needs_reauth",
                grantedScopes: [],
                lastError: "the CRM refused the refresh token",
                updatedAt: "2026-11-02T10:00:00Z",
              },
            }),
          ],
        }),
    });
    renderApp("/integrations/crm");
    expect(
      await screen.findByText("This connection needs signing in again"),
    ).toBeVisible();
    expect(screen.getByText(/refused the refresh token/)).toBeVisible();
    expect(screen.getByRole("button", { name: "Connect again" })).toBeEnabled();
  });

  it("starts the sign-in and sends the browser to the CRM", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () =>
        json({ items: [connector({ connection: null })] }),
      "POST /api/v1/integrations/crm/1/connect": () =>
        json({
          authorizeUrl: "https://app.hubspot.com/oauth/authorize?state=abc",
        }),
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("button", { name: "Connect" }));
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://app.hubspot.com/oauth/authorize?state=abc",
      ),
    );
  });

  it("cannot connect without HELLO_PUBLIC_URL or credentials", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () =>
        json({
          items: [connector({ connection: null, redirectUri: undefined })],
        }),
    });
    renderApp("/integrations/crm");
    expect(
      await screen.findByRole("button", { name: "Connect" }),
    ).toBeDisabled();
  });

  it("shows why a sign-in failed from the redirect", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [] }),
    });
    renderApp("/integrations/crm?error=state");
    expect(
      await screen.findByText(/expired or was already used/),
    ).toBeVisible();
  });

  it("saves settings without sending the secret unless it was typed, and needs it for a new client id", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [connector()] }),
      "PATCH /api/v1/integrations/crm/1": () =>
        json(connector({ recordings: true })),
    });
    renderApp("/integrations/crm");
    const form = await screen.findByRole("form", { name: "HubSpot settings" });
    fireEvent.click(
      within(form).getByRole("switch", { name: /Attach recording links/ }),
    );
    fireEvent.change(
      within(form).getByLabelText("Recording link lifetime (days)"),
      { target: { value: "14" } },
    );
    fireEvent.click(
      within(form).getByRole("button", { name: "Save settings" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    const patch = calls.find((c) => c.method === "PATCH")!.body as Record<
      string,
      unknown
    >;
    expect(patch).toMatchObject({
      recordings: true,
      linkTtlHours: 336,
      config: { clientId: "cid-1", region: "na" },
    });
    expect(patch).not.toHaveProperty("clientSecret");

    fireEvent.change(within(form).getByLabelText("Client ID"), {
      target: { value: "other" },
    });
    expect(within(form).getByLabelText("Client secret")).toBeRequired();
  });

  it("maps a user by hand and shows unmapped users", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [connector()] }),
      "GET /api/v1/integrations/crm/1/users": () =>
        json({
          items: [
            {
              userId: 7,
              username: "anna",
              email: "anna@example.com",
              extension: "101",
              externalUserId: "55",
              source: "email",
            },
            { userId: 8, username: "bob", email: "", extension: "102" },
          ],
        }),
      "PUT /api/v1/integrations/crm/1/users/8": () => noContent(),
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("tab", { name: "Users" }));
    const table = await screen.findByRole("table", {
      name: "HubSpot user mapping",
    });
    expect(within(table).getByText("unmapped")).toBeVisible();
    expect(within(table).getByText("email")).toBeVisible();
    fireEvent.change(within(table).getByLabelText("CRM user id of bob"), {
      target: { value: "99" },
    });
    fireEvent.click(within(table).getAllByRole("button", { name: "Save" })[1]!);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")!.body).toEqual({
      externalUserId: "99",
    });
  });

  it("lists the dead letters and retries one", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [connector()] }),
      "GET /api/v1/integrations/crm/1/logs?limit=50": () =>
        json({
          items: [
            log(3, "logged", { externalId: "9001" }),
            log(2, "dead", {
              attempts: 8,
              lastError: "crm: status 400: bad property",
            }),
            log(1, "skipped", {
              lastError: "no CRM user has the email of anna",
            }),
          ],
          nextBefore: 0,
        }),
      "POST /api/v1/integrations/crm/logs/2/retry": () =>
        json(log(2, "pending")),
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("tab", { name: "Calls" }));
    const table = await screen.findByRole("table", {
      name: "HubSpot call log",
    });
    expect(
      within(table).getByText("crm: status 400: bad property"),
    ).toBeVisible();
    expect(
      within(table).getByText("no CRM user has the email of anna"),
    ).toBeVisible();
    expect(
      within(table).getAllByRole("button", { name: "Retry" }),
    ).toHaveLength(2);
    fireEvent.click(
      within(table).getAllByRole("button", { name: "Retry" })[0]!,
    );
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/logs/2/retry"))).toBe(true),
    );
  });

  it("filters the call log by status", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [connector()] }),
      "GET /api/v1/integrations/crm/1/logs?limit=50": () =>
        json({ items: [], nextBefore: 0 }),
      "GET /api/v1/integrations/crm/1/logs?limit=50&status=dead": () =>
        json({ items: [log(2, "dead")], nextBefore: 0 }),
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("tab", { name: "Calls" }));
    await screen.findByText("Nothing here yet");
    fireEvent.change(screen.getByLabelText("Show"), {
      target: { value: "dead" },
    });
    await screen.findByRole("table", { name: "HubSpot call log" });
    expect(calls.some((c) => c.url.includes("status=dead"))).toBe(true);
  });

  it("adds a connector and reports the API's refusal", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [] }),
      "POST /api/v1/integrations/crm": [
        apiError(
          409,
          "conflict",
          "CRM connector: a connector with that name exists",
        ),
        () => json(connector(), 201),
      ],
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("button", { name: "Add a CRM" }));
    const form = screen.getByRole("form", { name: "New CRM connector" });
    fireEvent.change(within(form).getByLabelText("Client ID"), {
      target: { value: "cid" },
    });
    fireEvent.change(within(form).getByLabelText("Client secret"), {
      target: { value: "shh" },
    });
    fireEvent.click(
      within(form).getByRole("button", { name: "Add connector" }),
    );
    expect(
      await screen.findByText(/a connector with that name exists/),
    ).toBeVisible();
    fireEvent.click(
      within(form).getByRole("button", { name: "Add connector" }),
    );
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "POST")).toHaveLength(2),
    );
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({
      kind: "hubspot",
      name: "HubSpot",
      clientSecret: "shh",
      config: { clientId: "cid", region: "na" },
    });
  });

  it("adds an Odoo connector with its server, database, login and key", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [] }),
      "POST /api/v1/integrations/crm": () =>
        json({ ...connector({ kind: "odoo", auth: "credential" }) }, 201),
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("button", { name: "Add a CRM" }));
    const form = screen.getByRole("form", { name: "New CRM connector" });
    fireEvent.change(within(form).getByLabelText("CRM"), {
      target: { value: "odoo" },
    });
    expect(within(form).getByLabelText("Name")).toHaveValue("Odoo");
    fireEvent.change(
      within(form).getByLabelText("Login of the API key's user"),
      { target: { value: "me@x.com" } },
    );
    fireEvent.change(within(form).getByLabelText("API key"), {
      target: { value: "k" },
    });
    fireEvent.change(within(form).getByLabelText("Odoo address"), {
      target: { value: "https://odoo.example.com" },
    });
    fireEvent.change(within(form).getByLabelText("Database"), {
      target: { value: "prod" },
    });
    fireEvent.click(
      within(form).getByRole("button", { name: "Add connector" }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")).toBeTruthy(),
    );
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({
      kind: "odoo",
      name: "Odoo",
      clientSecret: "k",
      config: {
        clientId: "me@x.com",
        url: "https://odoo.example.com",
        db: "prod",
      },
    });
  });

  it("a Bitrix24 connector asks for the webhook only and sends no client id", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [] }),
      "POST /api/v1/integrations/crm": () =>
        json({ ...connector({ kind: "bitrix24", auth: "credential" }) }, 201),
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("button", { name: "Add a CRM" }));
    const form = screen.getByRole("form", { name: "New CRM connector" });
    fireEvent.change(within(form).getByLabelText("CRM"), {
      target: { value: "bitrix24" },
    });
    expect(within(form).queryByLabelText("Client ID")).toBeNull();
    fireEvent.change(within(form).getByLabelText("Inbound webhook address"), {
      target: { value: "https://p.bitrix24.com/rest/1/abc/" },
    });
    fireEvent.click(
      within(form).getByRole("button", { name: "Add connector" }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")).toBeTruthy(),
    );
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({
      kind: "bitrix24",
      name: "Bitrix24",
      clientSecret: "https://p.bitrix24.com/rest/1/abc/",
      config: {},
    });
  });

  it("a credential connector connects on the spot, without leaving the page", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () =>
        json({
          items: [
            connector({
              kind: "odoo",
              auth: "credential",
              config: {
                clientId: "me@x.com",
                url: "https://o.example",
                db: "d",
              },
              connection: null,
              redirectUri: undefined,
            }),
          ],
        }),
      "POST /api/v1/integrations/crm/1/connect": () =>
        json({ authorizeUrl: "", connected: true }),
    });
    renderApp("/integrations/crm");
    expect(screen.queryByText("Redirect URI")).toBeNull();
    const btn = await screen.findByRole("button", { name: "Connect" });
    expect(btn).toBeEnabled();
    fireEvent.click(btn);
    await waitFor(() =>
      expect(
        calls.some((c) => c.method === "POST" && c.url.endsWith("/connect")),
      ).toBe(true),
    );
    expect(assign).not.toHaveBeenCalled();
  });

  it("changing the Odoo address asks for the key again", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () =>
        json({
          items: [
            connector({
              kind: "odoo",
              auth: "credential",
              config: {
                clientId: "me@x.com",
                url: "https://o.example",
                db: "d",
              },
            }),
          ],
        }),
    });
    renderApp("/integrations/crm");
    const url = await screen.findByLabelText("Odoo address");
    const key = screen.getByLabelText("API key");
    expect(key).not.toBeRequired();
    fireEvent.change(url, { target: { value: "https://other.example" } });
    expect(key).toBeRequired();
  });

  it("disconnects after a confirmation", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () => json({ items: [connector()] }),
      "DELETE /api/v1/integrations/crm/1/connection": () => noContent(),
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("button", { name: "Disconnect" }));
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(
      within(await screen.findByRole("dialog")).getByRole("button", {
        name: "Disconnect",
      }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });
});
