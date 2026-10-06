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
