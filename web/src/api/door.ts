// Client for the door station routes: the door-open settings of a station
// (admin) and the door calls the signed-in user answered (self-service), and
// the doorbell webhooks (spec phone-feature-parity S-48): the non-SIP
// doorbells that ring the Door group or chime a paging group.
import { request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

/** A station's door-open settings. The code itself is never returned. */
export interface DoorOpen {
  codeSet: boolean;
  hangupAfterOpen: boolean;
}

/** GET /api/v1/extensions/{id}/door-open */
export const getDoorOpen = (id: Id, signal?: AbortSignal) =>
  request<DoorOpen>("GET", `/api/v1/extensions/${seg(id)}/door-open`, {
    signal,
  });

/**
 * PUT /api/v1/extensions/{id}/door-open. `code` is write-only: "" clears it,
 * an absent field keeps it.
 */
export const putDoorOpen = (
  id: Id,
  change: { code?: string; hangupAfterOpen?: boolean },
) =>
  request<DoorOpen>("PUT", `/api/v1/extensions/${seg(id)}/door-open`, {
    body: change,
  });

/** A live door call the user can open the door of. */
export interface DoorCall {
  id: string;
  door: string;
  startedAt: string;
}

/** GET /api/v1/me/door-calls */
export async function listMyDoorCalls(
  signal?: AbortSignal,
): Promise<DoorCall[]> {
  const r = await request<{ items: DoorCall[] }>(
    "GET",
    "/api/v1/me/door-calls",
    { signal },
  );
  return r.items;
}

/** POST /api/v1/calls/{id}/door-open */
export const openDoor = (callId: string) =>
  request<{ sent: boolean }>(
    "POST",
    `/api/v1/calls/${encodeURIComponent(callId)}/door-open`,
  );

/** What a webhook does when its doorbell is pressed. */
export type DoorAction = "ring_door" | "chime_page";

/** A doorbell webhook. The secret is never part of it. */
export interface DoorWebhook {
  id: Id;
  name: string;
  action: DoorAction;
  ringGroupId?: Id | null;
  ringGroup?: string;
  pagingGroupId?: Id | null;
  pagingGroup?: string;
  announcementId?: Id | null;
  announcement?: string;
  /** Triggers accepted per minute. */
  rateLimit: number;
  enabled: boolean;
  lastTriggeredAt?: string | null;
  createdAt: string;
  updatedAt: string;
}

/** A webhook with its secret, returned only by create and rotate. */
export interface DoorWebhookCreated extends DoorWebhook {
  secret: string;
}

/** The editable fields of a webhook. */
export interface DoorWebhookFields {
  name: string;
  action: DoorAction;
  ringGroupId: Id | null;
  pagingGroupId: Id | null;
  announcementId: Id | null;
  rateLimit: number;
  enabled: boolean;
}

export async function listDoorWebhooks(
  signal?: AbortSignal,
): Promise<DoorWebhook[]> {
  const r = await request<{ items: DoorWebhook[] }>(
    "GET",
    "/api/v1/door/webhooks",
    { signal },
  );
  return r.items;
}

export const createDoorWebhook = (fields: DoorWebhookFields) =>
  request<DoorWebhookCreated>("POST", "/api/v1/door/webhooks", {
    body: fields,
  });

export const putDoorWebhook = (id: Id, fields: DoorWebhookFields) =>
  request<DoorWebhook>("PUT", `/api/v1/door/webhooks/${seg(id)}`, {
    body: fields,
  });

export const deleteDoorWebhook = (id: Id) =>
  request<void>("DELETE", `/api/v1/door/webhooks/${seg(id)}`);

export const rotateDoorWebhookSecret = (id: Id) =>
  request<DoorWebhookCreated>(
    "POST",
    `/api/v1/door/webhooks/${seg(id)}/rotate-secret`,
  );

/** The URL a doorbell posts to. */
export const doorTriggerUrl = (id: Id, origin = window.location.origin) =>
  `${origin}/api/v1/door/webhooks/${seg(id)}/trigger`;
