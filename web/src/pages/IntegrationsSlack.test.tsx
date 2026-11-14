import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, mockApi, noContent, renderApp } from "../test/api";
import type { SlackDestination } from "../api/slack";

const me = (role: string) => ({
  "GET /api/v1/auth/me": () => json({ username: "pat", role }),
});

function dest(extra: Partial<SlackDestination> = {}): SlackDestination {
  return {
    id: 1,
    name: "Phone",
    mode: "webhook",
    channel: "",
    events: ["call.missed", "voicemail.received"],
    extensions: [],
    enabled: true,
    disabledReason: "",
    consecutiveFailures: 0,
    lastSuccessAt: null,
    lastFailureAt: null,
    lastError: "",
    createdBy: "user:pat",
    createdAt: "2026-11-12T09:00:00Z",
    updatedAt: "2026-11-12T09:00:00Z",
    pending: 0,
    dead: 0,
    hasSecret: true,
    ...extra,
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("Slack notifications (spec integrations P17)", () => {
  it("is linked from the sidebar for administrators only", async () => {
    mockApi({
      ...me("operator"),
      "GET /api/v1/integrations/slack": () => json({ items: [] }),
    });
    const view = renderApp("/integrations/events");
    await screen.findByRole("link", { name: "Events" });
    expect(screen.queryByRole("link", { name: "Slack" })).toBeNull();
    view.unmount();
    vi.unstubAllGlobals();
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/slack": () => json({ items: [] }),
    });
    renderApp("/integrations/events");
    expect(await screen.findByRole("link", { name: "Slack" })).toHaveAttribute(
      "href",
      "/integrations/slack",
    );
  });

  it("adds an incoming webhook and never shows the URL again", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/slack": [
        () => json({ items: [] }),
        () => json({ items: [dest()] }),
      ],
      "POST /api/v1/integrations/slack": () => json(dest(), 201),
    });
    renderApp("/integrations/slack");
    fireEvent.click(
      await screen.findByRole("button", { name: "Add a Slack channel" }),
    );
    const form = screen.getByRole("form", { name: "New Slack destination" });
    fireEvent.change(within(form).getByLabelText("Webhook URL"), {
      target: { value: "https://hooks.slack.com/services/T0/B0/XYZ" },
    });
    fireEvent.click(within(form).getByRole("button", { name: "Add channel" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST")).toBe(true),
    );
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      name: "Phone",
      mode: "webhook",
      secret: "https://hooks.slack.com/services/T0/B0/XYZ",
      events: ["call.missed", "voicemail.received"],
      extensions: [],
    });
    const panel = await screen.findByRole("article", {
      name: "Phone Slack destination",
    });
    expect(panel.textContent).not.toContain("hooks.slack.com");
  });

  it("asks for a channel in bot mode and sends the extension filter", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/slack": () => json({ items: [] }),
      "POST /api/v1/integrations/slack": () => json(dest({ mode: "bot" }), 201),
    });
    renderApp("/integrations/slack");
    fireEvent.click(
      await screen.findByRole("button", { name: "Add a Slack channel" }),
    );
    const form = screen.getByRole("form", { name: "New Slack destination" });
    fireEvent.change(within(form).getByLabelText("How Hello posts"), {
      target: { value: "bot" },
    });
    fireEvent.change(within(form).getByLabelText("Bot token"), {
      target: { value: "xoxb-1" },
    });
    fireEvent.change(within(form).getByLabelText("Channel"), {
      target: { value: "#phone" },
    });
    fireEvent.change(within(form).getByLabelText("Only these extensions"), {
      target: { value: "101, 102" },
    });
    fireEvent.click(
      within(form).getByRole("checkbox", { name: "New voicemail messages" }),
    );
    fireEvent.click(within(form).getByRole("button", { name: "Add channel" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST")).toBe(true),
    );
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      mode: "bot",
      channel: "#phone",
      secret: "xoxb-1",
      events: ["call.missed"],
      extensions: ["101", "102"],
    });
  });

  it("explains why a channel is off, and shows the failed posts", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/slack": () =>
        json({
          items: [
            dest({
              enabled: false,
              disabledReason: "auth_failed",
              lastError: "slack: token_revoked",
              dead: 2,
            }),
          ],
        }),
    });
    renderApp("/integrations/slack");
    expect(
      await screen.findByText("This channel is switched off"),
    ).toBeVisible();
    expect(screen.getByText(/Slack refused the credential/)).toBeVisible();
    expect(screen.getByText(/token_revoked/)).toBeVisible();
    expect(screen.getByText("2 failed")).toBeVisible();
  });

  it("sends a test message and reports Slack's answer", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/slack": () => json({ items: [dest()] }),
      "POST /api/v1/integrations/slack/1/test": () =>
        json({
          delivered: false,
          status: 404,
          error: "the Slack webhook no longer exists",
        }),
    });
    renderApp("/integrations/slack");
    fireEvent.click(
      await screen.findByRole("button", { name: "Send a test message" }),
    );
    expect(
      await screen.findByText(
        /Slack refused the test message: the Slack webhook no longer exists/,
      ),
    ).toBeVisible();
  });

  it("saves without the secret unless one was typed", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/slack": () => json({ items: [dest()] }),
      "PATCH /api/v1/integrations/slack/1": () => json(dest()),
    });
    renderApp("/integrations/slack");
    const form = await screen.findByRole("form", { name: "Phone settings" });
    fireEvent.click(
      within(form).getByRole("button", { name: "Save settings" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PATCH")?.body).not.toHaveProperty(
      "secret",
    );
  });

  it("retries a dead post and deletes a channel after a confirmation", async () => {
    const calls = mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/slack": () =>
        json({ items: [dest({ dead: 1 })] }),
      "GET /api/v1/integrations/slack/1/posts?limit=50": () =>
        json({
          items: [
            {
              id: 9,
              destinationId: 1,
              eventId: "evt_9",
              eventType: "call.missed",
              status: "dead",
              attempts: 8,
              nextAttemptAt: null,
              lastError: "slack: channel_not_found",
              deliveredAt: null,
              createdAt: "2026-11-12T09:30:00Z",
              updatedAt: "2026-11-12T09:31:00Z",
            },
          ],
          nextBefore: 0,
        }),
      "POST /api/v1/integrations/slack/posts/9/retry": () => json({}),
      "DELETE /api/v1/integrations/slack/1": () => noContent(),
    });
    renderApp("/integrations/slack");
    fireEvent.click(await screen.findByRole("tab", { name: "Posts" }));
    expect(await screen.findByText("slack: channel_not_found")).toBeVisible();
    fireEvent.click(screen.getByRole("button", { name: "Retry" }));
    await waitFor(() =>
      expect(calls.some((c) => c.url.endsWith("/posts/9/retry"))).toBe(true),
    );
    fireEvent.click(screen.getByRole("tab", { name: "Settings" }));
    fireEvent.click(await screen.findByRole("button", { name: "Delete" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete channel" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });

  it("shows the server's message when the destination is refused", async () => {
    mockApi({
      ...me("admin"),
      "GET /api/v1/integrations/slack": () => json({ items: [] }),
      "POST /api/v1/integrations/slack": () =>
        apiError(
          400,
          "invalid",
          "secret: must be a Slack incoming webhook URL",
        ),
    });
    renderApp("/integrations/slack");
    fireEvent.click(
      await screen.findByRole("button", { name: "Add a Slack channel" }),
    );
    const form = screen.getByRole("form", { name: "New Slack destination" });
    fireEvent.change(within(form).getByLabelText("Webhook URL"), {
      target: { value: "https://evil.example" },
    });
    fireEvent.click(within(form).getByRole("button", { name: "Add channel" }));
    expect(
      await screen.findByText(/must be a Slack incoming webhook URL/),
    ).toBeVisible();
  });
});
