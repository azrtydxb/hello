// The messaging inbox API (spec messaging-channels, "inbox core"): channels,
// numbers, opt-outs and settings for administrators, and the conversations
// agents and supervisors answer: the HTTP JSON of internal/api/inbox.go.

import { request, type Id } from "../api";

export type ConversationStatus =
  "open" | "pending" | "snoozed" | "resolved" | "spam";

/** The state filter of a list: the active ones, or one state, or every one. */
export type StatusFilter = "" | ConversationStatus | "any";

export interface Conversation {
  id: number;
  channelId: number;
  channelKind: string;
  channelName: string;
  addressId: number;
  /** Our number the customer texted. */
  address: string;
  /** The customer's number. */
  peer: string;
  peerKind: "phone" | "visitor";
  peerName: string;
  peerNameSource: string;
  status: ConversationStatus;
  snoozedUntil: string | null;
  assigneeUserId: number | null;
  assigneeName: string;
  priority: "low" | "normal" | "high";
  tags: string[];
  preview: string;
  unread: number;
  optedOut: boolean;
  lastMessageAt: string;
  lastInboundAt: string | null;
  firstResponseAt: string | null;
  resolvedAt: string | null;
  createdAt: string;
}

export type MessageState =
  "queued" | "sending" | "sent" | "delivered" | "read" | "failed" | "unknown";

export interface InboxMessage {
  id: number;
  conversationId: number;
  direction: "in" | "out";
  /** A note is internal and never sent; a system line records what happened. */
  kind: "message" | "note" | "system";
  senderKind: "customer" | "user" | "ai" | "system";
  senderUserId: number | null;
  senderName: string;
  body: string;
  status: MessageState;
  errorCode: string;
  clientMsgId: string;
  createdAt: string;
  sentAt: string | null;
  deliveredAt: string | null;
  readAt: string | null;
}

export interface ConversationQuery {
  view?: "all" | "mine" | "unassigned";
  status?: StatusFilter;
  q?: string;
  cursor?: string;
  limit?: number;
}

export interface InboxAgent {
  id: number;
  username: string;
  displayName: string;
}

export interface InboxStatus {
  enabled: boolean;
  unread: number;
  unassigned: number;
}

function query(params: Record<string, string | number | undefined>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") u.set(k, String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : "";
}

