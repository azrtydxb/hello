// Slack notifications (docs/slack.md): missed calls and voicemail messages
// posted to a Slack channel. The incoming webhook URL and the bot token are
// write-only: the API never returns them.
import { request } from "../api";

export type SlackMode = "webhook" | "bot";
export type SlackEvent = "call.missed" | "voicemail.received";

export const SLACK_EVENTS: readonly { id: SlackEvent; label: string }[] = [
  { id: "call.missed", label: "Missed calls" },
  { id: "voicemail.received", label: "New voicemail messages" },
];

export interface SlackDestination {
  id: number;
  name: string;
  mode: SlackMode;
  channel: string;
  events: SlackEvent[];
  extensions: string[];
  enabled: boolean;
  /** manual, consecutive_failures, auth_failed or destination_gone. */
  disabledReason: string;
  consecutiveFailures: number;
  lastSuccessAt: string | null;
  lastFailureAt: string | null;
  lastError: string;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  pending: number;
  dead: number;
  hasSecret: boolean;
}

export interface SlackInput {
  name?: string;
  mode?: SlackMode;
  /** Write-only: the incoming webhook URL or the bot token. */
  secret?: string;
  channel?: string;
  events?: SlackEvent[];
  extensions?: string[];
  enabled?: boolean;
}

export type SlackPostStatus = "pending" | "retrying" | "succeeded" | "dead";

export interface SlackPost {
  id: number;
  destinationId: number;
  eventId: string;
  eventType: string;
  status: SlackPostStatus;
  attempts: number;
  nextAttemptAt: string | null;
  lastError: string;
  deliveredAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface SlackTestResult {
  delivered: boolean;
  status: number;
  error: string;
}

const base = "/api/v1/integrations/slack";

export async function listSlack(
  signal?: AbortSignal,
): Promise<SlackDestination[]> {
  const r = await request<{ items: SlackDestination[] }>("GET", base, {
    signal,
  });
  return r.items;
}

export function createSlack(body: SlackInput): Promise<SlackDestination> {
  return request("POST", base, { body });
}

export function updateSlack(
  id: number,
  body: SlackInput,
): Promise<SlackDestination> {
  return request("PATCH", `${base}/${id}`, { body });
}

export function deleteSlack(id: number): Promise<void> {
  return request("DELETE", `${base}/${id}`);
}

export function testSlack(id: number): Promise<SlackTestResult> {
  return request("POST", `${base}/${id}/test`);
}

export function listSlackPosts(
  id: number,
  status: SlackPostStatus | "",
  signal?: AbortSignal,
): Promise<{ items: SlackPost[]; nextBefore: number }> {
  const q = new URLSearchParams({ limit: "50" });
  if (status) q.set("status", status);
  return request("GET", `${base}/${id}/posts?${q.toString()}`, { signal });
}

export function retrySlackPost(id: number): Promise<SlackPost> {
  return request("POST", `${base}/posts/${id}/retry`);
}
