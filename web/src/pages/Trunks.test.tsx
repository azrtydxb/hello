import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, noContent, renderApp } from "../test/api";

const PASSWORD = "carrier-test-password";

const TRUNK = {
  id: 4,
  name: "carrier-primary",
  mode: "registration",
  username: "acct1001",
  hasPassword: true,
  realm: "sip.carrier.test",
  fromDomain: "sip.carrier.test",
  registerExpires: 3600,
  optionsInterval: 30,
  sourceCidrs: ["203.0.113.0/24"],
  maxCalls: 10,
  defaultCallerId: "+97142000000",
  enabled: true,
  destinations: [{ host: "10.0.0.5", port: 5060, priority: 0, weight: 1 }],
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const STATUS = {
  trunkId: 4,
  name: "carrier-primary",
  registration: {
    state: "registered",
    node: "hello-sip-1",
    lastCode: 200,
    updatedAt: "2026-10-01T10:00:00Z",
  },
  destinations: [
    {
      destination: "10.0.0.5:5060",
      up: true,
      latencyNs: 12_400_000,
      checkedAt: "2026-10-01T10:00:00Z",
    },
    {
      destination: "10.0.0.6:5060",
      up: false,
      lastCode: 503,
      checkedAt: "2026-10-01T10:00:00Z",
    },
  ],
  activeCalls: 3,
};

/** Nothing on the page may carry the password: not as text, not as a value. */
function expectPasswordNowhere() {
  expect(document.body.textContent).not.toContain(PASSWORD);
  expect(screen.queryByDisplayValue(PASSWORD)).toBeNull();
  for (const input of document.querySelectorAll("input")) {
    expect(input.value).not.toBe(PASSWORD);
  }
}

/** Types into a field of the open drawer (or the page when none is open). */
function field(label: string, value: string) {
  const scope = screen.queryByRole("dialog");
  fireEvent.change(
    scope ? within(scope).getByLabelText(label) : screen.getByLabelText(label),
    { target: { value } },
  );
}

const card = (name: RegExp | string) => screen.findByRole("listitem", { name });

describe("Trunks", () => {
  it("never shows the password after saving a new trunk", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: [] }),
      "GET /api/v1/trunks/status": () => json({ items: [] }),
      "POST /api/v1/trunks": () => json(TRUNK, 201),
    });
    renderApp("/trunks");

    expect(await screen.findByText("No trunks yet.")).toBeVisible();
    fireEvent.click(screen.getAllByRole("button", { name: "New trunk" })[0]!);
    const drawer = screen.getByRole("dialog", { name: "New trunk" });
    expect(within(drawer).getByLabelText("Name")).toHaveFocus();
    field("Name", "carrier-primary");
    field("Username", "acct1001");
    field("Password (optional)", PASSWORD);
    expect(screen.getByLabelText("Password (optional)")).toHaveAttribute(
      "type",
      "password",
    );
    field("Host 1", "10.0.0.5");
    fireEvent.click(screen.getByRole("button", { name: "Create trunk" }));

    const saved = await card(/carrier-primary/);
    expect(
      await screen.findByText("Trunk carrier-primary created."),
    ).toBeVisible();
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({
      name: "carrier-primary",
      password: PASSWORD,
      destinations: [{ host: "10.0.0.5", port: 5060, priority: 0, weight: 1 }],
    });
    expect(within(saved).getByText("set (write-only)")).toBeVisible();
    expectPasswordNowhere();

    // Opening the saved trunk again does not bring the password back.
    fireEvent.click(
      within(saved).getByRole("button", { name: "Edit trunk carrier-primary" }),
    );
    expect(screen.queryByLabelText(/password/i)).toBeNull();
    expect(
      screen.getByRole("button", { name: "Change password" }),
    ).toBeVisible();
    expectPasswordNowhere();
  });

  it("shows only set / not set on reload, and rotates the password only when asked", async () => {
    const calls = mockApi({
      ...ME,
      // Even if a server mistakenly echoed a password, the page must not show it.
      "GET /api/v1/trunks": () =>
        json({
          items: [
            { ...TRUNK, password: PASSWORD },
            {
              ...TRUNK,
              id: 5,
              name: "peer-ip",
              mode: "ip",
              hasPassword: false,
            },
          ],
        }),
      "GET /api/v1/trunks/status": () => json({ items: [STATUS] }),
      "PATCH /api/v1/trunks/4": () => json(TRUNK),
    });
    renderApp("/trunks");

    const primary = await card(/carrier-primary/);
    expect(within(primary).getByText("set (write-only)")).toBeVisible();
    expect(within(await card(/peer-ip/)).getByText("not set")).toBeVisible();
    expectPasswordNowhere();

    fireEvent.click(
      within(primary).getByRole("button", {
        name: "Edit trunk carrier-primary",
      }),
    );
    expectPasswordNowhere();
    field("Concurrent calls", "20");
    fireEvent.click(screen.getByRole("button", { name: "Save trunk" }));
    expect(
      await screen.findByText("Trunk carrier-primary saved."),
    ).toBeVisible();
    const first = calls.filter((c) => c.method === "PATCH")[0];
    expect(first?.body).toMatchObject({ maxCalls: 20 });
    expect(first?.body).not.toHaveProperty("password");

    // Changing it is an explicit action, with an empty field.
    fireEvent.click(
      screen.getByRole("button", { name: "Edit trunk carrier-primary" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(screen.getByLabelText("New password")).toHaveValue("");
    expect(screen.getByLabelText("New password")).toHaveFocus();
    field("New password", "new-test-password");
    fireEvent.click(screen.getByRole("button", { name: "Save trunk" }));
    expect(await screen.findByText(/^Secret rotated\./)).toBeVisible();
    const second = calls.filter((c) => c.method === "PATCH")[1];
    expect(second?.body).toMatchObject({ password: "new-test-password" });
    expect(screen.queryByDisplayValue("new-test-password")).toBeNull();
    expect(document.body.textContent).not.toContain("new-test-password");
  });

  it("renders a card with live registration, destination health, calls and settings", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/trunks": () =>
        json({
          items: [
            {
              ...TRUNK,
              destinations: [
                { host: "10.0.0.5", port: 5060, priority: 0, weight: 1 },
                { host: "10.0.0.6", port: 5060, priority: 1, weight: 2 },
                { host: "10.0.0.7", port: 5060, priority: 2, weight: 1 },
              ],
            },
          ],
        }),
      "GET /api/v1/trunks/status": () => json({ items: [STATUS] }),
    });
    renderApp("/trunks");

    const c = await card(/carrier-primary/);
    expect(
      await within(c).findByText(/registered on hello-sip-1 \(200\)/),
    ).toBeVisible();
    // One destination is down: the trunk is degraded, not just registered.
    expect(within(c).getByText("Degraded")).toBeVisible();
    const row = (host: string) =>
      within(c).getByRole("cell", { name: host }).closest("tr")!;
    expect(
      within(row("10.0.0.5:5060"))
        .getAllByRole("cell")
        .map((td) => td.textContent),
    ).toEqual(["10.0.0.5:5060", "0", "1", "12 ms"]);
    expect(row("10.0.0.6:5060")).toHaveTextContent("down · 503");
    // No health result yet: unknown, not "up".
    expect(row("10.0.0.7:5060")).toHaveTextContent("—");
    expect(
      within(c).getByRole("meter", { name: "Concurrent calls" }),
    ).toHaveAttribute("aria-valuenow", "3");
    expect(within(c).getByText("3 of 10")).toBeVisible();
    const props = (label: string) =>
      within(c).getByText(label).nextElementSibling;
    expect(props("OPTIONS interval")).toHaveTextContent("30 s");
    expect(props("Default caller ID")).toHaveTextContent("+97142000000");
    expect(props("Realm")).toHaveTextContent("sip.carrier.test");
    expect(
      within(c).getByRole("link", { name: "Test a route" }),
    ).toHaveAttribute("href", "/routes/test?from=trunk%3A4");
  });

  it("shows an IP peer's source CIDRs and from domain, and — while status is unknown", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/trunks": () =>
        json({
          items: [
            {
              ...TRUNK,
              id: 5,
              name: "peer-ip",
              mode: "ip",
              hasPassword: false,
              defaultCallerId: "",
              fromDomain: "pbx.hello.lab",
            },
          ],
        }),
      "GET /api/v1/trunks/status": () =>
        json({ error: { code: "unavailable", message: "valkey down" } }, 503),
    });
    renderApp("/trunks");

    const c = await card(/peer-ip/);
    expect(
      within(c).getByText(/IP peer · accepts 203\.0\.113\.0\/24/),
    ).toBeVisible();
    const props = (label: string) =>
      within(c).getByText(label).nextElementSibling;
    expect(props("Source CIDRs")).toHaveTextContent("203.0.113.0/24");
    expect(props("From domain")).toHaveTextContent("pbx.hello.lab");
    expect(props("Default caller ID")).toHaveTextContent("—");
    expect(await screen.findByText("Live status unavailable")).toBeVisible();
    expect(within(c).getByText("Status unknown")).toBeVisible();
    // Calls are unknown, not zero.
    expect(
      within(c).getByText("Concurrent calls").parentElement,
    ).toHaveTextContent("Concurrent calls—");
  });

  it("deletes a trunk after the confirmation dialog", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: [TRUNK] }),
      "GET /api/v1/trunks/status": () => json({ items: [] }),
      "DELETE /api/v1/trunks/4": noContent,
    });
    renderApp("/trunks");

    fireEvent.click(
      within(await card(/carrier-primary/)).getByRole("button", {
        name: "Delete trunk carrier-primary",
      }),
    );
    const dialog = screen.getByRole("dialog", {
      name: "Delete trunk carrier-primary?",
    });
    expect(
      within(dialog).getByRole("button", { name: "Cancel" }),
    ).toHaveFocus();
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete trunk" }),
    );
    expect(await screen.findByText("No trunks yet.")).toBeVisible();
    expect(screen.getByText("Trunk carrier-primary deleted.")).toBeVisible();
    expect(calls.some((c) => c.method === "DELETE")).toBe(true);
  });

  it("shows a server field error on the matching destination input", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: [] }),
      "GET /api/v1/trunks/status": () => json({ items: [] }),
      "POST /api/v1/trunks": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid trunk",
              fields: [
                {
                  path: "destinations[0].host",
                  message: "host does not resolve",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/trunks");

    await screen.findByText("No trunks yet.");
    fireEvent.click(screen.getAllByRole("button", { name: "New trunk" })[0]!);
    field("Name", "carrier-x");
    field("Host 1", "nowhere.invalid");
    fireEvent.click(screen.getByRole("button", { name: "Create trunk" }));

    const host = screen.getByLabelText("Host 1");
    expect(await screen.findByText("host does not resolve")).toBeVisible();
    expect(host).toHaveAttribute("aria-invalid", "true");
    expect(host).toHaveAccessibleDescription("host does not resolve");
  });
});
