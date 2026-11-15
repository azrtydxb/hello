import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const site = (over: Record<string, unknown> = {}) => ({
  id: 4,
  channelId: 9,
  addressId: 11,
  name: "Help desk",
  enabled: true,
  siteKey: "0123456789abcdef01234567", // gitleaks:allow (a public chat site key, not a secret)
  allowedOrigins: ["https://intranet.lan"],
  greeting: "Hi!",
  accent: "#2563eb",
  position: "right",
  collect: "off",
  hours: { zone: "", days: [] },
  offlineMode: "collect",
  privacyUrl: "",
  challenge: "pow",
  powBits: 16,
  turnstileSiteKey: "",
  hasTurnstileSecret: false,
  stripLinks: false,
  maxOpenUnassigned: 200,
  transcripts: false,
  retentionDays: null,
  attachments: false,
  logo: false,
  conversations: 3,
  createdAt: STAMP,
  updatedAt: STAMP,
  warnings: [],
  ...over,
});

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "a", role: "admin" }),
    "GET /api/v1/messaging/chat/sites": () => json({ items: [site()] }),
    ...more,
  });
}

describe("Chat sites", () => {
  it("lists the sites and warns about what deserves a second look", async () => {
    api({
      "GET /api/v1/messaging/chat/sites": () =>
        json({
          items: [
            site({
              challenge: "none",
              warnings: [
                "no challenge: anyone on the network can open sessions",
              ],
            }),
          ],
        }),
    });
    renderApp("/messaging/chat");
    const table = await screen.findByRole("table", { name: "Chat sites" });
    expect(within(table).getByText("Help desk")).toBeInTheDocument();
    expect(within(table).getByText("https://intranet.lan")).toBeInTheDocument();
    expect(await screen.findByText(/no challenge/)).toBeInTheDocument();
    expect(document.body.textContent).not.toMatch(/secret/i);
  });

  it("adds a site with the defaults the API expects", async () => {
    const calls = api({
      "POST /api/v1/messaging/chat/sites": () =>
        json(site({ id: 5, name: "Sales" }), 201),
    });
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(screen.getByRole("button", { name: "Add chat site" }));
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Sales" },
    });
    fireEvent.change(screen.getByLabelText("Allowed origins"), {
      target: { value: "https://a.lan, http://10.0.0.5:8080" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add site" }));
    await waitFor(() => {
      const post = calls.find((c) => c.method === "POST");
      expect(post?.body).toMatchObject({
        name: "Sales",
        enabled: true,
        allowedOrigins: ["https://a.lan", "http://10.0.0.5:8080"],
        challenge: "pow",
        powBits: 16,
        collect: "off",
        maxOpenUnassigned: 200,
        retentionDays: 0,
      });
      expect(post?.body).not.toHaveProperty("turnstileSecret");
      // The hours are the site's routing schedule, edited there.
      expect(post?.body).not.toHaveProperty("hours");
    });
  });

  it("shows field errors from the API", async () => {
    api({
      "POST /api/v1/messaging/chat/sites": () =>
        json(
          {
            error: {
              code: "validation_failed",
              message: "validation failed",
              fields: [
                { path: "accent", message: "must be a colour like #2563eb" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(screen.getByRole("button", { name: "Add chat site" }));
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Sales" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add site" }));
    expect(
      await screen.findByText("must be a colour like #2563eb"),
    ).toBeInTheDocument();
  });

  it("never shows a Turnstile secret, and sends one only when typed", async () => {
    const calls = api({
      "GET /api/v1/messaging/chat/sites": () =>
        json({
          items: [
            site({
              challenge: "turnstile",
              turnstileSiteKey: "0xAAA",
              hasTurnstileSecret: true,
            }),
          ],
        }),
      "PUT /api/v1/messaging/chat/sites/4": () =>
        json(
          site({
            challenge: "turnstile",
            turnstileSiteKey: "0xAAA",
            hasTurnstileSecret: true,
          }),
        ),
    });
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(screen.getByRole("button", { name: "Edit Help desk" }));
    const secret = await screen.findByLabelText("Turnstile secret key");
    expect(secret).toHaveValue("");
    expect(secret).toHaveAttribute("type", "password");
    expect(screen.getByText(/A secret is stored/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Save site" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")?.body).not.toHaveProperty(
      "turnstileSecret",
    );
    fireEvent.click(screen.getByRole("button", { name: "Edit Help desk" }));
    fireEvent.change(await screen.findByLabelText("Turnstile secret key"), {
      target: { value: "cf-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save site" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PUT")[1]?.body).toMatchObject({
        turnstileSecret: "cf-secret",
      }),
    );
    expect(document.body.textContent).not.toContain("cf-secret");
  });

  it("turns visitors' attachments on, warning there is no virus scanner", async () => {
    const calls = api({
      "PUT /api/v1/messaging/chat/sites/4": () =>
        json(site({ attachments: true })),
    });
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(screen.getByRole("button", { name: "Edit Help desk" }));
    const toggle = await screen.findByRole("switch", {
      name: /Let visitors send images and PDFs/,
    });
    expect(toggle).not.toBeChecked();
    expect(toggle.closest("label")?.textContent).toMatch(/no virus scanner/);
    fireEvent.click(toggle);
    fireEvent.click(screen.getByRole("button", { name: "Save site" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
        attachments: true,
      }),
    );
  });

  it("uploads, shows and removes a site's logo", async () => {
    const calls = api({
      "PUT /api/v1/messaging/chat/sites/4/logo": () =>
        json(site({ logo: true, updatedAt: "2026-10-02T10:00:00Z" })),
      "DELETE /api/v1/messaging/chat/sites/4/logo": () => noContent(),
    });
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(screen.getByRole("button", { name: "Edit Help desk" }));
    expect(await screen.findByText(/No logo/)).toBeInTheDocument();
    const big = new File([new Uint8Array(256 * 1024 + 1)], "big.png", {
      type: "image/png",
    });
    fireEvent.change(screen.getByLabelText("Logo file"), {
      target: { files: [big] },
    });
    expect(await screen.findByText(/larger than 256 KB/)).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith("/logo"))).toBe(false);
    const png = new File([new Uint8Array([137, 80, 78, 71])], "logo.png", {
      type: "image/png",
    });
    fireEvent.change(screen.getByLabelText("Logo file"), {
      target: { files: [png] },
    });
    const img = await screen.findByRole("img", { name: "Logo of Help desk" });
    expect(img.getAttribute("src")).toMatch(
      /^\/api\/v1\/messaging\/chat\/sites\/4\/logo\?v=/,
    );
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toBeInstanceOf(FormData);
    fireEvent.click(screen.getByRole("button", { name: "Remove logo" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
    expect(await screen.findByText(/No logo/)).toBeInTheDocument();
  });

  it("builds the embed code from the chat address", async () => {
    api();
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(screen.getByRole("button", { name: "Embed Help desk" }));
    fireEvent.change(await screen.findByLabelText("Chat address"), {
      target: { value: "https://hello.lan/" },
    });
    expect(
      screen.getByText(
        '<script async src="https://hello.lan/chat/v1/widget.js" data-site="0123456789abcdef01234567"></script>',
      ),
    ).toBeInTheDocument();
  });

  it("lists and lifts blocks", async () => {
    let lifted = false;
    const calls = api({
      "GET /api/v1/messaging/chat/sites/4/blocks": () =>
        json({
          items: lifted
            ? []
            : [
                {
                  id: 8,
                  siteId: 4,
                  visitorId: 12,
                  byIp: true,
                  until: STAMP,
                  createdAt: STAMP,
                },
              ],
        }),
      "DELETE /api/v1/messaging/chat/blocks/8": () => {
        lifted = true;
        return noContent();
      },
    });
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(
      screen.getByRole("button", { name: "Blocked visitors of Help desk" }),
    );
    expect(await screen.findByText("Visitor 12")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Lift block 8" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
    expect(await screen.findByText("Nobody is blocked")).toBeInTheDocument();
  });

  it("refuses to delete a site that has chats and says why", async () => {
    api({
      "DELETE /api/v1/messaging/chat/sites/4": () =>
        apiError(
          409,
          "conflict",
          "the site has 3 conversations; they are removed by retention or an erase request",
        ),
    });
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(screen.getByRole("button", { name: "Delete Help desk" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete site" }));
    expect(
      await screen.findByText(/the site has 3 conversations/),
    ).toBeInTheDocument();
  });

  it("shows whether each widget takes live chats now, and why not", async () => {
    api({
      "GET /api/v1/messaging/chat/sites": () =>
        json({
          items: [
            site({ availability: { online: true } }),
            site({
              id: 5,
              name: "Sales",
              availability: { online: false, reason: "no_agent" },
            }),
            site({
              id: 6,
              name: "Night",
              availability: { online: false, reason: "hours" },
            }),
            site({ id: 7, name: "Old", enabled: false }),
          ],
        }),
    });
    renderApp("/messaging/chat");
    const table = await screen.findByRole("table", { name: "Chat sites" });
    expect(within(table).getByText("Live")).toBeInTheDocument();
    expect(
      within(table).getByText("Offline (nobody available)"),
    ).toBeInTheDocument();
    expect(
      within(table).getByText("Closed (outside the hours)"),
    ).toBeInTheDocument();
    expect(within(table).getByText("Off")).toBeInTheDocument();
  });

  it("routes a site to a queue and sets its hours in the routing", async () => {
    const routing = {
      addressId: 11,
      routeKind: "inbox",
      routeTarget: "",
      routeName: "",
      schedule: { zone: "", days: [] },
      afterHours: {
        action: "none",
        text: "",
        routeKind: "inbox",
        routeTarget: "",
        routeName: "",
      },
      firstContactText: "",
    };
    const calls = api({
      "GET /api/v1/messaging/addresses/11/routing": () => json(routing),
      "PUT /api/v1/messaging/addresses/11/routing": () =>
        json({ ...routing, routeKind: "queue", routeTarget: "7" }),
      "GET /api/v1/queues": () =>
        json({
          items: [
            { id: 7, name: "support", textEnabled: true },
            { id: 8, name: "voiceonly", textEnabled: false },
          ],
        }),
      "GET /api/v1/me/inbox/agents": () => json({ items: [] }),
      "GET /api/v1/extensions": () => json({ items: [] }),
      "GET /api/v1/ring-groups": () => json({ items: [] }),
    });
    renderApp("/messaging/chat");
    await screen.findByRole("table", { name: "Chat sites" });
    fireEvent.click(
      screen.getByRole("button", { name: "Routing and hours of Help desk" }),
    );
    expect(
      await screen.findByRole("dialog", {
        name: "Routing and hours of Help desk",
      }),
    ).toBeInTheDocument();
    fireEvent.change(await screen.findByLabelText("New conversations go to"), {
      target: { value: "queue" },
    });
    fireEvent.change(await screen.findByLabelText("Which"), {
      target: { value: "7" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Add a window" }));
    fireEvent.change(screen.getByLabelText("Time zone"), {
      target: { value: "Europe/Brussels" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save routing" }));
    await waitFor(() =>
      expect(
        calls.find(
          (c) =>
            c.method === "PUT" &&
            c.url === "/api/v1/messaging/addresses/11/routing",
        )?.body,
      ).toMatchObject({
        routeKind: "queue",
        routeTarget: "7",
        schedule: {
          zone: "Europe/Brussels",
          days: [{ day: 1, from: "09:00", to: "17:00" }],
        },
      }),
    );
    expect(
      await screen.findByText("Routing of Help desk saved."),
    ).toBeVisible();
  });
});
