import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, noContent, renderApp } from "../test/api";

const UNAUTHORIZED = () => apiError(401, "unauthorized", "not signed in");
const VERSION = {
  "GET /api/v1/version": () =>
    json({ version: "v0.9.0-test", commit: "abc1234", configRevision: 3 }),
};
const OFF = {
  enabled: false,
  pending: false,
  required: false,
  recoveryCodesLeft: 0,
};
const ON = {
  enabled: true,
  pending: false,
  required: false,
  recoveryCodesLeft: 10,
};

function type(label: string | RegExp, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Login second step", () => {
  function api(extra = {}) {
    return mockApi({
      ...VERSION,
      "GET /api/v1/auth/me": [
        UNAUTHORIZED,
        () => json({ username: "sam", role: "viewer" }),
      ],
      "POST /api/v1/auth/login": () =>
        json({ mfa: "required", mfaToken: "chal-1" }, 202),
      "GET /api/v1/cdrs?limit=50": () => json({ items: [], next: "" }),
      ...extra,
    });
  }

  async function passwordStep() {
    await screen.findByRole("heading", { level: 1, name: "Welcome back" });
    type("Username", "sam");
    type("Password", "right-password");
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
    return screen.findByLabelText("Authentication code");
  }

  it("asks for a code after the password and signs in with it", async () => {
    const calls = api({ "POST /api/v1/auth/login/2fa": noContent });
    renderApp("/login?next=%2Fhistory");
    await passwordStep();
    // The password field is gone: no session exists yet.
    expect(screen.queryByLabelText("Password")).toBeNull();
    type("Authentication code", "123456");
    fireEvent.click(screen.getByRole("button", { name: "Verify" }));
    expect(
      await screen.findByRole("heading", { level: 1, name: "Call history" }),
    ).toBeInTheDocument();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/auth/login/2fa",
      body: { mfaToken: "chal-1", code: "123456" },
    });
  });

  it("says a wrong code is wrong without naming a cause, and lets it retry", async () => {
    api({
      "POST /api/v1/auth/login/2fa": () =>
        apiError(401, "unauthorized", "invalid or expired code"),
    });
    renderApp("/login");
    await passwordStep();
    type("Authentication code", "000000");
    fireEvent.click(screen.getByRole("button", { name: "Verify" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("The code is incorrect or has expired");
    expect(alert).not.toHaveTextContent(/clock|time/i);
    expect(screen.getByLabelText("Authentication code")).toHaveValue("");
  });

  it("goes back to the password step", async () => {
    api();
    renderApp("/login");
    await passwordStep();
    fireEvent.click(
      screen.getByRole("button", { name: "Use a different account" }),
    );
    expect(await screen.findByLabelText("Password")).toBeInTheDocument();
  });
});

describe("Profile two-factor card", () => {
  const profile = {
    "GET /api/v1/auth/me": () => json({ username: "sam", role: "user" }),
  };

  it("enrols with a QR code, shows the recovery codes once and then reports 2FA on", async () => {
    let state = OFF;
    const calls = mockApi({
      ...profile,
      "GET /api/v1/me/extensions": () => json({ items: [] }),
      "GET /api/v1/me/2fa": () => json(state),
      "POST /api/v1/me/2fa/setup": () =>
        json({
          secret: "JBSWY3DPEHPK3PXP",
          otpauthUri: "otpauth://totp/Hello%20PBX:sam?secret=JBSWY3DPEHPK3PXP",
        }),
      "POST /api/v1/me/2fa/enable": () => {
        state = ON;
        return json({
          recoveryCodes: ["AAAA-BBBB-CCCC-DDDD", "EEEE-FFFF-GGGG-HHHH"],
        });
      },
    });
    renderApp("/me/profile");
    const card = await screen.findByRole("region", {
      name: "Two-factor authentication",
    });
    fireEvent.click(
      await within(card).findByRole("button", { name: "Set up" }),
    );
    expect(
      await screen.findByRole("img", { name: /QR code/ }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Secret")).toHaveValue("JBSWY3DPEHPK3PXP");

    type("Code from the app", "654321");
    fireEvent.click(screen.getByRole("button", { name: "Turn on" }));
    expect(await screen.findByText(/AAAA-BBBB-CCCC-DDDD/)).toBeInTheDocument();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/me/2fa/enable",
      body: { code: "654321" },
    });
    fireEvent.click(screen.getByRole("button", { name: "I have saved them" }));
    await waitFor(() =>
      expect(screen.queryByText(/AAAA-BBBB-CCCC-DDDD/)).toBeNull(),
    );
    expect(
      await screen.findByText("10 recovery codes left."),
    ).toBeInTheDocument();
  });

  it("warns when fewer than 3 recovery codes are left", async () => {
    mockApi({
      ...profile,
      "GET /api/v1/me/extensions": () => json({ items: [] }),
      "GET /api/v1/me/2fa": () => json({ ...ON, recoveryCodesLeft: 2 }),
    });
    renderApp("/me/profile");
    expect(
      await screen.findByText("Few recovery codes left"),
    ).toBeInTheDocument();
  });

  it("refuses to turn off with a wrong code and shows the reason", async () => {
    mockApi({
      ...profile,
      "GET /api/v1/me/extensions": () => json({ items: [] }),
      "GET /api/v1/me/2fa": () => json(ON),
      "POST /api/v1/me/2fa/disable": () =>
        apiError(401, "invalid_code", "invalid password or code"),
    });
    renderApp("/me/profile");
    const card = await screen.findByRole("region", {
      name: "Two-factor authentication",
    });
    fireEvent.click(
      await within(card).findByRole("button", { name: "Turn off" }),
    );
    type("Password", "pw");
    type("Code from the app", "000000");
    fireEvent.click(screen.getByRole("button", { name: "Turn off" }));
    expect(
      await screen.findByText("That code is not right."),
    ).toBeInTheDocument();
  });

  it("does not offer turning off while 2FA is required", async () => {
    mockApi({
      ...profile,
      "GET /api/v1/me/extensions": () => json({ items: [] }),
      "GET /api/v1/me/2fa": () => json({ ...ON, required: true }),
    });
    renderApp("/me/profile");
    const card = await screen.findByRole("region", {
      name: "Two-factor authentication",
    });
    await within(card).findByText("10 recovery codes left.");
    expect(within(card).queryByRole("button", { name: "Turn off" })).toBeNull();
  });
});

describe("Required enrolment", () => {
  it("shows only the enrolment while 2FA is required and not set up", async () => {
    mockApi({
      "GET /api/v1/auth/me": () =>
        json({ username: "sam", role: "viewer", mustEnrollMfa: true }),
      "GET /api/v1/me/2fa": () => json({ ...OFF, required: true }),
    });
    renderApp("/extensions");
    expect(
      await screen.findByRole("heading", {
        level: 1,
        name: "Set up two-factor authentication",
      }),
    ).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Primary" })).toBeNull();
    expect(
      await screen.findByRole("button", { name: "Set up" }),
    ).toBeInTheDocument();
  });
});

describe("Users: two-factor policy", () => {
  const USERS = [
    {
      id: 1,
      username: "pat",
      displayName: "Pat",
      email: "",
      authMethod: "local",
      role: "admin",
      enabled: true,
      mustChangePassword: false,
      extensionId: null,
      extensionIds: [],
      voicemailEmail: { notify: true, attach: true, after: "none" },
      totpEnabled: true,
      totpRequired: false,
      createdAt: "2026-10-02T00:00:00Z",
    },
    {
      id: 2,
      username: "sam",
      displayName: "Sam",
      email: "",
      authMethod: "local",
      role: "user",
      enabled: true,
      mustChangePassword: false,
      extensionId: null,
      extensionIds: [],
      voicemailEmail: { notify: true, attach: true, after: "none" },
      totpEnabled: true,
      totpRequired: true,
      createdAt: "2026-10-02T00:00:00Z",
    },
  ];
  const base = {
    "GET /api/v1/auth/me": () => json({ username: "pat", role: "admin" }),
    "GET /api/v1/users": () => json({ items: USERS }),
    "GET /api/v1/extensions": () => json({ items: [] }),
  };

  it("saves the roles ticked and shows the server's refusal", async () => {
    const calls = mockApi({
      ...base,
      "GET /api/v1/auth/2fa-policy": () => json({ require2faRoles: [] }),
      "PUT /api/v1/auth/2fa-policy": [
        () => json({ require2faRoles: ["viewer"] }),
        () => apiError(409, "two_factor_required", "enrol first"),
      ],
    });
    renderApp("/users");
    const policy = await screen.findByRole("group", {
      name: "Two-factor policy",
    });
    fireEvent.click(await within(policy).findByLabelText("Require for viewer"));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PUT",
        url: "/api/v1/auth/2fa-policy",
        body: { require2faRoles: ["viewer"] },
      }),
    );
    await waitFor(() =>
      expect(within(policy).getByLabelText("Require for viewer")).toBeChecked(),
    );
    fireEvent.click(within(policy).getByLabelText("Require for admin"));
    expect(
      await within(policy).findByText(
        /Set up two-factor authentication for your own account first/,
      ),
    ).toBeInTheDocument();
  });

  it("shows 2FA state and resets a user's 2FA after confirmation", async () => {
    const calls = mockApi({
      ...base,
      "GET /api/v1/auth/2fa-policy": () => json({ require2faRoles: [] }),
      "DELETE /api/v1/users/2/2fa": noContent,
    });
    renderApp("/users");
    const table = await screen.findByRole("table", { name: "Users" });
    const row = within(table).getByText("sam").closest("tr") as HTMLElement;
    expect(within(row).getByText("On")).toBeInTheDocument();
    fireEvent.click(
      within(row).getByRole("button", { name: "Reset 2FA for sam" }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Reset 2FA" }));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "DELETE",
        url: "/api/v1/users/2/2fa",
        body: undefined,
      }),
    );
    await waitFor(() => expect(within(row).queryByText("On")).toBeNull());
  });

  it("requires 2FA of one user from the edit dialog", async () => {
    const calls = mockApi({
      ...base,
      "GET /api/v1/auth/2fa-policy": () => json({ require2faRoles: [] }),
      "PATCH /api/v1/users/2": () => json({ ...USERS[1], totpRequired: false }),
    });
    renderApp("/users");
    fireEvent.click(await screen.findByRole("button", { name: "Edit sam" }));
    const sw = await screen.findByRole("switch", {
      name: /Require two-factor authentication/,
    });
    expect(sw).toBeChecked();
    fireEvent.click(sw);
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PATCH")?.body).toMatchObject({
        totpRequired: false,
      }),
    );
  });
});
