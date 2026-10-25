import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, renderApp } from "../test/api";

function me(role: string) {
  return { "GET /api/v1/auth/me": () => json({ username: "pat", role }) };
}

const ACCOUNTS = [
  {
    id: "hello_sa_1",
    name: "crm-bridge",
    description: "",
    role: "operator",
    scopes: ["read", "calls", "events"],
    enabled: true,
    createdAt: "2026-10-01T10:00:00Z",
    lastUsedAt: null,
    secrets: [],
    extensionScope: ["101", "102"],
  },
  {
    id: "hello_sa_2",
    name: "nightly-report",
    description: "",
    role: "viewer",
    scopes: ["read"],
    enabled: true,
    createdAt: "2026-10-01T10:00:00Z",
    lastUsedAt: null,
    secrets: [],
    extensionScope: null,
  },
];

const TOKENS = [
  {
    id: 7,
    name: "zapier",
    kind: "personal",
    scopes: ["read", "events"],
    createdAt: "2026-10-02T10:00:00Z",
    lastUsedAt: null,
    expiresAt: null,
  },
  {
    id: 8,
    name: "grafana",
    kind: "personal",
    scopes: ["read"],
    createdAt: "2026-10-02T10:00:00Z",
    lastUsedAt: null,
    expiresAt: null,
  },
];

function setup(role: string) {
  return mockApi({
    ...me(role),
    "GET /api/v1/service-accounts": () => json({ items: ACCOUNTS }),
    "GET /api/v1/tokens": () => json({ items: TOKENS }),
  });
}

describe("Integrations (spec integrations P0)", () => {
  it("lists only the credentials that hold an integration scope, with their extension limits", async () => {
    setup("admin");
    renderApp("/integrations");

    const table = await screen.findByRole("table", {
      name: "Credentials with integration scopes",
    });
    expect(within(table).getByText("crm-bridge")).toBeInTheDocument();
    expect(within(table).getByText("101, 102")).toBeInTheDocument();
    expect(within(table).getByText("zapier")).toBeInTheDocument();
    expect(within(table).queryByText("nightly-report")).toBeNull();
    expect(within(table).queryByText("grafana")).toBeNull();
  });

  it("is linked from the sidebar for administrators", async () => {
    setup("admin");
    renderApp("/integrations");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(within(nav).getByRole("link", { name: /Overview/ })).toBeVisible();
    expect(within(nav).getByRole("link", { name: /API access/ })).toBeVisible();
  });

  it("is hidden from operators, and the page says so", async () => {
    setup("operator");
    renderApp("/integrations");
    expect(await screen.findByText("Administrators only")).toBeVisible();
    const nav = screen.getByRole("navigation", { name: "Primary" });
    expect(within(nav).queryByRole("link", { name: /^API access/ })).toBeNull();
  });

  it("offers the integration scopes and the extension limit when creating a service account", async () => {
    setup("admin");
    renderApp("/integrations/api-access");
    fireEvent.click(
      await screen.findByRole("button", { name: /New service account/ }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Role"), {
      target: { value: "operator" },
    });
    expect(
      within(dialog).getByRole("checkbox", { name: /^calls(?![a-z])/ }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("checkbox", { name: /^events(?![a-z])/ }),
    ).toBeInTheDocument();
    expect(
      within(dialog).getByRole("checkbox", { name: /^recordings(?![a-z])/ }),
    ).toBeInTheDocument();
    expect(
      within(dialog).queryByLabelText("Extensions it may act for"),
    ).toBeNull();
    fireEvent.click(
      within(dialog).getByRole("checkbox", { name: /^calls(?![a-z])/ }),
    );
    expect(
      within(dialog).getByLabelText("Extensions it may act for"),
    ).toBeVisible();
  });
});
