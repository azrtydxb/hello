import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

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

function field(label: string, value: string) {
  fireEvent.change(screen.getByLabelText(label), { target: { value } });
}

describe("Trunks", () => {
  it("never shows the password after saving a new trunk", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: [] }),
      "GET /api/v1/trunks/status": () => json({ items: [] }),
      "POST /api/v1/trunks": () => json(TRUNK, 201),
    });
    renderApp("/trunks");

    await screen.findByText("No trunks yet.");
    fireEvent.click(screen.getByRole("button", { name: "New trunk" }));
    field("Name", "carrier-primary");
    field("Username", "acct1001");
    field("Password (optional)", PASSWORD);
    expect(screen.getByLabelText("Password (optional)")).toHaveAttribute(
      "type",
      "password",
    );
    field("Host 1", "10.0.0.5");
    fireEvent.click(screen.getByRole("button", { name: "Create trunk" }));

    const row = await screen.findByRole("row", { name: /carrier-primary/ });
    const post = calls.find((c) => c.method === "POST");
    expect(post?.body).toMatchObject({
      name: "carrier-primary",
      password: PASSWORD,
      destinations: [{ host: "10.0.0.5", port: 5060, priority: 0, weight: 1 }],
    });
    expect(within(row).getByText("set")).toBeVisible();
    expectPasswordNowhere();

    // Opening the saved trunk again does not bring the password back.
    fireEvent.click(
      within(row).getByRole("button", { name: "Edit trunk carrier-primary" }),
    );
    expect(screen.queryByLabelText(/password/i)).toBeNull();
    expect(
      screen.getByRole("button", { name: "Change password" }),
    ).toBeVisible();
    expectPasswordNowhere();
  });

  it("shows only set / not set on reload, and sends a password only when changed", async () => {
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

    const row = await screen.findByRole("row", { name: /carrier-primary/ });
    expect(within(row).getByText("set")).toBeVisible();
    expect(
      within(screen.getByRole("row", { name: /peer-ip/ })).getByText("not set"),
    ).toBeVisible();
    expectPasswordNowhere();

    fireEvent.click(
      within(row).getByRole("button", { name: "Edit trunk carrier-primary" }),
    );
    expectPasswordNowhere();
    field("Max calls", "20");
    fireEvent.click(screen.getByRole("button", { name: "Save trunk" }));
    await screen.findByText("Trunk carrier-primary saved.");
    const first = calls.filter((c) => c.method === "PATCH")[0];
    expect(first?.body).toMatchObject({ maxCalls: 20 });
    expect(first?.body).not.toHaveProperty("password");

    // Changing it is an explicit action, with an empty field.
    fireEvent.click(
      screen.getByRole("button", { name: "Edit trunk carrier-primary" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Change password" }));
    expect(screen.getByLabelText("New password")).toHaveValue("");
    field("New password", "new-test-password");
    fireEvent.click(screen.getByRole("button", { name: "Save trunk" }));
    await screen.findByText("Trunk carrier-primary saved.");
    const second = calls.filter((c) => c.method === "PATCH")[1];
    expect(second?.body).toMatchObject({ password: "new-test-password" });
    expect(screen.queryByDisplayValue("new-test-password")).toBeNull();
    expect(document.body.textContent).not.toContain("new-test-password");
  });

  it("renders live status: registration, destination health and calls", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/trunks": () => json({ items: [TRUNK] }),
      "GET /api/v1/trunks/status": () => json({ items: [STATUS] }),
    });
    renderApp("/trunks");

    const row = await screen.findByRole("row", { name: /carrier-primary/ });
    expect(await within(row).findByText("registered (200)")).toBeVisible();
    const up = within(row).getByText("10.0.0.5:5060").closest("li");
    expect(up).toHaveTextContent("10.0.0.5:5060 up, 12 ms");
    const down = within(row).getByText("10.0.0.6:5060").closest("li");
    expect(down).toHaveTextContent("10.0.0.6:5060 down (503)");
    expect(within(row).getByText("3 of 10")).toBeVisible();
    expect(
      screen.getByRole("columnheader", { name: "Registration" }),
    ).toBeVisible();
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
    fireEvent.click(screen.getByRole("button", { name: "New trunk" }));
    field("Name", "carrier-x");
    field("Host 1", "nowhere.invalid");
    fireEvent.click(screen.getByRole("button", { name: "Create trunk" }));

    const host = screen.getByLabelText("Host 1");
    expect(await screen.findByText("host does not resolve")).toBeVisible();
    expect(host).toHaveAttribute("aria-invalid", "true");
    expect(host).toHaveAccessibleDescription("host does not resolve");
  });
});
