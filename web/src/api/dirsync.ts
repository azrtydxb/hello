// Directory sync (docs/integrations.md, "Directory sync"): AD/LDAP, Google
// Workspace and Microsoft Entra connectors, their runs, previews and
// changes. Administrators only.
import { request } from "../api";

export type ConnectorMode = "authoritative" | "create_only" | "link_only";

export interface Transform {
  kind: "digits" | "last" | "regex";
  n?: number;
  pattern?: string;
}

export interface AttrMap {
  sources: string[];
  transforms: Transform[];
}

export interface ActiveMap {
  attribute: string;
  mode: "" | "always" | "uac" | "bool" | "bool_inverted";
}

export interface Mappings {
  username: AttrMap;
  email: AttrMap;
  displayName: AttrMap;
  extension: AttrMap;
  active: ActiveMap;
}

export interface RoleRule {
  groupId: string;
  groupName?: string;
  role: "user" | "viewer" | "operator" | "admin";
}

export interface RingGroupRule {
  groupId: string;
  groupName?: string;
  ringGroupId: number;
}

export interface GroupRules {
  roles: RoleRule[];
  ringGroups: RingGroupRule[];
}

/** How a connector's users sign in: they have no local password. */
export type LoginMethod = "" | "oidc" | "ldap_bind";

export interface Options {
  absenceRuns: number;
  disableDevices: boolean | null;
  allowAdminFromDirectory: boolean;
  runTimeoutMinutes: number;
  userCap: number;
  loginMethod: LoginMethod;
  oidcProvider: "" | "google" | "entra";
}

export type ConnectorKind = "ldap" | "google" | "entra";

export type GoogleAuthMode = "service_account" | "oauth_admin" | "delegation";

/** Config of a Google Workspace connector. */
export interface GoogleConfig {
  authMode: GoogleAuthMode;
  customer?: string;
  domain?: string;
  /** delegation only: the administrator to impersonate. */
  subject?: string;
  /** oauth_admin only: the customer's OAuth client id. */
  clientId?: string;
  query?: string;
  timeoutSeconds?: number;
}

/** Config of a Microsoft Entra connector. */
export interface EntraConfig {
  tenantId: string;
  clientId: string;
  authMethod: "secret" | "certificate";
  certificatePem?: string;
  includeGuests?: boolean;
  timeoutSeconds?: number;
}

/** The read-only Google scopes Hello asks for (and a delegated client must be authorised for). */
export const GOOGLE_SCOPES = [
  "https://www.googleapis.com/auth/admin.directory.user.readonly",
  "https://www.googleapis.com/auth/admin.directory.group.readonly",
  "https://www.googleapis.com/auth/admin.directory.group.member.readonly",
];

export interface LdapConfig {
  url: string;
  startTls?: boolean;
  bindDn: string;
  baseDn: string;
  scope?: "subtree" | "onelevel" | "base";
  userFilter?: string;
  groupBaseDn?: string;
  groupFilter?: string;
  caCertPem?: string;
  serverName?: string;
  pageSize?: number;
  timeoutSeconds?: number;
  nestedGroups?: boolean;
}

/** A connector's config: the fields of its kind. */
export type ConnectorConfig = Partial<LdapConfig> &
  Partial<GoogleConfig> &
  Partial<EntraConfig>;

export interface Connector {
  id: number;
  kind: ConnectorKind;
  name: string;
  enabled: boolean;
  mode: ConnectorMode;
  scheduleMinutes: number;
  config: ConnectorConfig;
  mappings: Mappings;
  groupRules: GroupRules;
  options: Options;
  /** A credential (bind password, key file, client secret or private key) is stored. It is write-only. */
  hasCredential: boolean;
  status: "new" | "ok" | "failing";
  lastRunAt: string | null;
  lastSuccessAt: string | null;
  lastErrorCode: string;
  lastErrorDetail: string;
  consecutiveFailures: number;
  linkedUsers: number;
  /** When the last full run finished (a full run is forced every 24 h). */
  lastFullRunAt: string | null;
  createdAt: string;
  updatedAt: string;
}

/** POST and PATCH /directory/connectors body (every field optional on PATCH). */
export interface ConnectorInput {
  kind?: ConnectorKind;
  name?: string;
  enabled?: boolean;
  mode?: ConnectorMode;
  scheduleMinutes?: number;
  config?: ConnectorConfig;
  mappings?: Mappings;
  groupRules?: GroupRules;
  options?: Partial<Options>;
  /** The LDAP bind password. */
  bindPassword?: string;
  /** The Google or Entra credential. */
  credential?: string;
}

