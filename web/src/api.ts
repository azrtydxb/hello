// Typed client for the hello-control management API (plan: Shared contracts).
// Every call sends the session cookie. A 401 from any call except login and
// the initial "who am I" probe triggers the unauthorized handler, which the
// app wires to its sign-in redirect.

/** Resource identifiers are opaque: passed back exactly as the server sent them. */
export type Id = number | string;

/** GET /api/v1/version */
export interface VersionInfo {
  version: string;
  commit: string;
  configRevision: number;
}

/** What happens to a voicemail message once it was emailed. */
export type VoicemailEmailAfter = "none" | "mark_read" | "delete";

/** A user's voicemail-to-email options. */
export interface VoicemailEmailPrefs {
  notify: boolean;
  attach: boolean;
  after: VoicemailEmailAfter;
}

/** An extension as the signed-in user's profile lists it. */
export interface MeExtension {
  id: Id;
  number: string;
  name: string;
}

/** GET /api/v1/auth/me */
export interface Me {
  username: string;
  /** The caller's role (spec S-23); read fresh on every request. */
  role: "user" | "viewer" | "operator" | "admin";
  displayName?: string;
  email?: string;
  authMethod?: string;
  /** An administrator set the password: nothing else works until it is changed. */
  mustChangePassword?: boolean;
  /** The extensions the user manages. */
  extensions?: MeExtension[];
  voicemailEmail?: VoicemailEmailPrefs;
  /** Whether the system can send email at all. */
  emailDelivery?: boolean;
}

/** An API token as listed; the token itself is never returned. */
export interface ApiToken {
  id: Id;
  name: string;
  createdAt: string;
  lastUsedAt?: string | null;
  /** legacy tokens predate scopes and carry every one. */
  kind?: "legacy" | "personal";
  /** Absent or null on a legacy token: every scope. */
  scopes?: string[] | null;
  expiresAt?: string | null;
}

/** POST /api/v1/tokens response. */
export interface CreatedApiToken extends ApiToken {
  /** Shown once; never returned again. */
  token: string;
}

/** An extension: a dialable number, with its Phase 4 call features. */
export interface Extension {
  id: Id;
  number: string;
  name: string;
  /** E.164-ish number presented to carriers; "" when none. */
  externalNumber: string;
  /** Do-not-disturb; false when the server omits it. */
  dnd?: boolean;
  /** Forward target; "" = off. */
  forwardAlways?: string;
  forwardBusy?: string;
  forwardNoAnswer?: string;
  /** Unanswered/busy calls go to this extension's voicemail; true by default. */
  voicemailEnabled?: boolean;
  /** Calls are recorded by default; `*1` toggles recording mid-call either way. */
  recordDefault?: boolean;
  /** Left out of the automatic extensions address book; false by default. */
  directoryHidden?: boolean;
  /** An intercom call (`*80` plus the number) auto-answers; true by default. */
  allowIntercom?: boolean;
  /**
   * Call waiting; true by default. Off: a call to the extension while it is in
   * a call goes to forward-busy (else busy), and the phone's own call-waiting
   * setting is switched off at the next resync.
   */
  callWaiting?: boolean;
  /** The extension's voicemail box, when it has one. */
  voicemailBoxId?: Id | null;
  /** Music on hold class callers hear while this extension holds them; null = the default. */
  mohClassId?: Id | null;
  createdAt: string;
  updatedAt: string;
}

