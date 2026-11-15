// The Diagnostics "Clients" tab (clients-webrtc-mobile-video S-52): web and
// mobile clients, their WebRTC legs, TURN and the recent client errors, from
// GET /api/v1/webrtc/status.
import type { ReactNode } from "react";
import {
  getWebRTCStatus,
  type ClientAuthFailure,
  type ClientError,
  type ClientRegistration,
  type TurnProbe,
  type WebRTCLeg,
  type WebRTCNode,
  type WebRTCStatus,
} from "../../api/platform";
import {
  Alert,
  Badge,
  type BadgeTone,
  Button,
  EmptyState,
  PropertyList,
  Spinner,
  Table,
} from "../../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../../usePolling";
import { ago, until } from "./health";
import "./platform.css";

const SOURCE_NAMES: Record<string, string> = {
  devices: "the device list",
  live: "registrations and calls (Valkey)",
  webrtc: "the SIP nodes' WebRTC state (Valkey)",
  quality: "the clients' call reports",
  register_attempts: "the REGISTER history (Valkey)",
};

const ICE_TONE: Record<WebRTCLeg["ice"], BadgeTone> = {
  checking: "warn",
  connected: "good",
  failed: "bad",
};

const DTLS_TONE: Record<WebRTCLeg["dtls"], BadgeTone> = {
  new: "neutral",
  connecting: "warn",
  connected: "good",
  failed: "bad",
};

const ERROR_KIND: Record<ClientError["kind"], string> = {
  register: "REGISTER",
  ice: "ICE",
  dtls: "DTLS",
};

const CANDIDATE_TYPES = ["host", "srflx", "prflx", "relay", "unknown"];

const CANDIDATE_LABEL: Record<string, string> = {
  host: "Host (direct on the LAN)",
  srflx: "Server reflexive (through NAT)",
  prflx: "Peer reflexive",
  relay: "Relay (TURN)",
  unknown: "Not reported",
};

function count(m: Record<string, number> | undefined, k: string): number {
  return m?.[k] ?? 0;
}

function section(id: string, title: string, desc?: ReactNode) {
  return (
    <div className="pf-section">
      <div>
        <h2 className="pf-h2" id={id}>
          {title}
        </h2>
        {desc && <p className="pf-head__desc">{desc}</p>}
      </div>
    </div>
  );
}

/** The Clients tab. onDevice opens a device on the Registrations tab. */
export function ClientsTab({ onDevice }: { onDevice: (id: string) => void }) {
  const status = usePolling(getWebRTCStatus, LIVE_REFRESH_MS);
  if (status.status === "loading") {
    return <Spinner label="Loading clients…" />;
  }
  if (!status.data) {
    return (
      <Alert tone="bad" title="Could not load the client status">
        {status.status === "error" ? status.message : "Try again shortly."}
      </Alert>
    );
  }
  const s = status.data;
  const now = status.status === "ready" ? status.updatedAt : new Date();
  return <ClientsView s={s} now={now} onDevice={onDevice} />;
}

