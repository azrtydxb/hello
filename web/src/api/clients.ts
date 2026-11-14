// Client for the browser softphone's devices (/api/v1/me/devices): the web
// devices of the signed-in user, their one-time credentials, and the ICE
// servers a call uses.
import { request, type Id } from "../api";

/** One of the user's web devices. */
export interface MyDevice {
  id: Id;
  kind: "web" | "mobile";
  label: string;
  platform: string;
  appVersion: string;
  extensionId: Id;
  extensionNumber: string;
  /** The stable name (w<id>), not the expiring one a client registers with. */
  sipUsername: string;
  lastSeenAt: string | null;
  createdAt: string;
  /** A live registration now; null when the live state is unavailable. */
  registered: boolean | null;
}

/** What a client needs to REGISTER; the password is shown once. */
export interface DeviceCredentials {
  deviceId: Id;
  sipUsername: string;
  sipPassword: string;
  realm: string;
  wssUrl: string;
  expiresAt: string;
}

export interface IceServer {
  urls: string[];
  username?: string;
  credential?: string;
}

export interface IceServers {
  iceServers: IceServer[];
  expiresAt: string;
  ttlSeconds: number;
}

export interface Enrolment extends DeviceCredentials {
  device: MyDevice;
  ice: IceServers;
}

const seg = (value: Id) => encodeURIComponent(String(value));

export async function listMyDevices(signal?: AbortSignal): Promise<MyDevice[]> {
  const r = await request<{ items: MyDevice[] }>("GET", "/api/v1/me/devices", {
    signal,
  });
  return r.items;
}

/** Enrol a web device on the caller's own extension (the first when none is named). */
export function createMyDevice(input: {
  label: string;
  platform?: string;
  extensionId?: Id;
}): Promise<Enrolment> {
  return request<Enrolment>("POST", "/api/v1/me/devices", { body: input });
}

export function refreshMyDevice(id: Id): Promise<DeviceCredentials> {
  return request<DeviceCredentials>(
    "POST",
    `/api/v1/me/devices/${seg(id)}/credentials`,
  );
}

export function revokeMyDevice(id: Id): Promise<void> {
  return request<void>("DELETE", `/api/v1/me/devices/${seg(id)}`);
}

export function getIceServers(
  deviceId?: Id,
  signal?: AbortSignal,
): Promise<IceServers> {
  const q = deviceId === undefined ? "" : `?deviceId=${seg(deviceId)}`;
  return request<IceServers>("GET", `/api/v1/me/ice-servers${q}`, { signal });
}

/** The end-of-call quality summary a client posts (S-20); never media. */
export interface CallQualityReport {
  codec: string;
  lossPct: number;
  jitterMs: number;
  rttMs: number;
  candidateLocal: string;
  candidateRemote: string;
  video?: { width: number; height: number; fps: number; codec: string };
  durationMs: number;
  /** The web device that took the call. */
  deviceId?: Id;
}

/** POST /api/v1/me/calls/{callId}/quality; callId is the SIP Call-ID the browser saw. */
export function postCallQuality(
  callId: string,
  report: CallQualityReport,
): Promise<void> {
  return request<void>(
    "POST",
    `/api/v1/me/calls/${encodeURIComponent(callId)}/quality`,
    { body: report },
  );
}
