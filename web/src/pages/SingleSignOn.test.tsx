import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, renderApp } from "../test/api";

const AT = "2026-11-10T10:00:00Z";

function settings(over: object = {}) {
  return {
    providers: [
      {
        provider: "google",
        enabled: false,
        clientId: "",
        hasClientSecret: false,
        tenant: "",
        domains: [],
        jit: false,
        updatedAt: AT,
      },
      {
        provider: "entra",
        enabled: true,
        clientId: "app-1",
        hasClientSecret: true,
        tenant: "11111111-2222-3333-4444-555555555555",
        domains: [],
        jit: false,
        updatedAt: AT,
      },
    ],
    ssoRequired: false,
    breakGlass: [{ id: 1, username: "admin", enabled: true }],
    redirectUris: {
      google: "https://hello.example/api/v1/auth/oidc/google/callback",
      entra: "https://hello.example/api/v1/auth/oidc/entra/callback",
      directoryConsent: "https://hello.example/api/v1/directory/oauth/callback",
    },
    available: true,
    ...over,
  };
}

function api(role: string, more: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "admin", role }),
    "GET /api/v1/auth/sso": () => json(settings()),
    ...more,
  });
}

describe("SingleSignOn", () => {
  it("is linked from the sidebar and shows redirect URIs and break-glass admins", async () => {
    api("admin");
    renderApp("/integrations/sso");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(
      within(nav).getByRole("link", { name: /Single sign-on/ }),
    ).toBeInTheDocument();
    expect(
      await screen.findByText(
        "https://hello.example/api/v1/auth/oidc/entra/callback",
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText("https://hello.example/api/v1/directory/oauth/callback"),
    ).toBeInTheDocument();
    expect(
      within(
        screen.getByRole("list", { name: "Break-glass administrators" }),
      ).getByText(/admin/),
    ).toBeInTheDocument();
  });

  it("saves a Google provider without echoing a stored secret", async () => {
    const calls = api("admin", {
      "PUT /api/v1/auth/sso": () => json(settings()),
    });
    renderApp("/integrations/sso");
    const form = await screen.findByRole("form", { name: "Google Workspace" });
    fireEvent.change(within(form).getByLabelText("OAuth client id"), {
      target: { value: "cid.apps.googleusercontent.com" },
    });
    fireEvent.change(within(form).getByLabelText("Client secret"), {
      target: { value: "s3cret" },
    });
    fireEvent.change(within(form).getByLabelText("Workspace domains"), {
      target: { value: "corp.test, corp2.test" },
    });
    fireEvent.click(
      within(form).getByRole("switch", { name: /Enable Google Workspace/ }),
    );
    fireEvent.click(
      within(form).getByRole("button", { name: "Save Google Workspace" }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PUT",
        url: "/api/v1/auth/sso",
        body: {
          providers: [
            {
              provider: "google",
              enabled: true,
              clientId: "cid.apps.googleusercontent.com",
              clientSecret: "s3cret",
              domains: ["corp.test", "corp2.test"],
              jit: false,
            },
          ],
        },
      }),
    );
    expect(within(form).getByLabelText("Client secret")).toHaveValue("");
  });

  it("keeps the stored Entra secret when the field is left empty and shows field errors", async () => {
    const calls = api("admin", {
      "PUT /api/v1/auth/sso": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid single sign-on settings",
              fields: [
                {
                  path: "providers[0].tenant",
                  message: "use your own tenant id",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/integrations/sso");
    const form = await screen.findByRole("form", {
      name: "Microsoft Entra ID",
    });
    fireEvent.change(within(form).getByLabelText("Directory (tenant) id"), {
      target: { value: "common" },
    });
    fireEvent.click(
      within(form).getByRole("button", { name: "Save Microsoft Entra ID" }),
    );
    expect(
      await within(form).findByText("use your own tenant id"),
    ).toBeInTheDocument();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toEqual({
      providers: [
        {
          provider: "entra",
          enabled: true,
          clientId: "app-1",
          tenant: "common",
          jit: false,
        },
      ],
    });
  });

  it("tests a provider", async () => {
    api("admin", {
      "POST /api/v1/auth/sso/entra/test": () =>
        json({
          ok: true,
          issuer: "https://login.microsoftonline.com/t/v2.0",
          keys: 3,
          errorDetail: "",
        }),
    });
    renderApp("/integrations/sso");
    fireEvent.click(
      await screen.findByRole("button", { name: "Test Microsoft Entra ID" }),
    );
    expect(
      await screen.findByText(/publishes 3 signing key/),
    ).toBeInTheDocument();
  });

  it("requires single sign-on and explains a refusal", async () => {
    const calls = api("admin", {
      "PUT /api/v1/auth/sso": () =>
        apiError(
          409,
          "no_break_glass",
          "flag a break-glass administrator first",
        ),
    });
    renderApp("/integrations/sso");
    fireEvent.click(
      await screen.findByRole("switch", { name: /Require single sign-on/ }),
    );
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PUT",
        url: "/api/v1/auth/sso",
        body: { ssoRequired: true },
      }),
    );
    expect(
      await screen.findByText(/flag a break-glass administrator first/),
    ).toBeInTheDocument();
  });

  it("is off without a public URL", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => json({ username: "admin", role: "admin" }),
      "GET /api/v1/auth/sso": () =>
        json(settings({ available: false, redirectUris: {} })),
    });
    renderApp("/integrations/sso");
    expect(
      await screen.findByText(/It needs HELLO_PUBLIC_URL/),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("switch", { name: /Require single sign-on/ }),
    ).toBeDisabled();
  });

  it("is for administrators only", async () => {
    const calls = api("operator");
    renderApp("/integrations/sso");
    await screen.findByRole("navigation", { name: "Primary" });
    expect(calls.some((c) => c.url === "/api/v1/auth/sso")).toBe(false);
  });
});
