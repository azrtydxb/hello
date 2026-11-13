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
  /** The text queue the conversation is placed in; null in the pool. */
  queueId?: number | null;
  queueName?: string;
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
  /** What an agent sees of a website chat visitor; null for a text. */
  chat?: ChatInfo | null;
}

/** The facts kept about a chat visitor (spec messaging-channels M-29). */
export interface ChatInfo {
  siteId: number;
  /** The chat was left while nobody was available. */
  offline: boolean;
  /** The address the visitor gave, empty for none. */
  email: string;
  /** Hostname and path of the page the chat started on. */
  page: string;
  /** A coarse browser family, nothing more. */
  browser: string;
  blocked: boolean;
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
  /** Only the conversations placed in this text queue. */
  queueId?: number;
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

/** A carrier number a supervisor may start a conversation from. */
export interface InboxSender {
  addressId: number;
  address: string;
  label: string;
  channel: string;
}

export interface InboxStatus {
  enabled: boolean;
  unread: number;
  unassigned: number;
  /** Whether email is configured, so an offline chat can be answered by email. */
  emailReply?: boolean;
  /** Empty for an agent and while first contact is off. */
  senders: InboxSender[];
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
  /** Blocks the visitor of a website chat for `hours` (24 when omitted). */
  block(id: number, hours?: number): Promise<ChatBlock>;
  /** Answers an offline chat by email. */
  emailReply(
    id: number,
    body: string,
    clientMsgId: string,
  ): Promise<InboxMessage>;
  /** Tells the visitor an agent is typing. */
  typing(id: number): Promise<void>;
  /** Saves the customer's number as a contact and returns the renamed conversation. */
  addContact(id: number, contact: ContactInput): Promise<Conversation>;
  /** The customer's most recent calls, newest first (an agent sees their own). */
  calls(id: number, signal?: AbortSignal): Promise<{ items: InboxCall[] }>;
}

/** One call to or from the customer's number. */
export interface InboxCall {
  id: number;
  startTime: string;
  durationMs: number;
  direction: string;
  kind: string;
  source: string;
  destination: string;
  answered: boolean;
}

/** The name an "Add to contacts" saves the customer under. */
export interface ContactInput {
  firstName?: string;
  lastName?: string;
  company?: string;
  /** personal (the caller's own contacts, the default) or company (supervisors). */
  book?: "personal" | "company";
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
    block: (id, hours) =>
      request("POST", at(id, "/block"), { body: hours ? { hours } : {} }),
    emailReply: (id, body, clientMsgId) =>
      request("POST", at(id, "/email-reply"), { body: { body, clientMsgId } }),
    typing: (id) => request("POST", at(id, "/typing")),
    addContact: (id, contact) =>
      request("POST", at(id, "/contact"), { body: contact }),
    calls: (id, signal) => request("GET", at(id, "/calls"), { signal }),
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
  "typing",
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
  /** The last four characters of a carrier account's SID; never the token. */
  accountHint: string;
  messagingService: boolean;
  status: "ok" | "degraded" | "credentials_invalid";
  statusDetail: string;
  lastEventAt: string | null;
  addresses: number;
  createdAt: string;
  updatedAt: string;
}

export interface ChannelInput {
  kind?: "sms";
  provider?: "sip_trunk" | "twilio";
  name?: string;
  enabled?: boolean;
  trunkId?: number;
  /** A Twilio channel's account; the token is write-only. */
  accountSid?: string;
  authToken?: string;
  messagingServiceSid?: string;
}

/** A number a carrier account owns. */
export interface CarrierNumber {
  number: string;
  label: string;
  sms: boolean;
  mms: boolean;
  voice: boolean;
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
  routeKind: "inbox" | "queue" | "user" | "extension" | "ring_group";
  enabled: boolean;
  optOutKeywords: string[];
  helpText: string;
  optOutText: string;
  /** Customer texts also go to the desk phones of the assigned user's extension. */
  deskPhoneDelivery: boolean;
  /** The extension whose phone may reply into a conversation on this number. */
  ownerExtensionId: number | null;
  sendsPerMinute: number;
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
  deskPhoneDelivery?: boolean;
  ownerExtensionId?: number | null;
  sendsPerMinute?: number;
}

export interface MessagingSettings {
  retentionSmsDays: number;
  retentionChatDays: number;
  reopenDays: number;
  emailNotify: boolean;
  firstContact: "off" | "attested";
  firstContactPerUserHour: number;
  firstContactPerAddressDay: number;
}

/** A first-contact text: one customer, one message, one attestation. */
export interface StartConversationInput {
  addressId: number;
  to: string;
  body: string;
  consentAttestation: boolean;
  consentNote: string;
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
export const listChannelNumbers = (id: Id) =>
  request<{ items: CarrierNumber[] }>(
    "GET",
    `${MESSAGING}/channels/${id}/numbers`,
  );
export const startConversation = (body: StartConversationInput) =>
  request<Conversation>("POST", "/api/v1/inbox/conversations", { body });

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

// --- website chat ------------------------------------------------------------

export interface ChatHours {
  zone: string;
  /** 0 is Sunday. */
  days: { day: number; from: string; to: string }[];
}

export interface ChatSite {
  id: number;
  channelId: number;
  addressId: number;
  name: string;
  enabled: boolean;
  siteKey: string;
  allowedOrigins: string[];
  greeting: string;
  accent: string;
  position: "left" | "right";
  collect: "off" | "optional" | "required";
  hours: ChatHours;
  offlineMode: "collect" | "hidden";
  privacyUrl: string;
  challenge: "pow" | "turnstile" | "none";
  powBits: number;
  turnstileSiteKey: string;
  hasTurnstileSecret: boolean;
  stripLinks: boolean;
  maxOpenUnassigned: number;
  transcripts: boolean;
  retentionDays: number | null;
  conversations: number;
  createdAt: string;
  updatedAt: string;
  warnings: string[];
}

/** What a create or replace sends; a field left out keeps its value. */
export type ChatSiteInput = Partial<
  Omit<
    ChatSite,
    | "id"
    | "channelId"
    | "addressId"
    | "siteKey"
    | "hasTurnstileSecret"
    | "conversations"
    | "createdAt"
    | "updatedAt"
    | "warnings"
    | "retentionDays"
  >
> & {
  /** Sealed when stored and never returned; "" removes it. */
  turnstileSecret?: string;
  /** 0 follows the messaging settings. */
  retentionDays?: number;
};

export interface ChatBlock {
  id: number;
  siteId: number;
  visitorId: number | null;
  byIp: boolean;
  until: string;
  createdAt: string;
}

const CHAT = `${MESSAGING}/chat`;

export const listChatSites = (signal?: AbortSignal) =>
  itemsOf(request<{ items: ChatSite[] }>("GET", `${CHAT}/sites`, { signal }));
export const createChatSite = (body: ChatSiteInput) =>
  request<ChatSite>("POST", `${CHAT}/sites`, { body });
export const updateChatSite = (id: Id, body: ChatSiteInput) =>
  request<ChatSite>("PUT", `${CHAT}/sites/${id}`, { body });
export const deleteChatSite = (id: Id) =>
  request<void>("DELETE", `${CHAT}/sites/${id}`);
export const listChatBlocks = (id: Id, signal?: AbortSignal) =>
  itemsOf(
    request<{ items: ChatBlock[] }>("GET", `${CHAT}/sites/${id}/blocks`, {
      signal,
    }),
  );
export const deleteChatBlock = (id: Id) =>
  request<void>("DELETE", `${CHAT}/blocks/${id}`);
