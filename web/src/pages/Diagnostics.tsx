import { useCallback, useEffect, useMemo, useState } from "react";
import { useSearchParams } from "react-router";
import {
  errorMessage,
  getCdr,
  getCluster,
  listCdrs,
  listDevices,
  listExtensions,
  listRegistrations,
  listTrunks,
  listTrunkStatus,
  type Binding,
  type Cdr,
  type CdrDetail,
  type ClusterStatus,
  type Device,
  type Extension,
  type Trunk,
  type TrunkStatus,
} from "../api";
import {
  clearAuthFailures,
  getDeviceDiagnostics,
  listAuthFailures,
  type AuthFailure,
  type AuthFailureList,
  type DeviceDiagnostics,
  type RegisterAttempt,
} from "../api/platform";
import {
  Alert,
  Badge,
  type BadgeTone,
  Button,
  EmptyState,
  Icon,
  Input,
  LinkButton,
  PageHeader,
  type Property,
  PropertyList,
  Select,
  Spinner,
  Table,
  Tabs,
  useToast,
} from "../design/azrty/components";
import { sortedSteps } from "../format";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import {
  ago,
  destinationConfig,
  healthChecks,
  until,
  type HealthCheck,
} from "./platform/health";
import "./platform/platform.css";

type TabId = "trace" | "reg" | "probes" | "health";
const TABS: readonly TabId[] = ["trace", "reg", "probes", "health"];

async function settled<T>(p: Promise<T>): Promise<T | undefined> {
  try {
    return await p;
  } catch {
    return undefined;
  }
}

/** Everything the health checks, probes and device list read; each part may be missing. */
interface Live {
  cluster?: ClusterStatus;
  trunks?: Trunk[];
  trunkStatus?: TrunkStatus[];
  devices?: Device[];
  extensions?: Extension[];
  bindings?: Binding[];
  authFailures?: AuthFailureList;
}

async function loadLive(signal: AbortSignal): Promise<Live> {
  const [
    cluster,
    trunks,
    trunkStatus,
    devices,
    extensions,
    bindings,
    authFailures,
  ] = await Promise.all([
    settled(getCluster(signal)),
    settled(listTrunks(signal)),
    settled(listTrunkStatus(signal)),
    settled(listDevices(signal)),
    settled(listExtensions(signal)),
    settled(listRegistrations(signal)),
    settled(listAuthFailures(signal)),
  ]);
  return {
    cluster,
    trunks,
    trunkStatus,
    devices,
    extensions,
    bindings,
    authFailures,
  };
}

/** Diagnostics: call traces, registration attempts, trunk probes, health checks. */
export function Diagnostics() {
  const [params, setParams] = useSearchParams();
  const tabParam = params.get("tab") as TabId | null;
  const tab: TabId = tabParam && TABS.includes(tabParam) ? tabParam : "trace";
  const live = usePolling(loadLive, LIVE_REFRESH_MS);
  const data: Live = live.status === "loading" ? {} : (live.data ?? {});
  const toast = useToast();
  const showToast = toast.show;

  const checks = healthChecks({
    cluster: data.cluster,
    trunkStatus: data.trunkStatus,
    devices: data.devices,
    bindings: data.bindings,
    authFailures: data.authFailures?.items,
  });
  const failing = checks.filter(
    (c) => c.tone === "bad" || c.tone === "warn",
  ).length;

  const go = useCallback(
    (next: TabId, extra: Record<string, string> = {}) =>
      setParams({ tab: next, ...extra }),
    [setParams],
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Platform"
        title="Diagnostics"
        description="Call traces, registration attempts, trunk probes and cluster checks. Secrets and digest responses are never shown."
        actions={
          <LinkButton to="/routes/test" icon="flask-conical">
            Route tester
          </LinkButton>
        }
      />
      <Tabs<TabId>
        className="pf-tabs"
        aria-label="Diagnostics"
        idPrefix="diag"
        value={tab}
        onChange={(id) => go(id)}
        items={[
          { id: "trace", label: "Call trace" },
          { id: "reg", label: "Registrations" },
          { id: "probes", label: "Trunk probes" },
          {
            id: "health",
            label: "Health checks",
            count: failing || undefined,
          },
        ]}
      />
      <div
        role="tabpanel"
        id={`diag-panel-${tab}`}
        aria-labelledby={`diag-tab-${tab}`}
      >
        {tab === "trace" && (
          <TraceTab
            callParam={params.get("call")}
            onCall={(id) => go("trace", { call: id })}
          />
        )}
        {tab === "reg" && (
          <RegistrationsTab
            live={data}
            reloadLive={live.reload}
            deviceParam={params.get("device")}
            onDevice={(id) => go("reg", { device: id })}
            showToast={showToast}
          />
        )}
        {tab === "probes" && <ProbesTab live={data} />}
        {tab === "health" && (
          <HealthTab
            checks={checks}
            loading={live.status === "loading"}
            updatedAt={live.status === "ready" ? live.updatedAt : undefined}
            onTab={(t) => go(t as TabId)}
          />
        )}
      </div>
      {toast.node}
    </section>
  );
}

