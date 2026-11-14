// Client for the Platform pages' diagnostics endpoints (Cluster, Diagnostics,
// System). The shared request helpers and types live in ../api.
import { request, type Binding, type Id } from "../api";

/** One REGISTER and the final response a SIP node sent to it. */
export interface RegisterAttempt {
  at: string;
  device: string;
  /** ip:port the REGISTER came from. */
  source: string;
  /** The client IP the failed-auth throttle counts. */
  ip: string;
  node: string;
  userAgent?: string;
  /** The request carried an Authorization header (a challenge answer). */
  credentials: boolean;
  code: number;
  reason: string;
  /** A 401 whose challenge carried stale=true. */
  stale?: boolean;
}

/** A source IP's failed-auth counter in its current window. */
export interface AuthFailure {
  ip: string;
  failures: number;
  windowEndsAt: string;
  /** At or above the limit: hello-sip answers this IP 403. */
  blocked: boolean;
}

/** Why a device is not registered, from what Hello measured. */
export interface RegisterVerdict {
  code: string;
  message: string;
}

/** GET /api/v1/diagnostics/devices/{id} */
export interface DeviceDiagnostics {
  deviceId: Id;
  device: string;
  extensionId: Id;
  enabled: boolean;
  aor: string;
  registered: boolean;
  bindings: Binding[];
  /** Newest first. */
  attempts: RegisterAttempt[];
  attemptsRetentionSeconds: number;
  source: AuthFailure | null;
  authFailLimit: number;
  verdict: RegisterVerdict | null;
}

export const getDeviceDiagnostics = (deviceId: Id, signal?: AbortSignal) =>
  request<DeviceDiagnostics>(
    "GET",
    `/api/v1/diagnostics/devices/${encodeURIComponent(String(deviceId))}`,
    { signal },
  );

/** GET /api/v1/diagnostics/auth-failures */
export interface AuthFailureList {
  items: AuthFailure[];
  limit: number;
}

export const listAuthFailures = (signal?: AbortSignal) =>
  request<AuthFailureList>("GET", "/api/v1/diagnostics/auth-failures", {
    signal,
  });

/** DELETE /api/v1/diagnostics/auth-failures/{ip}: unblock a source. */
export const clearAuthFailures = (ip: string) =>
  request<void>(
    "DELETE",
    `/api/v1/diagnostics/auth-failures/${encodeURIComponent(ip)}`,
  );

/** GET /api/v1/webrtc/status: web and mobile clients and their WebRTC media. */
export interface WebRTCCounters {
  /** Legs that reached an ICE state: connected, restarted, failed. */
  ice: Record<string, number>;
  dtlsHandshakes: number;
  /** Failed DTLS handshakes by reason: timeout, fingerprint, failed. */
  dtlsFailures: Record<string, number>;
  /** Packets that failed SRTP authentication, by side. */
  srtpAuthFailures: Record<string, number>;
}

export interface WebRTCLeg {
  /** The caller leg's SIP Call-ID. */
  call: string;
  side: "caller" | "callee";
  /** An alternate callee leg offered to a browser that has not answered. */
  ringing: boolean;
  ice: "checking" | "connected" | "failed";
  dtls: "new" | "connecting" | "connected" | "failed";
  dtlsReason?: "timeout" | "fingerprint" | "failed";
  dtlsMs?: number;
  iceRestarts: number;
  /** The address ICE selected (ip:port). */
  remote?: string;
  video: boolean;
  srtpAuthFailures: number;
  since: string;
  node: string;
  from?: string;
  to?: string;
  /** The selected address is a TURN server's. */
  turn: boolean;
}

export interface WebRTCNode {
  node: string;
  port: number;
  hosts: string[];
  legs: number;
  counters: WebRTCCounters;
  updatedAt: string;
}

export interface TurnProbe {
  url: string;
  transport: string;
  /** stun: a Binding request was answered; connect: a TCP connection. */
  method: string;
  reachable: boolean;
  latencyMs: number;
  error?: string;
  addrs: string[];
  checkedAt: string;
}

export interface ClientCandidateStats {
  since: string;
  reports: number;
  local: Record<string, number>;
  remote: Record<string, number>;
  relayed: number;
}

export interface ClientRegistration {
  deviceId: Id;
  device: string;
  kind: string;
  label: string;
  extension: string;
  transport: string;
  source: string;
  userAgent: string;
  node: string;
  expires: string;
}

export interface ClientAuthFailure {
  deviceId: Id;
  device: string;
  label: string;
  kind: string;
  at: string;
  code: number;
  reason: string;
  source: string;
  node: string;
  userAgent?: string;
  credentials: boolean;
}

export interface ClientError {
  at: string;
  kind: "register" | "ice" | "dtls";
  reason: string;
  detail: string;
  node?: string;
  call?: string;
  device?: string;
}

export interface WebRTCStatus {
  generatedAt: string;
  clients: {
    enrolled: Record<string, number>;
    registered: Record<string, number>;
    byTransport: Record<string, number>;
  };
  registrations: ClientRegistration[];
  nodes: WebRTCNode[];
  legs: WebRTCLeg[];
  turn: {
    configured: boolean;
    ttlSeconds: number;
    stun: string[];
    servers: TurnProbe[];
    relayedLegs: number;
  };
  push: { configured: boolean; detail: string };
  candidates: ClientCandidateStats | null;
  authFailures: ClientAuthFailure[];
  lastErrors: ClientError[];
  /** Sources that could not be read; their parts are empty. */
  unavailable: string[];
}

export const getWebRTCStatus = (signal?: AbortSignal) =>
  request<WebRTCStatus>("GET", "/api/v1/webrtc/status", { signal });
