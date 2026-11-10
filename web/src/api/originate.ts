// Click-to-call (docs/integrations.md, "Click-to-call"): ring the user's own
// phone first, then dial the destination once it is answered.
import { request } from "../api";

/** Where a click-to-call is (spec I-37). */
export type OriginateState =
  | "queued"
  | "ringing_agent"
  | "agent_answered"
  | "dialing_target"
  | "connected"
  | "ended"
  | "failed";

/** Why a click-to-call failed. */
export type OriginateReason =
  | "no_registration"
  | "agent_no_answer"
  | "agent_busy"
  | "agent_dnd"
  | "target_busy"
  | "target_no_answer"
  | "route_denied"
  | "rate_limited"
  | "node_unavailable"
  | "cancelled";

/** POST /api/v1/calls/originate */
export interface OriginateRequest {
  destination: string;
  /** The extension whose phone rings; absent is the caller's own. */
  extension?: string;
  deviceId?: number;
  autoAnswer?: boolean;
  idempotencyKey?: string;
}

/** The status of a click-to-call. */
export interface OriginateStatus {
  originateId: string;
  state: OriginateState;
  reason?: OriginateReason;
  extension: string;
  destination: string;
  correlationId?: string;
  createdAt: string;
  updatedAt: string;
}

export const originateFinished = (s: OriginateState) =>
  s === "ended" || s === "failed";

/** A random idempotency key, so a double click is one call. */
export function newIdempotencyKey(): string {
  return crypto.randomUUID();
}

/** The destination a tel: link carries, or "" when it is not dialable. */
export function dialable(raw: string): string {
  let n = raw.trim();
  // A tel: URI may carry visual separators and parameters (RFC 3966).
  n = n.replace(/^tel:/i, "").split(";")[0] ?? "";
  try {
    n = decodeURIComponent(n);
  } catch {
    return "";
  }
  n = n.replace(/[\s().-]/g, "");
  return /^[0-9+*#]{1,64}$/.test(n) ? n : "";
}

/** POST /api/v1/calls/originate: queue a click-to-call. */
export function originateCall(
  body: OriginateRequest,
  signal?: AbortSignal,
): Promise<OriginateStatus> {
  return request("POST", "/api/v1/calls/originate", { body, signal });
}

/** GET /api/v1/calls/originate/{id}: the state of a click-to-call. */
export function getOriginate(
  id: string,
  signal?: AbortSignal,
): Promise<OriginateStatus> {
  return request("GET", `/api/v1/calls/originate/${encodeURIComponent(id)}`, {
    signal,
  });
}

/** DELETE /api/v1/calls/originate/{id}: hang up a click-to-call. */
export function cancelOriginate(
  id: string,
  signal?: AbortSignal,
): Promise<OriginateStatus> {
  return request(
    "DELETE",
    `/api/v1/calls/originate/${encodeURIComponent(id)}`,
    { signal },
  );
}

/** GET/PUT /api/v1/integrations/click-to-call */
export interface ClickToCallSettings {
  destinations: "inherit" | "restricted";
  countryPrefixes: string[];
  autoAnswerAllowed: boolean;
}

/** GET the click-to-call policy (administrators). */
export function getClickToCall(
  signal?: AbortSignal,
): Promise<ClickToCallSettings> {
  return request("GET", "/api/v1/integrations/click-to-call", { signal });
}

/** PUT the click-to-call policy (administrators). */
export function putClickToCall(
  body: ClickToCallSettings,
): Promise<ClickToCallSettings> {
  return request("PUT", "/api/v1/integrations/click-to-call", { body });
}
