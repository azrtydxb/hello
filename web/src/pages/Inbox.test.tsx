import {
  act,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import {
  apiError,
  json,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const conv = (over: Record<string, unknown> = {}) => ({
  id: 7,
  channelId: 1,
  channelKind: "sms",
  channelName: "Main",
  addressId: 1,
  address: "+3270123456",
  peer: "+32475123456",
  peerKind: "phone",
  peerName: "",
  peerNameSource: "",
  status: "open",
  snoozedUntil: null,
  assigneeUserId: null,
  assigneeName: "",
  priority: "normal",
  tags: [],
  preview: "where is my parcel",
  unread: 0,
  optedOut: false,
  lastMessageAt: STAMP,
  lastInboundAt: STAMP,
  firstResponseAt: null,
  resolvedAt: null,
  createdAt: STAMP,
  ...over,
});

const msg = (over: Record<string, unknown>) => ({
  id: 1,
  conversationId: 7,
  direction: "in",
  kind: "message",
  senderKind: "customer",
  senderUserId: null,
  senderName: "",
  body: "where is my parcel",
  status: "delivered",
  errorCode: "",
  clientMsgId: "",
  createdAt: STAMP,
  sentAt: null,
  deliveredAt: null,
  readAt: null,
  ...over,
});

const STATUS = {
  "GET /api/v1/me/inbox/status": () =>
    json({ enabled: true, unread: 0, unassigned: 1, senders: [] }),
  "GET /api/v1/me/inbox/agents": () =>
    json({ items: [{ id: 2, username: "agnes", displayName: "Agnes" }] }),
};

/** A stand-in for EventSource that tests can push events through. */
class FakeEventSource {
  static instances: FakeEventSource[] = [];
  onopen: (() => void) | null = null;
  closed = false;
  listeners = new Map<string, ((e: MessageEvent<string>) => void)[]>();
  constructor(
    public url: string,
    public init?: EventSourceInit,
  ) {
    FakeEventSource.instances.push(this);
  }
  addEventListener(type: string, fn: (e: MessageEvent<string>) => void) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }
  close() {
    this.closed = true;
  }
  emit(type: string, data: object) {
    for (const fn of this.listeners.get(type) ?? []) {
      fn(new MessageEvent(type, { data: JSON.stringify(data) }));
    }
  }
}

function inbox(role: string, more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    ...STATUS,
    "GET /api/v1/inbox/conversations?view=all": () =>
      json({ items: [conv({ unread: 1 })], next: "" }),
    "GET /api/v1/inbox/conversations/7": () => json(conv({ unread: 1 })),
    "GET /api/v1/inbox/conversations/7/messages": () =>
      json({
        items: [
          msg({
            id: 3,
            direction: "out",
            senderKind: "user",
            senderName: "otto",
            body: "Let me check",
            status: "sent",
            createdAt: "2026-10-01T10:02:00Z",
          }),
          msg({
            id: 2,
            kind: "note",
            direction: "out",
            senderKind: "user",
            senderName: "otto",
            body: "VIP customer",
            createdAt: "2026-10-01T10:01:00Z",
          }),
          msg({ id: 1 }),
        ],
        next: "",
      }),
    "POST /api/v1/inbox/conversations/7/read": () => noContent(),
    "GET /api/v1/inbox/conversations/7/calls": () =>
      json({
        items: [
          {
            id: 40,
            startTime: "2026-10-01T09:00:00Z",
            durationMs: 65000,
            direction: "inbound",
            kind: "call",
            source: "+32475123456",
            destination: "101",
            answered: true,
          },
          {
            id: 39,
            startTime: "2026-09-30T09:00:00Z",
            durationMs: 0,
            direction: "outbound",
            kind: "call",
            source: "101",
            destination: "+32475123456",
            answered: false,
          },
        ],
        next: "",
      }),
    ...more,
  });
}

beforeEach(() => {
  FakeEventSource.instances = [];
  vi.stubGlobal("EventSource", FakeEventSource);
});

afterEach(() => {
  vi.unstubAllGlobals();
});

async function open() {
  renderApp("/inbox");
  const list = await screen.findByRole("list", { name: "Conversations" });
  fireEvent.click(
    await within(list).findByRole("button", { name: /\+32475123456/ }),
  );
  return screen.findByRole("list", { name: "Messages" });
}

