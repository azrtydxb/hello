import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, noContent, renderApp } from "../test/api";
import { filterText } from "./Webhooks";
import type { WebhookEndpoint } from "../api/webhooks";

const AT = "2026-10-26T10:00:00Z";

function endpoint(over: Partial<WebhookEndpoint> = {}): WebhookEndpoint {
  return {
    id: 3,
    name: "CRM sync",
    url: "https://hooks.example.com/in",
    events: ["call.missed", "call.ended"],
    extensions: [],
    enabled: true,
    allowPrivate: false,
    disabledReason: "",
    consecutiveFailures: 0,
    previousSecretExpiresAt: null,
    lastSuccessAt: AT,
    lastFailureAt: null,
    lastStatus: 200,
    createdBy: "user:admin",
    createdAt: AT,
    updatedAt: AT,
    pending: 1,
    dead: 2,
    ...over,
  };
}

function delivery(id: number, over: Record<string, unknown> = {}) {
  return {
    id,
    endpointId: 3,
    eventId: `evt_${String(id)}`,
    eventType: "call.ended",
    status: "dead",
    attempts: 8,
    nextAttemptAt: null,
    lastStatus: 502,
    lastError: "http 502; retries exhausted",
    lastLatencyMs: 40,
    deliveredAt: null,
    createdAt: AT,
    updatedAt: AT,
    ...over,
  };
}

function me(role: string) {
  return { "GET /api/v1/auth/me": () => json({ username: "pat", role }) };
}

function setup(
  extra: Parameters<typeof mockApi>[0] = {},
  items = [endpoint()],
) {
  return mockApi({
    ...me("admin"),
    "GET /api/v1/webhooks": () => json({ items }),
    ...extra,
  });
}