/** The calls of one inbox area: a supervisor's (`/inbox`) or an agent's own (`/me/inbox`). */
export interface InboxApi {
  conversations(
    q: ConversationQuery,
    signal?: AbortSignal,
  ): Promise<{ items: Conversation[]; next: string }>;
  conversation(id: number, signal?: AbortSignal): Promise<Conversation>;
  messages(
    id: number,
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<{ items: InboxMessage[]; next: string }>;
  send(id: number, body: string, clientMsgId: string): Promise<InboxMessage>;
  note(id: number, body: string): Promise<InboxMessage>;
  claim(id: number): Promise<Conversation>;
  assign(id: number, userId: number): Promise<Conversation>;
  setStatus(
    id: number,
    status: ConversationStatus,
    snoozedUntil?: string,
  ): Promise<Conversation>;
  setTags(id: number, tags: string[]): Promise<Conversation>;
  read(id: number): Promise<void>;
}

function area(base: string): InboxApi {
  const at = (id: number, tail = "") => `${base}/conversations/${id}${tail}`;
  return {
    conversations: (q, signal) =>
      request("GET", `${base}/conversations${query({ ...q })}`, { signal }),
    conversation: (id, signal) => request("GET", at(id), { signal }),
    messages: (id, cursor, signal) =>
      request("GET", at(id, `/messages${query({ cursor })}`), { signal }),
    send: (id, body, clientMsgId) =>
      request("POST", at(id, "/messages"), { body: { body, clientMsgId } }),
    note: (id, body) => request("POST", at(id, "/notes"), { body: { body } }),
    claim: (id) => request("POST", at(id, "/claim")),
    assign: (id, userId) =>
      request("POST", at(id, "/assign"), { body: { userId } }),
    setStatus: (id, status, snoozedUntil) =>
      request("POST", at(id, "/status"), { body: { status, snoozedUntil } }),
    setTags: (id, tags) => request("POST", at(id, "/tags"), { body: { tags } }),
    read: (id) => request("POST", at(id, "/read")),
  };
}

export const supervisorInbox = area("/api/v1/inbox");
export const myInbox = area("/api/v1/me/inbox");

export const getInboxStatus = (signal?: AbortSignal) =>
  request<InboxStatus>("GET", "/api/v1/me/inbox/status", { signal });

export const listInboxAgents = async (signal?: AbortSignal) =>
  (
    await request<{ items: InboxAgent[] }>("GET", "/api/v1/me/inbox/agents", {
      signal,
    })
  ).items;

/** The server-sent events of the caller's inbox. */
export const INBOX_STREAM = "/api/v1/me/inbox/stream";

/** The event names the stream sends; each carries only ids. */
export const INBOX_EVENTS = [
  "conversation.new",
  "conversation.assigned",
  "conversation.updated",
  "message.in",
  "message.sent",
  "message.status",
] as const;

// --- administration ----------------------------------------------------------

export interface MessagingChannel {
  id: number;
  kind: "sms" | "whatsapp" | "webchat";
  provider: string;
  name: string;
  enabled: boolean;
  trunkId: number | null;
  trunkName: string;
  hasCredentials: boolean;
  status: "ok" | "degraded" | "credentials_invalid";
  statusDetail: string;
  lastEventAt: string | null;
  addresses: number;
  createdAt: string;
  updatedAt: string;
}

export interface ChannelInput {
  kind?: "sms";
  provider?: "sip_trunk";
  name?: string;
  enabled?: boolean;
  trunkId?: number;
}

export interface ChannelTest {
  ok: boolean;
  checks: { name: string; ok: boolean; detail: string }[];
}

export interface MessagingAddress {
  id: number;
  channelId: number;
  channelName: string;
  address: string;
  label: string;
  routeKind: "inbox";
  enabled: boolean;
  optOutKeywords: string[];
  helpText: string;
  optOutText: string;
  conversations: number;
  createdAt: string;
}

export interface AddressInput {
  channelId?: number;
  address?: string;
  label?: string;
  enabled?: boolean;
  optOutKeywords?: string[];
  helpText?: string;
  optOutText?: string;
}

export interface MessagingSettings {
  retentionSmsDays: number;
  retentionChatDays: number;
  reopenDays: number;
  emailNotify: boolean;
}

export interface OptOut {
  id: number;
  addressId: number;
  address: string;
  channel: string;
  peer: string;
  state: "out" | "in";
  source: "keyword" | "provider" | "admin" | "customer";
  note: string;
  at: string;
}

export interface EraseReport {
  conversations: number;
  messages: number;
  optOutsKept: number;
  providers: { provider: string; redacted: boolean; detail: string }[];
}

const MESSAGING = "/api/v1/messaging";
const itemsOf = async <T>(p: Promise<{ items: T[] }>) => (await p).items;

export const listChannels = (signal?: AbortSignal) =>
  itemsOf(
    request<{ items: MessagingChannel[] }>("GET", `${MESSAGING}/channels`, {
      signal,
    }),
  );
export const createChannel = (body: ChannelInput) =>
  request<MessagingChannel>("POST", `${MESSAGING}/channels`, { body });
export const updateChannel = (id: Id, body: ChannelInput) =>
  request<MessagingChannel>("PUT", `${MESSAGING}/channels/${id}`, { body });
export const deleteChannel = (id: Id) =>
  request<void>("DELETE", `${MESSAGING}/channels/${id}`);
export const testChannel = (id: Id) =>
  request<ChannelTest>("POST", `${MESSAGING}/channels/${id}/test`);

export const listAddresses = (signal?: AbortSignal) =>
  itemsOf(
    request<{ items: MessagingAddress[] }>("GET", `${MESSAGING}/addresses`, {
      signal,
    }),
  );
export const createAddress = (body: AddressInput) =>
  request<MessagingAddress>("POST", `${MESSAGING}/addresses`, { body });
export const updateAddress = (id: Id, body: AddressInput) =>
  request<MessagingAddress>("PUT", `${MESSAGING}/addresses/${id}`, { body });
export const deleteAddress = (id: Id) =>
  request<void>("DELETE", `${MESSAGING}/addresses/${id}`);

export const getMessagingSettings = (signal?: AbortSignal) =>
  request<MessagingSettings>("GET", `${MESSAGING}/settings`, { signal });
export const putMessagingSettings = (body: Partial<MessagingSettings>) =>
  request<MessagingSettings>("PUT", `${MESSAGING}/settings`, { body });

export const listOptOuts = (
  q: { q?: string; all?: boolean },
  signal?: AbortSignal,
) =>
  itemsOf(
    request<{ items: OptOut[] }>(
      "GET",
      `${MESSAGING}/optouts${query({ q: q.q, all: q.all ? "true" : undefined })}`,
      { signal },
    ),
  );
export const recordOptOut = (body: {
  addressId: number;
  peer: string;
  note?: string;
}) => request<OptOut>("POST", `${MESSAGING}/optouts`, { body });
export const reconsentOptOut = (id: Id, note: string) =>
  request<OptOut>("POST", `${MESSAGING}/optouts/${id}/reconsent`, {
    body: { note },
  });

export const erasePeer = (peer: string) =>
  request<EraseReport>("POST", "/api/v1/inbox/erase", { body: { peer } });
