// Directory sync (docs/integrations.md, "Directory sync"): AD/LDAP and SCIM
// connectors, their runs, previews and changes, and the SCIM tokens and
// status (docs/scim.md). Administrators only.
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

export interface Options {
  absenceRuns: number;
  disableDevices: boolean | null;
  allowAdminFromDirectory: boolean;
  runTimeoutMinutes: number;
  userCap: number;
}

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

/** Config of a scim connector; it has no credential (SCIM tokens instead). */
export interface ScimConfig {
  /** A pushed user may take over a local (non-admin) user with the same
   * e-mail or username; off, such a push is refused (409). */
  adoptExisting?: boolean;
}

export type ConnectorKind = "ldap" | "scim";

export interface Connector {
  id: number;
  kind: ConnectorKind;
  name: string;
  enabled: boolean;
  mode: ConnectorMode;
  scheduleMinutes: number;
  /** LdapConfig for kind ldap, ScimConfig for kind scim. */
  config: LdapConfig & ScimConfig;
  mappings: Mappings;
  groupRules: GroupRules;
  options: Options;
  /** A bind password is stored. It is write-only. Always false for scim. */
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
  /** scim: set while the mass-deprovision guard pauses SCIM writes. */
  pausedAt?: string | null;
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
  config?: LdapConfig | ScimConfig;
  mappings?: Mappings;
  groupRules?: GroupRules;
  options?: Partial<Options>;
  bindPassword?: string;
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

/** A SCIM bearer token's metadata; the token itself is shown once. */
export interface ScimToken {
  id: number;
  connectorId: number;
  hint: string;
  createdBy: string;
  createdAt: string;
  lastUsedAt: string | null;
  revokedAt: string | null;
}

/** The answer to an issue: the token, shown this once, and the base URL. */
export interface ScimTokenCreated extends ScimToken {
  token: string;
  baseUrl: string;
}

export interface ScimStatus {
  connectorId: number;
  baseUrl: string;
  enabled: boolean;
  pausedAt: string | null;
  pausedRunId: number | null;
  guard: { disables: number; limit: number; linked: number };
  resources: {
    users: number;
    activeUsers: number;
    deletedUsers: number;
    groups: number;
  };
  lastRequestAt: string | null;
  activeTokens: number;
  adoptExisting: boolean;
}

export const issueScimToken = (id: number) =>
  request<ScimTokenCreated>("POST", `${conn(id)}/scim-token`);

export async function listScimTokens(
  id: number,
  signal?: AbortSignal,
): Promise<ScimToken[]> {
  const res = await request<{ items: ScimToken[] }>(
    "GET",
    `${conn(id)}/scim-tokens`,
    { signal },
  );
  return res.items;
}

export const revokeScimToken = (tokenId: number) =>
  request<void>("DELETE", `/api/v1/directory/scim-tokens/${String(tokenId)}`);

export const getScimStatus = (id: number, signal?: AbortSignal) =>
  request<ScimStatus>("GET", `${conn(id)}/scim`, { signal });
