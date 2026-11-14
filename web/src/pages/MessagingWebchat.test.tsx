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
  siteKey: "0123456789abcdef01234567",
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
        hours: { zone: "", days: [] },
        maxOpenUnassigned: 200,
        retentionDays: 0,
      });
      expect(post?.body).not.toHaveProperty("turnstileSecret");
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
});
