import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  renderApp,
  type Routes,
} from "../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const direct = {
  key: "100>200",
  scope: "direct",
  scopeRef: "",
  messages: 2,
  lastId: 7,
  lastAt: STAMP,
  lastSender: "100",
  lastRecipient: "200",
  lastSnippet: "lunch at noon",
  lastStatus: "delivered",
  parties: ["100", "200"],
};

const drill = {
  key: "broadcast",
  scope: "broadcast",
  scopeRef: "",
  messages: 1,
  lastId: 9,
  lastAt: STAMP,
  lastSender: "system",
  lastRecipient: "100",
  lastSnippet: "Fire drill at 3pm",
  lastStatus: "pending",
  parties: [],
};

const message = (over: Record<string, unknown>) => ({
  id: 7,
  batch: "b7",
  sender: "100",
  senderKind: "extension",
  senderName: "Sales",
  actor: "",
  recipient: "200",
  recipientKind: "extension",
  scope: "direct",
  scopeRef: "",
  trunk: "",
  contentType: "text/plain",
  body: "lunch at noon",
  bodyBytes: 13,
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
  ...over,
});

const EXTENSIONS = {
  items: [
    { id: 1, number: "100", name: "Sales", externalNumber: "" },
    { id: 2, number: "200", name: "Support", externalNumber: "" },
  ],
};

function api(role: string, more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    "GET /api/v1/messages/conversations": () => json({ items: [direct, drill] }),
    "GET /api/v1/extensions": () => json(EXTENSIONS),
    "GET /api/v1/ring-groups": () => json({ items: [{ id: 1, name: "sales" }] }),
    "GET /api/v1/paging-groups": () => json({ items: [] }),
    ...more,
  });
}