describe("Inbox", () => {
  it("lists the conversations and opens a thread, oldest message first", async () => {
    inbox("operator");
    const thread = await open();
    const items = within(thread).getAllByRole("listitem");
    expect(items.map((i) => i.textContent)).toEqual([
      expect.stringContaining("where is my parcel"),
      expect.stringContaining("VIP customer"),
      expect.stringContaining("Let me check"),
    ]);
    expect(within(thread).getByText("Note by otto")).toBeInTheDocument();
    expect(within(thread).getByText("Sent to the carrier")).toBeInTheDocument();
    // Opening it reads it.
    await waitFor(() =>
      expect(
        (globalThis.fetch as ReturnType<typeof vi.fn>).mock.calls.some(
          ([url, init]) =>
            String(url) === "/api/v1/inbox/conversations/7/read" &&
            (init as RequestInit).method === "POST",
        ),
      ).toBe(true),
    );
  });

  it("lets an administrator redact a message, after a confirmation", async () => {
    let redacted = false;
    const calls = inbox("admin", {
      "GET /api/v1/inbox/conversations/7/messages": () =>
        json({
          items: [
            msg(
              redacted
                ? { id: 1, body: "", redactedAt: "2026-10-02T08:00:00Z" }
                : { id: 1, body: "my card is 4111" },
            ),
          ],
          next: "",
        }),
      "POST /api/v1/inbox/conversations/7/messages/1/redact": () => {
        redacted = true;
        return json({
          message: msg({ id: 1, body: "", redactedAt: "2026-10-02T08:00:00Z" }),
          providers: [],
          mediaObjects: 1,
          mediaObjectsPending: 0,
        });
      },
    });
    const thread = await open();
    expect(within(thread).getByText("my card is 4111")).toBeInTheDocument();
    fireEvent.click(within(thread).getByRole("button", { name: /Redact/ }));
    const dialog = await screen.findByRole("dialog", {
      name: /Redact this message/,
    });
    expect(calls.some((c) => c.url.endsWith("/redact"))).toBe(false);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Redact message" }),
    );
    await waitFor(() =>
      expect(within(thread).queryByText("my card is 4111")).toBeNull(),
    );
    expect(within(thread).getByText(/^Redacted/)).toBeInTheDocument();
    expect(
      calls.filter((c) => c.method === "POST" && c.url.endsWith("/redact")),
    ).toHaveLength(1);
    expect(within(thread).queryByRole("button", { name: /Redact/ })).toBeNull();
  });

  it("offers no redaction to an operator", async () => {
    inbox("operator");
    const thread = await open();
    expect(within(thread).queryByRole("button", { name: /Redact/ })).toBeNull();
  });

  it("names the router's automatic texts as such", async () => {
    inbox("operator", {
      "GET /api/v1/inbox/conversations/7/messages": () =>
        json({
          items: [
            msg({
              id: 2,
              direction: "out",
              senderKind: "system",
              body: "We open at nine",
              status: "queued",
            }),
            msg({ id: 1 }),
          ],
          next: "",
        }),
    });
    const thread = await open();
    expect(within(thread).getByText("We open at nine")).toBeInTheDocument();
    expect(within(thread).getByText(/Automatic reply/)).toBeInTheDocument();
  });

  it("shows customer text as text, never as markup", async () => {
    inbox("operator", {
      "GET /api/v1/inbox/conversations/7/messages": () =>
        json({
          items: [
            msg({
              id: 1,
              body: '<img src=x onerror="document.title=1"><b>bold</b>',
            }),
          ],
          next: "",
        }),
    });
    const thread = await open();
    expect(
      within(thread).getByText(
        '<img src=x onerror="document.title=1"><b>bold</b>',
      ),
    ).toBeInTheDocument();
    expect(thread.querySelector("img, b, script")).toBeNull();
  });

  it("will not let anyone text a customer who opted out", async () => {
    inbox("operator", {
      "GET /api/v1/inbox/conversations?view=all": () =>
        json({ items: [conv({ optedOut: true })], next: "" }),
      "GET /api/v1/inbox/conversations/7": () => json(conv({ optedOut: true })),
    });
    await open();
    expect(
      await screen.findByText("The customer opted out"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Reply to the customer")).toBeDisabled();
    expect(screen.getByRole("button", { name: "Send" })).toBeDisabled();
    // Internal notes stay possible.
    fireEvent.click(screen.getByRole("button", { name: "Internal note" }));
    expect(screen.getByLabelText("Internal note")).toBeEnabled();
  });

  it("sends a reply once, even when the first try fails", async () => {
    const answers = [
      () => apiError(422, "opted_out", "the customer opted out"),
      () =>
        json(
          msg({
            id: 9,
            direction: "out",
            senderKind: "user",
            body: "On its way",
            status: "sending",
          }),
          201,
        ),
    ];
    let n = 0;
    const calls = inbox("operator", {
      "POST /api/v1/inbox/conversations/7/messages": () => answers[n++]!(),
    });
    await open();
    const box = await screen.findByLabelText("Reply to the customer");
    fireEvent.change(box, { target: { value: "On its way" } });
    expect(screen.getByText("10 characters, 1 text")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    expect(
      await screen.findByText("the customer opted out"),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(n).toBe(2));
    const sends = calls.filter(
      (c) => c.method === "POST" && c.url.endsWith("/conversations/7/messages"),
    );
    const ids = sends.map(
      (c) => (c.body as { clientMsgId: string }).clientMsgId,
    );
    expect(ids).toHaveLength(2);
    expect(ids[0]).toBeTruthy();
    // The same id: the server sends it once.
    expect(ids[1]).toBe(ids[0]);
    await waitFor(() => expect(box).toHaveValue(""));
  });

  it("counts texts for non-GSM characters", async () => {
    inbox("operator");
    await open();
    const box = await screen.findByLabelText("Reply to the customer");
    fireEvent.change(box, { target: { value: "日".repeat(71) } });
    expect(
      screen.getByText("71 characters, 2 texts (non-GSM characters)"),
    ).toBeInTheDocument();
  });

  it("takes, resolves and hands over a conversation", async () => {
    const calls = inbox("operator", {
      "POST /api/v1/inbox/conversations/7/claim": () =>
        json(conv({ assigneeUserId: 1, assigneeName: "u" })),
      "POST /api/v1/inbox/conversations/7/status": () =>
        json(conv({ status: "resolved" })),
      "POST /api/v1/inbox/conversations/7/assign": () =>
        json(conv({ assigneeUserId: 2, assigneeName: "Agnes" })),
    });
    await open();
    fireEvent.click(await screen.findByRole("button", { name: "Take it" }));
    fireEvent.click(await screen.findByRole("button", { name: "Resolve" }));
    fireEvent.change(await screen.findByLabelText("Hand over to"), {
      target: { value: "2" },
    });
    await waitFor(() => {
      expect(calls.some((c) => c.url.endsWith("/claim"))).toBe(true);
      expect(
        calls.find((c) => c.method === "POST" && c.url.endsWith("/status"))
          ?.body,
      ).toEqual({
        status: "resolved",
      });
      expect(calls.find((c) => c.url.endsWith("/assign"))?.body).toEqual({
        userId: 2,
      });
    });
  });

  it("saves the customer as a contact and shows the new name", async () => {
    const calls = inbox("operator", {
      "POST /api/v1/inbox/conversations/7/contact": () =>
        json(conv({ peerName: "Carla Peeters", peerNameSource: "personal" })),
    });
    await open();
    fireEvent.click(
      await screen.findByRole("button", { name: "Add to contacts" }),
    );
    fireEvent.change(screen.getByLabelText("First name"), {
      target: { value: "Carla" },
    });
    fireEvent.change(screen.getByLabelText("Last name"), {
      target: { value: "Peeters" },
    });
    // A supervisor may share it; the default is their own contacts.
    expect(screen.getByLabelText("Save to")).toHaveValue("personal");
    fireEvent.click(screen.getByRole("button", { name: "Save contact" }));
    await waitFor(() => {
      expect(calls.find((c) => c.url.endsWith("/contact"))?.body).toEqual({
        firstName: "Carla",
        lastName: "Peeters",
        company: "",
        book: "personal",
      });
    });
    expect(await screen.findByText("Carla Peeters")).toBeInTheDocument();
  });

  it("offers a call to the customer through click-to-call and lists recent calls", async () => {
    inbox("operator");
    await open();
    const call = await screen.findByRole("link", {
      name: "Call +32475123456",
    });
    expect(call).toHaveAttribute("href", "/dial?n=%2B32475123456");
    const recent = await screen.findByRole("list", { name: "Recent calls" });
    const rows = within(recent).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent("Incoming, 65 s");
    expect(rows[1]).toHaveTextContent("Outgoing, not answered");
  });

  it("says so when the customer has no recent calls", async () => {
    inbox("operator", {
      "GET /api/v1/inbox/conversations/7/calls": () =>
        json({ items: [], next: "" }),
    });
    await open();
    expect(await screen.findByText("No recent calls.")).toBeInTheDocument();
  });

  it("offers no call and asks for no calls for a chat visitor", async () => {
    const visitor = conv({ peerKind: "visitor", peer: "+32475123456" });
    const calls = inbox("operator", {
      "GET /api/v1/inbox/conversations?view=all": () =>
        json({ items: [visitor], next: "" }),
      "GET /api/v1/inbox/conversations/7": () => json(visitor),
    });
    await open();
    await screen.findByText("Details");
    expect(screen.queryByRole("link", { name: /^Call / })).toBeNull();
    expect(calls.some((c) => c.url.endsWith("/calls"))).toBe(false);
  });

  it("reads an agent's calls from their own route", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/me": () => json({ username: "u", role: "user" }),
      ...STATUS,
      "GET /api/v1/me/inbox/conversations?view=all": () =>
        json({
          items: [conv({ assigneeUserId: 5, assigneeName: "u" })],
          next: "",
        }),
      "GET /api/v1/me/inbox/conversations/7": () =>
        json(conv({ assigneeUserId: 5, assigneeName: "u" })),
      "GET /api/v1/me/inbox/conversations/7/messages": () =>
        json({ items: [msg({ id: 1 })], next: "" }),
      "GET /api/v1/me/inbox/conversations/7/calls": () =>
        json({ items: [], next: "" }),
    });
    renderApp("/me/inbox");
    const list = await screen.findByRole("list", { name: "Conversations" });
    fireEvent.click(
      await within(list).findByRole("button", { name: /\+32475123456/ }),
    );
    expect(await screen.findByText("No recent calls.")).toBeInTheDocument();
    expect(
      calls.some((c) => c.url === "/api/v1/me/inbox/conversations/7/calls"),
    ).toBe(true);
    expect(calls.some((c) => c.url.startsWith("/api/v1/inbox/"))).toBe(false);
  });

  it("refreshes on a stream event and notifies without the text", async () => {
    const shown: { title: string; options?: NotificationOptions }[] = [];
    class FakeNotification {
      static permission = "granted";
      constructor(title: string, options?: NotificationOptions) {
        shown.push({ title, options });
      }
    }
    vi.stubGlobal("Notification", FakeNotification);
    let lists = 0;
    inbox("operator", {
      "GET /api/v1/inbox/conversations?view=all": () => {
        lists++;
        return json({ items: [conv()], next: "" });
      },
      "GET /api/v1/inbox/conversations/9": () =>
        json(conv({ id: 9, peer: "+32475999999", peerName: "Ann" })),
    });
    renderApp("/inbox");
    await screen.findByRole("list", { name: "Conversations" });
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    expect(FakeEventSource.instances[0]!.url).toBe("/api/v1/me/inbox/stream");
    const before = lists;
    act(() => {
      FakeEventSource.instances[0]!.emit("message.in", {
        conversationId: 9,
        messageId: 12,
      });
    });
    await waitFor(() => expect(lists).toBeGreaterThan(before));
    await waitFor(() => expect(shown).toHaveLength(1));
    expect(shown[0]!.title).toBe("New message from Ann (+32475999999)");
    expect(JSON.stringify(shown[0])).not.toContain("parcel");
  });

  it("closes the stream when the page goes", async () => {
    inbox("operator");
    const view = renderApp("/inbox");
    await screen.findByRole("list", { name: "Conversations" });
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    view.unmount();
    expect(FakeEventSource.instances[0]!.closed).toBe(true);
  });

  it("starts a first-contact text only with an attestation", async () => {
    const calls = inbox("operator", {
      "GET /api/v1/me/inbox/status": () =>
        json({
          enabled: true,
          unread: 0,
          unassigned: 0,
          senders: [
            {
              addressId: 9,
              address: "+3270999000",
              label: "",
              channel: "Twilio",
            },
          ],
        }),
      "POST /api/v1/inbox/conversations": () =>
        json(conv({ id: 12, peer: "+32475123456" }), 201),
    });
    renderApp("/inbox");
    fireEvent.click(
      await screen.findByRole("button", { name: "Text a customer" }),
    );
    fireEvent.change(await screen.findByLabelText("Customer's number"), {
      target: { value: "+32475123456" },
    });
    fireEvent.change(screen.getByLabelText("Message"), {
      target: { value: "Your appointment is tomorrow" },
    });
    fireEvent.change(screen.getByLabelText("How did they agree?"), {
      target: { value: "asked on the web form" },
    });
    // Without the attestation the send button stays off.
    expect(screen.getByRole("button", { name: "Send text" })).toBeDisabled();
    fireEvent.click(screen.getByRole("checkbox"));
    fireEvent.click(screen.getByRole("button", { name: "Send text" }));
    await waitFor(() =>
      expect(
        calls.find(
          (c) => c.method === "POST" && c.url.endsWith("/inbox/conversations"),
        )?.body,
      ).toEqual({
        addressId: 9,
        to: "+32475123456",
        body: "Your appointment is tomorrow",
        consentAttestation: true,
        consentNote: "asked on the web form",
      }),
    );
    expect(
      await screen.findByText(
        "The text is queued and will be sent in a moment.",
      ),
    ).toBeInTheDocument();
  });

  it("offers no first-contact button when first contact is off", async () => {
    inbox("operator");
    renderApp("/inbox");
    await screen.findByRole("list", { name: "Conversations" });
    expect(
      screen.queryByRole("button", { name: "Text a customer" }),
    ).toBeNull();
  });

  it("says so when no channel has a number", async () => {
    inbox("operator", {
      "GET /api/v1/me/inbox/status": () =>
        json({ enabled: false, unread: 0, unassigned: 0, senders: [] }),
    });
    renderApp("/inbox");
    expect(
      await screen.findByText("No messaging channel yet"),
    ).toBeInTheDocument();
    expect(screen.queryByRole("list", { name: "Conversations" })).toBeNull();
  });

  it("offers an agent only their own inbox", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/me": () => json({ username: "u", role: "user" }),
      ...STATUS,
      "GET /api/v1/me/inbox/conversations?view=all": () =>
        json({
          items: [conv({ assigneeUserId: 5, assigneeName: "u" })],
          next: "",
        }),
    });
    renderApp("/me/inbox");
    const list = await screen.findByRole("list", { name: "Conversations" });
    expect(await within(list).findByText("+32475123456")).toBeInTheDocument();
    // No supervisor filter, and nothing from the supervisor routes.
    expect(screen.queryByLabelText("Show")).toBeNull();
    expect(calls.some((c) => c.url.startsWith("/api/v1/inbox/"))).toBe(false);
    expect(
      screen.getByRole("heading", { level: 1, name: "My inbox" }),
    ).toBeInTheDocument();
  });

  it("filters and searches through the API", async () => {
    const calls = inbox("operator", {
      "GET /api/v1/inbox/conversations?view=unassigned&status=resolved&q=parcel":
        () => json({ items: [], next: "" }),
    });
    renderApp("/inbox");
    await screen.findByRole("list", { name: "Conversations" });
    fireEvent.change(screen.getByLabelText("Show"), {
      target: { value: "unassigned" },
    });
    fireEvent.change(screen.getByLabelText("State"), {
      target: { value: "resolved" },
    });
    fireEvent.change(screen.getByLabelText("Search"), {
      target: { value: "parcel" },
    });
    fireEvent.submit(screen.getByLabelText("Search").closest("form")!);
    expect(await screen.findByText("No conversations")).toBeInTheDocument();
    expect(
      calls.some(
        (c) =>
          c.url ===
          "/api/v1/inbox/conversations?view=unassigned&status=resolved&q=parcel",
      ),
    ).toBe(true);
  });
});

