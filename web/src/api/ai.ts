// Client for external AI access (spec ai-external-access): the consent
// screen, connected apps, service accounts, scoped API tokens, the MCP
// settings and the skills download. The shared request helpers live in ../api.
import { list, request, type Id } from "../api";
import type { Role } from "../role";

export type { Role };

/** An OAuth scope (internal/auth Scope). */
export type Scope = "read" | "write" | "admin" | "secrets" | "session";

/** The scopes a credential can hold, weakest first. */
export const SCOPES: readonly Scope[] = ["read", "write", "admin", "secrets"];

/** Plain-language lines for each scope, shown on consent and on /ai. */
export const SCOPE_TEXT: Record<Scope, string> = {
  read: "See the configuration, live state and call history.",
  write:
    "Change extensions, devices, voicemail, trunks, routes, ring groups, phones and templates.",
  admin:
    "Manage credentials and the cluster: API tokens, service accounts, other users' apps, vendor redirect accounts and node drain.",
  secrets:
    "Reveal and rotate secrets in plaintext: device SIP secrets, phone provisioning tokens and phone admin passwords.",
  session: "Approve or deny access requests in the console.",
};

/** The operations the `secrets` scope unlocks (the route table's secrets rows). */
export const SECRETS_OPERATIONS: readonly string[] = [
  "rotate a device's SIP secret",
  "rotate or re-arm a phone's provisioning token",
  "reveal or rotate a phone's admin password",
];

/** The scopes a role may grant (auth.GrantableScopes). */
export function grantableScopes(role: Role): readonly Scope[] {
  switch (role) {
    case "admin":
      return SCOPES;
    case "operator":
      return ["read", "write"];
    default:
      return ["read"];
  }
}

/** The signed-in user's role (GET /api/v1/auth/me, schema Me). */
export async function fetchRole(signal?: AbortSignal): Promise<Role> {
  const me = await request<{ username: string; role: Role }>(
    "GET",
    "/api/v1/auth/me",
    { signal },
  );
  return me.role;
}

// --- consent -----------------------------------------------------------------

/** GET /api/v1/oauth/requests/{id}: a pending authorization request (schema OAuthRequest). */
export interface ConsentRequest {
  id: string;
  /** The client id: a metadata document URL, a registered id or a service account. */
  clientId: string;
  /** Self-asserted by the client. */
  clientName: string;
  /** Empty when the client gave none. */
  clientUri: string;
  /** The host the client id names, worked out by the server. */
  clientHost: string;
  redirectUri: string;
  redirectHost: string;
  /** False for dynamically registered clients. */
  verified: boolean;
  scopes: Scope[];
  resources: string[];
  expiresAt: string;
  /** The scopes the signed-in user's role may grant. */
  grantableScopes: Scope[];
}

export const getConsentRequest = (requestId: string, signal?: AbortSignal) =>
  request<ConsentRequest>(
    "GET",
    `/api/v1/oauth/requests/${encodeURIComponent(requestId)}`,
    { signal },
  );

/** The approve and deny answer: where to send the browser next. */
export interface ConsentRedirect {
  redirect: string;
}

export const approveConsent = (requestId: string, scopes: Scope[]) =>
  request<ConsentRedirect>(
    "POST",
    `/api/v1/oauth/requests/${encodeURIComponent(requestId)}/approve`,
    { body: { scopes } },
  );

export const denyConsent = (requestId: string) =>
  request<ConsentRedirect>(
    "POST",
    `/api/v1/oauth/requests/${encodeURIComponent(requestId)}/deny`,
  );

// --- AI settings ---------------------------------------------------------------

/** GET /api/v1/ai/settings */
export interface AISettings {
  enabled: boolean;
  publicUrl: string;
  issuer: string;
  apiUrl: string;
  mcpUrl: string;
  authorizationServerMetadataUrl: string;
  apiMetadataUrl: string;
  mcpMetadataUrl: string;
  protocolVersions: string[];
  dcr: boolean;
  scopes: { name: Scope; description: string }[];
}

export const getAISettings = (signal?: AbortSignal) =>
  request<AISettings>("GET", "/api/v1/ai/settings", { signal });

// --- connected apps --------------------------------------------------------------

/** GET /api/v1/oauth/grants item (schema OAuthGrant). */
export interface Grant {
  id: number;
  userId: number;
  /** The user who approved it (shown to administrators). */
  username: string;
  clientId: string;
  clientName: string;
  clientHost: string;
  verified: boolean;
  scopes: Scope[];
  resources: string[];
  createdAt: string;
  lastUsedAt: string | null;
}

export const listGrants = (signal?: AbortSignal) =>
  list<Grant>("/api/v1/oauth/grants", signal);

export const revokeGrant = (grantId: Id) =>
  request<void>(
    "DELETE",
    `/api/v1/oauth/grants/${encodeURIComponent(String(grantId))}`,
  );

// --- service accounts ------------------------------------------------------------

/** A live client secret as listed (schema ClientSecret); the secret itself never is. */
export interface ServiceSecret {
  id: number;
  createdAt: string;
  expiresAt: string | null;
  lastUsedAt: string | null;
}

/** GET /api/v1/service-accounts item (schema ServiceAccount). */
export interface ServiceAccount {
  /** The OAuth client id (hello_sa_...). */
  id: string;
  name: string;
  description: string;
  role: Role;
  scopes: Scope[];
  enabled: boolean;
  createdAt: string;
  lastUsedAt: string | null;
  /** Live secrets only. */
  secrets: ServiceSecret[];
}

/** POST /api/v1/service-accounts/{id}/secrets response (schema CreatedClientSecret). */
export interface CreatedServiceSecret {
  id: number;
  clientId: string;
  /** Shown once; never returned again. */
  secret: string;
  createdAt: string;
  expiresAt: string | null;
}

export const listServiceAccounts = (signal?: AbortSignal) =>
  list<ServiceAccount>("/api/v1/service-accounts", signal);

export const createServiceAccount = (input: {
  name: string;
  description?: string;
  role: Role;
  scopes: Scope[];
}) =>
  request<ServiceAccount>("POST", "/api/v1/service-accounts", {
    body: { ...input, enabled: true },
  });

export const updateServiceAccount = (
  accountId: Id,
  patch: Partial<Pick<ServiceAccount, "enabled" | "description" | "scopes">>,
) =>
  request<ServiceAccount>(
    "PATCH",
    `/api/v1/service-accounts/${encodeURIComponent(String(accountId))}`,
    { body: patch },
  );

export const deleteServiceAccount = (accountId: Id) =>
  request<void>(
    "DELETE",
    `/api/v1/service-accounts/${encodeURIComponent(String(accountId))}`,
  );

export const createServiceSecret = (accountId: Id) =>
  request<CreatedServiceSecret>(
    "POST",
    `/api/v1/service-accounts/${encodeURIComponent(String(accountId))}/secrets`,
  );

export const revokeServiceSecret = (accountId: Id, secretId: Id) =>
  request<void>(
    "DELETE",
    `/api/v1/service-accounts/${encodeURIComponent(String(accountId))}/secrets/${encodeURIComponent(String(secretId))}`,
  );

// --- skills ------------------------------------------------------------------------

/** GET /api/v1/skills item. */
export interface Skill {
  name: string;
  description: string;
  version: string;
}

export const listSkills = (signal?: AbortSignal) =>
  list<Skill>("/api/v1/skills", signal);

/** Where a skill's zip is downloaded from (same origin, the session cookie applies). */
export const skillDownloadPath = (name: string) =>
  `/api/v1/skills/${encodeURIComponent(name)}/download`;
