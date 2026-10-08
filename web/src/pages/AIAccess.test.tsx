import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, noContent, renderApp } from "../test/api";

const SETTINGS = {
  enabled: true,
  publicUrl: "https://pbx.example.net",
  issuer: "https://pbx.example.net",
  apiUrl: "https://pbx.example.net/api/v1",
  mcpUrl: "https://pbx.example.net/mcp",
  authorizationServerMetadataUrl:
    "https://pbx.example.net/.well-known/oauth-authorization-server",
  apiMetadataUrl:
    "https://pbx.example.net/.well-known/oauth-protected-resource/api/v1",
  mcpMetadataUrl:
    "https://pbx.example.net/.well-known/oauth-protected-resource/mcp",
  protocolVersions: ["2026-07-28", "2025-11-25"],
  dcr: true,
  scopes: [
    { name: "read", description: "Read everything." },
    { name: "write", description: "Change configuration." },
  ],
};

const GRANT = {
  id: 1,
  userId: 1,
  username: "pat",
  clientId: "https://agent.example.com/client.json",
  clientName: "Hello Assistant",
  clientHost: "agent.example.com",
  verified: true,
  scopes: ["read", "write"],
  resources: ["https://pbx.example.net/mcp"],
  createdAt: "2026-10-01T10:00:00Z",
  lastUsedAt: null,
};

const ACCOUNT = {
  id: "hello_sa_4",
  name: "nightly-report",
  description: "",
  role: "viewer",
  scopes: ["read"],
  enabled: true,
  createdAt: "2026-10-01T10:00:00Z",
  lastUsedAt: null,
  secrets: [],
};

const TOKEN = {
  id: 7,
  name: "grafana-read",
  kind: "personal",
  scopes: ["read"],
  expiresAt: "2027-01-01T00:00:00Z",
  createdAt: "2026-09-12T10:00:00Z",
  lastUsedAt: null,
};

function setup(role: string, extra = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "pat", role }),
    "GET /api/v1/ai/settings": () => json(SETTINGS),
    "GET /api/v1/oauth/grants": () => json({ items: [GRANT] }),
    "GET /api/v1/service-accounts": () => json({ items: [ACCOUNT] }),
    "GET /api/v1/tokens": () => json({ items: [TOKEN] }),
    "GET /api/v1/skills": () =>
      json({
        items: [
          {
            name: "hello-setup",
            description: "Set up users.",
            version: "v1",
          },
        ],
      }),
    ...extra,
  });
}

