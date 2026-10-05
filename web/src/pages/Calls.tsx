import {
  getCluster,
  listCalls,
  type ActiveCall,
  type ClusterMember,
} from "../api";
import {
  Alert,
  Badge,
  EmptyState,
  Spinner,
  Table,
  type TableColumn,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import {
  callDuration,
  callState,
  clock,
  LiveMarker,
  mediaLabel,
  PageHeader,
  Party,
  shortCallId,
  useExtensionNames,
  useNow,
} from "./calls/common";

/** A node's state as a dot tone. */
export function nodeTone(state: string | undefined): "good" | "warn" | "bad" {
  if (state === "READY") return "good";
  if (state === "UNHEALTHY" || state === "OFFLINE" || state === undefined)
    return "bad";
  return "warn";
}

interface NodeCount {
  id: string;
  calls: number;
  state?: string;
}

/** Calls per SIP node: every SIP member, plus any node a call names that is not one. */
export function callsPerNode(
  calls: readonly ActiveCall[],
  members: readonly ClusterMember[] | undefined,
): NodeCount[] {
  const out = new Map<string, NodeCount>();
  for (const m of members ?? []) {
    if (m.kind === "sip") out.set(m.id, { id: m.id, calls: 0, state: m.state });
  }
  for (const c of calls) {
    const n = out.get(c.node) ?? { id: c.node, calls: 0 };
    n.calls += 1;
    out.set(c.node, n);
  }
  return [...out.values()].sort((a, b) => a.id.localeCompare(b.id));
}

/** Active calls: GET /api/v1/calls, refreshed every 5 s, with per-node counts. */
export function Calls() {
  const state = usePolling(listCalls, LIVE_REFRESH_MS);
  const cluster = usePolling(getCluster, LIVE_REFRESH_MS);
  const names = useExtensionNames();
  const now = useNow();
  const calls = state.status === "loading" ? undefined : state.data;
  const members =
    cluster.status === "loading" ? undefined : cluster.data?.members;

  const columns: TableColumn<ActiveCall>[] = [
    {
      key: "from",
      label: "From",
      render: (c) => <Party number={c.from} names={names} />,
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
      key: "started",
      label: "Started",
      mono: true,
      render: (c) => clock(c.startedAt),
    },
    {
      key: "duration",
      label: "Duration",
      align: "right",
      mono: true,
      render: (c) => (
        <span className="calls-strong">{callDuration(c, now)}</span>
      ),
    },
    { key: "node", label: "Node", mono: true },
    { key: "media", label: "Media", render: (c) => mediaLabel(c.media) },
    {
      key: "callId",
      label: "SIP Call-ID",
      mono: true,
      render: (c) => (
        <span className="calls-muted" title={c.sipCallId}>
          {shortCallId(c.sipCallId)}
        </span>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Activity"
        title="Active calls"
        subtitle="Calls in progress on every SIP node, from live state in Valkey."
        actions={<LiveMarker live={state.status === "ready"} />}
      />
      {state.status === "error" && (
        <Alert
          tone="bad"
          title="Could not load active calls"
          className="calls-detail-alert"
        >
          {state.message}
          {calls ? " Showing the last calls loaded." : ""}
        </Alert>
      )}
      {calls && (
        <div
          className="calls-nodes"
          aria-label="Calls per SIP node"
          role="list"
        >
          {callsPerNode(calls, members).map((n) => (
            <div key={n.id} className="az-card calls-node" role="listitem">
              <span className="calls-node__id">
                <span className={`az-dot calls-dot--${nodeTone(n.state)}`} />
                {n.id}
                <span className="visually-hidden">
                  {n.state ? `, ${n.state}` : ", not a cluster member"}, calls:
                </span>
              </span>
              <span className="calls-node__count">{n.calls}</span>
            </div>
          ))}
        </div>
      )}
      {state.status === "loading" && <Spinner label="Loading active calls" />}
      {calls && calls.length === 0 && (
        <EmptyState
          icon="phone-off"
          title="No calls in progress"
          description="Calls appear here as soon as a SIP node accepts them."
        />
      )}
      {calls && calls.length > 0 && (
        <Table
          caption="Active calls"
          columns={columns}
          rows={calls}
          rowKey={(c) => c.id}
        />
      )}
    </section>
  );
}
