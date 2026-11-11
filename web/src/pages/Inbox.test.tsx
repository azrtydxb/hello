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
    json({ enabled: true, unread: 0, unassigned: 1 }),
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

  it("says so when no channel has a number", async () => {
    inbox("operator", {
      "GET /api/v1/me/inbox/status": () =>
        json({ enabled: false, unread: 0, unassigned: 0 }),
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
