// Directory pages (Extensions, Devices, Registrations): the API calls and the
// pure views they derive from the extension, device and live-state lists.
import {
  createDevice as createDeviceRaw,
  listPresence,
  listRegistrations,
  rotateDeviceSecret as rotateDeviceSecretRaw,
  type Binding,
  type Device,
  type DeviceState,
  type DeviceWithSecret,
  type Extension,
  type Id,
} from "../api";

/** Device create / rotate-secret response, with the realm the secret is for. */
export interface IssuedDevice extends DeviceWithSecret {
  /** HELLO_SIP_DOMAIN; absent from servers older than the console redesign. */
  sipDomain?: string;
}

/** POST /api/v1/devices. */
export const createDevice = (input: {
  extensionId: Id;
  sipUsername: string;
  enabled: boolean;
}) => createDeviceRaw(input) as Promise<IssuedDevice>;

/** POST /api/v1/devices/{id}/rotate-secret. */
export const rotateDeviceSecret = (deviceId: Id) =>
  rotateDeviceSecretRaw(deviceId) as Promise<IssuedDevice>;

/**
 * Live state used next to the directory. Each part is undefined when it
 * could not be read (Valkey down answers 503 while management keeps
 * working), so the pages show "—" instead of guessing.
 */
export interface LiveDirectory {
  bindings?: Binding[];
  presence?: DeviceState[];
}

/** Registrations and presence, each allowed to fail on its own. */
export async function loadLiveDirectory(
  signal: AbortSignal,
): Promise<LiveDirectory> {
  const [bindings, presence] = await Promise.allSettled([
    listRegistrations(signal),
    listPresence(signal),
  ]);
  return {
    bindings: bindings.status === "fulfilled" ? bindings.value : undefined,
    presence: presence.status === "fulfilled" ? presence.value : undefined,
  };
}

/** Bindings (contacts) of one device, by SIP username. */
export function bindingsOf(
  bindings: readonly Binding[],
  sipUsername: string,
): Binding[] {
  return bindings.filter((b) => b.device === sipUsername);
}

/** Devices of one extension. */
export function devicesOf(
  devices: readonly Device[],
  ext: Extension,
): Device[] {
  return devices.filter((d) => String(d.extensionId) === String(ext.id));
}

/** "2 · 1 registered", "None", or "2" with "—" registered when live state is unknown. */
export function devicesLabel(
  devices: readonly Device[],
  bindings: readonly Binding[] | undefined,
): string {
  if (devices.length === 0) return "None";
  if (!bindings) return `${devices.length} · — registered`;
  const registered = devices.filter(
    (d) => bindingsOf(bindings, d.sipUsername).length > 0,
  ).length;
  return `${devices.length} · ${registered} registered`;
}

export type PresenceTone = "info" | "warn" | "outline" | "neutral";

/** An extension's presence for the list's badge. */
export interface PresenceView {
  tone: PresenceTone;
  label: string;
}

/**
 * Presence of an extension from its devices' states: on a call beats ringing
 * beats DND; with no registered device it is "Not registered". Unknown when
 * live state could not be read.
 */
export function presenceOf(
  ext: Extension,
  devices: readonly Device[],
  live: LiveDirectory,
): PresenceView | null {
  if (!live.bindings || !live.presence) return null;
  const names = new Set(devices.map((d) => d.sipUsername));
  const states = live.presence
    .filter((p) => names.has(p.device) || p.extension === ext.number)
    .map((p) => p.state);
  if (states.includes("on-call")) return { tone: "info", label: "On call" };
  if (states.includes("ringing")) return { tone: "neutral", label: "Ringing" };
  if (ext.dnd || states.includes("dnd")) return { tone: "warn", label: "DND" };
  const registered = devices.some(
    (d) => bindingsOf(live.bindings ?? [], d.sipUsername).length > 0,
  );
  return registered
    ? { tone: "outline", label: "Idle" }
    : { tone: "outline", label: "Not registered" };
}

/** "Always → 1100 · Busy → 1011 · No answer → +97150…", or "—" when none. */
export function forwardingLabel(ext: Extension): string {
  const parts = [
    ext.forwardAlways && `Always → ${ext.forwardAlways}`,
    ext.forwardBusy && `Busy → ${ext.forwardBusy}`,
    ext.forwardNoAnswer && `No answer → ${ext.forwardNoAnswer}`,
  ].filter(Boolean);
  return parts.length ? parts.join(" · ") : "—";
}

/** Time until `expires` as "53 min", "40 s" or "Expired"; "—" when unreadable. */
export function expiresIn(expires: string, now: number = Date.now()): string {
  const at = new Date(expires).getTime();
  if (Number.isNaN(at)) return "—";
  const s = Math.round((at - now) / 1000);
  if (s <= 0) return "Expired";
  if (s < 60) return `${s} s`;
  return `${Math.floor(s / 60)} min`;
}
