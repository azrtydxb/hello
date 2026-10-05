// Health derived from the live views, shared by the Platform pages and the
// sidebar's degraded badges. Pure functions: each takes what the API
// answered and counts only what it reports.
import type {
  Binding,
  ClusterMember,
  ClusterStatus,
  Device,
  Trunk,
  TrunkStatus,
} from "../../api";
import type { AuthFailure } from "../../api/platform";

/** A status tone in the design system's terms. */
export type Tone = "good" | "warn" | "bad" | "neutral";

const STATE_TONE: Record<string, Tone> = {
  READY: "good",
  DRAINING: "warn",
  UNHEALTHY: "bad",
  JOINING: "neutral",
  OFFLINE: "neutral",
};

/** The tone of a node's lifecycle state. */
export function memberTone(state: string): Tone {
  return STATE_TONE[state] ?? "neutral";
}

const STATE_LABEL: Record<string, string> = {
  JOINING: "Joining",
  READY: "Ready",
  DRAINING: "Draining",
  UNHEALTHY: "Unhealthy",
  OFFLINE: "Offline",
};

/** "Ready", "Draining", …; an unknown state as the server sent it. */
export function stateLabel(state: string): string {
  return STATE_LABEL[state] ?? state;
}

/**
 * Cluster problems: every node that is not READY, plus Valkey and
 * PostgreSQL when down. The sidebar's Cluster badge shows the count.
 */
export function clusterIssues(c: ClusterStatus): number {
  return (
    c.members.filter((m) => m.state !== "READY").length +
    (c.valkey.up ? 0 : 1) +
    (c.postgres.up ? 0 : 1)
  );
}

/** Trunk destinations reported down; the sidebar's Trunks badge. */
export function trunkIssues(status: readonly TrunkStatus[]): number {
  return status.reduce(
    (n, t) => n + t.destinations.filter((d) => !d.up).length,
    0,
  );
}

/** The revision lag in words; "—" when unknown (PostgreSQL down). */
export function lagText(lag: number | undefined): string {
  if (lag === undefined) return "—";
  if (lag <= 0) return "current";
  return `${lag} behind`;
}

/** Seconds between `iso` and `now`, as "4 s ago" / "3 min ago" / "2 h ago". */
export function ago(iso: string | undefined, now: Date): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "—";
  const s = Math.max(0, Math.round((now.getTime() - t) / 1000));
  if (s < 120) return `${s} s ago`;
  const m = Math.round(s / 60);
  if (m < 120) return `${m} min ago`;
  return `${Math.round(m / 60)} h ago`;
}

/** Time until `iso`, as "in 4 s" / "in 3 min"; "—" when unknown. */
export function until(iso: string | undefined, now: Date): string {
  if (!iso) return "—";
  const t = new Date(iso).getTime();
  if (Number.isNaN(t)) return "—";
  const s = Math.max(0, Math.round((t - now.getTime()) / 1000));
  if (s < 120) return `in ${s} s`;
  const m = Math.round(s / 60);
  if (m < 120) return `in ${m} min`;
  return `in ${Math.round(m / 60)} h`;
}

/** Plural helper: "1 node", "2 nodes". */
export function count(n: number, one: string, many = `${one}s`): string {
  return `${n} ${n === 1 ? one : many}`;
}

/** One health check row of the Diagnostics view. */
export interface HealthCheck {
  id: string;
  tone: "good" | "warn" | "bad" | "info";
  title: string;
  detail: string;
  /** Where to look next: a route or a Diagnostics tab. */
  link?: { label: string; to?: string; tab?: string };
}

/** What the health checks are computed from; undefined = not available. */
export interface HealthInput {
  cluster?: ClusterStatus;
  trunkStatus?: readonly TrunkStatus[];
  devices?: readonly Device[];
  bindings?: readonly Binding[];
  authFailures?: readonly AuthFailure[];
}

const sipMembers = (ms: readonly ClusterMember[]) =>
  ms.filter((m) => m.kind === "sip");

/**
 * The health checks, computed only from the live views Hello reports.
 * A view that could not be read yields no check rather than a guess.
 */
