// CRM connectors (docs/integrations.md, "CRM connectors"): the settings, the
// sign-in, the user mapping and the logging jobs. Client secrets and tokens
// are never returned by the API; a secret is only ever sent.
import { request } from "../api";

export interface CRMCapabilities {
  lookup: boolean;
  logCalls: boolean;
  recordings: boolean;
  clickToCall: boolean;
  screenPop: boolean;
  ticket: boolean;
  /** The CRM stores the audio file itself; Hello uploads it. */
  recordingUpload?: boolean;
  /** The CRM fetches a short-lived link once and keeps a copy. */
  recordingCopy?: boolean;
}

export type CRMConnectionStatus = "connected" | "needs_reauth" | "revoked";

export interface CRMConnection {
  status: CRMConnectionStatus;
  externalAccount?: string;
  grantedScopes: string[];
  lastError?: string;
  expiresAt?: string;
  updatedAt: string;
}

export interface CRMConnector {
  id: number;
  kind: string;
  name: string;
  enabled: boolean;
  config: Record<string, string>;
  hasClientSecret: boolean;
  recordings: boolean;
  linkTtlHours: number;
  logMissed: boolean;
  fallbackOwner: string;
  createdAt: string;
  capabilities: CRMCapabilities;
  connection: CRMConnection | null;
  /** oauth signs in through the browser; credential verifies the secret on Connect. Absent means oauth. */
  auth?: "oauth" | "credential";
  /** The redirect URI to register in the CRM app; absent without HELLO_PUBLIC_URL. */
  redirectUri?: string;
}

/** The fields of a create or update; an absent field keeps its value. */
export interface CRMConnectorInput {
  kind?: string;
  name?: string;
  enabled?: boolean;
  config?: Record<string, string>;
  /** Write-only; an empty string clears it. */
  clientSecret?: string;
  recordings?: boolean;
  linkTtlHours?: number;
  logMissed?: boolean;
  fallbackOwner?: string;
}

export type CRMLogStatus =
  "waiting" | "pending" | "logged" | "update" | "dead" | "skipped";

export const CRM_LOG_STATUSES: readonly CRMLogStatus[] = [
  "waiting",
  "pending",
  "logged",
  "update",
  "dead",
  "skipped",
];

export interface CRMLog {
  id: number;
  connectorId: number;
  callId: string;
  direction: string;
  status: CRMLogStatus;
  externalId: string;
  attempts: number;
  nextAttemptAt: string;
  lastError: string;
  eventType: string;
  ownerExtension: string;
  recordingId?: number;
  linkExpiresAt?: string;
  createdAt: string;
  updatedAt: string;
}

export interface CRMUserMapping {
  userId: number;
  username: string;
  email: string;
  extension?: string;
  externalUserId?: string;
  source?: "email" | "manual";
}

export interface CRMTestResult {
  ok: boolean;
  error?: string;
}

const base = "/api/v1/integrations/crm";

export async function listCRMConnectors(
  signal?: AbortSignal,
): Promise<CRMConnector[]> {
  const r = await request<{ items: CRMConnector[] }>("GET", base, { signal });
  return r.items;
}

export function createCRMConnector(
  body: CRMConnectorInput,
): Promise<CRMConnector> {
  return request("POST", base, { body });
}

export function updateCRMConnector(
  id: number,
  body: CRMConnectorInput,
): Promise<CRMConnector> {
  return request("PATCH", `${base}/${id}`, { body });
}

export function deleteCRMConnector(id: number): Promise<void> {
  return request("DELETE", `${base}/${id}`);
}

/**
 * Starts the sign-in; the browser goes to the returned address. An empty
 * address means a credential connector was verified and is connected.
 */
export async function connectCRMConnector(id: number): Promise<string> {
  const r = await request<{ authorizeUrl: string }>(
    "POST",
    `${base}/${id}/connect`,
  );
  return r.authorizeUrl;
}

export function disconnectCRMConnector(id: number): Promise<void> {
  return request("DELETE", `${base}/${id}/connection`);
}

export function testCRMConnector(id: number): Promise<CRMTestResult> {
  return request("POST", `${base}/${id}/test`);
}

export function listCRMLogs(
  id: number,
  status: CRMLogStatus | "",
  signal?: AbortSignal,
): Promise<{ items: CRMLog[]; nextBefore: number }> {
  const q = new URLSearchParams({ limit: "50" });
  if (status) q.set("status", status);
  return request("GET", `${base}/${id}/logs?${q.toString()}`, { signal });
}

export function retryCRMLog(id: number): Promise<CRMLog> {
  return request("POST", `${base}/logs/${id}/retry`);
}

export async function listCRMUsers(
  id: number,
  signal?: AbortSignal,
): Promise<CRMUserMapping[]> {
  const r = await request<{ items: CRMUserMapping[] }>(
    "GET",
    `${base}/${id}/users`,
    { signal },
  );
  return r.items;
}

/** Sets the CRM user of a Hello user; an empty id clears the mapping. */
export function setCRMUser(
  id: number,
  userId: number,
  externalUserId: string,
): Promise<void> {
  return request("PUT", `${base}/${id}/users/${userId}`, {
    body: { externalUserId },
  });
}