const aiState = (over: Record<string, unknown> = {}) => ({
  mode: "draft",
  triage: {
    language: "en",
    intent: "billing",
    urgency: "high",
    sentiment: "negative",
    spam: false,
    sensitive: false,
    confidence: 0.9,
    summary: "<b>Charged twice</b> for one order.",
    suggestedQueue: "Billing",
    suggestedTags: ["refund"],
    handoffRecommended: true,
    messageId: 1,
    reverted: false,
  },
  applied: { tags: ["refund"], priority: "normal" },
  spam: false,
  handoff: false,
  handoffReason: "",
  draft: {
    runId: 5,
    messageId: 1,
    text: "Sorry about that, we will refund the second charge.",
    createdAt: STAMP,
  },
  pending: false,
  ...over,
});

describe("Inbox AI", () => {
  it("shows the triage labels in the list and the summary as text", async () => {
    inbox("operator", {
      "GET /api/v1/inbox/conversations?view=all": () =>
        json({
          items: [conv({ ai: aiState({ mode: "auto", handoff: true }) })],
          next: "",
        }),
      "GET /api/v1/inbox/conversations/7": () =>
        json(
          conv({
            ai: aiState({
              mode: "auto",
              handoff: true,
              handoffReason: "urgent",
            }),
          }),
        ),
    });
    await open();
    const list = screen.getByRole("list", { name: "Conversations" });
    expect(within(list).getByText("Auto-reply")).toBeInTheDocument();
    expect(within(list).getByText("Needs a person")).toBeInTheDocument();
    expect(within(list).getByText("billing")).toBeInTheDocument();
    expect(within(list).getByText("Urgent")).toBeInTheDocument();
    const panel = await screen.findByRole("region", { name: "AI triage" });
    expect(
      within(panel).getByText("<b>Charged twice</b> for one order."),
    ).toBeInTheDocument();
    expect(panel.querySelector("b")).toBeNull();
    expect(within(panel).getByText("Auto-reply on")).toBeInTheDocument();
    expect(within(panel).getByText(/the text is urgent/)).toBeInTheDocument();
  });

  it("never sends the AI draft without a click", async () => {
    const calls = inbox("operator", {
      "GET /api/v1/inbox/conversations/7": () => json(conv({ ai: aiState() })),
      "POST /api/v1/inbox/conversations/7/messages": () =>
        json(
          msg({
            id: 9,
            direction: "out",
            senderKind: "user",
            status: "sending",
          }),
          201,
        ),
    });
    await open();
    const chip = await screen.findByRole("group", { name: "AI draft" });
    expect(
      within(chip).getByText(/we will refund the second charge/),
    ).toBeInTheDocument();
    const box = screen.getByLabelText("Reply to the customer");
    expect(box).toHaveValue("");
    const sent = () =>
      calls.filter(
        (c) =>
          c.method === "POST" && c.url.endsWith("/conversations/7/messages"),
      );
    expect(sent()).toHaveLength(0);
    fireEvent.click(within(chip).getByRole("button", { name: "Use draft" }));
    expect(box).toHaveValue(
      "Sorry about that, we will refund the second charge.",
    );
    // Using it only fills the composer: the agent still sends.
    expect(sent()).toHaveLength(0);
    fireEvent.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(sent()).toHaveLength(1));
  });

  it("dismisses a draft", async () => {
    inbox("operator", {
      "GET /api/v1/inbox/conversations/7": () => json(conv({ ai: aiState() })),
    });
    await open();
    const chip = await screen.findByRole("group", { name: "AI draft" });
    fireEvent.click(within(chip).getByRole("button", { name: "Dismiss" }));
    expect(screen.queryByRole("group", { name: "AI draft" })).toBeNull();
  });

  it("offers no draft while the customer is opted out", async () => {
    inbox("operator", {
      "GET /api/v1/inbox/conversations?view=all": () =>
        json({ items: [conv({ optedOut: true })], next: "" }),
      "GET /api/v1/inbox/conversations/7": () =>
        json(conv({ optedOut: true, ai: aiState() })),
    });
    await open();
    await screen.findByText("The customer opted out");
    expect(screen.queryByRole("group", { name: "AI draft" })).toBeNull();
  });

  it("undoes the triage's changes", async () => {
    const calls = inbox("operator", {
      "GET /api/v1/inbox/conversations/7": () =>
        json(conv({ tags: ["refund"], priority: "high", ai: aiState() })),
      "POST /api/v1/inbox/conversations/7/ai/revert": () =>
        json(
          conv({
            ai: aiState({
              applied: { tags: [], priority: "" },
              triage: { ...aiState().triage, reverted: true },
            }),
          }),
        ),
    });
    await open();
    const panel = await screen.findByRole("region", { name: "AI triage" });
    expect(
      within(panel).getByText(/Added refund; raised the priority/),
    ).toBeInTheDocument();
    fireEvent.click(within(panel).getByRole("button", { name: "Undo" }));
    expect(await within(panel).findByText("Undone")).toBeInTheDocument();
    expect(
      calls.some(
        (c) =>
          c.method === "POST" && c.url.endsWith("/conversations/7/ai/revert"),
      ),
    ).toBe(true);
  });

  it("labels a message the AI sent", async () => {
    inbox("operator", {
      "GET /api/v1/inbox/conversations/7/messages": () =>
        json({
          items: [
            msg({
              id: 2,
              direction: "out",
              senderKind: "ai",
              body: "Open 9 to 5",
              status: "sent",
            }),
            msg({ id: 1 }),
          ],
          next: "",
        }),
    });
    const thread = await open();
    expect(within(thread).getByText("Sent by the AI")).toBeInTheDocument();
    expect(within(thread).getByText("AI assistant")).toBeInTheDocument();
  });
});

