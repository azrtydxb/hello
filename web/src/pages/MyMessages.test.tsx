import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  renderApp,
  type Routes,
} from "../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const conv = {
  key: "100>200",
  scope: "direct",
  scopeRef: "",
  messages: 1,
  lastId: 7,
  lastAt: STAMP,
  lastSender: "200",
  lastRecipient: "100",
  lastSnippet: "are you in?",
  lastStatus: "delivered",
  parties: ["100", "200"],
};

function api(role: string, more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    "GET /api/v1/me/messages/conversations": () => json({ items: [conv] }),
    "GET /api/v1/extensions": () => json({ items: [] }),
    "GET /api/v1/ring-groups": () => json({ items: [] }),
    "GET /api/v1/paging-groups": () => json({ items: [] }),
    ...more,
  });
}

describe("MyMessages", () => {
  it("shows a viewer their own conversations and lets them reply", async () => {
    const calls = api("viewer", {
      "GET /api/v1/me/messages?conversation=100%3E200": () =>
        json({
          items: [
            {
              id: 7,
              batch: "b",
              sender: "200",
              senderKind: "extension",
              senderName: "Support",
              actor: "",
              recipient: "100",
              recipientKind: "extension",
              scope: "direct",
              scopeRef: "",
              trunk: "",
              contentType: "text/plain",
              body: "are you in?",
              bodyBytes: 11,
              status: "delivered",
              detail: "",
              attempts: 1,
              createdAt: STAMP,
              expiresAt: STAMP,
              deliveredAt: STAMP,
              recipients: 1,
              delivered: 1,
              pending: 0,
              failed: 0,
            },
          ],
          next: "",
        }),
      "POST /api/v1/me/messages": () =>
        json({ batch: "c", scope: "direct", recipients: 1 }, 202),
    });
    renderApp("/me/messages");
    fireEvent.click(await screen.findByRole("button", { name: /100 and 200/ }));
    const thread = await screen.findByRole("list", { name: "Messages" });
    expect(within(thread).getByText("are you in?")).toBeInTheDocument();
    // Never the administrators' history.
    expect(calls.some((c) => c.url.startsWith("/api/v1/messages"))).toBe(false);

    fireEvent.click(screen.getByRole("button", { name: "New message" }));
    const dialog = await screen.findByRole("dialog", { name: "New message" });
    // A user cannot broadcast to every phone.
    expect(
      within(dialog).queryByRole("option", { name: "All phones" }),
    ).toBeNull();
    fireEvent.change(within(dialog).getByLabelText("Send to"), {
      target: { value: "external" },
    });
    fireEvent.change(within(dialog).getByLabelText("Phone number"), {
      target: { value: "+32475000000" },
    });
    fireEvent.change(within(dialog).getByLabelText("Message"), {
      target: { value: "On my way" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Send message" }));
    await screen.findByText("Message queued for 1 recipient.");
    expect(calls.find((c) => c.method === "POST")?.url).toBe(
      "/api/v1/me/messages",
    );
  });

  it("explains an account that is not linked to an extension", async () => {
    mockApi({
      "GET /api/v1/auth/me": () => json({ username: "u", role: "viewer" }),
      "GET /api/v1/me/messages/conversations": () =>
        apiError(403, "no_extension", "not linked"),
    });
    renderApp("/me/messages");
    expect(
      await screen.findByText("Your account is not linked to an extension"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "New message" })).toBeNull();
  });
});