describe("AIAccess", () => {
  it("shows the MCP URL that GET /ai/settings returns, the scopes and the skills", async () => {
    setup("viewer");
    renderApp("/ai");

    const url = await screen.findByText("https://pbx.example.net/mcp");
    expect(url.closest(".az-code")).toHaveTextContent("MCP server URL");
    expect(
      screen.getByText(
        "claude mcp add --transport http hello https://pbx.example.net/mcp",
      ),
    ).toBeInTheDocument();
    const scopes = screen.getByRole("list", { name: "What each scope allows" });
    expect(scopes).toHaveTextContent("Change configuration.");

    const download = await screen.findByRole("link", {
      name: "Download hello-setup",
    });
    expect(download).toHaveAttribute(
      "href",
      "/api/v1/skills/hello-setup/download",
    );
  });

  it("revokes a connected app through the API after confirming", async () => {
    const calls = setup("viewer", {
      "GET /api/v1/oauth/grants": [
        () => json({ items: [GRANT] }),
        () => json({ items: [] }),
      ],
      "DELETE /api/v1/oauth/grants/1": noContent,
    });
    renderApp("/ai");

    const apps = await screen.findByRole("list", { name: "Connected apps" });
    expect(apps).toHaveTextContent("Hello Assistant");
    fireEvent.click(
      within(apps).getByRole("button", { name: "Revoke Hello Assistant" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "Revoke Hello Assistant?",
    });
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Revoke access" }),
    );
    await screen.findByText("No connected apps");
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/oauth/grants/1",
      body: undefined,
    });
  });

  it("hides service accounts and tokens from a non-admin", async () => {
    const calls = setup("operator");
    renderApp("/ai");
    await screen.findByRole("list", { name: "Connected apps" });
    expect(screen.queryByText("Service accounts")).toBeNull();
    expect(screen.queryByText("API tokens")).toBeNull();
    expect(calls.some((c) => c.url === "/api/v1/tokens")).toBe(false);
    expect(calls.some((c) => c.url === "/api/v1/service-accounts")).toBe(false);
  });

  it("shows a service-account secret once", async () => {
    const calls = setup("admin", {
      "GET /api/v1/service-accounts": [
        () => json({ items: [ACCOUNT] }),
        () =>
          json({
            items: [
              {
                ...ACCOUNT,
                secrets: [
                  {
                    id: 11,
                    createdAt: "2026-10-08T09:00:00Z",
                    expiresAt: null,
                    lastUsedAt: null,
                  },
                ],
              },
            ],
          }),
      ],
      "POST /api/v1/service-accounts/hello_sa_4/secrets": () =>
        json(
          {
            id: 11,
            clientId: "hello_sa_4",
            secret: "hello_cs_abSECRET",
            createdAt: "2026-10-08T09:00:00Z",
            expiresAt: null,
          },
          201,
        ),
    });
    renderApp("/ai");

    const list = await screen.findByRole("list", { name: "Service accounts" });
    fireEvent.click(
      within(list).getByRole("button", {
        name: "New secret for nightly-report",
      }),
    );
    const shown = await screen.findByRole("dialog", { name: "Secret created" });
    expect(within(shown).getByLabelText(/Client secret/)).toHaveValue(
      "hello_cs_abSECRET",
    );
    expect(within(shown).getByText("Shown once")).toBeVisible();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(1);

    fireEvent.click(within(shown).getByRole("button", { name: "Done" }));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Secret created" }),
      ).toBeNull(),
    );
    expect(document.body).not.toHaveTextContent("hello_cs_abSECRET");
    expect(await screen.findByText(/Secret #11 created/)).toBeInTheDocument();
  });

  it("creates a scoped personal token with an expiry and shows it once", async () => {
    const calls = setup("admin", {
      "GET /api/v1/tokens": [
        () => json({ items: [TOKEN] }),
        () =>
          json({
            items: [
              TOKEN,
              { ...TOKEN, id: 8, name: "ci", scopes: ["read", "write"] },
            ],
          }),
      ],
      "POST /api/v1/tokens": () =>
        json(
          {
            ...TOKEN,
            id: 8,
            name: "ci",
            scopes: ["read", "write"],
            token: "hello_pat_SECRET",
          },
          201,
        ),
    });
    renderApp("/ai");

    const list = await screen.findByRole("list", { name: "API tokens" });
    expect(list).toHaveTextContent("grafana-read");
    expect(list).toHaveTextContent("expires");

    fireEvent.click(screen.getByRole("button", { name: "New token" }));
    const create = await screen.findByRole("dialog", { name: "New API token" });
    fireEvent.change(within(create).getByLabelText("Name"), {
      target: { value: "ci" },
    });
    fireEvent.click(
      within(create).getByRole("checkbox", { name: /^write(?![a-z])/ }),
    );
    fireEvent.click(
      within(create).getByRole("button", { name: "Create token" }),
    );

    const shown = await screen.findByRole("dialog", { name: "Token created" });
    expect(within(shown).getByLabelText(/API token/)).toHaveValue(
      "hello_pat_SECRET",
    );
    const post = calls.find((c) => c.method === "POST");
    const body = post?.body as {
      name: string;
      scopes: string[];
      expiresAt?: string;
    };
    expect(body.name).toBe("ci");
    expect(body.scopes).toEqual(["read", "write"]);
    expect(Date.parse(body.expiresAt ?? "")).toBeGreaterThan(Date.now());

    fireEvent.click(within(shown).getByRole("button", { name: "Done" }));
    await waitFor(() =>
      expect(
        screen.queryByRole("dialog", { name: "Token created" }),
      ).toBeNull(),
    );
    expect(document.body).not.toHaveTextContent("hello_pat_SECRET");
  });
});