// --- call trace --------------------------------------------------------------

const DIR_ICON: Record<string, string> = {
  internal: "arrow-left-right",
  inbound: "phone-incoming",
  outbound: "phone-outgoing",
};

function statusTone(code: number): BadgeTone {
  if (code > 0 && code < 300) return "good";
  if (code === 487) return "neutral";
  return "bad";
}

const loadRecent = (signal: AbortSignal) => listCdrs({ limit: 25 }, signal);

/**
 * The Call trace tab: recent calls on the left, the selected call's trace on
 * the right. ?call=<id> selects a call (Call detail's "SIP trace" links
 * here), even one no longer in the recent list.
 */
function TraceTab({
  callParam,
  onCall,
}: {
  callParam: string | null;
  onCall: (id: string) => void;
}) {
  const recent = usePolling(loadRecent, LIVE_REFRESH_MS);
  const [q, setQ] = useState("");
  const selected = callParam || null;
  const calls: Cdr[] =
    recent.status === "loading" ? [] : (recent.data?.items ?? []);
  const needle = q.trim().toLowerCase();
  const shown = needle
    ? calls.filter((c) =>
        [
          String(c.id),
          c.sipCallId,
          c.correlationId,
          c.source,
          c.destination,
          c.originalDestination,
        ].some((v) => v?.toLowerCase().includes(needle)),
      )
    : calls;
  const current = selected ?? (calls[0] ? String(calls[0].id) : null);

  return (
    <div className="pf-trace">
      <div className="pf-trace__list">
        <Input
          icon="search"
          size="sm"
          placeholder="Call-ID, number or extension"
          aria-label="Search calls"
          value={q}
          onChange={(e) => setQ(e.target.value)}
        />
        {recent.status === "loading" && <Spinner label="Loading calls…" />}
        {recent.status === "error" && !recent.data && (
          <Alert tone="bad" title="Could not load calls">
            {recent.message}
          </Alert>
        )}
        {recent.status !== "loading" && (
          <div className="az-card">
            {shown.length === 0 ? (
              <EmptyState
                icon="phone-off"
                title={calls.length ? "No call matches" : "No calls yet"}
                description={
                  calls.length
                    ? "Try a Call-ID, a number or an extension."
                    : "Calls appear here when they end."
                }
              />
            ) : (
              <ul className="pf-calls" aria-label="Recent calls">
                {shown.map((c) => (
                  <li key={String(c.id)}>
                    <button
                      type="button"
                      className="pf-call"
                      aria-current={
                        String(c.id) === current ? "true" : undefined
                      }
                      onClick={() => onCall(String(c.id))}
                    >
                      <span className="pf-call__top">
                        <Icon
                          name={DIR_ICON[c.direction] ?? "phone"}
                          size={13}
                        />
                        <span className="pf-call__route">
                          {c.source} → {c.originalDestination || c.destination}
                        </span>
                        <Badge tone={statusTone(c.finalStatus)}>
                          {c.finalStatus || "—"}
                        </Badge>
                      </span>
                      <span className="pf-call__meta">
                        Call {String(c.id)} ·{" "}
                        {new Date(c.startTime).toLocaleTimeString()} ·{" "}
                        {c.sipNode || "—"}
                      </span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
        <p className="pf-muted">Recent calls, newest first.</p>
      </div>
      <div className="pf-trace__main">
        {current ? (
          <CallTrace key={current} id={current} />
        ) : (
          recent.status === "ready" && (
            <div className="az-card pf-card">
              <p className="pf-muted">Select a call to see its trace.</p>
            </div>
          )
        )}
      </div>
    </div>
  );
}

function CallTrace({ id }: { id: string }) {
  const [state, setState] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; cdr: CdrDetail }
  >({ status: "loading" });
  useEffect(() => {
    const controller = new AbortController();
    getCdr(id, controller.signal)
      .then((cdr) => setState({ status: "ready", cdr }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [id]);

  if (state.status === "loading") {
    return (
      <div className="az-card pf-card">
        <Spinner label={`Loading call ${id}…`} />
      </div>
    );
  }
  if (state.status === "error") {
    return (
      <Alert tone="bad" title={`Could not load call ${id}`}>
        {state.message}
      </Alert>
    );
  }
  const c = state.cdr;
  const steps = sortedSteps(c.trace);
  return (
    <div className="az-card pf-card">
      <div className="pf-tracehead">
        <div>
          <h2 className="pf-tracehead__title">
            Call {String(c.id)}
            <Badge tone={statusTone(c.finalStatus)}>
              {c.finalStatus || "—"}
            </Badge>
          </h2>
          <div className="pf-tracehead__meta">
            {c.sipCallId || "—"} · {steps.length}{" "}
            {steps.length === 1 ? "step" : "steps"}
          </div>
        </div>
        <div className="pf-tracehead__actions">
          <LinkButton
            to={`/history/${encodeURIComponent(String(c.id))}`}
            size="sm"
            icon="file-text"
          >
            Call record
          </LinkButton>
        </div>
      </div>
      {c.explanation && (
        <Alert tone="bad" title="Why it failed">
          {c.explanation}
        </Alert>
      )}
      <PropertyList
        items={[
          { label: "From", value: c.source || "—", mono: true },
          {
            label: "Destination",
            value:
              c.rewrittenDestination &&
              c.rewrittenDestination !== c.originalDestination
                ? `${c.originalDestination} → ${c.rewrittenDestination}`
                : c.originalDestination || c.destination || "—",
            mono: true,
          },
          { label: "Route", value: c.route || "—" },
          { label: "Trunk", value: c.trunk || "—", mono: true },
          { label: "Node", value: c.sipNode || "—", mono: true },
        ]}
      />
      {steps.length === 0 ? (
        <p className="pf-muted">No trace was recorded.</p>
      ) : (
        <ol className="pf-steps" aria-label={`Trace of call ${String(c.id)}`}>
          {steps.map((s) => (
            <li key={s.n}>
              <span className="pf-steps__n">{s.n}</span>
              <span>{s.text}</span>
            </li>
          ))}
        </ol>
      )}
      <p className="pf-muted">
        Hello records each call&apos;s routing decision. SIP messages are not
        captured, so no message ladder is shown.
      </p>
    </div>
  );
}

// --- registrations -----------------------------------------------------------

function responseTone(a: RegisterAttempt): BadgeTone {
  if (a.code >= 200 && a.code < 300) return "good";
  if (a.code === 401) return "warn";
  return "bad";
}

function RegistrationsTab({
  live,
  reloadLive,
  deviceParam,
  onDevice,
  showToast,
}: {
  live: Live;
  reloadLive: () => void;
  deviceParam: string | null;
  onDevice: (id: string) => void;
  showToast: (message: string) => void;
}) {
  const devices = live.devices;
  const extensions = live.extensions;
  const registered = useMemo(
    () => new Set((live.bindings ?? []).map((b) => b.device)),
    [live.bindings],
  );
  // Default to the first enabled device without a contact: the one to explain.
  const fallback =
    devices?.find((d) => d.enabled && !registered.has(d.sipUsername)) ??
    devices?.[0];
  const deviceId =
    deviceParam && devices?.some((d) => String(d.id) === deviceParam)
      ? deviceParam
      : fallback
        ? String(fallback.id)
        : null;
  const extOf = (d: Device) =>
    extensions?.find((e) => String(e.id) === String(d.extensionId));

  const authFailures = live.authFailures;
  const blocked = (authFailures?.items ?? []).filter((f) => f.blocked);

  return (
    <>
      {devices === undefined ? (
        <Alert tone="bad" title="Could not load devices">
          The device list is unavailable; try again shortly.
        </Alert>
      ) : devices.length === 0 ? (
        <EmptyState
          icon="smartphone"
          title="No devices"
          description="Add a device to an extension to see its registration attempts here."
        />
      ) : (
        deviceId && (
          <DeviceReg
            key={deviceId}
            deviceId={deviceId}
            options={devices.map((d) => {
              const e = extOf(d);
              return {
                value: String(d.id),
                label: `${d.sipUsername}${e ? ` · ${e.number}` : ""}`,
              };
            })}
            extension={(() => {
              const d = devices.find((x) => String(x.id) === deviceId);
              const e = d && extOf(d);
              return e ? `${e.number} · ${e.name}` : "—";
            })()}
            onDevice={onDevice}
          />
        )
      )}

      <div className="pf-section">
        <div>
          <h2 className="pf-h2" id="blocked-title">
            Blocked sources
          </h2>
          <p className="pf-head__desc">
            Failed-auth throttling: {authFailures ? authFailures.limit : "—"}{" "}
            failures within the window per source IP blocks that IP until the
            window passes.
          </p>
        </div>
      </div>
      {authFailures === undefined ? (
        <p className="pf-muted">Throttling state is unavailable.</p>
      ) : blocked.length === 0 ? (
        <p className="pf-muted">No source is blocked.</p>
      ) : (
        <Table<AuthFailure>
          caption="Blocked sources"
          rowKey={(b) => b.ip}
          rows={blocked}
          columns={[
            { key: "ip", label: "Source IP", mono: true },
            {
              key: "failures",
              label: "Failures",
              render: (b) => `${b.failures} in the window`,
            },
            {
              key: "until",
              label: "Blocked until",
              mono: true,
              render: (b) =>
                b.windowEndsAt
                  ? new Date(b.windowEndsAt).toLocaleTimeString()
                  : "—",
            },
            {
              key: "actions",
              label: <span className="visually-hidden">Actions</span>,
              align: "right",
              render: (b) => (
                <Button
                  variant="secondary"
                  size="sm"
                  aria-label={`Unblock ${b.ip}`}
                  onClick={() => {
                    clearAuthFailures(b.ip)
                      .then(() => {
                        showToast(`${b.ip} unblocked.`);
                        reloadLive();
                      })
                      .catch((err: unknown) =>
                        showToast(
                          `Could not unblock ${b.ip}: ${errorMessage(err)}`,
                        ),
                      );
                  }}
                >
                  Unblock
                </Button>
              ),
            },
          ]}
        />
      )}
    </>
  );
}

const VERDICT_ACTION: Record<string, boolean> = {
  disabled: true,
  auth_failed: true,
  forbidden: true,
  challenge_unanswered: true,
  stale_nonce: false,
};

function DeviceReg({
  deviceId,
  options,
  extension,
  onDevice,
}: {
  deviceId: string;
  options: { value: string; label: string }[];
  extension: string;
  onDevice: (id: string) => void;
}) {
  const load = useCallback(
    (signal: AbortSignal) => getDeviceDiagnostics(deviceId, signal),
    [deviceId],
  );
  const diag = usePolling(load, LIVE_REFRESH_MS);
  const d: DeviceDiagnostics | undefined =
    diag.status === "loading" ? undefined : diag.data;
  const now = diag.status === "ready" ? diag.updatedAt : new Date();
  const last = d?.attempts[0];
  const hours = d ? Math.round(d.attemptsRetentionSeconds / 3600) : 1;

  let props: Property[] = [];
  if (d?.registered && d.bindings[0]) {
    const b = d.bindings[0];
    props = [
      { label: "Contact", value: b.contactUri, mono: true },
      {
        label: "Source",
        value: `${b.source} · ${b.transport.toUpperCase()}`,
        mono: true,
      },
      { label: "Path", value: b.path?.join(", ") || "—", mono: true },
      { label: "Received by", value: b.receivedNode || "—", mono: true },
      { label: "Expires", value: until(b.expires, now) },
      { label: "User agent", value: b.userAgent || "—" },
    ];
    if (d.bindings.length > 1) {
      props.push({ label: "Contacts", value: String(d.bindings.length) });
    }
  } else if (d) {
    props = [
      { label: "Device", value: d.device, mono: true },
      { label: "Extension", value: extension },
      { label: "Last source", value: last?.source || "—", mono: true },
      { label: "Last user agent", value: last?.userAgent || "—" },
      {
        label: "Auth failures (window)",
        value: !last
          ? "—"
          : d.source
            ? `${d.source.failures} of ${d.authFailLimit} before throttling`
            : "None",
      },
    ];
  }

  return (
    <div className="pf-grid2">
      <div className="az-card pf-card">
        <Select
          label="Device"
          options={options}
          value={deviceId}
          onChange={(e) => onDevice(e.target.value)}
        />
        {diag.status === "loading" && <Spinner label="Loading diagnostics…" />}
        {diag.status === "error" && (
          <Alert tone="bad" title="Could not load diagnostics">
            {diag.message}
          </Alert>
        )}
        {d && (
          <>
            <div className="pf-aor">
              <span className="pf-mono">{d.aor}</span>
              <Badge tone={d.registered ? "good" : "bad"} dot>
                {d.registered ? "Registered" : "Not registered"}
              </Badge>
            </div>
            {d.verdict && (
              <Alert
                tone="bad"
                title="Why it is not registered"
                action={
                  VERDICT_ACTION[d.verdict.code] ? (
                    <LinkButton to="/devices" size="sm">
                      Open devices
                    </LinkButton>
                  ) : undefined
                }
              >
                {d.verdict.message}
              </Alert>
            )}
            <PropertyList items={props} />
          </>
        )}
      </div>
      <div className="pf-stack" style={{ gap: 12, minWidth: 0 }}>
        <h2 className="pf-h2" id="attempts-title">
          Recent REGISTER attempts
        </h2>
        {d && d.attempts.length === 0 ? (
          <p className="pf-muted">
            No REGISTER received in the last {hours} h.
          </p>
        ) : (
          d && (
            <Table<RegisterAttempt>
              caption="Recent REGISTER attempts"
              rowKey={(a, i) => `${a.at}-${i}`}
              rows={d.attempts}
              columns={[
                {
                  key: "at",
                  label: "Time",
                  mono: true,
                  primary: false,
                  render: (a) => new Date(a.at).toLocaleTimeString(),
                },
                {
                  key: "req",
                  label: "Request",
                  render: (a) =>
                    a.credentials ? "REGISTER (credentials)" : "REGISTER",
                },
                { key: "source", label: "Source", mono: true },
                { key: "node", label: "Node", mono: true },
                {
                  key: "res",
                  label: "Response",
                  render: (a) => (
                    <Badge tone={responseTone(a)} className="pf-badge-mono">
                      {a.code} {a.reason}
                      {a.stale ? " (stale)" : ""}
                    </Badge>
                  ),
                },
              ]}
            />
          )
        )}
        <p className="pf-muted">
          The last 20 attempts per device, kept for {hours} h after the latest.
        </p>
      </div>
    </div>
  );
}

// --- trunk probes ------------------------------------------------------------

interface ProbeRow {
  key: string;
  destination: string;
  trunk: string;
  priority?: number;
  weight?: number;
  up: boolean;
  lastCode?: number;
  latencyNs?: number;
  checkedAt: string;
}

function ProbesTab({ live }: { live: Live }) {
  if (live.trunkStatus === undefined) {
    return (
      <Alert tone="bad" title="Could not load trunk status">
        Live state (Valkey) is unavailable; try again shortly.
      </Alert>
    );
  }
  const now = new Date();
  const rows: ProbeRow[] = live.trunkStatus.flatMap((t) => {
    const trunk = live.trunks?.find((x) => String(x.id) === String(t.trunkId));
    return t.destinations.map((d) => {
      const cfg = destinationConfig(trunk, d.destination);
      return {
        key: `${String(t.trunkId)}-${d.destination}`,
        destination: d.destination,
        trunk: t.name,
        priority: cfg?.priority,
        weight: cfg?.weight,
        up: d.up,
        lastCode: d.lastCode,
        latencyNs: d.latencyNs,
        checkedAt: d.checkedAt,
      };
    });
  });
  return (
    <>
      {rows.length === 0 ? (
        <EmptyState
          icon="cable"
          title="No probes yet"
          description="Trunk destinations appear here once a SIP node has sent them OPTIONS."
        />
      ) : (
        <Table<ProbeRow>
          caption="Trunk probes"
          rowKey={(r) => r.key}
          rows={rows}
          columns={[
            {
              key: "destination",
              label: "Destination",
              mono: true,
              render: (r) => (
                <>
                  {r.destination}
                  <small className="pf-small">
                    priority {r.priority ?? "—"} · weight {r.weight ?? "—"}
                  </small>
                </>
              ),
            },
            { key: "trunk", label: "Trunk", mono: true },
            {
              key: "state",
              label: "Last response",
              render: (r) => (
                <Badge tone={r.up ? "good" : "bad"} className="pf-badge-mono">
                  {r.up ? "Up" : "Down"}
                  {r.lastCode ? ` · ${r.lastCode}` : ""}
                </Badge>
              ),
            },
            {
              key: "latency",
              label: "Latency",
              align: "right",
              mono: true,
              render: (r) =>
                r.up && r.latencyNs
                  ? `${(r.latencyNs / 1e6).toFixed(1)} ms`
                  : "—",
            },
            {
              key: "checked",
              label: "Checked",
              mono: true,
              render: (r) => ago(r.checkedAt, now),
            },
          ]}
        />
      )}
      <p className="pf-foot">
        Each destination gets an OPTIONS every trunk&apos;s options interval: up
        after any final response, down after a timeout. Calls fail over by
        priority, then weight.
      </p>
    </>
  );
}

// --- health checks -----------------------------------------------------------

const CHECK_ICON: Record<HealthCheck["tone"], string> = {
  good: "circle-check",
  warn: "triangle-alert",
  bad: "circle-x",
  info: "info",
};

function HealthTab({
  checks,
  loading,
  updatedAt,
  onTab,
}: {
  checks: HealthCheck[];
  loading: boolean;
  updatedAt?: Date;
  onTab: (tab: string) => void;
}) {
  if (loading) {
    return <Spinner label="Running checks…" />;
  }
  return (
    <>
      <div className="az-card">
        {checks.length === 0 ? (
          <EmptyState
            icon="stethoscope"
            title="No checks could run"
            description="The cluster and live views are unavailable."
          />
        ) : (
          <ul className="pf-checks" aria-label="Health checks">
            {checks.map((c) => (
              <li key={c.id} className={`pf-check pf-tone--${c.tone}`}>
                <Icon name={CHECK_ICON[c.tone]} size={17} />
                <div style={{ minWidth: 0 }}>
                  <div className="pf-check__title">
                    <span className="visually-hidden">
                      {c.tone === "good"
                        ? "Passed: "
                        : c.tone === "info"
                          ? "Note: "
                          : c.tone === "warn"
                            ? "Warning: "
                            : "Failed: "}
                    </span>
                    {c.title}
                  </div>
                  <div className="pf-check__detail">{c.detail}</div>
                </div>
                {c.link &&
                  (c.link.to ? (
                    <LinkButton
                      to={c.link.to}
                      variant="ghost"
                      size="sm"
                      iconRight="arrow-right"
                    >
                      {c.link.label}
                    </LinkButton>
                  ) : (
                    <Button
                      variant="ghost"
                      size="sm"
                      iconRight="arrow-right"
                      onClick={() => c.link?.tab && onTab(c.link.tab)}
                    >
                      {c.link.label}
                    </Button>
                  ))}
              </li>
            ))}
          </ul>
        )}
      </div>
      <p className="pf-foot">
        Computed in this page from the live cluster, trunk and registration
        views, refreshed every 5 s
        {updatedAt ? `; last at ${updatedAt.toLocaleTimeString()}` : ""}.
      </p>
    </>
  );
}