export interface SampleRow {
  externalId: string;
  raw: Record<string, string[]>;
  mapped: {
    username: string;
    email: string;
    displayName: string;
    extension: string;
    extensionError?: string;
    active: boolean;
  };
}

export interface TestResult {
  ok: boolean;
  errorCode: string;
  errorDetail: string;
  serverCertSubject: string;
  serverCertExpires: string | null;
  matchedUsers: number;
  sample: SampleRow[];
  groups: { id: string; name: string }[];
}

export type RunStatus =
  | "queued"
  | "running"
  | "preview"
  | "succeeded"
  | "failed"
  | "needs_confirmation"
  | "stale";

export interface Run {
  id: number;
  connectorId: number;
  kind: "preview" | "apply" | "scheduled" | "scim";
  status: RunStatus;
  requestedBy: string;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
  replica: string;
  counts: Record<string, number>;
  diffHash: string;
  /** The run read only what changed since the last run. It never disables a
   * user for being absent; only a full run does. */
  incremental: boolean;
  /** Why a full run was full (manual, no_mark, full_interval, ...). */
  fullReason: string;
  errorCode: string;
  errorDetail: string;
}

export type ChangeAction =
  | "create"
  | "update"
  | "link"
  | "adopt"
  | "disable"
  | "enable"
  | "conflict"
  | "skip"
  | "error";

export interface Change {
  id: number;
  runId: number;
  action: ChangeAction;
  userId: number | null;
  externalId: string;
  username: string;
  fieldDiffs: Record<string, { before: string; after: string }>;
  reason: string;
  overridable: boolean;
  createdAt: string;
}

export interface Decisions {
  adopt?: string[];
  adoptAll?: boolean;
  override?: string[];
}

const conn = (id: number) => `/api/v1/directory/connectors/${String(id)}`;
const run = (id: number) => `/api/v1/directory/runs/${String(id)}`;

export async function listConnectors(
  signal?: AbortSignal,
): Promise<Connector[]> {
  const res = await request<{ items: Connector[] }>(
    "GET",
    "/api/v1/directory/connectors",
    { signal },
  );
  return res.items;
}

export const createConnector = (input: ConnectorInput) =>
  request<Connector>("POST", "/api/v1/directory/connectors", { body: input });

export const updateConnector = (id: number, input: ConnectorInput) =>
  request<Connector>("PATCH", conn(id), { body: input });

export const deleteConnector = (id: number, release: boolean) =>
  request<void>("DELETE", `${conn(id)}${release ? "?release=true" : ""}`);

export const testConnector = (id: number, mappings?: Mappings) =>
  request<TestResult>("POST", `${conn(id)}/test`, {
    body: mappings ? { mappings } : {},
  });

export const startRun = (id: number, dryRun: boolean) =>
  request<Run>("POST", `${conn(id)}/sync`, { body: { dryRun } });

export async function listRuns(
  id: number,
  signal?: AbortSignal,
): Promise<Run[]> {
  const res = await request<{ items: Run[] }>("GET", `${conn(id)}/runs`, {
    signal,
  });
  return res.items;
}

export const getRun = (id: number, signal?: AbortSignal) =>
  request<Run>("GET", run(id), { signal });

export async function listChanges(
  id: number,
  opts: { action?: ChangeAction | ""; after?: number; limit?: number } = {},
  signal?: AbortSignal,
): Promise<{ items: Change[]; nextAfter: number | null }> {
  const q = new URLSearchParams();
  if (opts.action) q.set("action", opts.action);
  if (opts.after) q.set("after", String(opts.after));
  if (opts.limit) q.set("limit", String(opts.limit));
  const qs = q.toString();
  return request("GET", `${run(id)}/changes${qs ? `?${qs}` : ""}`, { signal });
}

export const applyRun = (id: number, decisions: Decisions) =>
  request<Run>("POST", `${run(id)}/apply`, { body: decisions });

export const confirmRun = (id: number) =>
  request<Run>("POST", `${run(id)}/confirm`, { body: {} });

/** Starts a Google OAuth-admin connector's consent: the URL to send the browser to. */
export const startConsent = (id: number) =>
  request<{ url: string }>("POST", `${conn(id)}/consent`);