describe("Webhooks (spec integrations P2)", () => {
  it("lists the endpoints with what they receive, their state and their queue", async () => {
    setup({}, [
      endpoint(),
      endpoint({
        id: 4,
        name: "Old",
        url: "https://old.example.com/in",
        enabled: false,
        disabledReason: "consecutive_failures",
        events: [],
        extensions: ["101"],
        pending: 0,
        dead: 0,
        lastStatus: null,
        lastSuccessAt: null,
      }),
    ]);
    renderApp("/integrations/webhooks");
    const table = await screen.findByRole("table", {
      name: "Webhook endpoints",
    });
    expect(
      within(table).getByText("https://hooks.example.com/in"),
    ).toBeVisible();
    expect(within(table).getByText("call.missed, call.ended")).toBeVisible();
    expect(within(table).getByText("2 dead")).toBeVisible();
    expect(within(table).getByText("on")).toBeVisible();
    expect(
      within(table).getByText("off: 50 deliveries failed in a row"),
    ).toBeVisible();
    expect(within(table).getByText("all events for 101")).toBeVisible();
    expect(within(table).getByText("no delivery yet")).toBeVisible();
  });

  it("describes a filter in words", () => {
    expect(filterText(endpoint({ events: [], extensions: [] }))).toBe(
      "all events",
    );
    expect(
      filterText(endpoint({ events: ["call.*"], extensions: ["101", "102"] })),
    ).toBe("call.* for 101, 102");
  });

  it("is for administrators only, in the sidebar and on the page", async () => {
    setup({ ...me("operator") });
    renderApp("/integrations/webhooks");
    expect(await screen.findByText("Administrators only")).toBeVisible();
    const nav = screen.getByRole("navigation", { name: "Primary" });
    expect(within(nav).queryByRole("link", { name: /^Webhooks/ })).toBeNull();
  });

  it("creates an endpoint and shows its signing secret once", async () => {
    const calls = setup({
      "POST /api/v1/webhooks": () =>
        json(
          {
            ...endpoint({ id: 9, name: "Zap", pending: 0, dead: 0 }),
            secret: "whsec_shownonce",
          },
          201,
        ),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", { name: /New endpoint/ }),
    );
    const form = await screen.findByRole("form", {
      name: "New webhook endpoint",
    });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "Zap" },
    });
    fireEvent.change(within(form).getByLabelText("URL"), {
      target: { value: "https://zapier.example.com/hooks/1" },
    });
    fireEvent.click(
      within(form).getByRole("switch", { name: /Every event type/ }),
    );
    fireEvent.click(
      within(form).getByRole("checkbox", { name: "call.missed" }),
    );
    fireEvent.change(within(form).getByLabelText("Extensions"), {
      target: { value: "101, 102" },
    });
    fireEvent.submit(form);
    const dialog = await screen.findByRole("dialog", {
      name: /Signing secret of Zap/,
    });
    expect(within(dialog).getByText("whsec_shownonce")).toBeVisible();
    expect(within(dialog).getByText(/shown once/i)).toBeVisible();
    const post = calls.find(
      (c) => c.method === "POST" && c.url === "/api/v1/webhooks",
    );
    expect(post?.body).toEqual({
      name: "Zap",
      url: "https://zapier.example.com/hooks/1",
      events: ["call.missed"],
      extensions: ["101", "102"],
      enabled: true,
      allowPrivate: false,
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: /I have copied it/ }),
    );
    await waitFor(() =>
      expect(screen.queryByText("whsec_shownonce")).toBeNull(),
    );
  });

  it("checks the form before posting: https, extensions, at least one type", async () => {
    const calls = setup();
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", { name: /New endpoint/ }),
    );
    const form = await screen.findByRole("form", {
      name: "New webhook endpoint",
    });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "x" },
    });
    fireEvent.change(within(form).getByLabelText("URL"), {
      target: { value: "http://hooks.example.com/in" },
    });
    fireEvent.change(within(form).getByLabelText("Extensions"), {
      target: { value: "1" },
    });
    fireEvent.click(
      within(form).getByRole("switch", { name: /Every event type/ }),
    );
    fireEvent.submit(form);
    expect(await within(form).findByText(/Use https/)).toBeVisible();
    expect(within(form).getByText(/2 to 10 digits/)).toBeVisible();
    expect(
      within(form).getByText(/Choose at least one event type/),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("shows the server's field errors on the form", async () => {
    setup({
      "POST /api/v1/webhooks": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration: url: the address is private",
              fields: [
                {
                  path: "url",
                  message: "the address is private, loopback or link-local",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", { name: /New endpoint/ }),
    );
    const form = await screen.findByRole("form", {
      name: "New webhook endpoint",
    });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "lan" },
    });
    fireEvent.change(within(form).getByLabelText("URL"), {
      target: { value: "https://10.0.0.5/in" },
    });
    fireEvent.submit(form);
    expect(
      await within(form).findByText(/address is private, loopback/),
    ).toBeVisible();
  });

  it("rotates a secret after confirming and shows the new one once", async () => {
    const calls = setup({
      "POST /api/v1/webhooks/3/rotate-secret": () =>
        json({
          ...endpoint({ previousSecretExpiresAt: "2026-10-27T10:00:00Z" }),
          secret: "whsec_rotated",
        }),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Rotate the secret of CRM sync",
      }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: /Rotate the secret of CRM sync/,
    });
    expect(
      within(confirm).getByText(/both the new and the old secret sign/),
    ).toBeVisible();
    expect(calls.some((c) => c.url.endsWith("/rotate-secret"))).toBe(false);
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Rotate secret" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /Signing secret of CRM sync/,
    });
    expect(within(dialog).getByText("whsec_rotated")).toBeVisible();
    expect(
      within(dialog).getByText(/previous secret keeps signing until/),
    ).toBeVisible();
  });

  it("sends a test event and reports what the receiver answered", async () => {
    setup({
      "POST /api/v1/webhooks/3/test": () =>
        json({
          delivered: false,
          status: 503,
          latencyMs: 120,
          error: "",
          body: "maintenance",
          eventId: "evt_test_1",
        }),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Send a test event to CRM sync",
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /Test event to CRM sync/,
    });
    expect(within(dialog).getByText("Not delivered")).toBeVisible();
    expect(within(dialog).getByText(/HTTP 503 in 120 ms/)).toBeVisible();
    expect(within(dialog).getByText("maintenance")).toBeVisible();
  });

  it("switches an endpoint off and deletes one after confirming", async () => {
    const calls = setup({
      "PATCH /api/v1/webhooks/3": () => json(endpoint({ enabled: false })),
      "DELETE /api/v1/webhooks/3": () => noContent(),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", { name: "Switch off CRM sync" }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
        enabled: false,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete CRM sync" }));
    const confirm = await screen.findByRole("dialog", {
      name: /Delete CRM sync\?/,
    });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete endpoint" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });

  it("shows the delivery log of an endpoint and redelivers one delivery", async () => {
    const calls = setup({
      "GET /api/v1/webhooks/3/deliveries?limit=50": () =>
        json({
          items: [
            delivery(12),
            delivery(11, {
              status: "succeeded",
              attempts: 1,
              lastStatus: 200,
              lastError: "",
            }),
            delivery(10, {
              status: "retrying",
              attempts: 2,
              nextAttemptAt: "2026-10-26T10:05:00Z",
            }),
          ],
          nextCursor: "",
        }),
      "POST /api/v1/webhooks/deliveries/12/redeliver": () =>
        json(delivery(12, { status: "pending" })),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Show deliveries of CRM sync",
      }),
    );
    const log = await screen.findByRole("table", { name: "Deliveries" });
    expect(within(log).getByText("dead")).toBeVisible();
    expect(within(log).getByText("succeeded")).toBeVisible();
    expect(within(log).getByText("retrying")).toBeVisible();
    expect(within(log).getByText("HTTP 200")).toBeVisible();
    fireEvent.click(
      within(log).getByRole("button", { name: "Redeliver call.ended evt_12" }),
    );
    await waitFor(() =>
      expect(
        calls.some((c) => c.url === "/api/v1/webhooks/deliveries/12/redeliver"),
      ).toBe(true),
    );
    expect(await screen.findByText("Queued for delivery again.")).toBeVisible();
  });

  it("opens a delivery to read its attempts and event", async () => {
    setup({
      "GET /api/v1/webhooks/3/deliveries?limit=50": () =>
        json({ items: [delivery(12)], nextCursor: "" }),
      "GET /api/v1/webhooks/3/deliveries/12": () =>
        json({
          ...delivery(12),
          log: [
            {
              attempt: 1,
              deliveryId: "dlv_aaa",
              at: AT,
              status: 502,
              error: "http 502",
              latencyMs: 31,
            },
            {
              attempt: 2,
              deliveryId: "dlv_bbb",
              at: AT,
              status: null,
              error: "timeout",
              latencyMs: 10000,
            },
          ],
          event: { id: "evt_12", type: "call.ended" },
        }),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Show deliveries of CRM sync",
      }),
    );
    const log = await screen.findByRole("table", { name: "Deliveries" });
    fireEvent.click(within(log).getByText("call.ended"));
    const attempts = await screen.findByRole("table", { name: "Attempts" });
    expect(within(attempts).getByText("dlv_aaa")).toBeVisible();
    expect(within(attempts).getByText("timeout")).toBeVisible();
    expect(await screen.findByText(/"evt_12"/)).toBeVisible();
  });

  it("filters the log by state and pages back", async () => {
    const calls = setup({
      "GET /api/v1/webhooks/3/deliveries?limit=50": () =>
        json({ items: [delivery(12)], nextCursor: "12" }),
      "GET /api/v1/webhooks/3/deliveries?before=12&limit=50": () =>
        json({
          items: [delivery(7, { eventType: "call.missed" })],
          nextCursor: "",
        }),
      "GET /api/v1/webhooks/3/deliveries?status=succeeded&limit=50": () =>
        json({ items: [], nextCursor: "" }),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Show deliveries of CRM sync",
      }),
    );
    await screen.findByRole("table", { name: "Deliveries" });
    fireEvent.click(screen.getByRole("button", { name: "Load older" }));
    expect(await screen.findByText("call.missed")).toBeVisible();
    fireEvent.change(screen.getByLabelText("State"), {
      target: { value: "succeeded" },
    });
    expect(await screen.findByText("No deliveries yet")).toBeVisible();
    expect(
      calls.some(
        (c) =>
          c.url === "/api/v1/webhooks/3/deliveries?status=succeeded&limit=50",
      ),
    ).toBe(true);
  });

  it("redelivers dead letters one at a time and all at once", async () => {
    const calls = setup({
      "GET /api/v1/webhooks/3/deliveries?status=dead&limit=50": () =>
        json({ items: [delivery(12), delivery(11)], nextCursor: "" }),
      "POST /api/v1/webhooks/deliveries/12/redeliver": () =>
        json(delivery(12, { status: "pending" })),
      "POST /api/v1/webhooks/3/deliveries/redeliver-dead": () =>
        json({ redelivered: 2 }),
    });
    renderApp("/integrations/webhooks");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Show deliveries of CRM sync",
      }),
    );
    fireEvent.click(await screen.findByRole("tab", { name: /Dead letters/ }));
    const table = await screen.findByRole("table", { name: "Dead letters" });
    fireEvent.click(
      within(table).getByRole("button", {
        name: "Redeliver call.ended evt_12",
      }),
    );
    await waitFor(() =>
      expect(
        calls.some((c) => c.url === "/api/v1/webhooks/deliveries/12/redeliver"),
      ).toBe(true),
    );
    fireEvent.click(
      screen.getByRole("button", { name: "Redeliver all dead letters" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: /Redeliver all dead letters of CRM sync/,
    });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Redeliver all" }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.url === "/api/v1/webhooks/3/deliveries/redeliver-dead",
        ),
      ).toBe(true),
    );
    expect(
      await screen.findByText(/2 dead letters queued again/),
    ).toBeVisible();
  });

  it("says so when the endpoints cannot be loaded", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/webhooks": () =>
        apiError(503, "unavailable", "webhooks are not configured"),
    });
    renderApp("/integrations/webhooks");
    expect(
      await screen.findByText("Could not load the webhook endpoints"),
    ).toBeVisible();
    expect(screen.getByText("webhooks are not configured")).toBeVisible();
  });
});
