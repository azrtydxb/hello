import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, renderApp } from "../../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const phone = {
  id: 5,
  mac: "805ec0123456",
  serial: "",
  vendor: "yealink",
  model: "T54W",
  label: "Front desk",
  deviceId: 3,
  extensionId: 7,
  extensionNumber: "101",
  templateId: null,
  groupId: 9,
  blf: [],
  enabled: true,
  tokenExposed: false,
  uaMismatch: false,
  bootArmed: false,
  bootReclaimed: false,
  redirectStatus: { state: "manual" },
  firstFetchAt: null,
  lastFetchAt: null,
  lastFetchIp: null,
  lastFetchUa: null,
  lastFetchFile: null,
  firmwareSeen: null,
  renderError: false,
  createdAt: STAMP,
  updatedAt: STAMP,
};

const ldap = (over: Record<string, unknown> = {}) => ({
  served: true,
  supported: true,
  groupEnabled: true,
  host: "prov.hello.test",
  port: 636,
  base: "ou=phonebook,dc=hello,dc=local",
  bindDn: "uid=5,ou=phones,dc=hello,dc=local",
  passwordSet: true,
  rotatedAt: null,
  previousValid: false,
  lastBindAt: null,
  ...over,
});

function api(state: Record<string, unknown>, extra = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role: "admin" }),
    "GET /api/v1/phones": () => json({ items: [phone] }),
    "GET /api/v1/phones/5": () => json(phone),
    "GET /api/v1/phones/5/fetches?limit=50": () =>
      json({ items: [], next: "" }),
    "GET /api/v1/phones/5/ldap": () => json(state),
    ...extra,
  });
}

describe("Phone LDAP directory", () => {
  it("shows the bind DN and that the directory was not queried", async () => {
    api(ldap());
    renderApp("/phones/5");
    expect(
      await screen.findByText("uid=5,ou=phones,dc=hello,dc=local"),
    ).toBeInTheDocument();
    expect(screen.getByText(/Directory not queried/)).toBeInTheDocument();
  });

  it("says when the group has LDAP off or the server does not serve it", async () => {
    api(ldap({ groupEnabled: false }));
    renderApp("/phones/5");
    expect(
      await screen.findByText(/LDAP is off for this phone's group/),
    ).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Rotate LDAP password" }),
    ).toBeNull();
  });

  it("says when the model has no LDAP", async () => {
    api(ldap({ supported: false }));
    renderApp("/phones/5");
    expect(
      await screen.findByText(/no LDAP directory setting/),
    ).toBeInTheDocument();
  });

  it("rotates the password after confirmation and shows it once", async () => {
    const secret = ["ldap", "-pw-", "9x"].join("");
    const calls = api(ldap(), {
      "POST /api/v1/phones/5/ldap/password": () =>
        json({
          bindDn: "uid=5,ou=phones,dc=hello,dc=local",
          password: secret,
          resyncQueued: true,
        }),
    });
    renderApp("/phones/5");
    fireEvent.click(
      await screen.findByRole("button", { name: "Rotate LDAP password" }),
    );
    expect(document.body.innerHTML).not.toContain(secret);
    const confirm = screen.getByRole("dialog", {
      name: "Rotate the LDAP password?",
    });
    expect(calls.some((c) => c.url.endsWith("/ldap/password"))).toBe(false);
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Rotate LDAP password" }),
    );
    const shown = await screen.findByRole("dialog", { name: "LDAP password" });
    expect(within(shown).getByText(secret)).toBeInTheDocument();
    fireEvent.click(within(shown).getByRole("button", { name: "Done" }));
    await waitFor(() => expect(document.body.innerHTML).not.toContain(secret));
  });
});