/** A device: SIP credentials belonging to one extension. */
export interface Device {
  id: Id;
  extensionId: Id;
  sipUsername: string;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

/** Device create / rotate-secret response. */
export interface DeviceWithSecret extends Device {
  /** Shown once; never returned again. */
  secret: string;
}

/** livestate.Binding */
export interface Binding {
  aor: string;
  extension: string;
  device: string;
  contactUri: string;
  source: string;
  transport: string;
  userAgent: string;
  path?: string[];
  receivedNode: string;
  expires: string;
  updatedAt: string;
}

/** livestate.Call */
export interface ActiveCall {
  id: string;
  sipCallId: string;
  from: string;
  to: string;
  state: string;
  node: string;
  media: string;
  startedAt: string;
  answeredAt?: string;
  /** In-call HA: "taken-over" once a surviving node re-homed the call. */
  ha: CallHA;
}

/** A live call's in-call HA state (GET /api/v1/calls `ha`). */
export type CallHA = "owned" | "taken-over";

/** A call detail record; null times are omitted. */
export interface Cdr {
  id: Id;
  correlationId: string;
  sipCallId: string;
  source: string;
  destination: string;
  startTime: string;
  ringTime?: string;
  answerTime?: string;
  endTime: string;
  durationMs: number;
  billableMs: number;
  sipNode: string;
  mediaMode: string;
  finalStatus: number;
  terminationSide: string;
  failureReason: string;
  direction: "internal" | "inbound" | "outbound";
  originalDestination: string;
  rewrittenDestination: string;
  route: string;
  trunk: string;
  /** Call quality of an anchored call; null when not measured. */
  rtpPackets?: number | null;
  rtpLost?: number | null;
  rtpJitterMs?: number | null;
}

/** One line of a routing trace (routing.Step). */
export interface TraceStep {
  n: number;
  text: string;
}

/** One paged member's outcome. */
export interface PageLegResult {
  extension: string;
  /** The member contact's device; "" when the member was never rung. */
  device: string;
  result: "answered" | "busy" | "dnd" | "no_answer" | "no_registration";
}

/** GET /api/v1/cdrs/{id}: the CDR plus its routing trace. */
export interface CdrDetail extends Cdr {
  /** call, page or intercom. */
  kind?: "call" | "page" | "intercom";
  /** A page's outcome per member, in the order they were paged. */
  pageLegs?: PageLegResult[];
  trace: TraceStep[];
  /** For failed calls, the reason of the last trace step. */
  explanation?: string;
}

/** A number rewrite (routing.Transform); every field optional, {} is identity. */
export interface Transform {
  strip?: number;
  prefix?: string;
  regex?: string;
  template?: string;
}

/** One weekly window (routing.Window). Days are 0 = Sunday … 6 = Saturday. */
export interface ScheduleWindow {
  days: number[];
  start: string;
  end: string;
}

/** routing.Schedule; a null schedule is always open. */
export interface Schedule {
  timeZone: string;
  windows: ScheduleWindow[];
}

/** One address of a trunk; port 0 means SRV, else 5060. */
export interface TrunkDestination {
  host: string;
  port: number;
  priority: number;
  weight: number;
}

/**
 * A trunk's internal dialing policy (routing.Dialing): whether, and what, it
 * may dial in the extension namespace. The zero value is mode "off".
 */
export interface InternalDialing {
  /** "off" (default) | "patterns" | "all". */
  mode: "off" | "patterns" | "all" | string;
  /** Non-empty iff mode is "patterns"; each ≤500 chars, at most 100. */
  patterns: string[];
}

/**
 * The policy in a POST/PATCH body: mode "all" without `confirm: true` is a
 * 400, so the console asks for explicit confirmation first.
 */
export type InternalDialingInput = InternalDialing & { confirm?: true };

/** The writable fields of a trunk. */
export interface TrunkFields {
  name: string;
  mode: "registration" | "ip";
  username: string;
  realm: string;
  fromDomain: string;
  /** Seconds. */
  registerExpires: number;
  /** Seconds. */
  optionsInterval: number;
  sourceCidrs: string[];
  /** 0 = unlimited. */
  maxCalls: number;
  defaultCallerId: string;
  enabled: boolean;
  destinations: TrunkDestination[];
  /** What the trunk may dial in the extension namespace. */
  internalDialing: InternalDialing;
  /**
   * The audio codecs offered to the trunk (store.Trunk.codecs): any of
   * "PCMU", "PCMA", "G722", "opus". Default ["PCMU", "PCMA"]: most carriers
   * accept G.711 only.
   */
  codecs: string[];
}

/** A trunk as returned: the password is never returned, only hasPassword. */
export interface Trunk extends TrunkFields {
  id: Id;
  hasPassword: boolean;
  createdAt: string;
  updatedAt: string;
}

/**
 * POST/PATCH body: `password` is write-only and sent only when changed;
 * `internalDialing` is sent whole, with `confirm` when its mode is "all".
 */
export type TrunkInput = Partial<TrunkFields> & {
  password?: string;
  internalDialing?: InternalDialingInput;
};

/** livestate.TrunkRegistration */
export interface TrunkRegistration {
  state: string;
  node: string;
  lastCode?: number;
  expires?: string;
  updatedAt: string;
}

/** livestate.DestinationHealth */
export interface DestinationHealth {
  destination: string;
  up: boolean;
  lastCode?: number;
  latencyNs?: number;
  checkedAt: string;
}

/** GET /api/v1/trunks/status item: livestate.TrunkStatus plus the name. */
export interface TrunkStatus {
  trunkId: Id;
  name: string;
  registration?: TrunkRegistration;
  destinations: DestinationHealth[];
  activeCalls: number;
}

/** The writable fields of an outbound route. */
export interface OutboundRouteFields {
  name: string;
  matchKind: "prefix" | "regex";
  match: string;
  sourceExtensions: string[];
  schedule: Schedule | null;
  numberTransform: Transform;
  callerIdTransform: Transform;
  /** Trunk ids in try order. */
  trunks: Id[];
  failoverCodes: number[];
  emergency: boolean;
  enabled: boolean;
}

/** An outbound route as stored, with its match-order position. */
export interface OutboundRoute extends OutboundRouteFields {
  id: Id;
  position: number;
}

/** The writable fields of an inbound route. */
export interface InboundRouteFields {
  name: string;
  didKind: "any" | "exact" | "prefix" | "regex";
  did: string;
  /** null = any trunk. */
  trunkId: Id | null;
  sipDomain: string;
  headerName: string;
  headerRegex: string;
  schedule: Schedule | null;
  callerIdTransform: Transform;
  /** "internal" looks the DID up in the extension namespace. */
  destinationKind: "extension" | "internal" | "external" | "sip_uri" | "ivr";
  destination: string;
  enabled: boolean;
  /** Music on hold class of the calls this route delivers; null = the default. */
  mohClassId?: Id | null;
  /** Play the class to the caller instead of ringback while the destination rings. */
  mohRingback?: boolean;
}

/** An inbound route as stored, with its match-order position. */
export interface InboundRoute extends InboundRouteFields {
  id: Id;
  position: number;
}

/** Which route list: calls out to trunks, or calls in from them. */
export type RouteDirection = "outbound" | "inbound";

/** POST /api/v1/routing/test body. */
export interface RouteTestRequest {
  /** An extension number, or "trunk:<id>". */
  from: string;
  number: string;
  callerId?: string;
  /** RFC 3339; omitted means now. */
  at?: string;
}

/** The route tester's decision. */
export interface RouteDecision {
  kind: "internal" | "outbound" | "inbound" | "reject" | string;
  extension?: string;
  sipUri?: string;
  number?: string;
  callerId?: string;
  route?: string;
  /** Trunk names in try order. */
  trunks?: string[];
  rejectCode?: number;
  reason?: string;
}

/** POST /api/v1/routing/test response. */
export interface RouteTestResult {
  decision: RouteDecision;
  trace: TraceStep[];
}

/** One page of GET /api/v1/cdrs. */
export interface CdrPage {
  items: Cdr[];
  /** Cursor for the next (older) page; empty when there is none. */
  next: string;
}

export const EXTENSION_NUMBER_PATTERN = /^[0-9]{2,10}$/;
export const SIP_USERNAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;

/** An API failure, carrying the error envelope's code and message when present. */
export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  /** Per-field validation failures from a 400, if any. */
  readonly fields: FieldError[];

  constructor(
    status: number,
    code: string,
    message: string,
    fields: FieldError[] = [],
  ) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.code = code;
    this.fields = fields;
  }
}

/** A validation failure on one field (routing.FieldError). */
export interface FieldError {
  path: string;
  /** 1-based line of a template body, when the failure has one. */
  line?: number;
  message: string;
}

/** The field errors carried by err, or [] for any other error. */
export function fieldErrors(err: unknown): FieldError[] {
  return err instanceof ApiError ? err.fields : [];
}

/** The sign-in URL that returns to `next` afterwards. */
export function loginPath(next: string): string {
  return `/login?next=${encodeURIComponent(next)}`;
}

/** Only same-origin absolute paths are accepted as a post-login target. */
export function safeNext(next: string | null): string {
  if (!next || !next.startsWith("/") || next.startsWith("//")) return "/";
  if (next.startsWith("/login")) return "/";
  return next;
}

