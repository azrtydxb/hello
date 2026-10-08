import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, mockApi, noContent, renderApp } from "../test/api";
import { browser } from "./Consent";

const REQUEST = {
  id: "req-1",
  clientId: "https://agent.example.com/oauth/client.json",
  clientName: "Hello Assistant",
  clientUri: "",
  clientHost: "agent.example.com",
  redirectUri: "http://127.0.0.1:43123/callback",
  redirectHost: "127.0.0.1:43123",
  verified: true,
  scopes: ["read", "write", "admin", "secrets"],
  resources: ["https://hello.test/mcp"],
  expiresAt: "2026-10-08T10:10:00Z",
};

const AT = "/oauth/consent?request=req-1";

const GRANTABLE: Record<string, string[]> = {
  viewer: ["read"],
  operator: ["read", "write"],
  admin: ["read", "write", "admin", "secrets"],
};

function setup(role: "viewer" | "operator" | "admin", extra = {}) {
  const go = vi.spyOn(browser, "go").mockImplementation(() => {});
  const calls = mockApi({
    "GET /api/v1/auth/me": () => json({ username: "pat", role }),
    "GET /api/v1/oauth/requests/req-1": () =>
      json({ ...REQUEST, grantableScopes: GRANTABLE[role] }),
    ...extra,
  });
  return { go, calls };
}

const scope = (name: string) =>
  screen.getByRole("checkbox", { name: new RegExp(`^${name}(?![a-z])`) });

describe("Consent", () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("shows the client host, pre-checks all but secrets, and approves the narrowed scopes", async () => {
    const { go, calls } = setup("admin", {
      "POST /api/v1/oauth/requests/req-1/approve": () =>
        json({
          redirect:
            "http://127.0.0.1:43123/callback?code=c&state=s&iss=https%3A%2F%2Fhello.test",
        }),
    });
    renderApp(AT);

    expect(await screen.findByTestId("client-host")).toHaveTextContent(
      "agent.example.com",
    );
    expect(
      screen.getByRole("heading", {
        level: 1,
        name: "Hello Assistant wants access to Hello",
      }),
    ).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Resources" })).toHaveTextContent(
      "https://hello.test/mcp",
    );
    expect(scope("read")).toBeChecked();
    expect(scope("write")).toBeChecked();
    expect(scope("admin")).toBeChecked();
    expect(scope("secrets")).not.toBeChecked();
    expect(
      screen.getByText("secrets shows plaintext credentials"),
    ).toBeVisible();

    // The user narrows: no admin.
    fireEvent.click(scope("admin"));
    fireEvent.click(screen.getByRole("button", { name: "Approve" }));

    await waitFor(() =>
      expect(go).toHaveBeenCalledWith(
        "http://127.0.0.1:43123/callback?code=c&state=s&iss=https%3A%2F%2Fhello.test",
      ),
    );
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/oauth/requests/req-1/approve",
      body: { scopes: ["read", "write"] },
    });
  });

  it("disables the scopes a viewer cannot grant and never sends them", async () => {
    const { calls } = setup("viewer", {
      "POST /api/v1/oauth/requests/req-1/approve": () =>
        json({ redirect: "http://127.0.0.1:43123/callback?code=c" }),
    });
    renderApp(AT);

    await screen.findByTestId("client-host");
    expect(scope("read")).toBeEnabled();
    expect(scope("read")).toBeChecked();
    for (const s of ["write", "admin", "secrets"]) {
      expect(scope(s)).toBeDisabled();
      expect(scope(s)).not.toBeChecked();
    }
    fireEvent.click(scope("write"));
    expect(scope("write")).not.toBeChecked();
    expect(
      screen.getAllByText(/Your role \(Viewer\) cannot grant this/),
    ).toHaveLength(3);

    fireEvent.click(screen.getByRole("button", { name: "Approve" }));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "POST",
        url: "/api/v1/oauth/requests/req-1/approve",
        body: { scopes: ["read"] },
      }),
    );
  });

  it("denies and sends the browser to the redirect with access_denied", async () => {
    const denied =
      "http://127.0.0.1:43123/callback?error=access_denied&state=s&iss=https%3A%2F%2Fhello.test";
    const { go, calls } = setup("operator", {
      "POST /api/v1/oauth/requests/req-1/deny": () =>
        json({ redirect: denied }),
    });
    renderApp(AT);

    await screen.findByTestId("client-host");
    fireEvent.click(screen.getByRole("button", { name: "Deny" }));
    await waitFor(() => expect(go).toHaveBeenCalledWith(denied));
    expect(go.mock.calls[0]?.[0]).toContain("error=access_denied");
    expect(calls.some((c) => c.url.endsWith("/approve"))).toBe(false);
  });

  it("marks an unverified client and explains an expired request", async () => {
    setup("admin", {
      "GET /api/v1/oauth/requests/req-1": [
        () =>
          json({
            ...REQUEST,
            grantableScopes: GRANTABLE.admin,
            verified: false,
            clientId: "hello_dcr_x1",
            clientHost: "hello_dcr_x1",
          }),
      ],
    });
    renderApp(AT);
    expect(await screen.findByTestId("client-host")).toHaveTextContent(
      "hello_dcr_x1",
    );
    expect(screen.getByText("Unverified")).toBeVisible();
  });

  it("shows an error when the request is gone", async () => {
    setup("admin", {
      "GET /api/v1/oauth/requests/req-1": () =>
        apiError(404, "not_found", "request: not found"),
    });
    renderApp(AT);
    const alert = await screen.findByText("Could not load the request");
    expect(alert.closest(".az-alert")).toHaveTextContent("start again");
    expect(screen.queryByRole("button", { name: "Approve" })).toBeNull();
  });

  it("returns to the consent page after signing in", async () => {
    const unauthorized = () => apiError(401, "unauthorized", "not signed in");
    mockApi({
      "GET /api/v1/auth/me": [unauthorized, () => json({ username: "pat" })],
      "POST /api/v1/auth/login": noContent,
      "GET /api/v1/oauth/requests/req-1": () =>
        json({ ...REQUEST, grantableScopes: GRANTABLE.admin }),
    });
    renderApp(AT);

    await screen.findByText(
      "Sign in to review an app's request for access to Hello.",
    );
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/login?next=%2Foauth%2Fconsent%3Frequest%3Dreq-1",
    );
    fireEvent.change(screen.getByLabelText("Username"), {
      target: { value: "pat" },
    });
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "s3cret-pass" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    const host = await screen.findByTestId("client-host");
    expect(within(host).getByText("agent.example.com")).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(AT);
  });
});
