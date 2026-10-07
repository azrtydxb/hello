import {
  useEffect,
  useRef,
  useState,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import {
  ApiError,
  drainNode,
  errorMessage,
  getCluster,
  listCalls,
  listDevices,
  listRegistrations,
  listTrunks,
  listTrunkStatus,
  undrainNode,
  type ActiveCall,
  type Binding,
  type ClusterMember,
  type ClusterStatus,
  type Device,
  type Trunk,
  type TrunkStatus,
} from "../api";
import {
  Alert,
  Badge,
  Button,
  Icon,
  LiveTag,
  Modal,
  PageHeader,
  type Property,
  PropertyList,
  Spinner,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import {
  ago,
  count,
  lagText,
  memberTone,
  stateLabel,
  type Tone,
} from "./platform/health";
import "./platform/platform.css";
import { Can } from "../role";

/** The live views around the cluster; each is absent when it cannot be read. */
interface Around {
  trunks?: Trunk[];
  trunkStatus?: TrunkStatus[];
  devices?: Device[];
  bindings?: Binding[];
  calls?: ActiveCall[];
}

async function settled<T>(p: Promise<T>): Promise<T | undefined> {
  try {
    return await p;
  } catch {
    return undefined;
  }
}

async function loadAround(signal: AbortSignal): Promise<Around> {
  const [trunks, trunkStatus, devices, bindings, calls] = await Promise.all([
    settled(listTrunks(signal)),
    settled(listTrunkStatus(signal)),
    settled(listDevices(signal)),
    settled(listRegistrations(signal)),
    settled(listCalls(signal)),
  ]);
  return { trunks, trunkStatus, devices, bindings, calls };
}

const BADGE_TONE: Record<Tone, "good" | "warn" | "bad" | "neutral"> = {
  good: "good",
  warn: "warn",
  bad: "bad",
  neutral: "neutral",
};

const num = (n: number | undefined) => (n === undefined ? "—" : String(n));

/** One service of the topology, as drawn and as detailed in the modal. */
interface Service {
  key: string;
  title: string;
  icon: string;
  kind: string;
  tone: Tone;
  label: string;
  reason?: string;
  metrics: { k: string; v: string }[];
  props: Property[];
  /** Nodes whose drain controls the details show. */
  nodes?: ClusterMember[];
  /** Per-item lines drawn in the topology box (trunks, control nodes). */
  rows?: { name: string; tone: Tone; meta: string }[];
  foot?: string;
}

/** Cluster: how calls and state flow through the services, live. */
export function Cluster() {
  const state = usePolling(getCluster, LIVE_REFRESH_MS);
  const around = usePolling(loadAround, LIVE_REFRESH_MS);
  const { reload } = state;
  const [notice, setNotice] = useState("");
  const [selected, setSelected] = useState<string | null>(null);
  const returnTo = useRef<HTMLElement | null>(null);

  const data: ClusterStatus | undefined =
    state.status === "loading" ? undefined : state.data;
  const extra: Around = around.status === "loading" ? {} : (around.data ?? {});
  const now = state.status === "ready" ? state.updatedAt : new Date();

  const services = data ? describe(data, extra, now) : [];
  const sel = services.find((s) => s.key === selected);
  const sip = data?.members.filter((m) => m.kind === "sip") ?? [];
  const sipReady = sip.filter((m) => m.state === "READY").length;

  function open(key: string, el: HTMLElement) {
    returnTo.current = el;
    setSelected(key);
  }
  function close() {
    setSelected(null);
    returnTo.current?.focus();
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Platform"
        title="Cluster"
        description={
          <>
            How calls and state flow through the services. Select a service for
            details. Configuration revision{" "}
            {data ? (data.configRevision ?? "—") : "—"}.
          </>
        }
        actions={
          <>
            <LiveTag every="5 s" live={state.status === "ready"} />
          </>
        }
      />
      <p className="pf-notice" role="status" aria-live="polite">
        {notice}
      </p>
      {state.status === "loading" && (
        <Spinner label="Loading cluster status…" />
      )}
      {state.status === "error" && (
        <Alert tone="bad" title="Could not load cluster status">
          {state.message}
        </Alert>
      )}
      {data && sip.length > 0 && sipReady === 0 && (
        <Alert
          tone="bad"
          title="No SIP node is READY"
          style={{ marginBottom: "var(--az-space-4)" }}
        >
          New calls are rejected (503) until a SIP node returns to READY.
        </Alert>
      )}
      {data && <Topology services={services} sip={sip} onSelect={open} />}
      {sel && (
        <ServiceModal
          service={sel}
          now={now}
          onClose={close}
          onDone={(message) => {
            setNotice(message);
            reload();
          }}
        />
      )}
    </section>
  );
}

/** Builds every service of the topology from the live views. */
function describe(c: ClusterStatus, a: Around, now: Date): Service[] {
  const out: Service[] = [];
  const enabled = a.devices?.filter((d) => d.enabled);
  const registered = a.bindings
    ? new Set(a.bindings.map((b) => b.device))
    : undefined;
  const missing =
    enabled && registered
      ? enabled.filter((d) => !registered.has(d.sipUsername))
      : undefined;
  out.push({
    key: "phones",
    title: "Phones",
    icon: "smartphone",
    kind: "Registered SIP devices",
    tone: registered ? "good" : "neutral",
    label: registered ? `${registered.size} reg` : "—",
    metrics: [
      { k: "Devices", v: num(enabled?.length) },
      { k: "Registered", v: num(registered?.size) },
      { k: "Calls", v: num(a.calls?.length) },
    ],
    props: [
      {
        label: "Not registered",
        value: missing
          ? missing.length
            ? missing.map((d) => d.sipUsername).join(", ")
            : "None"
          : "—",
        mono: true,
      },
    ],
  });

  const status = a.trunkStatus;
  const dests = status?.flatMap((t) => t.destinations) ?? [];
  const down = dests.filter((d) => !d.up);
  const carriersTone: Tone = !status
    ? "neutral"
    : down.length === 0
      ? "good"
      : down.length === dests.length
        ? "bad"
        : "warn";
  const maxCalls = (id: unknown) =>
    a.trunks?.find((t) => String(t.id) === String(id))?.maxCalls;
  out.push({
    key: "carriers",
    title: "Carriers",
    icon: "cable",
    kind: "SIP trunks",
    tone: carriersTone,
    label: !status ? "—" : down.length === 0 ? "Up" : "Degraded",
    reason: down.length
      ? down
          .map(
            (d) =>
              `${d.destination} is down${d.lastCode ? ` (${d.lastCode})` : ""}.`,
          )
          .join(" ")
      : undefined,
    metrics: [
      { k: "Trunks", v: num(status?.length) },
      {
        k: "Calls",
        v: num(status?.reduce((n, t) => n + t.activeCalls, 0)),
      },
      {
        k: "Destinations up",
        v: status ? `${dests.length - down.length} / ${dests.length}` : "—",
      },
    ],
    rows: (status ?? []).map((t) => {
      const up = t.destinations.filter((d) => d.up).length;
      const max = maxCalls(t.trunkId);
      return {
        name: t.name,
        tone: up === t.destinations.length ? "good" : up === 0 ? "bad" : "warn",
        meta: `${t.activeCalls}${max ? ` / ${max}` : ""}`,
      };
    }),
    foot: status
      ? `${dests.length - down.length} of ${dests.length} destinations up`
      : "Trunk health unavailable",
    props: (status ?? []).map((t) => {
      const up = t.destinations.filter((d) => d.up).length;
      const max = maxCalls(t.trunkId);
      return {
        label: t.name,
        value: [
          t.registration
            ? `${t.registration.state}${t.registration.lastCode ? ` (${t.registration.lastCode})` : ""}`
            : null,
          `${up} of ${t.destinations.length} up`,
          `${t.activeCalls}${max ? ` / ${max}` : ""} calls`,
        ]
          .filter(Boolean)
          .join(" · "),
      };
    }),
  });
  out.push({
    key: "kamailio",
    title: "kamailio",
    icon: "shuffle",
    kind: "SIP balancer",
    tone: "neutral",
    label: "Not monitored",
    metrics: [],
    props: [
      {
        label: "Health",
        value: "Hello does not report the balancer's health.",
      },
    ],
  });
  out.push({
    key: "ui",
    title: "hello-ui",
    icon: "monitor",
    kind: "Web UI",
    tone: "good",
    label: "Up",
    metrics: [],
    props: [
      { label: "Address", value: window.location.host, mono: true },
      { label: "Upstream", value: "hello-control (/api)" },
    ],
  });

  for (const m of c.members.filter((x) => x.kind === "sip")) {
    out.push({
      key: m.id,
      title: m.id,
      icon: "phone",
      kind: `SIP node · ${m.version}`,
      tone: memberTone(m.state),
      label: stateLabel(m.state),
      reason: m.reason,
      metrics: [
        { k: "Calls", v: String(m.activeCalls) },
        { k: "Registrations", v: String(m.registrations) },
        { k: "Heartbeat", v: ago(m.heartbeat, now) },
      ],
      props: memberProps(m),
      nodes: [m],
    });
  }

  const control = c.members.filter((x) => x.kind !== "sip");
  const controlReady = control.filter((m) => m.state === "READY").length;
  out.push({
    key: "control",
    title: "hello-control",
    icon: "sliders-horizontal",
    kind: `Control plane · ${count(control.length, "node")}`,
    tone:
      control.length === 0
        ? "neutral"
        : controlReady === control.length
          ? "good"
          : controlReady === 0
            ? "bad"
            : "warn",
    label:
      control.length === 0
        ? "—"
        : controlReady === control.length
          ? "Ready"
          : `${controlReady} of ${control.length} ready`,
    metrics: [
      { k: "Nodes", v: String(control.length) },
      { k: "Ready", v: String(controlReady) },
      { k: "Revision", v: num(c.configRevision ?? undefined) },
    ],
    props: [],
    nodes: control,
  });

  out.push({
    key: "valkey",
    title: "valkey",
    icon: "layers",
    kind: c.valkey.mode === "sentinel" ? "Live state · Sentinel" : "Live state",
    tone: c.valkey.up ? "good" : "bad",
    label: c.valkey.up ? "Up" : "Down",
    reason: c.valkey.up ? undefined : c.valkey.error,
    metrics: [
      { k: "Bindings", v: num(a.bindings?.length) },
      { k: "Calls", v: num(a.calls?.length) },
      { k: "Mode", v: c.valkey.mode === "sentinel" ? "Sentinel" : "Single" },
    ],
    props: [
      {
        label: "Mode",
        value: c.valkey.mode === "sentinel" ? "Sentinel" : "Single node",
      },
      { label: "Primary", value: c.valkey.primary || "—", mono: true },
      { label: "Holds", value: "registrations, active calls, trunk state" },
    ],
  });
  out.push({
    key: "postgres",
    title: "postgres",
    icon: "database",
    kind: "Configuration and call records",
    tone: c.postgres.up ? "good" : "bad",
    label: c.postgres.up ? "Up" : "Down",
    reason: c.postgres.up ? undefined : c.postgres.error,
    metrics: [
      { k: "Revision", v: num(c.configRevision ?? undefined) },
      { k: "Devices", v: num(a.devices?.length) },
      { k: "Trunks", v: num(a.trunks?.length) },
    ],
    props: [{ label: "Used by", value: "hello-control, hello-sip" }],
  });
  out.push({
    key: "minio",
    title: "minio",
    icon: "hard-drive",
    kind: "Object storage",
    tone: "neutral",
    label: "Not monitored",
    metrics: [],
    props: [
      { label: "Holds", value: "voicemail, recordings, announcements" },
      {
        label: "Health",
        value: "Hello does not report object storage health here.",
      },
    ],
  });
  return out;
}

function memberProps(m: ClusterMember): Property[] {
  return [
    {
      label: "Address",
      value: m.sipAddr
        ? `${m.sipAddr}${m.transports?.length ? ` (${m.transports.join(", ")})` : ""}`
        : m.httpAddr || "—",
      mono: true,
    },
    { label: "Version", value: m.version || "—", mono: true },
    {
      label: "Revision",
      value: `${lagText(m.revisionLag)} (rev ${m.configRevision})`,
    },
  ];
}

// --- topology drawing --------------------------------------------------------

const X = { ext: 5, edge: 285, mid: 575, state: 905 };
const SIP_H = 150;
const GAP = 40;

interface Geometry {
  height: number;
  phones: number;
  carriers: number;
  kam: number;
  ui: number;
  sip: number[];
  control: number;
  valkey: number;
  pg: number;
  minio: number;
}

/** Box tops for n SIP nodes, from the design's 2-node layout (600 px tall). */
export function geometry(n: number): Geometry {
  const sip = Array.from(
    { length: Math.max(n, 1) },
    (_, i) => 30 + i * (SIP_H + GAP),
  );
  const control = Math.max(420, sip[sip.length - 1]! + SIP_H + 50);
  const height = Math.max(600, control + SIP_H + 30);
  const sipCentre = (sip[0]! + sip[sip.length - 1]! + SIP_H) / 2;
  const kc = Math.max(sipCentre, 205);
  return {
    height,
    sip: n === 0 ? [] : sip,
    control,
    kam: kc - 62,
    phones: kc - 130 - 62,
    carriers: kc + 160 - 75,
    ui: control + 75 - 48,
    valkey: 30,
    pg: Math.round(height / 2 - 76),
    minio: height - 186,
  };
}

const LINK_CLASS: Record<Tone, string> = {
  good: "pf-link pf-link--live",
  warn: "pf-link pf-link--warn",
  bad: "pf-link pf-link--bad",
  neutral: "pf-link",
};

function curve(x1: number, y1: number, x2: number, y2: number): string {
  const mx = (x1 + x2) / 2;
  return `M${x1},${y1} C${mx},${y1} ${mx},${y2} ${x2},${y2}`;
}

function Rows({ rows }: { rows: NonNullable<Service["rows"]> }) {
  return (
    <span className="pf-svc__rows">
      {rows.slice(0, 3).map((r) => (
        <span key={r.name} className="pf-svc__row">
          <span
            className={`az-dot pf-dot pf-tone--${r.tone}`}
            aria-hidden="true"
          />
          <span className="pf-svc__row-name">{r.name}</span>
          <span className="pf-svc__row-meta">{r.meta}</span>
        </span>
      ))}
    </span>
  );
}

function Topology({
  services,
  sip,
  onSelect,
}: {
  services: Service[];
  sip: ClusterMember[];
  onSelect: (key: string, el: HTMLElement) => void;
}) {
  const g = geometry(sip.length);
  const svc = (key: string) => services.find((s) => s.key === key)!;
  const centre = (top: number, h: number) => top + h / 2;
  const kc = centre(g.kam, 124);
  const cc = centre(g.control, SIP_H);
  const stateYs = [
    centre(g.valkey, 150),
    centre(g.pg, 132),
    centre(g.minio, 132),
  ];
  const midYs = [...g.sip.map((y) => centre(y, SIP_H)), cc];
  const busTop = Math.min(...midYs, ...stateYs);
  const busBottom = Math.max(...midYs, ...stateYs);
  const carriers = svc("carriers");
  const valkey = svc("valkey");
  const pg = svc("postgres");

  const box = (
    s: Service,
    left: number,
    top: number,
    h: number,
    body: ReactNode,
    sub: string,
  ) => (
    <button
      key={s.key}
      type="button"
      className={`pf-svc pf-tone--${s.tone}${s.tone === "bad" ? " pf-svc--bad" : s.tone === "warn" ? " pf-svc--warn" : ""}`}
      style={{ left, top, height: h }}
      aria-haspopup="dialog"
      aria-label={`${s.title}: ${s.label}`}
      onClick={(e) => onSelect(s.key, e.currentTarget)}
    >
      <span className="pf-svc__head">
        <Icon name={s.icon} size={15} />
        <span className="pf-svc__name">{s.title}</span>
        <span className="pf-svc__state">
          <span className="az-dot pf-dot" aria-hidden="true" />
          {s.label}
        </span>
      </span>
      <span className="pf-svc__sub">{sub}</span>
      {body}
    </button>
  );

  const metrics = (s: Service, mono?: string) => (
    <span className="pf-svc__metrics">
      {s.metrics.slice(0, 3).map((m) => (
        <span key={m.k} style={{ minWidth: 0 }}>
          <span className="az-eyebrow">{m.k}</span>
          <span
            className={`pf-svc__metric${m.k === mono ? " pf-svc__metric--mono" : ""}`}
            style={{ display: "block" }}
          >
            {m.v}
          </span>
        </span>
      ))}
    </span>
  );

  const control = svc("control");
  return (
    <div className="az-card pf-topo">
      <div
        className="pf-topo__canvas"
        style={{ height: g.height }}
        role="group"
        aria-label="Cluster topology"
      >
        <span
          className="az-eyebrow pf-topo__label"
          style={{ left: 12, top: -1 }}
        >
          External
        </span>
        <div
          className="pf-topo__zone"
          style={{ left: 268, top: 6, width: 554, height: g.height - 12 }}
        >
          <span className="az-eyebrow">edge</span>
        </div>
        <div
          className="pf-topo__zone"
          style={{ left: 888, top: 6, width: 264, height: g.height - 12 }}
        >
          <span className="az-eyebrow">state network</span>
        </div>
        <svg
          className="pf-topo__svg"
          width={1160}
          height={g.height}
          aria-hidden="true"
        >
          <path className="pf-link" d={curve(235, g.phones + 62, 285, kc)} />
          <path
            className={
              carriers.tone === "warn" || carriers.tone === "bad"
                ? LINK_CLASS[carriers.tone]
                : "pf-link"
            }
            d={curve(235, g.carriers + 75, 285, kc)}
          />
          {sip.map((m, i) => (
            <path
              key={m.id}
              className={LINK_CLASS[memberTone(m.state)]}
              d={curve(515, kc, 575, centre(g.sip[i]!, SIP_H))}
            />
          ))}
          <path className="pf-link" d={curve(515, g.ui + 48, 575, cc)} />
          {midYs.map((y, i) => {
            const m = sip[i];
            const bad = m && memberTone(m.state) === "bad";
            return (
              <path
                key={i}
                className={bad ? LINK_CLASS.bad : "pf-link"}
                d={`M805,${y} H855`}
              />
            );
          })}
          <path className="pf-link" d={`M855,${busTop} V${busBottom}`} />
          <path
            className={valkey.tone === "bad" ? LINK_CLASS.bad : "pf-link"}
            d={`M855,${stateYs[0]} H905`}
          />
          <path
            className={pg.tone === "bad" ? LINK_CLASS.bad : "pf-link"}
            d={`M855,${stateYs[1]} H905`}
          />
          <path className="pf-link" d={`M855,${stateYs[2]} H905`} />
        </svg>
        <span className="pf-topo__wire" style={{ left: 532, top: kc - 77 }}>
          SIP
        </span>
        <span className="pf-topo__wire" style={{ left: 826, top: 80 }}>
          state
        </span>

        {box(
          svc("phones"),
          X.ext,
          g.phones,
          124,
          metrics(svc("phones")),
          "SIP/UDP → kamailio",
        )}
        {box(
          carriers,
          X.ext,
          g.carriers,
          150,
          <>
            <Rows rows={carriers.rows ?? []} />
            <span className="pf-svc__foot">{carriers.foot}</span>
          </>,
          "OPTIONS health checks",
        )}
        {box(
          svc("kamailio"),
          X.edge,
          g.kam,
          124,
          <>
            <span className="pf-svc__line">Dispatches to the SIP nodes</span>
            <span className="pf-svc__foot">Not reported to Hello</span>
          </>,
          "SIP balancer · Path",
        )}
        {box(
          svc("ui"),
          X.edge,
          g.ui,
          96,
          <span className="pf-svc__line">Proxies /api to hello-control</span>,
          window.location.host,
        )}
        {sip.map((m, i) => {
          const s = svc(m.id);
          return box(
            s,
            X.mid,
            g.sip[i]!,
            SIP_H,
            <>
              {m.reason ? (
                <span className="pf-svc__reason">{m.reason}</span>
              ) : (
                <span className="pf-svc__line">
                  Revision {lagText(m.revisionLag)} (rev {m.configRevision})
                </span>
              )}
              {metrics(
                {
                  ...s,
                  metrics: [
                    s.metrics[0]!,
                    { k: "Regs", v: s.metrics[1]!.v },
                    s.metrics[2]!,
                  ],
                },
                "Heartbeat",
              )}
            </>,
            `${m.sipAddr ?? "—"} · ${m.version}`,
          );
        })}
        {box(
          control,
          X.mid,
          g.control,
          SIP_H,
          <>
            <Rows
              rows={(control.nodes ?? []).map((m) => ({
                name: m.id,
                tone: memberTone(m.state),
                meta: stateLabel(m.state),
              }))}
            />
            <span className="pf-svc__foot pf-tone--neutral">
              Revision {control.metrics[2]?.v}
            </span>
          </>,
          "REST API · migrations",
        )}
        {box(
          valkey,
          X.state,
          g.valkey,
          150,
          <>
            {valkey.reason && (
              <span className="pf-svc__reason">{valkey.reason}</span>
            )}
            <span className="pf-svc__line">
              {valkey.props[1]?.value !== "—"
                ? `Primary ${String(valkey.props[1]?.value)}`
                : String(valkey.props[0]?.value)}
            </span>
            {metrics(valkey)}
          </>,
          valkey.kind
            .replace("Live state · ", "")
            .replace("Live state", "Single node"),
        )}
        {box(
          pg,
          X.state,
          g.pg,
          132,
          <>
            {pg.reason && <span className="pf-svc__reason">{pg.reason}</span>}
            {metrics(pg)}
          </>,
          "config · call records",
        )}
        {box(
          svc("minio"),
          X.state,
          g.minio,
          132,
          <span className="pf-svc__line">
            voicemail · recordings · prompts
          </span>,
          "object storage",
        )}
      </div>
      <div className="pf-legend">
        <span>
          <span className="az-dot pf-dot pf-tone--good" aria-hidden="true" />
          Ready / up
        </span>
        <span>
          <span className="az-dot pf-dot pf-tone--warn" aria-hidden="true" />
          Draining / degraded
        </span>
        <span>
          <span className="az-dot pf-dot pf-tone--bad" aria-hidden="true" />
          Unhealthy / down
        </span>
        <span>
          <span className="pf-legend__dash" aria-hidden="true" />
          Impaired link
        </span>
        <span className="pf-legend__end">
          Select a service for details and actions
        </span>
      </div>
    </div>
  );
}

// --- details -------------------------------------------------------------------

function ServiceModal({
  service: s,
  now,
  onClose,
  onDone,
}: {
  service: Service;
  now: Date;
  onClose: () => void;
  onDone: (message: string) => void;
}) {
  const body = useRef<HTMLDivElement>(null);
  useEffect(() => {
    // Move focus into the dialog; the page returns it on close.
    body.current
      ?.closest<HTMLElement>(".az-modal")
      ?.querySelector<HTMLElement>("button")
      ?.focus();
  }, []);
  const multi = (s.nodes?.length ?? 0) > 1 || s.key === "control";
  return (
    <Modal
      title={s.title}
      description={s.kind}
      onClose={onClose}
      width={520}
      actions={
        <Button variant="secondary" size="sm" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="pf-detail" ref={body}>
        <div>
          <Badge tone={BADGE_TONE[s.tone]} dot>
            {s.label}
          </Badge>
        </div>
        {s.reason && <div className="pf-detail__reason">{s.reason}</div>}
        {s.metrics.length > 0 && (
          <div className="pf-metrics">
            {s.metrics.map((m) => (
              <div key={m.k}>
                <span className="az-eyebrow">{m.k}</span>
                <div className="pf-metrics__value">{m.v}</div>
              </div>
            ))}
          </div>
        )}
        {s.props.length > 0 && <PropertyList items={s.props} />}
        {multi
          ? (s.nodes ?? []).map((m) => (
              <div key={m.id} className="pf-node">
                <h3 className="pf-node__title">
                  {m.id}
                  <Badge tone={BADGE_TONE[memberTone(m.state)]} dot>
                    {stateLabel(m.state)}
                  </Badge>
                </h3>
                <PropertyList
                  items={[
                    ...memberProps(m),
                    { label: "Heartbeat", value: ago(m.heartbeat, now) },
                  ]}
                />
                <DrainControl member={m} onDone={onDone} />
              </div>
            ))
          : s.nodes?.[0] && (
              <DrainControl member={s.nodes[0]} onDone={onDone} />
            )}
        {s.nodes && s.nodes.length === 0 && s.key === "control" && (
          <p className="pf-muted">No control node has reported in.</p>
        )}
      </div>
    </Modal>
  );
}

type Step =
  | { kind: "idle" }
  | { kind: "confirm" }
  | { kind: "warn"; message: string }
  | { kind: "busy" };

/**
 * Drain or undrain one node, confirmed in place. If the server warns that
 * draining would leave no READY SIP node (409), the warning is shown and
 * only an explicit second confirmation retries with force=true.
 */
function DrainControl({
  member: m,
  onDone,
}: {
  member: ClusterMember;
  onDone: (message: string) => void;
}) {
  const draining = m.state === "DRAINING";
  const [step, setStep] = useState<Step>({ kind: "idle" });
  const [error, setError] = useState<string | null>(null);
  // Focus targets: the first button of the confirmation, or the trigger.
  const firstAction = useRef<HTMLDivElement>(null);
  const trigger = useRef<HTMLDivElement>(null);
  const returnFocus = useRef(false);

  useEffect(() => {
    if (step.kind === "confirm" || step.kind === "warn") {
      firstAction.current?.querySelector("button")?.focus();
    } else if (step.kind === "idle" && returnFocus.current) {
      returnFocus.current = false;
      trigger.current?.querySelector("button")?.focus();
    }
  }, [step]);

  if (m.state === "OFFLINE") return null;

  function cancel() {
    returnFocus.current = true;
    setStep({ kind: "idle" });
  }
  function onKeyDown(e: KeyboardEvent) {
    if (e.key !== "Escape") return;
    // Escape cancels the confirmation, not the whole dialog.
    e.stopPropagation();
    e.nativeEvent.stopImmediatePropagation();
    cancel();
  }

  async function run(force: boolean) {
    setStep({ kind: "busy" });
    setError(null);
    try {
      if (draining) {
        await undrainNode(m.id);
        onDone(`${m.id} is returning to service.`);
      } else {
        await drainNode(m.id, force);
        onDone(`${m.id} is draining.`);
      }
      setStep({ kind: "idle" });
    } catch (err) {
      if (
        !draining &&
        !force &&
        err instanceof ApiError &&
        err.status === 409
      ) {
        setStep({ kind: "warn", message: err.message });
        return;
      }
      setError(errorMessage(err));
      setStep({ kind: "idle" });
    }
  }

  const verb = draining ? "Undrain" : "Drain";
  return (
    <div className="pf-drain">
      {step.kind === "idle" && (
        <div ref={trigger}>
          <Can min="admin">
            <Button
              variant="secondary"
              size="sm"
              icon={draining ? "play" : "pause"}
              aria-label={`${verb} ${m.id}`}
              onClick={() => setStep({ kind: "confirm" })}
            >
              {verb}
            </Button>
          </Can>
        </div>
      )}
      {step.kind === "confirm" && (
        <div
          className="pf-drain"
          style={{ border: 0, padding: 0 }}
          role="group"
          aria-label={`${verb} ${m.id}?`}
          onKeyDown={onKeyDown}
        >
          <span className="pf-drain__prompt">
            {draining
              ? `Return ${m.id} to service?`
              : `Drain ${m.id}? It stops taking new calls; calls in progress continue.`}
          </span>
          <div className="pf-drain__actions" ref={firstAction}>
            <Button
              variant={draining ? "primary" : "danger"}
              size="sm"
              onClick={() => void run(false)}
            >
              {draining ? "Undrain node" : "Drain node"}
            </Button>
            <Button variant="secondary" size="sm" onClick={cancel}>
              Cancel
            </Button>
          </div>
        </div>
      )}
      {step.kind === "warn" && (
        <div
          className="pf-drain"
          style={{ border: 0, padding: 0 }}
          role="group"
          aria-label={`Drain ${m.id} anyway?`}
          onKeyDown={onKeyDown}
        >
          <Alert tone="bad" title="Warning">
            {step.message}
          </Alert>
          <span className="pf-drain__prompt">
            Draining anyway means new calls are rejected (503) until a SIP node
            is READY again.
          </span>
          <div className="pf-drain__actions" ref={firstAction}>
            <Button variant="secondary" size="sm" onClick={cancel}>
              Keep {m.id} in service
            </Button>
            <Button variant="danger" size="sm" onClick={() => void run(true)}>
              Drain anyway
            </Button>
          </div>
        </div>
      )}
      {step.kind === "busy" && (
        <span role="status" aria-live="polite" className="pf-drain__prompt">
          Working…
        </span>
      )}
      {error && (
        <Alert tone="bad" title={`Could not ${verb.toLowerCase()} ${m.id}`}>
          {error}
        </Alert>
      )}
    </div>
  );
}
