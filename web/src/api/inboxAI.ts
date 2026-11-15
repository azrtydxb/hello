// The inbox AI's settings (spec messaging-channels M-30, M-30a): the
// inbox-wide switches, each number's mode and guardrails, and the FAQ an
// auto-reply answers from: the HTTP JSON of internal/api/inbox_ai.go.

import { request, type Id } from "../api";
import type { AIMode } from "./inbox";

export const AI_MODE_LABEL: Record<AIMode, string> = {
  off: "Off",
  triage: "Triage only",
  draft: "Triage and reply drafts",
  auto: "Auto-reply from the FAQ",
};

export interface MessagingAIStatus {
  /** The in-product AI (HELLO_AI_*) is on. */
  available: boolean;
  reason: string;
  /** The model's endpoint is not on a private network. */
  endpointPublic: boolean;
  /** HELLO_MSG_AI_AUTO: false stops every auto-reply. */
  autoAllowed: boolean;
}

export interface MessagingAI {
  enabled: boolean;
  autoEnabled: boolean;
  businessName: string;
  intents: string[];
  sensitiveTopics: string[];
  channels: { id: number; name: string; kind: string; aiEnabled: boolean }[];
  addresses: {
    id: number;
    channelId: number;
    address: string;
    label: string;
    mode: AIMode;
    faqCount: number;
  }[];
  status: MessagingAIStatus;
}

export interface MessagingAIInput {
  enabled?: boolean;
  autoEnabled?: boolean;
  businessName?: string;
  intents?: string[];
  sensitiveTopics?: string[];
  channels?: { id: number; aiEnabled: boolean }[];
}

export interface AddressAI {
  addressId: number;
  mode: AIMode;
  redact: boolean;
  disclosure: boolean;
  disclosureText: string;
  holdingText: string;
  maxReplies: number;
  hourlyCap: number;
  minConfidence: number;
  autoIntents: string[];
  handoffWords: string[];
  faqCount: number;
}

export type AddressAIInput = Partial<Omit<AddressAI, "addressId" | "faqCount">>;

export interface FAQ {
  id: number;
  addressId: number;
  title: string;
  body: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

export interface FAQInput {
  addressId?: number;
  title?: string;
  body?: string;
  enabled?: boolean;
}

export interface InboxAIStats {
  runs: { kind: string; state: string; count: number }[];
  autoReplies: number;
  handoffs: number;
  spam: number;
  modes: Record<AIMode, number>;
}

export const getMessagingAI = (signal?: AbortSignal) =>
  request<MessagingAI>("GET", "/api/v1/messaging/ai", { signal });

export const putMessagingAI = (body: MessagingAIInput) =>
  request<MessagingAI>("PUT", "/api/v1/messaging/ai", { body });

export const getAddressAI = (id: Id, signal?: AbortSignal) =>
  request<AddressAI>("GET", `/api/v1/messaging/addresses/${id}/ai`, {
    signal,
  });

export const putAddressAI = (id: Id, body: AddressAIInput) =>
  request<AddressAI>("PUT", `/api/v1/messaging/addresses/${id}/ai`, { body });

export const listFAQ = async (addressId: Id, signal?: AbortSignal) =>
  (
    await request<{ items: FAQ[] }>(
      "GET",
      `/api/v1/messaging/faq?addressId=${addressId}`,
      { signal },
    )
  ).items;

export const createFAQ = (body: FAQInput) =>
  request<FAQ>("POST", "/api/v1/messaging/faq", { body });

export const updateFAQ = (id: Id, body: FAQInput) =>
  request<FAQ>("PUT", `/api/v1/messaging/faq/${id}`, { body });

export const deleteFAQ = (id: Id) =>
  request<void>("DELETE", `/api/v1/messaging/faq/${id}`);

export const getInboxAIStats = (signal?: AbortSignal) =>
  request<InboxAIStats>("GET", "/api/v1/inbox/ai/stats", { signal });

/** Splits a comma-separated list typed in a field. */
export function splitList(s: string): string[] {
  return s
    .split(",")
    .map((x) => x.trim())
    .filter((x) => x !== "");
}