export function healthChecks(input: HealthInput): HealthCheck[] {
  const out: HealthCheck[] = [];
  const { cluster } = input;
  const openCluster = { label: "Open cluster", to: "/cluster" };
  if (cluster) {
    const sip = sipMembers(cluster.members);
    const ready = sip.filter((m) => m.state === "READY");
    const notReady = sip.filter((m) => m.state !== "READY");
    out.push(
      sip.length === 0
        ? {
            id: "sip",
            tone: "bad",
            title: "SIP nodes ready: none reported",
            detail: "No SIP node has reported in; calls cannot be handled.",
            link: openCluster,
          }
        : notReady.length === 0
          ? {
              id: "sip",
              tone: "good",
              title: `SIP nodes ready: ${ready.length} of ${sip.length}`,
              detail:
                sip.length === 1
                  ? "The SIP node is READY."
                  : `All ${sip.length} SIP nodes are READY.`,
            }
          : {
              id: "sip",
              tone: ready.length === 0 ? "bad" : "warn",
              title: `SIP nodes ready: ${ready.length} of ${sip.length}`,
              detail: notReady
                .map(
                  (m) =>
                    `${m.id} is ${m.state}${m.reason ? ` (${m.reason})` : ""}.`,
                )
                .join(" "),
              link: openCluster,
            },
    );
    const control = cluster.members.filter((m) => m.kind !== "sip");
    const notReadyControl = control.filter((m) => m.state !== "READY");
    if (control.length > 0) {
      out.push(
        notReadyControl.length === 0
          ? {
              id: "control",
              tone: "good",
              title: `Control nodes ready: ${control.length} of ${control.length}`,
              detail: "hello-control answers on every node.",
            }
          : {
              id: "control",
              tone: "warn",
              title: `Control nodes ready: ${control.length - notReadyControl.length} of ${control.length}`,
              detail: notReadyControl
                .map((m) => `${m.id} is ${m.state}.`)
                .join(" "),
              link: openCluster,
            },
      );
    }
    if (cluster.configRevision !== null) {
      const behind = cluster.members.filter(
        (m) => m.revisionLag !== undefined && m.revisionLag > 0,
      );
      out.push(
        behind.length === 0
          ? {
              id: "revision",
              tone: "good",
              title: "Configuration revision consistent",
              detail: `${cluster.members.length === 1 ? "The node is" : `All ${cluster.members.length} nodes are`} on revision ${cluster.configRevision}.`,
            }
          : {
              id: "revision",
              tone: "warn",
              title: "Configuration revision drift",
              detail: behind
                .map(
                  (m) =>
                    `${m.id} is ${count(m.revisionLag ?? 0, "revision")} behind (${m.configRevision} of ${cluster.configRevision}).`,
                )
                .join(" "),
              link: openCluster,
            },
      );
    }
    out.push(
      cluster.valkey.up
        ? {
            id: "valkey",
            tone: "good",
            title: "Valkey reachable",
            detail:
              cluster.valkey.mode === "sentinel"
                ? `Sentinel mode${cluster.valkey.primary ? `; primary ${cluster.valkey.primary}` : ""}.`
                : "Single node.",
          }
        : {
            id: "valkey",
            tone: "bad",
            title: "Valkey unreachable",
            detail: `Live state (registrations, calls, trunk health) cannot be read${cluster.valkey.error ? `: ${cluster.valkey.error}` : ""}.`,
            link: openCluster,
          },
      cluster.postgres.up
        ? {
            id: "postgres",
            tone: "good",
            title: "PostgreSQL reachable",
            detail: `Configuration revision ${cluster.configRevision ?? "—"}.`,
          }
        : {
            id: "postgres",
            tone: "bad",
            title: "PostgreSQL unreachable",
            detail: `Configuration and call records cannot be read${cluster.postgres.error ? `: ${cluster.postgres.error}` : ""}.`,
            link: openCluster,
          },
    );
  }
  if (input.trunkStatus) {
    const dests = input.trunkStatus.flatMap((t) =>
      t.destinations.map((d) => ({ trunk: t.name, ...d })),
    );
    const down = dests.filter((d) => !d.up);
    if (dests.length > 0) {
      out.push(
        down.length === 0
          ? {
              id: "trunks",
              tone: "good",
              title: `Trunk destinations: ${dests.length} of ${dests.length} up`,
              detail: "Every destination answers OPTIONS.",
            }
          : {
              id: "trunks",
              tone: down.length === dests.length ? "bad" : "warn",
              title: `Trunk destinations: ${dests.length - down.length} of ${dests.length} up`,
              detail: down
                .map(
                  (d) =>
                    `${d.destination} (${d.trunk}) is down${d.lastCode ? ` (${d.lastCode})` : ""}.`,
                )
                .join(" "),
              link: { label: "Trunk probes", tab: "probes" },
            },
      );
    }
  }
  if (input.devices && input.bindings) {
    const enabled = input.devices.filter((d) => d.enabled);
    const registered = new Set(input.bindings.map((b) => b.device));
    const missing = enabled.filter((d) => !registered.has(d.sipUsername));
    if (enabled.length > 0) {
      out.push({
        id: "registrations",
        tone: missing.length === 0 ? "good" : "info",
        title: `Registrations: ${enabled.length - missing.length} of ${count(enabled.length, "enabled device")}`,
        detail:
          missing.length === 0
            ? "Every enabled device has a contact."
            : `Not registered: ${missing.map((d) => d.sipUsername).join(", ")}.`,
        link: missing.length > 0 ? { label: "Inspect", tab: "reg" } : undefined,
      });
    }
  }
  if (input.authFailures) {
    const blocked = input.authFailures.filter((f) => f.blocked);
    out.push(
      blocked.length === 0
        ? {
            id: "throttle",
            tone: "good",
            title: "Failed-auth throttling: none blocked",
            detail: "No source has reached the failed-authentication limit.",
          }
        : {
            id: "throttle",
            tone: "warn",
            title: `Failed-auth throttling: ${count(blocked.length, "source")} blocked`,
            detail: `${blocked.map((b) => b.ip).join(", ")} reached the failed-authentication limit.`,
            link: { label: "Review", tab: "reg" },
          },
    );
  }
  return out;
}

/** A destination's trunk configuration (priority and weight), if it matches. */
export function destinationConfig(
  trunk: Trunk | undefined,
  destination: string,
): { priority: number; weight: number } | undefined {
  return trunk?.destinations.find(
    (d) =>
      `${d.host}:${d.port || 5060}` === destination || d.host === destination,
  );
}