function ClientsView({
  s,
  now,
  onDevice,
}: {
  s: WebRTCStatus;
  now: Date;
  onDevice: (id: string) => void;
}) {
  const { enrolled, registered, byTransport } = s.clients;
  const transports = ["wss", "ws", "tls", "tcp", "udp"]
    .filter((t) => count(byTransport, t) > 0)
    .map((t) => `${t.toUpperCase()} ${count(byTransport, t)}`)
    .join(" · ");
  const webDown =
    count(enrolled, "web") > 0 &&
    count(registered, "web") === 0 &&
    !s.unavailable.includes("live");
  return (
    <>
      {s.unavailable.length > 0 && (
        <Alert tone="warn" title="Part of the status is missing">
          Hello could not read{" "}
          {s.unavailable.map((u) => SOURCE_NAMES[u] ?? u).join(", ")}; those
          parts are empty. Try again shortly.
        </Alert>
      )}
      {webDown && (
        <Alert tone="warn" title="No web client is registered">
          {count(enrolled, "web")} web{" "}
          {count(enrolled, "web") === 1 ? "device is" : "devices are"} enrolled
          but none is registered. Check the WSS edge (Kamailio) and the recent
          REGISTER failures below.
        </Alert>
      )}
      <div className="az-card pf-card">
        <PropertyList
          items={[
            {
              label: "Web clients",
              value: `${count(registered, "web")} registered · ${count(enrolled, "web")} enrolled`,
            },
            {
              label: "Mobile clients",
              value: `${count(registered, "mobile")} registered · ${count(enrolled, "mobile")} enrolled`,
            },
            {
              label: "Desk phones registered",
              value: count(registered, "desk"),
            },
            {
              label: "Client transports",
              value: transports || "—",
              mono: true,
            },
            {
              label: "Live WebRTC legs",
              value: `${s.legs.length}${s.turn.relayedLegs ? ` · ${s.turn.relayedLegs} through TURN` : ""}`,
            },
            {
              label: "TURN",
              value: s.turn.configured ? (
                <TurnSummary servers={s.turn.servers} />
              ) : (
                <Badge tone="neutral">Not configured</Badge>
              ),
            },
            {
              label: "Push",
              value: (
                <>
                  <Badge tone={s.push.configured ? "good" : "neutral"}>
                    {s.push.configured ? "Configured" : "Not configured"}
                  </Badge>{" "}
                  <small className="pf-small">{s.push.detail}</small>
                </>
              ),
            },
          ]}
        />
      </div>

      {section(
        "legs-title",
        "Live WebRTC legs",
        "Browser and app media legs on each SIP node's shared WebRTC port: the ICE and DTLS state, and the address ICE selected.",
      )}
      {s.legs.length === 0 ? (
        <p className="pf-muted">No browser or app is in a call.</p>
      ) : (
        <Table<WebRTCLeg>
          caption="Live WebRTC legs"
          rowKey={(l) => `${l.node}-${l.call}-${l.side}-${l.since}`}
          rows={s.legs}
          columns={[
            {
              key: "call",
              label: "Call",
              render: (l) => (
                <>
                  {l.from || l.to ? `${l.from ?? "?"} → ${l.to ?? "?"}` : "—"}
                  <small className="pf-small pf-mono">{l.call || "—"}</small>
                </>
              ),
            },
            {
              key: "side",
              label: "Leg",
              render: (l) =>
                `${l.side === "caller" ? "Caller" : "Callee"}${l.ringing ? " (ringing)" : ""}${l.video ? " · video" : ""}`,
            },
            {
              key: "ice",
              label: "ICE",
              render: (l) => (
                <Badge tone={ICE_TONE[l.ice]}>
                  {l.ice}
                  {l.iceRestarts ? ` · ${l.iceRestarts} restarts` : ""}
                </Badge>
              ),
            },
            {
              key: "dtls",
              label: "DTLS",
              render: (l) => (
                <Badge tone={DTLS_TONE[l.dtls]}>
                  {l.dtls}
                  {l.dtlsReason ? ` · ${l.dtlsReason}` : ""}
                  {l.dtls === "connected" && l.dtlsMs
                    ? ` · ${l.dtlsMs} ms`
                    : ""}
                </Badge>
              ),
            },
            {
              key: "remote",
              label: "Selected address",
              mono: true,
              render: (l) => (
                <>
                  {l.remote ?? "—"}
                  {l.turn && (
                    <>
                      {" "}
                      <Badge tone="info">TURN</Badge>
                    </>
                  )}
                </>
              ),
            },
            {
              key: "srtp",
              label: "SRTP failures",
              align: "right",
              mono: true,
              render: (l) => l.srtpAuthFailures,
            },
            { key: "node", label: "Node", mono: true },
            {
              key: "since",
              label: "Since",
              mono: true,
              render: (l) => ago(l.since, now),
            },
          ]}
        />
      )}

      {section(
        "regs-title",
        "Registered clients",
        "Web and mobile devices with a live registration.",
      )}
      {s.registrations.length === 0 ? (
        <EmptyState
          icon="monitor-smartphone"
          title="No client registered"
          description="A web device registers when its owner opens the console with the softphone on."
        />
      ) : (
        <Table<ClientRegistration>
          caption="Registered clients"
          rowKey={(r) => `${r.device}-${r.source}`}
          rows={s.registrations}
          columns={[
            {
              key: "device",
              label: "Device",
              render: (r) => (
                <>
                  {r.label || r.device}
                  <small className="pf-small pf-mono">
                    {r.device} · {r.kind}
                  </small>
                </>
              ),
            },
            { key: "extension", label: "Extension", mono: true },
            {
              key: "transport",
              label: "Transport",
              render: (r) => (
                <Badge
                  tone={
                    r.transport === "wss" || r.transport === "tls"
                      ? "good"
                      : "warn"
                  }
                  className="pf-badge-mono"
                >
                  {r.transport.toUpperCase()}
                </Badge>
              ),
            },
            { key: "source", label: "Source", mono: true },
            { key: "userAgent", label: "User agent" },
            { key: "node", label: "Node", mono: true },
            {
              key: "expires",
              label: "Expires",
              mono: true,
              render: (r) => until(r.expires, now),
            },
          ]}
        />
      )}

      {section(
        "turn-title",
        "TURN servers",
        <>
          Each TURN address checked from hello-control: a STUN Binding request
          over UDP or TCP, a TCP connection for TLS. Credentials live{" "}
          {Math.round(s.turn.ttlSeconds / 60)} min.
        </>,
      )}
      {!s.turn.configured ? (
        <p className="pf-muted">
          TURN is not configured (HELLO_TURN_URLS and HELLO_TURN_SECRET).
          Clients on the LAN connect directly; a client behind a strict NAT or
          firewall cannot reach the media.
        </p>
      ) : (
        <Table<TurnProbe>
          caption="TURN servers"
          rowKey={(p) => p.url}
          rows={s.turn.servers}
          columns={[
            { key: "url", label: "Address", mono: true },
            {
              key: "state",
              label: "Check",
              render: (p) => (
                <Badge tone={p.reachable ? "good" : "bad"}>
                  {p.reachable ? "Reachable" : "Unreachable"}
                  {p.method ? ` · ${p.method}` : ""}
                </Badge>
              ),
            },
            {
              key: "latency",
              label: "Latency",
              align: "right",
              mono: true,
              render: (p) => (p.reachable ? `${p.latencyMs} ms` : "—"),
            },
            {
              key: "error",
              label: "Problem",
              render: (p) => p.error ?? "—",
            },
            {
              key: "checked",
              label: "Checked",
              mono: true,
              render: (p) => ago(p.checkedAt, now),
            },
          ]}
        />
      )}

      {section(
        "cand-title",
        "ICE candidate types",
        "How the clients' calls connected over the last 24 hours, from their end-of-call reports.",
      )}
      {!s.candidates ? (
        <p className="pf-muted">The call reports are unavailable.</p>
      ) : s.candidates.reports === 0 ? (
        <p className="pf-muted">
          No client reported a call in the last 24 hours.
        </p>
      ) : (
        <>
          <Table<string>
            caption="ICE candidate types"
            rowKey={(t) => t}
            rows={CANDIDATE_TYPES}
            columns={[
              {
                key: "type",
                label: "Candidate type",
                render: (t) => CANDIDATE_LABEL[t] ?? t,
              },
              {
                key: "local",
                label: "Client side",
                align: "right",
                mono: true,
                render: (t) => count(s.candidates?.local, t),
              },
              {
                key: "remote",
                label: "Hello side",
                align: "right",
                mono: true,
                render: (t) => count(s.candidates?.remote, t),
              },
            ]}
          />
          <p className="pf-foot">
            {s.candidates.relayed} of {s.candidates.reports}{" "}
            {s.candidates.reports === 1 ? "call" : "calls"} went through TURN.
          </p>
        </>
      )}

      {section(
        "fail-title",
        "Recent REGISTER failures",
        "Refused REGISTERs of web and mobile devices in the last hour. The first challenge of a REGISTER (401 without credentials) is normal and not listed.",
      )}
      {s.authFailures.length === 0 ? (
        <p className="pf-muted">No client REGISTER was refused.</p>
      ) : (
        <Table<ClientAuthFailure>
          caption="Recent REGISTER failures"
          rowKey={(f) => `${f.device}-${f.at}`}
          rows={s.authFailures}
          columns={[
            {
              key: "at",
              label: "Time",
              mono: true,
              primary: false,
              render: (f) => new Date(f.at).toLocaleTimeString(),
            },
            {
              key: "device",
              label: "Device",
              render: (f) => (
                <>
                  {f.label || f.device}
                  <small className="pf-small pf-mono">{f.device}</small>
                </>
              ),
            },
            {
              key: "res",
              label: "Response",
              render: (f) => (
                <Badge tone="bad" className="pf-badge-mono">
                  {f.code} {f.reason}
                </Badge>
              ),
            },
            { key: "source", label: "Source", mono: true },
            { key: "node", label: "Node", mono: true },
            {
              key: "actions",
              label: <span className="visually-hidden">Actions</span>,
              align: "right",
              render: (f) => (
                <Button
                  variant="ghost"
                  size="sm"
                  iconRight="arrow-right"
                  aria-label={`Diagnose ${f.label || f.device}`}
                  onClick={() => onDevice(String(f.deviceId))}
                >
                  Diagnose
                </Button>
              ),
            },
          ]}
        />
      )}

      {section(
        "errors-title",
        "Last client errors",
        "REGISTER, ICE and DTLS errors, newest first.",
      )}
      {s.lastErrors.length === 0 ? (
        <p className="pf-muted">No client error recently.</p>
      ) : (
        <ul className="pf-checks" aria-label="Last client errors">
          {s.lastErrors.map((e, i) => (
            <li key={`${e.at}-${i}`} className="pf-check pf-tone--bad">
              <Badge tone="bad" className="pf-badge-mono">
                {ERROR_KIND[e.kind]}
              </Badge>
              <div style={{ minWidth: 0 }}>
                <div className="pf-check__title">{e.detail || e.reason}</div>
                <div className="pf-check__detail">
                  {[
                    ago(e.at, now),
                    e.device,
                    e.call && `call ${e.call}`,
                    e.node,
                  ]
                    .filter(Boolean)
                    .join(" · ")}
                </div>
              </div>
            </li>
          ))}
        </ul>
      )}

      {section(
        "nodes-title",
        "SIP nodes",
        "Each node's WebRTC counters since it started.",
      )}
      {s.nodes.length === 0 ? (
        <p className="pf-muted">
          No SIP node reports WebRTC media (HELLO_MEDIA_WEBRTC_ENABLED is off,
          or the nodes are not reachable).
        </p>
      ) : (
        <Table<WebRTCNode>
          caption="SIP nodes with WebRTC media"
          rowKey={(n) => n.node}
          rows={s.nodes}
          columns={[
            {
              key: "node",
              label: "Node",
              mono: true,
              render: (n) => (
                <>
                  {n.node}
                  <small className="pf-small">
                    UDP {n.port} · {n.hosts.join(", ") || "—"}
                  </small>
                </>
              ),
            },
            { key: "legs", label: "Legs", align: "right", mono: true },
            {
              key: "ice",
              label: "ICE connected / failed",
              align: "right",
              mono: true,
              render: (n) =>
                `${count(n.counters.ice, "connected")} / ${count(n.counters.ice, "failed")}`,
            },
            {
              key: "dtls",
              label: "DTLS ok / failed",
              align: "right",
              mono: true,
              render: (n) =>
                `${n.counters.dtlsHandshakes} / ${Object.values(n.counters.dtlsFailures).reduce((a, b) => a + b, 0)}`,
            },
            {
              key: "srtp",
              label: "SRTP failures",
              align: "right",
              mono: true,
              render: (n) =>
                Object.values(n.counters.srtpAuthFailures).reduce(
                  (a, b) => a + b,
                  0,
                ),
            },
            {
              key: "updated",
              label: "Reported",
              mono: true,
              render: (n) => ago(n.updatedAt, now),
            },
          ]}
        />
      )}
      <p className="pf-foot">
        Refreshed every {LIVE_REFRESH_MS / 1000} s; generated{" "}
        {new Date(s.generatedAt).toLocaleTimeString()}.
      </p>
    </>
  );
}

function TurnSummary({ servers }: { servers: TurnProbe[] }) {
  const up = servers.filter((p) => p.reachable).length;
  const tone: BadgeTone =
    up === servers.length ? "good" : up === 0 ? "bad" : "warn";
  return (
    <Badge tone={tone}>
      {up} of {servers.length} reachable
    </Badge>
  );
}
