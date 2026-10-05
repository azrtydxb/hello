/** Display text for call-flow data, in the design's voice ("—" for unknown). */
import type { Schedule, Transform } from "../../api";

/** Unknown or empty values. */
export const UNKNOWN = "—";

const DAY_SHORT = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"] as const;

/** [0,1,2,3,4] -> "Sun–Thu"; [1,3] -> "Mon, Wed"; all seven -> "Every day". */
export function formatDays(days: readonly number[]): string {
  const sorted = [...new Set(days)]
    .filter((d) => d >= 0 && d <= 6)
    .sort((a, b) => a - b);
  if (sorted.length === 7) return "Every day";
  const runs: number[][] = [];
  for (const d of sorted) {
    const last = runs[runs.length - 1];
    if (last && last[last.length - 1] === d - 1) last.push(d);
    else runs.push([d]);
  }
  return runs
    .map((r) => {
      const first = DAY_SHORT[r[0] ?? 0];
      const end = DAY_SHORT[r[r.length - 1] ?? 0];
      if (r.length === 1) return first;
      if (r.length === 2) return `${first}, ${end}`;
      return `${first}–${end}`;
    })
    .join(", ");
}

/** null -> "Always"; else "Sun–Thu 08:00–18:00 Asia/Dubai". */
export function formatSchedule(s: Schedule | null | undefined): string {
  if (!s) return "Always";
  const windows = (s.windows ?? [])
    .map((w) => `${formatDays(w.days)} ${w.start}–${w.end}`)
    .join(", ");
  return [windows, s.timeZone].filter(Boolean).join(" ");
}

/** {strip:1,prefix:"+971"} -> "strip 1 · prefix +971"; {} -> "none". */
export function formatTransform(t: Transform | null | undefined): string {
  const parts: string[] = [];
  if (t?.strip) parts.push(`strip ${t.strip}`);
  if (t?.prefix) parts.push(`prefix ${t.prefix}`);
  if (t?.regex) parts.push(`${t.regex} → ${t.template ?? ""}`);
  return parts.length ? parts.join(" · ") : "none";
}

/** Nanoseconds -> "18.4 ms" (one decimal under 10 ms). */
export function formatLatency(ns: number | undefined): string | null {
  if (ns === undefined || !Number.isFinite(ns)) return null;
  const ms = ns / 1e6;
  return `${ms.toFixed(ms < 10 ? 1 : 0)} ms`;
}

/** Minutes from now until an RFC 3339 time; null when unknown or past. */
export function minutesUntil(iso: string | undefined, now = Date.now()) {
  if (!iso) return null;
  const t = Date.parse(iso);
  if (Number.isNaN(t) || t <= now) return null;
  return Math.max(1, Math.round((t - now) / 60000));
}

const REASON: Record<number, string> = {
  400: "Bad Request",
  403: "Forbidden",
  404: "Not Found",
  408: "Request Timeout",
  480: "Temporarily Unavailable",
  484: "Address Incomplete",
  486: "Busy Here",
  487: "Request Terminated",
  488: "Not Acceptable Here",
  500: "Server Internal Error",
  502: "Bad Gateway",
  503: "Service Unavailable",
  504: "Server Time-out",
  603: "Decline",
};

/** 404 -> "404 Not Found"; an unlisted code is shown alone. */
export function sipStatus(code: number): string {
  const reason = REASON[code];
  return reason ? `${code} ${reason}` : String(code);
}

/** "Amira Haddad" -> "Amira Haddad"; "" -> the fallback. */
export const orUnknown = (v: string | null | undefined) =>
  v && v.trim() !== "" ? v : UNKNOWN;