function defaultUnauthorized() {
  const here = window.location.pathname + window.location.search;
  if (window.location.pathname === "/login") return;
  window.location.assign(loginPath(here));
}

let unauthorizedHandler: () => void = defaultUnauthorized;

/** Replace the 401 handler; returns a function restoring the default. */
export function setUnauthorizedHandler(handler: () => void): () => void {
  unauthorizedHandler = handler;
  return () => {
    if (unauthorizedHandler === handler)
      unauthorizedHandler = defaultUnauthorized;
  };
}

interface RequestOptions {
  body?: unknown;
  /**
   * Sent as the request body untouched (e.g. a multipart FormData with file
   * parts); overrides `body`, and the browser picks the Content-Type.
   */
  rawBody?: BodyInit;
  signal?: AbortSignal;
  /** When false, a 401 is returned to the caller instead of redirecting. */
  redirectOn401?: boolean;
}

function isFieldError(value: unknown): value is FieldError {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return typeof v.path === "string" && typeof v.message === "string";
}

function envelope(
  value: unknown,
): { code: string; message: string; fields: FieldError[] } | null {
  if (typeof value !== "object" || value === null) return null;
  const err = (value as Record<string, unknown>).error;
  if (typeof err !== "object" || err === null) return null;
  const { code, message, fields } = err as Record<string, unknown>;
  if (typeof message !== "string" || message === "") return null;
  return {
    code: typeof code === "string" ? code : "",
    message,
    fields: Array.isArray(fields) ? fields.filter(isFieldError) : [],
  };
}

async function errorFrom(
  res: Response,
  method: string,
  path: string,
): Promise<ApiError> {
  let parsed: ReturnType<typeof envelope> = null;
  try {
    parsed = envelope(await res.json());
  } catch {
    // Not JSON: fall through to the generic message.
  }
  if (parsed) {
    return new ApiError(res.status, parsed.code, parsed.message, parsed.fields);
  }
  return new ApiError(
    res.status,
    "",
    `${method} ${path} failed: HTTP ${res.status}`,
  );
}

/** One API call: JSON in and out, the error envelope as ApiError, 401 handled. */
export async function request<T>(
  method: string,
  path: string,
  { body, rawBody, signal, redirectOn401 = true }: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, {
    method,
    headers,
    body:
      rawBody !== undefined
        ? rawBody
        : body === undefined
          ? undefined
          : JSON.stringify(body),
    credentials: "same-origin",
    signal,
  });
  return responseJson<T>(res, method, path, redirectOn401);
}

