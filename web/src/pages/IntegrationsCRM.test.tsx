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
    auth: "oauth",
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

  it("adds a Salesforce connector without credentials and shows its call center", async () => {
    let created = false;
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () =>
        json({
          items: created
            ? [
                connector({
                  id: 2,
                  kind: "salesforce",
                  auth: "direct",
                  name: "Salesforce",
                  hasClientSecret: false,
                  connection: null,
                  config: {
                    serverLogging: "false",
                    apiVersion: "62.0",
                    domain: "",
                  },
                }),
              ]
            : [],
        }),
      "POST /api/v1/integrations/crm": () => {
        created = true;
        return json({}, 201);
      },
      "GET /api/v1/integrations/crm/2/setup": () =>
        json({
          items: [
            {
              name: "callCenterXml",
              label: "Call Center definition",
              kind: "file",
              value: "<callCenter/>",
              filename: "HelloSoftphone.xml",
              hint: "Setup, Call Centers, Import.",
            },
            {
              name: "trustedSite",
              label: "CSP Trusted Site",
              kind: "url",
              value: "https://hello.example",
            },
          ],
        }),
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("button", { name: "Add a CRM" }));
    const form = await screen.findByRole("form", { name: "New CRM connector" });
    fireEvent.change(within(form).getByLabelText("CRM"), {
      target: { value: "salesforce" },
    });
    expect(within(form).getByLabelText("Name")).toHaveValue("Salesforce");
    // Without server-side logging nothing needs a credential.
    expect(within(form).getByLabelText("Client ID")).not.toBeRequired();
    expect(within(form).getByLabelText("Client secret")).not.toBeRequired();
    fireEvent.click(
      within(form).getByRole("switch", { name: /Also log calls/ }),
    );
    expect(within(form).getByLabelText("Client ID")).toBeRequired();
    fireEvent.click(
      within(form).getByRole("switch", { name: /Also log calls/ }),
    );
    fireEvent.click(
      within(form).getByRole("button", { name: "Add connector" }),
    );
    await waitFor(() => expect(created).toBe(true));
    expect(calls.find((c) => c.method === "POST")!.body).toEqual({
      kind: "salesforce",
      name: "Salesforce",
      config: {
        serverLogging: "false",
        apiVersion: "62.0",
        domain: "",
        clientId: "",
      },
    });
    expect(await screen.findByText("Set up Salesforce")).toBeVisible();
    expect(screen.getByText("https://hello.example")).toBeVisible();
    expect(
      screen.getByRole("button", { name: /Download Call Center definition/ }),
    ).toBeVisible();
    // No sign-in to start for the softphone alone.
    expect(screen.queryByRole("button", { name: "Connect" })).toBeNull();
  });

  it("connects a client-credentials connector without leaving the page", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign });
    let connectCalls = 0;
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/crm": () =>
        json({
          items: [
            connector({
              id: 3,
              kind: "dynamics",
              auth: "direct",
              name: "Dynamics 365",
              connection: null,
              config: {
                clientId: "app",
                orgUrl: "https://contoso.crm4.dynamics.com",
                tenantId: "11111111-2222-3333-4444-555555555555",
              },
            }),
          ],
        }),
      "GET /api/v1/integrations/crm/3/setup": () => json({ items: [] }),
      "POST /api/v1/integrations/crm/3/connect": () => {
        connectCalls++;
        return json({ authorizeUrl: "", connected: true });
      },
    });
    renderApp("/integrations/crm");
    fireEvent.click(await screen.findByRole("button", { name: "Connect" }));
    await waitFor(() => expect(connectCalls).toBe(1));
    expect(assign).not.toHaveBeenCalled();
    const form = await screen.findByRole("form", {
      name: "Dynamics 365 settings",
    });
    expect(within(form).getByLabelText("Environment URL")).toHaveValue(
      "https://contoso.crm4.dynamics.com",
    );
  });
});