describe("Messages", () => {
  it("lists conversations and opens a thread with delivery status", async () => {
    api("admin", {
      "GET /api/v1/messages?conversation=broadcast": () =>
        json({
          items: [
            message({
              id: 9,
              sender: "system",
              senderKind: "system",
              senderName: "Admin",
              scope: "broadcast",
              body: "Fire drill at 3pm",
              status: "pending",
              recipients: 5,
              delivered: 3,
              pending: 2,
              failed: 0,
            }),
          ],
          next: "",
        }),
    });
    renderApp("/messages");
    expect(await screen.findByText("Fire drill at 3pm")).toBeInTheDocument();
    expect(screen.getByText("100 and 200")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: /Everyone/ }));
    const thread = await screen.findByRole("list", { name: "Messages" });
    expect(within(thread).getByText("Fire drill at 3pm")).toBeInTheDocument();
    expect(
      within(thread).getByText("3 of 5 delivered, 2 waiting"),
    ).toBeInTheDocument();
    expect(within(thread).getByText("Waiting")).toBeInTheDocument();
  });

  it("searches the history by text and status", async () => {
    const calls = api("admin", {
      "GET /api/v1/messages/conversations?q=drill&status=pending": () =>
        json({ items: [drill] }),
    });
    renderApp("/messages");
    await screen.findByText("100 and 200");
    fireEvent.change(screen.getByLabelText("Search messages"), {
      target: { value: "drill" },
    });
    fireEvent.change(screen.getByLabelText("Status"), {
      target: { value: "pending" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    await waitFor(() => expect(screen.queryByText("100 and 200")).toBeNull());
    expect(screen.getByText("Fire drill at 3pm")).toBeInTheDocument();
    expect(calls.map((c) => c.url)).toContain(
      "/api/v1/messages/conversations?q=drill&status=pending",
    );
  });

  it("sends a message to an extension", async () => {
    const calls = api("operator", {
      "POST /api/v1/messages": () =>
        json({ batch: "b1", scope: "direct", recipients: 1 }, 202),
    });
    renderApp("/messages");
    fireEvent.click(await screen.findByRole("button", { name: "New message" }));
    const dialog = await screen.findByRole("dialog", { name: "New message" });
    await within(dialog).findByRole("option", { name: "200 Support" });
    fireEvent.change(within(dialog).getByLabelText("Recipient"), {
      target: { value: "200" },
    });
    fireEvent.change(within(dialog).getByLabelText("Message"), {
      target: { value: "Server room is cold" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Send message" }),
    );
    expect(
      await screen.findByText("Message queued for 1 recipient."),
    ).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")).toMatchObject({
      url: "/api/v1/messages",
      body: {
        targetKind: "extension",
        target: "200",
        body: "Server room is cold",
      },
    });
  });

  it("sends a system broadcast only after it is confirmed", async () => {
    const calls = api("admin", {
      "POST /api/v1/messages": () =>
        json({ batch: "b2", scope: "broadcast", recipients: 40 }, 202),
    });
    renderApp("/messages");
    fireEvent.click(await screen.findByRole("button", { name: "New message" }));
    const dialog = await screen.findByRole("dialog", { name: "New message" });
    fireEvent.change(within(dialog).getByLabelText("Send to"), {
      target: { value: "broadcast" },
    });
    fireEvent.change(within(dialog).getByLabelText("Message"), {
      target: { value: "Fire drill at 3pm" },
    });
    const send = within(dialog).getByRole("button", { name: "Send message" });
    expect(send).toBeDisabled();
    fireEvent.click(
      within(dialog).getByRole("checkbox", { name: /Send to every phone/ }),
    );
    expect(send).toBeEnabled();
    fireEvent.click(send);
    expect(
      await screen.findByText("Message queued for 40 recipients."),
    ).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      targetKind: "broadcast",
      target: "",
      body: "Fire drill at 3pm",
      confirm: true,
    });
  });

  it("sends to a ring group and an external number", async () => {
    const calls = api("operator", {
      "POST /api/v1/messages": () =>
        json({ batch: "b3", scope: "direct", recipients: 1 }, 202),
    });
    renderApp("/messages");
    fireEvent.click(await screen.findByRole("button", { name: "New message" }));
    const dialog = await screen.findByRole("dialog", { name: "New message" });
    await within(dialog).findByRole("option", { name: "200 Support" });
    fireEvent.change(within(dialog).getByLabelText("Send to"), {
      target: { value: "ring_group" },
    });
    fireEvent.change(within(dialog).getByLabelText("Recipient"), {
      target: { value: "sales" },
    });
    fireEvent.change(within(dialog).getByLabelText("Message"), {
      target: { value: "Standup now" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Send message" }),
    );
    await screen.findByText("Message queued for 1 recipient.");
    expect(calls.find((c) => c.method === "POST")?.body).toMatchObject({
      targetKind: "ring_group",
      target: "sales",
    });

    fireEvent.click(screen.getByRole("button", { name: "New message" }));
    const again = await screen.findByRole("dialog", { name: "New message" });
    fireEvent.change(within(again).getByLabelText("Send to"), {
      target: { value: "external" },
    });
    fireEvent.change(within(again).getByLabelText("Phone number"), {
      target: { value: "+32475000000" },
    });
    fireEvent.change(within(again).getByLabelText("Message"), {
      target: { value: "Your order shipped" },
    });
    fireEvent.click(within(again).getByRole("button", { name: "Send message" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "POST")).toHaveLength(2),
    );
    expect(calls.filter((c) => c.method === "POST")[1]?.body).toMatchObject({
      targetKind: "external",
      target: "+32475000000",
    });
  });

  it("shows why a send was refused", async () => {
    api("operator", {
      "POST /api/v1/messages": () =>
        apiError(
          409,
          "no_messaging_trunk",
          "no enabled trunk has messaging turned on",
        ),
    });
    renderApp("/messages");
    fireEvent.click(await screen.findByRole("button", { name: "New message" }));
    const dialog = await screen.findByRole("dialog", { name: "New message" });
    fireEvent.change(within(dialog).getByLabelText("Send to"), {
      target: { value: "external" },
    });
    fireEvent.change(within(dialog).getByLabelText("Phone number"), {
      target: { value: "+32475000000" },
    });
    fireEvent.change(within(dialog).getByLabelText("Message"), {
      target: { value: "x" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Send message" }));
    expect(
      await within(dialog).findByText(
        "no enabled trunk has messaging turned on",
      ),
    ).toBeInTheDocument();
    // The dialog stays open so the sender can fix it.
    expect(dialog).toBeInTheDocument();
  });

  it("lets an operator send but never reads other people's messages", async () => {
    const calls = api("operator");
    renderApp("/messages");
    expect(
      await screen.findByText("Message history is private"),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "New message" })).toBeEnabled();
    expect(screen.queryByRole("button", { name: "Settings" })).toBeNull();
    expect(
      calls.some((c) => c.url.startsWith("/api/v1/messages/conversations")),
    ).toBe(false);
  });

  it("gives a viewer no way to send", async () => {
    api("viewer");
    renderApp("/messages");
    await screen.findByText("Message history is private");
    expect(screen.queryByRole("button", { name: "New message" })).toBeNull();
  });

  it("edits the retention and limits, and purges the history", async () => {
    const calls = api("admin", {
      "GET /api/v1/messages/settings": () =>
        json({
          enabled: true,
          ttlHours: 24,
          retentionDays: 30,
          ratePerMinute: 30,
          maxQueuedPerExtension: 100,
        }),
      "PUT /api/v1/messages/settings": () =>
        json({
          enabled: true,
          ttlHours: 48,
          retentionDays: 90,
          ratePerMinute: 30,
          maxQueuedPerExtension: 100,
        }),
      "POST /api/v1/messages/purge": () => json({ deleted: 12 }),
    });
    renderApp("/messages");
    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Messaging settings",
    });
    const ttl = await within(dialog).findByLabelText(
      "Hold undelivered messages for (hours)",
    );
    fireEvent.change(ttl, { target: { value: "48" } });
    fireEvent.change(within(dialog).getByLabelText("Keep history for (days)"), {
      target: { value: "90" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Save settings" }),
    );
    expect(
      await screen.findByText("Messaging settings saved."),
    ).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      enabled: true,
      ttlHours: 48,
      retentionDays: 90,
      ratePerMinute: 30,
      maxQueuedPerExtension: 100,
    });

    fireEvent.click(screen.getByRole("button", { name: "Settings" }));
    const again = await screen.findByRole("dialog", {
      name: "Messaging settings",
    });
    await within(again).findByLabelText("Keep history for (days)");
    fireEvent.click(
      within(again).getByRole("button", { name: "Delete old history…" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: "Delete message history?",
    });
    fireEvent.change(within(confirm).getByLabelText("Days"), {
      target: { value: "7" },
    });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Delete history" }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.url === "/api/v1/messages/purge")).toBeTruthy(),
    );
    expect(calls.find((c) => c.url === "/api/v1/messages/purge")?.body).toEqual({
      olderThanDays: 7,
    });
  });

  it("refuses settings out of range before sending them", async () => {
    const calls = api("admin", {
      "GET /api/v1/messages/settings": () =>
        json({
          enabled: true,
          ttlHours: 24,
          retentionDays: 30,
          ratePerMinute: 30,
          maxQueuedPerExtension: 100,
        }),
    });
    renderApp("/messages");
    fireEvent.click(await screen.findByRole("button", { name: "Settings" }));
    const dialog = await screen.findByRole("dialog", {
      name: "Messaging settings",
    });
    fireEvent.change(
      await within(dialog).findByLabelText("Keep history for (days)"),
      { target: { value: "0" } },
    );
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Save settings" }),
    );
    expect(
      await within(dialog).findByText("Enter a whole number from 1 to 3650."),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });
});