/** A response's JSON, or its error envelope as ApiError (401 handled as `request` does). */
export async function responseJson<T>(
  res: Response,
  method: string,
  path: string,
  redirectOn401 = true,
): Promise<T> {
  if (!res.ok) {
    const err = await errorFrom(res, method, path);
    if (res.status === 401 && redirectOn401) unauthorizedHandler();
    throw err;
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

/** One API call answered with a text body (e.g. a rendered file); errors as `request`. */
export async function requestText(
  path: string,
  signal?: AbortSignal,
): Promise<string> {
  const res = await fetch(path, {
    method: "GET",
    headers: { Accept: "*/*" },
    credentials: "same-origin",
    signal,
  });
  if (!res.ok) {
    const err = await errorFrom(res, "GET", path);
    if (res.status === 401) unauthorizedHandler();
    throw err;
  }
  return res.text();
}

function items<T>(value: unknown, path: string): T[] {
  const list =
    typeof value === "object" && value !== null
      ? (value as Record<string, unknown>).items
      : undefined;
  if (!Array.isArray(list)) {
    throw new Error(`GET ${path} returned an unexpected payload`);
  }
  return list as T[];
}

/** The `items` of a list response, or an error naming the path. */
export { items as listItems };

/** GET `path` and return its `items`, the shape every Hello list returns. */
export async function list<T>(
  path: string,
  signal?: AbortSignal,
): Promise<T[]> {
  return items<T>(await request<unknown>("GET", path, { signal }), path);
}

const id = (value: Id) => encodeURIComponent(String(value));

// --- version ---------------------------------------------------------------

function isVersionInfo(value: unknown): value is VersionInfo {
  if (typeof value !== "object" || value === null) return false;
  const v = value as Record<string, unknown>;
  return (
    typeof v.version === "string" &&
    typeof v.commit === "string" &&
    typeof v.configRevision === "number"
  );
}

/** GET /api/v1/version */
export async function fetchVersion(signal?: AbortSignal): Promise<VersionInfo> {
  const body = await request<unknown>("GET", "/api/v1/version", { signal });
  if (!isVersionInfo(body)) {
    throw new Error("GET /api/v1/version returned an unexpected payload");
  }
  return body;
}

// --- auth ------------------------------------------------------------------

/** POST /api/v1/auth/login; a 401 is thrown to the caller, never redirected. */
export async function login(username: string, password: string): Promise<void> {
  await request<void>("POST", "/api/v1/auth/login", {
    body: { username, password },
    redirectOn401: false,
  });
}

/** POST /api/v1/auth/logout */
export async function logout(): Promise<void> {
  await request<void>("POST", "/api/v1/auth/logout", { redirectOn401: false });
}

/** The signed-in user, or null when there is no valid session. */
export async function fetchMe(signal?: AbortSignal): Promise<Me | null> {
  try {
    return await request<Me>("GET", "/api/v1/auth/me", {
      signal,
      redirectOn401: false,
    });
  } catch (err) {
    if (err instanceof ApiError && err.status === 401) return null;
    throw err;
  }
}

// --- API tokens --------------------------------------------------------------

export const listTokens = (signal?: AbortSignal) =>
  list<ApiToken>("/api/v1/tokens", signal);

export const createToken = (input: {
  name: string;
  scopes: string[];
  expiresAt?: string;
}) => request<CreatedApiToken>("POST", "/api/v1/tokens", { body: input });

export const deleteToken = (tokenId: Id) =>
  request<void>("DELETE", `/api/v1/tokens/${id(tokenId)}`);

// --- extensions --------------------------------------------------------------

export const listExtensions = (signal?: AbortSignal) =>
  list<Extension>("/api/v1/extensions", signal);

export const createExtension = (input: {
  number: string;
  name: string;
  externalNumber?: string;
}) => request<Extension>("POST", "/api/v1/extensions", { body: input });

export const updateExtension = (
  extensionId: Id,
  patch: {
    number?: string;
    name?: string;
    externalNumber?: string;
    dnd?: boolean;
    forwardAlways?: string;
    forwardBusy?: string;
    forwardNoAnswer?: string;
    voicemailEnabled?: boolean;
    recordDefault?: boolean;
    directoryHidden?: boolean;
    allowIntercom?: boolean;
    callWaiting?: boolean;
    /** null clears the pick (the global default plays). */
    mohClassId?: Id | null;
  },
) =>
  request<Extension>("PATCH", `/api/v1/extensions/${id(extensionId)}`, {
    body: patch,
  });

export const deleteExtension = (extensionId: Id) =>
  request<void>("DELETE", `/api/v1/extensions/${id(extensionId)}`);

// --- devices -----------------------------------------------------------------

export const listDevices = (signal?: AbortSignal) =>
  list<Device>("/api/v1/devices", signal);

export const createDevice = (input: {
  extensionId: Id;
  sipUsername: string;
  enabled?: boolean;
}) => request<DeviceWithSecret>("POST", "/api/v1/devices", { body: input });

export const updateDevice = (
  deviceId: Id,
  patch: { enabled?: boolean; extensionId?: Id },
) =>
  request<Device>("PATCH", `/api/v1/devices/${id(deviceId)}`, { body: patch });

export const deleteDevice = (deviceId: Id) =>
  request<void>("DELETE", `/api/v1/devices/${id(deviceId)}`);

export const rotateDeviceSecret = (deviceId: Id) =>
  request<DeviceWithSecret>(
    "POST",
    `/api/v1/devices/${id(deviceId)}/rotate-secret`,
  );

// --- live state and history --------------------------------------------------

export const listRegistrations = (signal?: AbortSignal) =>
  list<Binding>("/api/v1/registrations", signal);

export const listCalls = (signal?: AbortSignal) =>
  list<ActiveCall>("/api/v1/calls", signal);

/** GET /api/v1/cdrs, newest first; pass the previous page's `next` as `before`. */
export async function listCdrs(
  opts: { before?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<CdrPage> {
  const q = new URLSearchParams();
  if (opts.before) q.set("before", opts.before);
  if (opts.limit !== undefined) q.set("limit", String(opts.limit));
  const qs = q.toString();
  const path = `/api/v1/cdrs${qs ? `?${qs}` : ""}`;
  const body = await request<unknown>("GET", path, { signal });
  const page = items<Cdr>(body, path);
  const next = (body as Record<string, unknown>).next;
  return { items: page, next: typeof next === "string" ? next : "" };
}

/** GET /api/v1/cdrs/{id}: one CDR with its trace and explanation. */
export async function getCdr(
  cdrId: Id,
  signal?: AbortSignal,
): Promise<CdrDetail> {
  return request<CdrDetail>("GET", `/api/v1/cdrs/${id(cdrId)}`, { signal });
}

// --- trunks ------------------------------------------------------------------

export const listTrunks = (signal?: AbortSignal) =>
  list<Trunk>("/api/v1/trunks", signal);

export const getTrunk = (trunkId: Id, signal?: AbortSignal) =>
  request<Trunk>("GET", `/api/v1/trunks/${id(trunkId)}`, { signal });

export const createTrunk = (input: TrunkInput) =>
  request<Trunk>("POST", "/api/v1/trunks", { body: input });

export const updateTrunk = (trunkId: Id, patch: TrunkInput) =>
  request<Trunk>("PATCH", `/api/v1/trunks/${id(trunkId)}`, { body: patch });

export const deleteTrunk = (trunkId: Id) =>
  request<void>("DELETE", `/api/v1/trunks/${id(trunkId)}`);

export const listTrunkStatus = (signal?: AbortSignal) =>
  list<TrunkStatus>("/api/v1/trunks/status", signal);

// --- routes ------------------------------------------------------------------

export const listOutboundRoutes = (signal?: AbortSignal) =>
  list<OutboundRoute>("/api/v1/routes/outbound", signal);

export const getOutboundRoute = (routeId: Id, signal?: AbortSignal) =>
  request<OutboundRoute>("GET", `/api/v1/routes/outbound/${id(routeId)}`, {
    signal,
  });

export const createOutboundRoute = (input: OutboundRouteFields) =>
  request<OutboundRoute>("POST", "/api/v1/routes/outbound", { body: input });

export const updateOutboundRoute = (
  routeId: Id,
  patch: Partial<OutboundRouteFields>,
) =>
  request<OutboundRoute>("PATCH", `/api/v1/routes/outbound/${id(routeId)}`, {
    body: patch,
  });

export const deleteOutboundRoute = (routeId: Id) =>
  request<void>("DELETE", `/api/v1/routes/outbound/${id(routeId)}`);

export const listInboundRoutes = (signal?: AbortSignal) =>
  list<InboundRoute>("/api/v1/routes/inbound", signal);

export const getInboundRoute = (routeId: Id, signal?: AbortSignal) =>
  request<InboundRoute>("GET", `/api/v1/routes/inbound/${id(routeId)}`, {
    signal,
  });

export const createInboundRoute = (input: InboundRouteFields) =>
  request<InboundRoute>("POST", "/api/v1/routes/inbound", { body: input });

export const updateInboundRoute = (
  routeId: Id,
  patch: Partial<InboundRouteFields>,
) =>
  request<InboundRoute>("PATCH", `/api/v1/routes/inbound/${id(routeId)}`, {
    body: patch,
  });

export const deleteInboundRoute = (routeId: Id) =>
  request<void>("DELETE", `/api/v1/routes/inbound/${id(routeId)}`);

/** PUT /api/v1/routes/{direction}/order with the full permutation of ids. */
export const reorderRoutes = (direction: RouteDirection, ids: Id[]) =>
  request<void>("PUT", `/api/v1/routes/${direction}/order`, { body: { ids } });

/** POST /api/v1/routing/test: decide a call without placing it. */
export const testRoute = (input: RouteTestRequest) =>
  request<RouteTestResult>("POST", "/api/v1/routing/test", { body: input });

// --- cluster -------------------------------------------------------------------

/** A node's lifecycle state (cluster.State). */
export type MemberState =
  "JOINING" | "READY" | "DRAINING" | "UNHEALTHY" | "OFFLINE";

/** One node of the cluster (cluster.Member). */
export interface ClusterMember {
  id: string;
  kind: "sip" | "control" | string;
  state: MemberState | string;
  /** Why the node is not READY. */
  reason?: string;
  sipAddr?: string;
  httpAddr?: string;
  transports?: string[];
  activeCalls: number;
  registrations: number;
  version: string;
  configRevision: number;
  /**
   * Revisions this node's configuration is behind the current one; absent
   * while the current revision cannot be read (PostgreSQL down).
   */
  revisionLag?: number;
  startedAt: string;
  heartbeat: string;
  /**
   * An OFFLINE node that shut down cleanly: history, not a failure
   * (cluster.Member.Left).
   */
  left?: boolean;
  /**
   * An OFFLINE node whose slot a live node (its replacement pod) took over:
   * history, not a failure (cluster.Member.Superseded).
   */
  superseded?: boolean;
}

/** GET /api/v1/cluster */
export interface ClusterStatus {
  members: ClusterMember[];
  postgres: { up: boolean; error?: string };
  valkey: {
    up: boolean;
    mode: "single" | "sentinel" | string;
    primary?: string;
    error?: string;
  };
  /** The current configuration revision; null while PostgreSQL is down. */
  configRevision: number | null;
}

export const getCluster = (signal?: AbortSignal) =>
  request<ClusterStatus>("GET", "/api/v1/cluster", { signal });

/**
 * POST /api/v1/cluster/nodes/{id}/drain. Without `force`, the server answers
 * 409 when the drain would leave no READY SIP node.
 */
export const drainNode = (nodeId: string, force = false) =>
  request<void>(
    "POST",
    `/api/v1/cluster/nodes/${encodeURIComponent(nodeId)}/drain${force ? "?force=true" : ""}`,
  );

/** DELETE /api/v1/cluster/nodes/{id}/drain: return a node to service. */
export const undrainNode = (nodeId: string) =>
  request<void>(
    "DELETE",
    `/api/v1/cluster/nodes/${encodeURIComponent(nodeId)}/drain`,
  );

// --- voicemail ----------------------------------------------------------------

/** GET/PUT /api/v1/extensions/{id}/voicemail: a box's settings. */
export interface VoicemailBox {
  id: Id;
  extensionId: Id;
  /** Where messages are emailed; "" when none is set. */
  email: string;
  /** True when a password is set; the password itself is never returned. */
  hasPassword: boolean;
  /** True when a played-first greeting has been uploaded. */
  hasGreeting: boolean;
  /** True when an unreachable greeting has been uploaded. */
  hasUnreachableGreeting: boolean;
  createdAt: string;
  updatedAt: string;
}

/** PUT /api/v1/extensions/{id}/voicemail: what can change on a box. */
export interface VoicemailBoxInput {
  /** Sent only when changing it; never echoed back. */
  password?: string;
  email?: string;
  /** Uploaded as multipart `greeting` / `unreachable` file parts. */
  greeting?: File;
  unreachable?: File;
}

/** One stored voicemail message (GET /api/v1/voicemail/messages). */
export interface VoicemailMessage {
  id: Id;
  boxId: Id;
  /** Who left it: a number, or a SIP identity. */
  caller: string;
  durationMs: number;
  heard: boolean;
  /** pending | queued | sent | failed | skipped, when the server reports it. */
  emailStatus?: string;
  createdAt: string;
}

/**
 * GET /api/v1/voicemail/messages?box=&unheard=. `box` is the box id (the
 * extension's `voicemailBoxId`), not the extension id.
 */
export const listVoicemailMessages = (
  boxId: Id,
  opts: { unheard?: boolean } = {},
  signal?: AbortSignal,
) => {
  const q = new URLSearchParams({ box: String(boxId) });
  if (opts.unheard) q.set("unheard", "true");
  return list<VoicemailMessage>(`/api/v1/voicemail/messages?${q}`, signal);
};

/** POST /api/v1/voicemail/messages/{id}/heard. */
export const markMessageHeard = (messageId: Id, heard = true) =>
  request<void>("POST", `/api/v1/voicemail/messages/${id(messageId)}/heard`, {
    body: { heard },
  });

/** DELETE /api/v1/voicemail/messages/{id}. */
export const deleteVoicemailMessage = (messageId: Id) =>
  request<void>("DELETE", `/api/v1/voicemail/messages/${id(messageId)}`);

/**
 * GET /api/v1/voicemail/messages/{id}/audio: the audio, streamed by
 * hello-control (Range supported). Played by pointing an <audio> element at
 * this path; `isPlayableAudio` gates it.
 */
export const voicemailAudioPath = (messageId: Id) =>
  `/api/v1/voicemail/messages/${id(messageId)}/audio`;

/**
 * Whether GET audio answered with something an <audio> element can play: the
 * audio itself (200, or 206 for the gate's one-byte Range probe).
 */
export function isPlayableAudio(res: Response): boolean {
  return res.ok;
}

/** GET /api/v1/extensions/{id}/voicemail. */
export const getVoicemailBox = (extensionId: Id, signal?: AbortSignal) =>
  request<VoicemailBox>(
    "GET",
    `/api/v1/extensions/${id(extensionId)}/voicemail`,
    { signal },
  );

/**
 * PUT /api/v1/extensions/{id}/voicemail: JSON for password/email, multipart
 * form data when a greeting file is uploaded.
 */
export function updateVoicemailBox(
  extensionId: Id,
  input: VoicemailBoxInput,
): Promise<VoicemailBox> {
  const path = `/api/v1/extensions/${id(extensionId)}/voicemail`;
  if (!input.greeting && !input.unreachable) {
    return request<VoicemailBox>("PUT", path, {
      body: {
        ...(input.password !== undefined ? { password: input.password } : {}),
        ...(input.email !== undefined ? { email: input.email } : {}),
      },
    });
  }
  const form = new FormData();
  if (input.password !== undefined) form.set("password", input.password);
  if (input.email !== undefined) form.set("email", input.email);
  if (input.greeting) form.set("greeting", input.greeting, input.greeting.name);
  if (input.unreachable) {
    form.set("unreachable", input.unreachable, input.unreachable.name);
  }
  return request<VoicemailBox>("PUT", path, { rawBody: form });
}

// --- ring groups ----------------------------------------------------------------

/** How a group's members are rung. */
export type RingStrategy =
  "ring-all" | "sequential" | "round-robin" | "longest-idle" | "weighted";

/** All five strategies, in picker order. */
export const RING_STRATEGIES = [
  "ring-all",
  "sequential",
  "round-robin",
  "longest-idle",
  "weighted",
] as const;

/** Human names for the strategies. */
export const RING_STRATEGY_LABEL: Record<RingStrategy, string> = {
  "ring-all": "Ring all",
  sequential: "Sequential",
  "round-robin": "Round robin",
  "longest-idle": "Longest idle",
  weighted: "Weighted",
};

/** Where a call goes when every member missed it. */
export type FailureKind =
  "none" | "voicemail" | "external" | "announcement" | "ivr";

export const FAILURE_KIND_LABEL: Record<FailureKind, string> = {
  none: "Hang up",
  voicemail: "A member's voicemail box",
  external: "An external number",
  announcement: "An announcement",
  ivr: "An IVR menu",
};

/** One member of a ring group; `position` is 1-based ring order. */
export interface RingGroupMember {
  extensionId: Id | null;
  /** The member extension's number (read-only). */
  number?: string;
  /** The member extension's name (read-only). */
  name?: string;
  position: number;
  /** For the weighted strategy; 0 never rings first. */
  weight: number;
  /** Seconds this member rings before the next (hunt/sequential). */
  delay: number;
}

/** The writable fields of a ring group. */
export interface RingGroupFields {
  name: string;
  strategy: RingStrategy;
  /** True = hunt: one member at a time, advancing on no-answer/busy. */
  hunt: boolean;
  /** Seconds the whole group rings. */
  ringTimeout: number;
  /** Seconds added before the next member starts. */
  memberDelay: number;
  /** True = ring members even when they are on DND. */
  ignoreDnd: boolean;
  failureKind: FailureKind;
  /** Voicemail box (extension number) or external number; "" for none. */
  failureTarget: string;
  members: RingGroupMember[];
  /** Music on hold class of calls to the group; null = the default. */
  mohClassId?: Id | null;
  /** Play the class to the caller instead of ringback while the group rings. */
  mohRingback?: boolean;
}

/** A ring group as returned. */
export interface RingGroup extends RingGroupFields {
  id: Id;
  createdAt: string;
  updatedAt: string;
}

export const listRingGroups = (signal?: AbortSignal) =>
  list<RingGroup>("/api/v1/ring-groups", signal);

export const createRingGroup = (input: RingGroupFields) =>
  request<RingGroup>("POST", "/api/v1/ring-groups", { body: input });

export const getRingGroup = (groupId: Id, signal?: AbortSignal) =>
  request<RingGroup>("GET", `/api/v1/ring-groups/${id(groupId)}`, { signal });

export const updateRingGroup = (groupId: Id, patch: RingGroupFields) =>
  request<RingGroup>("PATCH", `/api/v1/ring-groups/${id(groupId)}`, {
    body: patch,
  });

export const deleteRingGroup = (groupId: Id) =>
  request<void>("DELETE", `/api/v1/ring-groups/${id(groupId)}`);

// --- paging groups ---------------------------------------------------------------

export type PagingPriority = "normal" | "emergency";

/** An extension in a paging group's member or caller list. */
export interface PagingExtension {
  extensionId: Id;
  /** The extension's number and name (read-only). */
  number: string;
  name: string;
}

/** The writable fields of a paging group (PUT replaces the whole group). */
export interface PagingGroupFields {
  name: string;
  /** The dialled page code, `*` plus 2 to 4 digits. */
  code: string;
  priority: PagingPriority;
  enabled: boolean;
  /** Extension ids paged together. */
  members: Id[];
  /** Extension ids allowed to page; empty means any internal extension. */
  callers: Id[];
  /** A catalog paging channel to page as well; omit for none. */
  multicastChannelId?: Id | null;
}

/** A paging group as returned. */
export interface PagingGroup {
  id: Id;
  name: string;
  code: string;
  priority: PagingPriority;
  enabled: boolean;
  members: PagingExtension[];
  callers: PagingExtension[];
  multicastChannelId?: Id | null;
  /** The multicast channel the page also goes to. */
  multicastChannel?: {
    id: Id;
    label: string;
    address: string;
    port: number;
    codec: string;
    enabled: boolean;
  } | null;
  createdAt: string;
  updatedAt: string;
}

export const listPagingGroups = (signal?: AbortSignal) =>
  list<PagingGroup>("/api/v1/paging-groups", signal);

export const createPagingGroup = (input: PagingGroupFields) =>
  request<PagingGroup>("POST", "/api/v1/paging-groups", { body: input });

export const putPagingGroup = (groupId: Id, input: PagingGroupFields) =>
  request<PagingGroup>("PUT", `/api/v1/paging-groups/${id(groupId)}`, {
    body: input,
  });

/** One registered contact's outcome in a test page. */
export interface PageTestContact {
  device: string;
  userAgent: string;
  result: "answered" | "busy" | "no_answer" | "failed";
  /** The auto-answer headers the INVITE carried, as "Name: value". */
  headers: string[];
}

/** POST /api/v1/paging-groups/{id}/test: what paging one member did. */
export interface PageTestResult {
  id: string;
  extension: string;
  result:
    "answered" | "busy" | "dnd" | "no_answer" | "no_registration" | "failed";
  detail?: string;
  node: string;
  contacts: PageTestContact[];
}

/** Pages one member of the group (the first when omitted) as a test. */
export const testPagingGroup = (groupId: Id, extension?: string) =>
  request<PageTestResult>("POST", `/api/v1/paging-groups/${id(groupId)}/test`, {
    body: extension === undefined ? {} : { extension },
  });

export const deletePagingGroup = (groupId: Id) =>
  request<void>("DELETE", `/api/v1/paging-groups/${id(groupId)}`);

// --- IVR menus -------------------------------------------------------------------

/** Where an IVR option, timeout or invalid choice sends the call. */
export const IVR_DESTINATION_KINDS = [
  "hangup",
  "extension",
  "ring_group",
  "voicemail",
  "announcement",
  "paging",
  "ivr",
  "external",
] as const;

export type IvrDestinationKind = (typeof IVR_DESTINATION_KINDS)[number];

export const IVR_DESTINATION_LABEL: Record<IvrDestinationKind, string> = {
  hangup: "Hang up",
  extension: "Extension",
  ring_group: "Ring group",
  voicemail: "Voicemail box",
  announcement: "Announcement, then hang up",
  paging: "Paging group",
  ivr: "Another IVR menu",
  external: "External number",
};

/**
 * A destination. The target is an extension number (extension, voicemail), a
 * name (ring_group, announcement, ivr), a page code (paging) or a number
 * (external); empty for hangup.
 */
export interface IvrDestination {
  kind: IvrDestinationKind;
  target: string;
}

/** One key of a menu and where it leads. */
export interface IvrOption extends IvrDestination {
  /** 0-9, * or #; each once per menu. */
  digit: string;
}

/** The writable fields of an IVR menu (PUT replaces the whole menu). */
export interface IvrMenuFields {
  name: string;
  /** Dials the menu directly; "" when it is only a destination. */
  number: string;
  enabled: boolean;
  greetingId: Id;
  /** null replays the greeting. */
  invalidPromptId: Id | null;
  timeoutPromptId: Id | null;
  digitTimeout: number;
  interDigitTimeout: number;
  maxRetries: number;
  allowDirectDial: boolean;
  timeout: IvrDestination;
  invalid: IvrDestination;
  options: IvrOption[];
}

/** An IVR menu as returned; the prompt names are read-only. */
export interface IvrMenu extends IvrMenuFields {
  id: Id;
  greetingName: string;
  invalidPromptName: string;
  timeoutPromptName: string;
  createdAt: string;
  updatedAt: string;
}

// --- call park and call pickup -------------------------------------------------

/** The writable fields of a park lot (PUT replaces the whole lot). */
export interface ParkLotFields {
  name: string;
  /** The first and last slot number; at most 100 slots. */
  slotStart: number;
  slotEnd: number;
  /** Seconds before an unretrieved call rings its parker again. */
  returnSecs: number;
  /** Seconds the parker then rings before the call goes to voicemail. */
  voicemailSecs: number;
  enabled: boolean;
}

/** A park lot as returned. */
export interface ParkLot extends ParkLotFields {
  id: Id;
  createdAt: string;
  updatedAt: string;
}

export const listIvrMenus = (signal?: AbortSignal) =>
  list<IvrMenu>("/api/v1/ivr-menus", signal);

export const createIvrMenu = (input: IvrMenuFields) =>
  request<IvrMenu>("POST", "/api/v1/ivr-menus", { body: input });

export const putIvrMenu = (menuId: Id, input: IvrMenuFields) =>
  request<IvrMenu>("PUT", `/api/v1/ivr-menus/${id(menuId)}`, { body: input });

export const deleteIvrMenu = (menuId: Id) =>
  request<void>("DELETE", `/api/v1/ivr-menus/${id(menuId)}`);

export const listParkLots = (signal?: AbortSignal) =>
  list<ParkLot>("/api/v1/park-lots", signal);

export const createParkLot = (input: ParkLotFields) =>
  request<ParkLot>("POST", "/api/v1/park-lots", { body: input });

export const putParkLot = (lotId: Id, input: ParkLotFields) =>
  request<ParkLot>("PUT", `/api/v1/park-lots/${id(lotId)}`, { body: input });

export const deleteParkLot = (lotId: Id) =>
  request<void>("DELETE", `/api/v1/park-lots/${id(lotId)}`);

/** One call parked right now (GET /api/v1/parked-calls). */
export interface ParkedCall {
  slot: string;
  lot: string;
  node: string;
  callId: string;
  correlationId: string;
  /** The extension that parked the call. */
  parkedBy: string;
  /** The parked party's number and name. */
  caller: string;
  callerName: string;
  parkedAt: string;
  /** When the call rings its parker again. */
  returnAt: string;
  phase: "parked" | "returning";
}

export const listParkedCalls = (signal?: AbortSignal) =>
  list<ParkedCall>("/api/v1/parked-calls", signal);

/** The writable fields of a pickup group (PUT replaces the whole group). */
export interface PickupGroupFields {
  name: string;
  /** Extension ids. */
  members: Id[];
}

/** A pickup group as returned. */
export interface PickupGroup {
  id: Id;
  name: string;
  members: PagingExtension[];
  createdAt: string;
  updatedAt: string;
}

export const listPickupGroups = (signal?: AbortSignal) =>
  list<PickupGroup>("/api/v1/pickup-groups", signal);

export const createPickupGroup = (input: PickupGroupFields) =>
  request<PickupGroup>("POST", "/api/v1/pickup-groups", { body: input });

export const putPickupGroup = (groupId: Id, input: PickupGroupFields) =>
  request<PickupGroup>("PUT", `/api/v1/pickup-groups/${id(groupId)}`, {
    body: input,
  });

export const deletePickupGroup = (groupId: Id) =>
  request<void>("DELETE", `/api/v1/pickup-groups/${id(groupId)}`);

// --- feature codes and presence -------------------------------------------------

/** A DTMF feature code (GET/PUT /api/v1/feature-codes). */
export interface FeatureCode {
  /** e.g. "*72" or "##". */
  code: string;
  action: FeatureCodeAction;
  /** What the code carries, e.g. the target number; "" when none. */
  argument: string;
}

export const FEATURE_CODE_PATTERN = /^(\*[0-9#]{2,4}|##)$/;

/**
 * The actions a feature code can perform (migration 00004's action column,
 * widened by 00005 with announcement, 00013 with intercom and 00026 with call waiting).
 */
export const FEATURE_CODE_ACTIONS = [
  "forward_always",
  "forward_busy",
  "forward_no_answer",
  "dnd_on",
  "dnd_off",
  "voicemail",
  "blind_transfer",
  "attended_transfer",
  "announcement",
  "intercom",
  "ivr",
  "call_waiting_on",
  "call_waiting_off",
  "park",
  "park_retrieve",
  "pickup_group",
  "pickup_directed",
] as const;

/** One of FEATURE_CODE_ACTIONS: what dialling a feature code performs. */
export type FeatureCodeAction = (typeof FEATURE_CODE_ACTIONS)[number];

/** A human label for each feature-code action. */
export const FEATURE_CODE_ACTION_LABELS: Record<FeatureCodeAction, string> = {
  forward_always: "Forward always",
  forward_busy: "Forward when busy",
  forward_no_answer: "Forward on no answer",
  dnd_on: "Do not disturb on",
  dnd_off: "Do not disturb off",
  voicemail: "Voicemail",
  blind_transfer: "Blind transfer",
  attended_transfer: "Attended transfer",
  announcement: "Play announcement",
  intercom: "Intercom (auto-answer)",
  ivr: "IVR menu",
  call_waiting_on: "Call waiting on",
  call_waiting_off: "Call waiting off",
  park: "Park the call",
  park_retrieve: "Retrieve a parked call",
  pickup_group: "Pick up a call in your group",
  pickup_directed: "Pick up an extension's call",
};

/** GET /api/v1/feature-codes. */
export const listFeatureCodes = (signal?: AbortSignal) =>
  list<FeatureCode>("/api/v1/feature-codes", signal);

/** PUT /api/v1/feature-codes: the full list, replacing what is stored. */
export const updateFeatureCodes = (codes: readonly FeatureCode[]) =>
  request<void>("PUT", "/api/v1/feature-codes", { body: { items: codes } });

/** livestate DeviceState (GET /api/v1/presence). */
export interface DeviceState {
  device: string;
  extension: string;
  /** idle | ringing | on-call | dnd. */
  state: string;
  updatedAt: string;
}

/** GET /api/v1/presence. */
export const listPresence = (signal?: AbortSignal) =>
  list<DeviceState>("/api/v1/presence", signal);

// --- media: recordings and announcements ---------------------------------------

/** How a recording came to be (migration 00005's initiated_by column). */
export type RecordingOrigin = "dtmf" | "default" | "api";

/** One stored call recording (GET /api/v1/recordings). */
export interface Recording {
  id: Id;
  /** Matches the CDR's correlationId. */
  correlationId: string;
  initiatedBy: RecordingOrigin;
  durationMs: number;
  createdAt: string;
}

/** One page of GET /api/v1/recordings. */
export interface RecordingPage {
  items: Recording[];
  /** Cursor for the next (older) page; empty when there is none. */
  next: string;
}

/**
 * GET /api/v1/recordings?extension=&before=&limit=, newest first; pass the
 * previous page's `next` as `before`, and an extension number to keep only
 * that extension's calls.
 */
export async function listRecordings(
  opts: { extension?: string; before?: string; limit?: number } = {},
  signal?: AbortSignal,
): Promise<RecordingPage> {
  const q = new URLSearchParams();
  if (opts.extension) q.set("extension", opts.extension);
  if (opts.before) q.set("before", opts.before);
  if (opts.limit !== undefined) q.set("limit", String(opts.limit));
  const qs = q.toString();
  const path = `/api/v1/recordings${qs ? `?${qs}` : ""}`;
  const body = await request<unknown>("GET", path, { signal });
  const page = items<Recording>(body, path);
  const next = (body as Record<string, unknown>).next;
  return { items: page, next: typeof next === "string" ? next : "" };
}

/**
 * GET /api/v1/recordings/{id}/audio: the audio, streamed by hello-control.
 * Played by pointing an <audio> element at this path; `isPlayableAudio`
 * gates it, like voicemail.
 */
export const recordingAudioPath = (recordingId: Id) =>
  `/api/v1/recordings/${id(recordingId)}/audio`;

/** DELETE /api/v1/recordings/{id}. */
export const deleteRecording = (recordingId: Id) =>
  request<void>("DELETE", `/api/v1/recordings/${id(recordingId)}`);

/** One named announcement (GET /api/v1/announcements). */
export interface Announcement {
  id: Id;
  /** 1-64 of A-Z a-z 0-9 . _ -; unique; what destinations name. */
  name: string;
  createdAt: string;
  updatedAt?: string;
  /** The authored text-to-speech prompt; "" for an uploaded recording. */
  sourceText?: string;
  voiceId?: string;
  modelId?: string;
  /** When audio was last generated from the text; null/absent when never. */
  generatedAt?: string | null;
  /** Whether the stored audio was generated from text. */
  generated?: boolean;
  /** The text, voice or model changed since the audio was generated. */
  textEdited?: boolean;
}

/** GET /api/v1/announcements. */
export const listAnnouncements = (signal?: AbortSignal) =>
  list<Announcement>("/api/v1/announcements", signal);

/** The largest announcement WAV the server accepts (spec S-5). */
export const MAX_ANNOUNCEMENT_BYTES = 64 * 1024 * 1024;

/**
 * POST /api/v1/announcements: multipart with `name` and `file` (WAV) parts.
 * Returns the created announcement (201).
 */
export function uploadAnnouncement(
  name: string,
  file: File,
): Promise<Announcement> {
  const form = new FormData();
  form.set("name", name);
  form.set("file", file, file.name);
  return request<Announcement>("POST", "/api/v1/announcements", {
    rawBody: form,
  });
}

/** DELETE /api/v1/announcements/{id}. */
export const deleteAnnouncement = (announcementId: Id) =>
  request<void>("DELETE", `/api/v1/announcements/${id(announcementId)}`);

// --- music on hold ------------------------------------------------------------

export type MOHMode = "loop" | "shuffle";

/** One announcement in a class's playlist. */
export interface MOHTrack {
  announcementId: Id;
  name: string;
}

/** The writable fields of a music on hold class (PUT replaces the class). */
export interface MOHClassFields {
  name: string;
  mode: MOHMode;
  /** The global default: what plays when no extension, group or route picks. */
  isDefault: boolean;
  /** Announcement ids in play order; empty plays the built-in tone. */
  announcementIds: Id[];
}

/** A music on hold class as returned. */
export interface MOHClass {
  id: Id;
  name: string;
  mode: MOHMode;
  isDefault: boolean;
  tracks: MOHTrack[];
  createdAt: string;
  updatedAt: string;
}

/** GET /api/v1/moh-classes. */
export const listMOHClasses = (signal?: AbortSignal) =>
  list<MOHClass>("/api/v1/moh-classes", signal);

/** POST /api/v1/moh-classes. */
export const createMOHClass = (input: MOHClassFields) =>
  request<MOHClass>("POST", "/api/v1/moh-classes", { body: input });

/** PUT /api/v1/moh-classes/{id}. */
export const putMOHClass = (classId: Id, input: MOHClassFields) =>
  request<MOHClass>("PUT", `/api/v1/moh-classes/${id(classId)}`, {
    body: input,
  });

/** DELETE /api/v1/moh-classes/{id}. */
export const deleteMOHClass = (classId: Id) =>
  request<void>("DELETE", `/api/v1/moh-classes/${id(classId)}`);

/** A human-readable message for any thrown value. */
export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
