// Client for the door station routes: the door-open settings of a station
// (admin), and the door calls the signed-in user answered (self-service).
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
