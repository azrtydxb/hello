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

/** GET /api/v1/auth/me */
export interface Me {
  username: string;
}

/** An API token as listed; the token itself is never returned. */
export interface ApiToken {
  id: Id;
  name: string;
  createdAt: string;
  lastUsedAt?: string | null;
}

/** POST /api/v1/tokens response. */
export interface CreatedApiToken extends ApiToken {
  /** Shown once; never returned again. */
  token: string;
}

/** An extension: a dialable number. */
export interface Extension {
  id: Id;
  number: string;
  name: string;
  /** E.164-ish number presented to carriers; "" when none. */
  externalNumber: string;
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
}

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
}

/** One line of a routing trace (routing.Step). */
export interface TraceStep {
  n: number;
  text: string;
}

/** GET /api/v1/cdrs/{id}: the CDR plus its routing trace. */
export interface CdrDetail extends Cdr {
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
}

/** A trunk as returned: the password is never returned, only hasPassword. */
export interface Trunk extends TrunkFields {
  id: Id;
  hasPassword: boolean;
  createdAt: string;
  updatedAt: string;
}

/** POST/PATCH body: `password` is write-only and sent only when changed. */
export type TrunkInput = Partial<TrunkFields> & { password?: string };

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
  destinationKind: "extension" | "external" | "sip_uri";
  destination: string;
  enabled: boolean;
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

async function request<T>(
  method: string,
  path: string,
  { body, signal, redirectOn401 = true }: RequestOptions = {},
): Promise<T> {
  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
    credentials: "same-origin",
    signal,
  });
  if (!res.ok) {
    const err = await errorFrom(res, method, path);
    if (res.status === 401 && redirectOn401) unauthorizedHandler();
    throw err;
  }
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
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

async function list<T>(path: string, signal?: AbortSignal): Promise<T[]> {
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

export const createToken = (name: string) =>
  request<CreatedApiToken>("POST", "/api/v1/tokens", { body: { name } });

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
  patch: { number?: string; name?: string; externalNumber?: string },
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
  const cdr = await request<CdrDetail>("GET", `/api/v1/cdrs/${id(cdrId)}`, {
    signal,
  });
  return { ...cdr, trace: Array.isArray(cdr.trace) ? cdr.trace : [] };
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
export async function testRoute(
  input: RouteTestRequest,
): Promise<RouteTestResult> {
  const res = await request<RouteTestResult>("POST", "/api/v1/routing/test", {
    body: input,
  });
  return {
    decision: res.decision,
    trace: Array.isArray(res.trace) ? res.trace : [],
  };
}

/** A human-readable message for any thrown value. */
export function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}
