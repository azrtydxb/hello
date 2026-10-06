import { useCallback, useMemo, useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import {
  fetchVersion,
  getCluster,
  listCalls,
  listDevices,
  listRegistrations,
  listTrunks,
  listTrunkStatus,
  type ActiveCall,
  type Binding,
  type ClusterStatus,
  type Device,
  type DestinationHealth,
  type Trunk,
  type TrunkStatus,
} from "../api";
import {
  getCallRecord,
  getCdrConcurrency,
  listCdrPage,
  type CallRecord,
  type CdrConcurrency,
  type ConcurrencyRange,
} from "../api/calls";
import {
  Alert,
  Badge,
  type BadgeTone,
  EmptyState,
  LineChart,
  LinkButton,
  LiveTag,
  Meter,
  PageHeader,
  SegmentedControl,
  Spinner,
  StatCard,
  Table,
  type TableColumn,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import {
  callDuration,
  callState,
  clock,
  Party,
  useExtensionNames,
  useNow,
} from "./calls/common";

const SLOW_REFRESH_MS = 30_000;
const CHART_REFRESH_MS = 60_000;

/** The data of a polled source, or undefined until it first loads. */
function dataOf<T>(state: { status: string; data?: T }): T | undefined {
  return state.status === "loading" ? undefined : state.data;
}

const plural = (n: number, one: string, many: string) =>
  `${n} ${n === 1 ? one : many}`;

/** "sbc1" for sbc1.carrier.lab:5060; an IP address stays whole. */
export function shortHost(destination: string): string {
  const host = destination.replace(/:\d+$/, "");
  return /^[\d.]+$/.test(host) || host.includes(":")
    ? host
    : (host.split(".")[0] ?? host);
}

/** A trunk's state badge: registration, destination health, or disabled. */
export function trunkState(
  trunk: Trunk,
  status: TrunkStatus | undefined,
): { label: string; tone: BadgeTone } {
  if (!trunk.enabled) return { label: "Disabled", tone: "outline" };
  if (!status) return { label: "Status unknown", tone: "outline" };
  const total = status.destinations.length;
  const down = status.destinations.filter((d) => !d.up).length;
  if (trunk.mode === "registration") {
    const reg = status.registration?.state;
    if (reg !== "registered") {
      return {
        label: reg ? reg[0]!.toUpperCase() + reg.slice(1) : "Not registered",
        tone: "bad",
      };
    }
    return down
      ? { label: "Degraded", tone: "warn" }
      : { label: "Registered", tone: "good" };
  }
  if (total > 0 && down === total) return { label: "Down", tone: "bad" };
  if (down) return { label: "Degraded", tone: "warn" };
  return { label: "Up", tone: "good" };
}

function destinationLabel(d: DestinationHealth): string {
  if (!d.up) return d.lastCode ? `down · ${d.lastCode}` : "down";
  return d.latencyNs ? `${(d.latencyNs / 1e6).toFixed(1)} ms` : "—";
}

/** Recent failures: the newest failed calls, without callers who gave up (487). */
async function loadFailures(signal: AbortSignal): Promise<CallRecord[]> {
  const page = await listCdrPage({ failed: true, limit: 10 }, signal);
  const picked = page.items.filter((c) => c.finalStatus !== 487).slice(0, 3);
  return Promise.all(picked.map((c) => getCallRecord(c.id, signal)));
}

/** The overview: health alerts, headline numbers, trunks, live calls and recent failures. */
export function Dashboard() {
  const navigate = useNavigate();
  const version = usePolling(fetchVersion, SLOW_REFRESH_MS);
  const callsState = usePolling(listCalls, LIVE_REFRESH_MS);
  const cluster = usePolling(getCluster, LIVE_REFRESH_MS);
  const trunkStatus = usePolling(listTrunkStatus, LIVE_REFRESH_MS);
  const registrations = usePolling(listRegistrations, LIVE_REFRESH_MS);
  const trunks = usePolling(listTrunks, SLOW_REFRESH_MS);
  const devices = usePolling(listDevices, SLOW_REFRESH_MS);
  const failures = usePolling(loadFailures, SLOW_REFRESH_MS);
  const names = useExtensionNames();
  const now = useNow();
  const [range, setRange] = useState<ConcurrencyRange>("6h");
  const loadConcurrency = useCallback(
    (signal: AbortSignal) => getCdrConcurrency(range, signal),
    [range],
  );
  const concurrency = usePolling(loadConcurrency, CHART_REFRESH_MS);

  const info = dataOf(version);
  const calls = dataOf(callsState);
  const statuses = dataOf(trunkStatus);
  const series = dataOf(concurrency);
  const trend = useMemo(
    () => series?.points.map((p) => p.inbound + p.outbound + p.internal),
    [series],
  );

  const callColumns: TableColumn<ActiveCall>[] = [
    {
      key: "from",
      label: "From",
      render: (c) => (
        <>
          <Link to="/calls" className="calls-row-link">
            <span className="visually-hidden">Active call from </span>
            {c.from}
          </Link>
          {names.get(c.from) && <small>{names.get(c.from)}</small>}
        </>
      ),
    },
    {
      key: "to",
      label: "To",
      render: (c) => <Party number={c.to} names={names} />,
    },
    {
      key: "state",
      label: "State",
      render: (c) => {
        const s = callState(c.state);
        return (
          <Badge tone={s.tone} dot>
            {s.label}
          </Badge>
        );
      },
    },
    {
      key: "duration",
      label: "Duration",
      align: "right",
      mono: true,
      render: (c) => callDuration(c, now),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Overview"
        title="Dashboard"
        description={
          info ? (
            <>
              hello-control {/^\d/.test(info.version) ? "v" : ""}
              {info.version} · commit{" "}
              <span className="calls-mono">{info.commit}</span> · configuration
              revision {info.configRevision}
            </>
          ) : version.status === "error" ? (
            "Could not reach the control plane."
          ) : (
            "hello-control —"
          )
        }
        actions={
          <>
            <LinkButton to="/history" icon="history">
              Call history
            </LinkButton>
            <LinkButton to="/extensions?new=1" variant="primary" icon="plus">
              New extension
            </LinkButton>
          </>
        }
      />
      <HealthAlerts
        cluster={dataOf(cluster)}
        trunks={dataOf(trunks)}
        statuses={statuses}
      />
      <div className="calls-stats">
        <StatCard
          label="Active calls"
          value={calls ? String(calls.length) : "—"}
          icon="phone-call"
          sub={calls ? callsSummary(calls) : unavailable(callsState)}
          trend={trend && trend.length > 1 ? trend : undefined}
        />
        <DevicesCard
          bindings={dataOf(registrations)}
          devices={dataOf(devices)}
        />
        <DestinationsCard trunks={dataOf(trunks)} statuses={statuses} />
        <NodesCard cluster={dataOf(cluster)} />
      </div>

      <div className="calls-grid">
        <div className="az-card calls-card">
          <div className="calls-card__head">
            <div>
              <h2 className="calls-card__title">Concurrent calls</h2>
              <p className="calls-muted">{peakLine(series)}</p>
            </div>
            <SegmentedControl
              aria-label="Time range"
              mono
              options={[
                { value: "1h", label: "1h" },
                { value: "6h", label: "6h" },
                { value: "24h", label: "24h" },
              ]}
              value={range}
              onChange={setRange}
            />
          </div>
          <ConcurrencyChart state={concurrency} range={range} />
          <div className="calls-legend" aria-hidden="true">
            <span className="calls-legend__item">
              <span className="calls-legend__line calls-legend__line--outbound" />
              Outbound
            </span>
            <span className="calls-legend__item">
              <span className="calls-legend__line calls-legend__line--inbound" />
              Inbound
            </span>
            <span className="calls-legend__item">
              <span className="calls-legend__line calls-legend__line--internal" />
              Internal
            </span>
          </div>
        </div>
        <TrunksCard
          trunks={dataOf(trunks)}
          statuses={statuses}
          error={trunks.status === "error" ? trunks.message : undefined}
        />
      </div>

      <div className="calls-grid">
        <div className="calls-col">
          <div className="calls-card__head">
            <h2 className="calls-section-title">
              Active calls
              <Badge tone="neutral">{calls ? calls.length : "—"}</Badge>
            </h2>
            <LiveTag every="5 s" live={callsState.status === "ready"} />
          </div>
          {callsState.status === "loading" && (
            <Spinner label="Loading active calls" />
          )}
          {callsState.status === "error" && (
            <Alert tone="bad" title="Could not load active calls">
              {callsState.message}
            </Alert>
          )}
          {calls && calls.length === 0 && (
            <EmptyState icon="phone-off" title="No calls in progress" />
          )}
          {calls && calls.length > 0 && (
            <Table
              caption="Active calls"
              columns={callColumns}
              rows={calls}
              rowKey={(c) => c.id}
              onRowClick={() => void navigate("/calls")}
            />
          )}
        </div>
        <div className="calls-col">
          <div className="calls-card__head">
            <h2 className="calls-section-title">Recent failures</h2>
            <LinkButton
              to="/history?tab=failed"
              variant="ghost"
              size="sm"
              iconRight="arrow-right"
            >
              All failed calls
            </LinkButton>
          </div>
          <RecentFailures state={failures} />
        </div>
      </div>
    </section>
  );
}

function unavailable(state: { status: string }): string {
  return state.status === "error" ? "Unavailable" : "Loading…";
}

function callsSummary(calls: readonly ActiveCall[]): string {
  const by = (label: string) =>
    calls.filter((c) => callState(c.state).label === label).length;
  const parts = [`${by("Answered")} answered`, `${by("Ringing")} ringing`];
  const held = by("On hold");
  if (held) parts.push(`${held} on hold`);
  return parts.join(" · ");
}

function DevicesCard({
  bindings,
  devices,
}: {
  bindings?: Binding[];
  devices?: Device[];
}) {
  const registered = bindings ? new Set(bindings.map((b) => b.device)) : null;
  let sub = "Loading…";
  if (registered && devices) {
    const missing = devices.filter(
      (d) => d.enabled && !registered.has(d.sipUsername),
    ).length;
    sub = missing
      ? `${plural(missing, "device has", "devices have")} no contact`
      : "Every enabled device has a contact";
  }
  return (
    <StatCard
      label="Registered devices"
      value={registered ? String(registered.size) : "—"}
      unit={devices ? `/ ${devices.length}` : undefined}
      icon="smartphone"
      sub={sub}
    />
  );
}

function DestinationsCard({
  trunks,
  statuses,
}: {
  trunks?: Trunk[];
  statuses?: TrunkStatus[];
}) {
  const dests = statuses?.flatMap((s) => s.destinations) ?? [];
  const down = dests.filter((d) => !d.up);
  let sub = "Loading…";
  if (statuses) {
    if (down.length) {
      sub = `${down.map((d) => shortHost(d.destination)).join(", ")} down`;
    } else if (trunks) {
      const active = statuses.reduce((n, s) => n + s.activeCalls, 0);
      const limited = trunks.every((t) => t.maxCalls > 0);
      const max = trunks.reduce((n, t) => n + t.maxCalls, 0);
      sub =
        limited && max > 0
          ? `${active} of ${max} trunk channels in use`
          : `${plural(active, "trunk call", "trunk calls")} in progress`;
    } else {
      sub = "—";
    }
  }
  return (
    <StatCard
      label="Trunk destinations up"
      value={statuses ? String(dests.length - down.length) : "—"}
      unit={statuses ? `/ ${dests.length}` : undefined}
      icon="cable"
      sub={sub}
    />
  );
}

function NodesCard({ cluster }: { cluster?: ClusterStatus }) {
  const sip = cluster?.members.filter((m) => m.kind === "sip") ?? [];
  const ready = sip.filter((m) => m.state === "READY");
  const notReady = sip.filter((m) => m.state !== "READY");
  const control = cluster?.members.filter(
    (m) => m.kind === "control" && m.state === "READY",
  );
  let sub = "Loading…";
  if (cluster) {
    sub = notReady.length
      ? notReady.map((m) => `${m.id} ${m.state.toLowerCase()}`).join(", ")
      : `${plural(control?.length ?? 0, "control node", "control nodes")} ready`;
  }
  return (
    <StatCard
      label="SIP nodes ready"
      value={cluster ? String(ready.length) : "—"}
      unit={cluster ? `/ ${sip.length}` : undefined}
      icon="server"
      sub={sub}
    />
  );
}

/** Alerts for what needs attention now: unhealthy SIP nodes, Valkey, down trunk destinations. */
export function HealthAlerts({
  cluster,
  trunks,
  statuses,
}: {
  cluster?: ClusterStatus;
  trunks?: Trunk[];
  statuses?: TrunkStatus[];
}) {
  const alerts: ReactNode[] = [];
  const openCluster = (
    <LinkButton to="/cluster" size="sm">
      Open cluster
    </LinkButton>
  );
  const openTrunks = (
    <LinkButton to="/trunks" size="sm">
      Open trunks
    </LinkButton>
  );
  if (cluster && !cluster.valkey.up) {
    alerts.push(
      <Alert
        key="valkey"
        tone="bad"
        title="Live state (Valkey) is unavailable"
        action={openCluster}
      >
        {cluster.valkey.error
          ? `Valkey: ${cluster.valkey.error}.`
          : "Registrations, active calls and trunk state cannot be read."}
      </Alert>,
    );
  }
  for (const m of cluster?.members ?? []) {
    if (m.kind !== "sip" || (m.state !== "UNHEALTHY" && m.state !== "OFFLINE"))
      continue;
    alerts.push(
      <Alert
        key={`node-${m.id}`}
        tone="bad"
        title={`${m.id} is ${m.state}`}
        action={openCluster}
      >
        {m.reason || undefined}
      </Alert>,
    );
  }
  const names = new Map((trunks ?? []).map((t) => [String(t.id), t]));
  for (const s of statuses ?? []) {
    const trunk = names.get(String(s.trunkId));
    if (trunk && !trunk.enabled) continue;
    for (const d of s.destinations) {
      if (d.up) continue;
      alerts.push(
        <Alert
          key={`dest-${s.trunkId}-${d.destination}`}
          tone="warn"
          title={`${s.name} · ${shortHost(d.destination)} is down`}
          action={openTrunks}
        >
          OPTIONS to {d.destination} failed
          {d.lastCode ? ` (${d.lastCode})` : ""}.
        </Alert>,
      );
    }
  }
  if (alerts.length === 0) return null;
  return <div className="calls-alerts">{alerts}</div>;
}

function peakLine(series: CdrConcurrency | undefined): string {
  if (!series) return "From call records";
  if (!series.peak)
    return "No recorded calls in this window · from call records";
  return `Peak ${series.peak.calls} at ${clock(series.peak.at, false)} · from call records`;
}

/** About five evenly spaced HH:MM labels for the chart's x axis. */
export function axisLabels(points: readonly { at: string }[]): string[] {
  if (points.length < 2) return points.map((p) => clock(p.at, false));
  const n = Math.min(5, points.length);
  return Array.from({ length: n }, (_, i) =>
    clock(points[Math.round((i * (points.length - 1)) / (n - 1))]!.at, false),
  );
}

function ConcurrencyChart({
  state,
  range,
}: {
  state: { status: string; data?: CdrConcurrency; message?: string };
  range: ConcurrencyRange;
}) {
  const data = dataOf(state);
  if (!data) {
    return state.status === "error" ? (
      <Alert tone="bad" title="Could not load call records">
        {state.message}
      </Alert>
    ) : (
      <Spinner label="Loading concurrent calls" />
    );
  }
  const pick = (k: "outbound" | "inbound" | "internal") =>
    data.points.map((p) => p[k]);
  return (
    <LineChart
      height={170}
      label={`Concurrent calls over the last ${range}, from call records. ${peakLine(data)}.`}
      labels={axisLabels(data.points)}
      series={[
        {
          name: "Outbound",
          data: pick("outbound"),
          color: "var(--az-brand-mint)",
        },
        {
          name: "Inbound",
          data: pick("inbound"),
          color: "var(--az-brand-sky)",
        },
        {
          name: "Internal",
          data: pick("internal"),
          color: "var(--az-sky-light)",
        },
      ]}
    />
  );
}

function TrunksCard({
  trunks,
  statuses,
  error,
}: {
  trunks?: Trunk[];
  statuses?: TrunkStatus[];
  error?: string;
}) {
  const byId = new Map((statuses ?? []).map((s) => [String(s.trunkId), s]));
  return (
    <div className="az-card calls-card">
      <div className="calls-card__head">
        <h2 className="calls-card__title">Trunks</h2>
        <LinkButton
          to="/trunks"
          variant="ghost"
          size="sm"
          iconRight="arrow-right"
        >
          Open trunks
        </LinkButton>
      </div>
      {!trunks && !error && <Spinner label="Loading trunks" />}
      {error && (
        <Alert tone="bad" title="Could not load trunks">
          {error}
        </Alert>
      )}
      {trunks && trunks.length === 0 && (
        <EmptyState
          icon="cable"
          title="No trunks"
          description="Outbound and inbound PSTN calls need a trunk."
        />
      )}
      {trunks?.map((t) => {
        const st = byId.get(String(t.id));
        const state = trunkState(t, st);
        const active = st?.activeCalls;
        return (
          <div key={t.id} className="calls-trunk">
            <div className="calls-trunk__head">
              <span className="calls-trunk__name">{t.name}</span>
              <Badge tone="outline">
                {t.mode === "registration" ? "Registration" : "IP peer"}
              </Badge>
              <Badge tone={state.tone} dot className="calls-trunk__state">
                {state.label}
              </Badge>
            </div>
            <Meter
              label="Calls"
              tone="auto"
              value={active ?? 0}
              max={t.maxCalls > 0 ? t.maxCalls : Math.max(1, active ?? 0)}
              valueLabel={
                active === undefined
                  ? "—"
                  : t.maxCalls > 0
                    ? `${active} of ${t.maxCalls}`
                    : `${active} · no limit`
              }
            />
            {st && st.destinations.length > 0 && (
              <ul
                className="calls-trunk__dests"
                aria-label={`${t.name} destinations`}
              >
                {st.destinations.map((d) => (
                  <li key={d.destination} className="calls-trunk__dest">
                    <span
                      className={`az-dot calls-dot--${d.up ? "good" : "bad"}`}
                    />
                    <span className="calls-mono">
                      {shortHost(d.destination)}
                    </span>
                    {destinationLabel(d)}
                  </li>
                ))}
              </ul>
            )}
          </div>
        );
      })}
    </div>
  );
}

function RecentFailures({
  state,
}: {
  state: { status: string; data?: CallRecord[]; message?: string };
}) {
  const rows = dataOf(state);
  if (!rows) {
    return state.status === "error" ? (
      <Alert tone="bad" title="Could not load failed calls">
        {state.message}
      </Alert>
    ) : (
      <Spinner label="Loading failed calls" />
    );
  }
  if (rows.length === 0) {
    return (
      <div className="az-card">
        <EmptyState icon="circle-check" title="No failed calls" />
      </div>
    );
  }
  return (
    <ul className="az-card calls-failures" aria-label="Recent failures">
      {rows.map((c) => (
        <li key={c.id}>
          <Link
            to={`/history/${encodeURIComponent(String(c.id))}`}
            className="calls-failure"
          >
            <Badge tone="bad" className="calls-mono">
              {c.finalStatus}
            </Badge>
            <span className="calls-failure__body">
              <span className="calls-failure__call">
                <span className="calls-mono">{c.source}</span> →{" "}
                <span className="calls-mono">
                  {c.originalDestination || c.destination}
                </span>
              </span>
              <span className="calls-failure__reason">
                {c.explanation || c.failureReason || "—"}
              </span>
            </span>
            <span className="calls-failure__time">
              {clock(c.startTime, false)}
            </span>
          </Link>
        </li>
      ))}
    </ul>
  );
}