describe("Inbox website chat", () => {
  const chatConv = (over: Record<string, unknown> = {}) =>
    conv({
      channelKind: "webchat",
      channelName: "Help desk",
      address: "0123456789abcdef01234567",
      peer: "visitor:12",
      peerKind: "visitor",
      peerName: "Vic",
      peerNameSource: "visitor",
      chat: {
        siteId: 1,
        offline: false,
        email: "vic@example.com",
        page: "intranet.lan/help",
        browser: "firefox",
        blocked: false,
      },
      ...over,
    });

  function chatInbox(
    over: Record<string, unknown> = {},
    more: Routes = {},
    emailReply = true,
    bodies: string[] = ["hello there"],
  ) {
    return inbox("operator", {
      "GET /api/v1/me/inbox/status": () =>
        json({ enabled: true, unread: 0, unassigned: 1, emailReply }),
      "GET /api/v1/inbox/conversations?view=all": () =>
        json({ items: [chatConv(over)], next: "" }),
      "GET /api/v1/inbox/conversations/7": () => json(chatConv(over)),
      "GET /api/v1/inbox/conversations/7/messages": () =>
        json({
          items: bodies.map((body, i) => msg({ id: i + 1, body })),
          next: "",
        }),
      ...more,
    });
  }

  async function openChat() {
    renderApp("/inbox");
    const list = await screen.findByRole("list", { name: "Conversations" });
    fireEvent.click(await within(list).findByRole("button", { name: /Vic/ }));
    return screen.findByRole("list", { name: "Messages" });
  }

  it("shows a visitor as a visitor, with what they gave", async () => {
    chatInbox();
    await openChat();
    expect(
      await screen.findByText("Website chat · Help desk"),
    ).toBeInTheDocument();
    expect(screen.getByText("vic@example.com")).toBeInTheDocument();
    expect(screen.getByText("intranet.lan/help")).toBeInTheDocument();
    expect(screen.getByText("firefox")).toBeInTheDocument();
    // A visitor has no number and a chat has no text-message count.
    expect(screen.queryByText(/visitor:12/)).toBeNull();
    fireEvent.change(screen.getByLabelText("Reply to the customer"), {
      target: { value: "Hi Vic" },
    });
    expect(screen.getByText("6 characters")).toBeInTheDocument();
  });

  it("renders a visitor's markup as text and asks before a link leaves Hello", async () => {
    chatInbox({}, {}, true, [
      '<img src=x onerror="document.title=1"> see https://example.com/page, thanks',
    ]);
    const thread = await openChat();
    expect(thread.querySelector("img, script, b")).toBeNull();
    const link = within(thread).getByRole("link", {
      name: "https://example.com/page",
    });
    expect(link).toHaveAttribute("rel", "noopener noreferrer nofollow");
    expect(link).toHaveAttribute("target", "_blank");
    const open = vi.fn();
    vi.stubGlobal("open", open);
    fireEvent.click(link);
    // The first click on a host asks.
    expect(
      await screen.findByText("This link leaves Hello"),
    ).toBeInTheDocument();
    expect(open).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Open the link" }));
    await waitFor(() =>
      expect(open).toHaveBeenCalledWith(
        "https://example.com/page",
        "_blank",
        "noopener,noreferrer",
      ),
    );
    // The same host again goes straight through.
    fireEvent.click(link);
    expect(open).toHaveBeenCalledTimes(2);
    expect(screen.queryByText("This link leaves Hello")).toBeNull();
  });

  it("never makes a link of a script address", async () => {
    chatInbox({}, {}, true, ["click javascript:alert(1) or data:text/html,x"]);
    const thread = await openChat();
    expect(within(thread).queryByRole("link")).toBeNull();
  });

  it("blocks a visitor after a confirmation", async () => {
    const calls = chatInbox(
      {},
      {
        "POST /api/v1/inbox/conversations/7/block": () =>
          json({
            id: 1,
            siteId: 1,
            visitorId: 12,
            byIp: true,
            until: STAMP,
            createdAt: STAMP,
          }),
      },
    );
    await openChat();
    fireEvent.click(
      await screen.findByRole("button", { name: "Block visitor" }),
    );
    expect(await screen.findByText("Block this visitor?")).toBeInTheDocument();
    expect(calls.some((c) => c.url.endsWith("/block"))).toBe(false);
    const dialog = screen.getByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Block visitor" }),
    );
    await waitFor(() =>
      expect(calls.find((c) => c.url.endsWith("/block"))?.body).toEqual({}),
    );
  });

  it("offers no block for a text conversation", async () => {
    inbox("operator");
    await open();
    expect(screen.queryByRole("button", { name: "Block visitor" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Reply by email" })).toBeNull();
  });

  it("answers an offline chat by email, once, when email is set up", async () => {
    const offline = {
      chat: {
        siteId: 1,
        offline: true,
        email: "vic@example.com",
        page: "",
        browser: "",
        blocked: false,
      },
    };
    const calls = chatInbox(offline, {
      "POST /api/v1/inbox/conversations/7/email-reply": () =>
        json(
          msg({
            id: 9,
            direction: "out",
            senderKind: "user",
            body: "Call you tomorrow",
            status: "sent",
          }),
          201,
        ),
    });
    await openChat();
    expect(
      await screen.findByText("Left while nobody was available"),
    ).toBeInTheDocument();
    const button = screen.getByRole("button", { name: "Reply by email" });
    expect(button).toBeDisabled(); // nothing written yet
    fireEvent.change(screen.getByLabelText("Reply to the customer"), {
      target: { value: "Call you tomorrow" },
    });
    expect(button).toBeEnabled();
    fireEvent.click(button);
    await waitFor(() => {
      const sent = calls.find((c) => c.url.endsWith("/email-reply"));
      expect(sent?.body).toMatchObject({ body: "Call you tomorrow" });
      expect((sent?.body as { clientMsgId: string }).clientMsgId).toBeTruthy();
    });
    expect(
      calls.some(
        (c) =>
          c.url.endsWith("/conversations/7/messages") && c.method === "POST",
      ),
    ).toBe(false);
  });

  it("disables replying by email while email is not configured", async () => {
    chatInbox(
      {
        chat: {
          siteId: 1,
          offline: true,
          email: "vic@example.com",
          page: "",
          browser: "",
          blocked: false,
        },
      },
      {},
      false,
    );
    await openChat();
    fireEvent.change(await screen.findByLabelText("Reply to the customer"), {
      target: { value: "Call you tomorrow" },
    });
    expect(
      screen.getByRole("button", { name: "Reply by email" }),
    ).toBeDisabled();
  });

  it("tells the visitor an agent is typing, at most every few seconds", async () => {
    const calls = chatInbox(
      {},
      { "POST /api/v1/inbox/conversations/7/typing": () => noContent() },
    );
    await openChat();
    const box = await screen.findByLabelText("Reply to the customer");
    fireEvent.change(box, { target: { value: "H" } });
    fireEvent.change(box, { target: { value: "Hi" } });
    fireEvent.change(box, { target: { value: "Hi V" } });
    await waitFor(() =>
      expect(calls.filter((c) => c.url.endsWith("/typing"))).toHaveLength(1),
    );
  });

  it("shows that the visitor is typing without reloading the thread", async () => {
    chatInbox();
    await openChat();
    await waitFor(() => expect(FakeEventSource.instances).toHaveLength(1));
    act(() => {
      FakeEventSource.instances[0]!.emit("typing", {
        conversationId: 7,
        messageId: 0,
      });
    });
    expect(
      await screen.findByText("The visitor is typing…"),
    ).toBeInTheDocument();
  });

  const media = (over: Record<string, unknown>) => ({
    id: 50,
    messageId: 1,
    direction: "in",
    status: "ready",
    contentType: "image/png",
    size: 2048,
    filename: "screen.png",
    reason: "",
    createdAt: STAMP,
    ...over,
  });

  it("shows attachments from the authorised route, and says why one is missing", async () => {
    chatInbox(
      {},
      {
        "GET /api/v1/inbox/conversations/7/messages": () =>
          json({
            items: [
              msg({
                id: 1,
                body: "",
                media: [
                  media({}),
                  media({
                    id: 51,
                    contentType: "application/pdf",
                    filename: "<b>invoice</b>.pdf",
                    size: 3 * 1024 * 1024,
                  }),
                  media({ id: 52, status: "pending", contentType: "" }),
                  media({
                    id: 53,
                    status: "rejected",
                    reason: "active_content",
                  }),
                ],
              }),
            ],
            next: "",
          }),
      },
    );
    const thread = await openChat();
    const img = await within(thread).findByRole("img", { name: "screen.png" });
    expect(img).toHaveAttribute("src", "/api/v1/inbox/media/50");
    const file = within(thread).getByRole("link", { name: /invoice/ });
    expect(file).toHaveAttribute("href", "/api/v1/inbox/media/51");
    expect(file).toHaveAttribute("download", "<b>invoice</b>.pdf");
    expect(file.textContent).toContain("3.0 MB");
    expect(thread.querySelector("b")).toBeNull();
    expect(
      within(thread).getByText("Downloading an attachment…"),
    ).toBeInTheDocument();
    expect(
      within(thread).getByText(/carried markup or script/),
    ).toBeInTheDocument();
  });

  it("uploads a file and sends it with the reply", async () => {
    const calls = chatInbox(
      {},
      {
        "POST /api/v1/inbox/media": () =>
          json(
            media({
              id: 60,
              messageId: null,
              direction: "out",
              filename: "map.png",
            }),
            201,
          ),
        "POST /api/v1/inbox/conversations/7/messages": () =>
          json(msg({ id: 9, direction: "out", body: "" }), 201),
      },
    );
    await openChat();
    const send = await screen.findByRole("button", { name: "Send" });
    expect(send).toBeDisabled();
    const file = new File([new Uint8Array([137, 80, 78, 71])], "map.png", {
      type: "image/png",
    });
    fireEvent.change(screen.getByLabelText("Attachment file"), {
      target: { files: [file] },
    });
    const chips = await screen.findByRole("list", {
      name: "Attachments to send",
    });
    expect(within(chips).getByText(/map\.png/)).toBeInTheDocument();
    const up = calls.find((c) => c.url === "/api/v1/inbox/media");
    expect(up?.body).toBeInstanceOf(FormData);
    expect((up?.body as FormData).get("conversationId")).toBe("7");
    // A file alone may be sent, without text.
    await waitFor(() => expect(send).toBeEnabled());
    fireEvent.click(send);
    await waitFor(() =>
      expect(
        calls.find(
          (c) =>
            c.method === "POST" &&
            c.url === "/api/v1/inbox/conversations/7/messages",
        )?.body,
      ).toMatchObject({ body: "", mediaIds: [60] }),
    );
    await waitFor(() =>
      expect(
        screen.queryByRole("list", { name: "Attachments to send" }),
      ).toBeNull(),
    );
  });

  it("refuses a file over the chat's cap before uploading it", async () => {
    const calls = chatInbox();
    await openChat();
    const big = new File([new Uint8Array(5 * 1024 * 1024 + 1)], "big.pdf", {
      type: "application/pdf",
    });
    fireEvent.change(await screen.findByLabelText("Attachment file"), {
      target: { files: [big] },
    });
    expect(
      await screen.findByText(/big\.pdf is larger than/),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.url === "/api/v1/inbox/media")).toBe(false);
  });
});

describe("Inbox attachments on a text number", () => {
  it("offers no attachments on an SMS conversation", async () => {
    inbox("operator");
    await open();
    await screen.findByLabelText("Reply to the customer");
    expect(screen.queryByRole("button", { name: "Attach" })).toBeNull();
    expect(screen.queryByLabelText("Attachment file")).toBeNull();
  });
});
