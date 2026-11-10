// Outbound webhooks (docs/integrations.md, "Webhooks"): endpoints, signing
// secrets, the delivery log and the dead-letter queue. Administrators only.
import { request } from "../api";

/** Why an endpoint is switched off. */
export type DisabledReason =
  "" | "manual" | "consecutive_failures" | "endpoint_gone";

/** A subscriber URL with its filter and health. The secret is never listed. */
export interface WebhookEndpoint {
  id: number;
  name: string;
  url: string;
  /** Event types or prefix patterns (call.*); empty is every type. */
  events: string[];
  /** Only events about these extensions; empty is every extension. */
  extensions: string[];
  enabled: boolean;
  allowPrivate: boolean;
  disabledReason: DisabledReason;
  consecutiveFailures: number;
  /** While set, the secret before the last rotation still signs. */
  previousSecretExpiresAt: string | null;
  lastSuccessAt: string | null;
  lastFailureAt: string | null;
  lastStatus: number | null;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
  /** Deliveries waiting or retrying. */
  pending: number;
  /** Dead letters. */
  dead: number;
}

/** An endpoint with its signing secret, returned once (create, rotate). */
export interface WebhookWithSecret extends WebhookEndpoint {
  secret: string | null;
}

export interface WebhookFields {
  name: string;
  url: string;
  events: string[];
  extensions: string[];
  enabled: boolean;
  allowPrivate: boolean;
}

export type DeliveryStatus = "pending" | "retrying" | "succeeded" | "dead";

export interface WebhookDelivery {
  id: number;
  endpointId: number;
  /** The event's id: what Hello-Event-Id carries. */
  eventId: string;
  eventType: string;
  status: DeliveryStatus;
  attempts: number;
  nextAttemptAt: string | null;
  lastStatus: number | null;
  lastError: string;
  lastLatencyMs: number | null;
  deliveredAt: string | null;
  createdAt: string;
  updatedAt: string;
}

export interface WebhookAttempt {
  attempt: number;
  /** The Hello-Delivery header of the attempt. */
  deliveryId: string;
  at: string;
  status: number | null;
  error: string;
  latencyMs: number;
}

export interface WebhookDeliveryDetail extends WebhookDelivery {
  log: WebhookAttempt[];
  /** The event this delivery carries; null once it was purged. */
  event: Record<string, unknown> | null;
}

export interface WebhookTestResult {
  delivered: boolean;
  status: number;
  latencyMs: number;
  error: string;
  body: string;
  eventId: string;
}

const hook = (id: number) => `/api/v1/webhooks/${String(id)}`;

export async function listWebhooks(
  signal?: AbortSignal,
): Promise<WebhookEndpoint[]> {
  const res = await request<{ items: WebhookEndpoint[] }>(
    "GET",
    "/api/v1/webhooks",
    { signal },
  );
  return res.items;
}

export const createWebhook = (input: WebhookFields) =>
  request<WebhookWithSecret>("POST", "/api/v1/webhooks", { body: input });

export const updateWebhook = (id: number, input: Partial<WebhookFields>) =>
  request<WebhookEndpoint>("PATCH", hook(id), { body: input });

export const deleteWebhook = (id: number) => request<void>("DELETE", hook(id));

export const rotateWebhookSecret = (id: number) =>
  request<WebhookWithSecret>("POST", `${hook(id)}/rotate-secret`);

export const testWebhook = (id: number) =>
  request<WebhookTestResult>("POST", `${hook(id)}/test`);

export async function listWebhookDeliveries(
  id: number,
  opts: { status?: DeliveryStatus; before?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<{ items: WebhookDelivery[]; nextCursor: string }> {
  const q = new URLSearchParams();
  if (opts.status) q.set("status", opts.status);
  if (opts.before) q.set("before", opts.before);
  if (opts.limit !== undefined) q.set("limit", String(opts.limit));
  const qs = q.toString();
  return request("GET", `${hook(id)}/deliveries${qs ? `?${qs}` : ""}`, {
    signal,
  });
}

export const getWebhookDelivery = (
  id: number,
  deliveryId: number,
  signal?: AbortSignal,
) =>
  request<WebhookDeliveryDetail>(
    "GET",
    `${hook(id)}/deliveries/${String(deliveryId)}`,
    { signal },
  );

export const redeliverWebhookDelivery = (deliveryId: number) =>
  request<WebhookDelivery>(
    "POST",
    `/api/v1/webhooks/deliveries/${String(deliveryId)}/redeliver`,
  );

export const redeliverDeadWebhooks = (id: number) =>
  request<{ redelivered: number }>(
    "POST",
    `${hook(id)}/deliveries/redeliver-dead`,
  );
