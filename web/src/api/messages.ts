// Text-message (SIP MESSAGE) API client for the Messages and My messages
// pages: the HTTP JSON of internal/api/messages.go.

import { request } from "../api";

/** What a send is addressed to. */
export type TargetKind =
  | "extension"
  | "ring_group"
  | "paging_group"
  | "broadcast"
  | "external";

export type MessageStatus = "pending" | "delivered" | "failed";

export type MessageScope =
  | "direct"
  | "ring_group"
  | "paging_group"
  | "broadcast";

/** POST /messages and POST /me/messages body. */
export interface MessageSend {
  targetKind: TargetKind;
  /** An extension number, a group name or code, a number; empty for broadcast. */
  target?: string;
  body: string;
  /** Must be true for a broadcast. */
  confirm?: boolean;
}

export interface MessageSent {
  batch: string;
  scope: MessageScope;
  recipients: number;
}

/** One send, with the counts of its recipients. */
export interface Message {
  id: number;
  batch: string;
  sender: string;
  senderKind: "extension" | "external" | "system";
  senderName: string;
  actor: string;
  recipient: string;
  recipientKind: "extension" | "external";
  scope: MessageScope;
  scopeRef: string;
  trunk: string;
  contentType: string;
  body: string;
  bodyBytes: number;
  status: MessageStatus;
  detail: string;
  attempts: number;
  createdAt: string;
  expiresAt: string;
  deliveredAt: string | null;
  recipients: number;
  delivered: number;
  pending: number;
  failed: number;
}

export interface MessagePage {
  items: Message[];
  next: string;
}

export interface Conversation {
  key: string;
  scope: MessageScope;
  scopeRef: string;
  messages: number;
  lastId: number;
  lastAt: string;
  lastSender: string;
  lastRecipient: string;
  lastSnippet: string;
  lastStatus: MessageStatus;
  /** The two sides of a direct conversation; empty for a group. */
  parties: string[];
}

export interface MessageSettings {
  enabled: boolean;
  ttlHours: number;
  retentionDays: number;
  ratePerMinute: number;
  maxQueuedPerExtension: number;
}

export interface MessageQuery {
  conversation?: string;
  status?: MessageStatus | "";
  q?: string;
  cursor?: string;
  limit?: number;
}

function query(params: Record<string, string | number | undefined>): string {
  const u = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== "") u.set(k, String(v));
  }
  const s = u.toString();
  return s ? `?${s}` : "";
}

/**
 * The calls of one message area: the administrators' (`/messages`) or the
 * signed-in user's own (`/me/messages`).
 */
export interface MessagesApi {
  list(q: MessageQuery, signal?: AbortSignal): Promise<MessagePage>;
  conversations(
    q: { q?: string; status?: MessageStatus | "" },
    signal?: AbortSignal,
  ): Promise<Conversation[]>;
  send(body: MessageSend): Promise<MessageSent>;
}

function area(base: string): MessagesApi {
  return {
    list: (q, signal) =>
      request<MessagePage>(
        "GET",
        `${base}${query({ conversation: q.conversation, status: q.status, q: q.q, cursor: q.cursor, limit: q.limit })}`,
        { signal },
      ),
    conversations: async (q, signal) =>
      (
        await request<{ items: Conversation[] }>(
          "GET",
          `${base}/conversations${query({ q: q.q, status: q.status })}`,
          { signal },
        )
      ).items,
    send: (body) => request<MessageSent>("POST", base, { body }),
  };
}

export const messagesApi = area("/api/v1/messages");
export const myMessagesApi = area("/api/v1/me/messages");

export const getMessageSettings = (signal?: AbortSignal) =>
  request<MessageSettings>("GET", "/api/v1/messages/settings", { signal });

export const putMessageSettings = (body: MessageSettings) =>
  request<MessageSettings>("PUT", "/api/v1/messages/settings", { body });

export const purgeMessages = (olderThanDays: number) =>
  request<{ deleted: number }>("POST", "/api/v1/messages/purge", {
    body: { olderThanDays },
  });
