import { useState } from "react";
import {
  errorMessage,
  listQueueStatus,
  setQueueAgent,
  type QueueAgentState,
  type QueueStatus,
  type QueueStatusAgent,
} from "../api";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  LiveTag,
  PageHeader,
  Spinner,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import { Can } from "../role";
import { useNow } from "./calls/common";

const STATE_LABEL: Record<QueueAgentState, string> = {
  available: "Available",
  on_call: "On a call",
  ringing: "Ringing",
  dnd: "Do not disturb",
  wrapping: "Wrapping up",
  paused: "Paused",
  logged_out: "Logged out",
};

const STATE_TONE: Record<QueueAgentState, "good" | "warn" | "bad" | "info"> = {
  available: "good",
  on_call: "info",
  ringing: "info",
  dnd: "warn",
  wrapping: "warn",
  paused: "warn",
  logged_out: "bad",
};

/** Seconds as m:ss (or h:mm:ss). */
export function mmss(total: number): string {
  const s = Math.max(0, Math.floor(total));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const pad = (n: number) => String(n).padStart(2, "0");
  return h > 0
    ? `${String(h)}:${pad(m)}:${pad(s % 60)}`
    : `${String(m)}:${pad(s % 60)}`;
}

/**
 * The live queue wallboard: waiting callers (position, wait), the longest
 * wait and every agent with a state, refreshed every 5 s like the other live
 * pages (GET /api/v1/queues/status).
 */
export function Wallboard() {
  const state = usePolling(listQueueStatus, LIVE_REFRESH_MS);
  const now = useNow();
  const items = state.status === "loading" ? undefined : state.data;
  const fetchedAt = state.status === "ready" ? state.updatedAt.getTime() : now;
  const elapsed = Math.max(0, (now - fetchedAt) / 1000);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Activity"
        title="Queue wallboard"
        description="Callers waiting in each queue and the agents serving them, from live state in Valkey."
        actions={<LiveTag every="5 s" live={state.status === "ready"} />}
      />
      <div className="cf-stack">
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load the queues">
            {state.message}
          </Alert>
        )}
        {state.status === "loading" && <Spinner label="Loading queues…" />}
        {items && items.length === 0 && (
          <EmptyState
            icon="headset"
            title="No queues"
            description="Create a queue under Call flow to see it here."
          />
        )}
        {items?.map((q) => (
          <QueuePanel
            key={q.name}
            q={q}
            elapsed={elapsed}
            onChanged={state.reload}
          />
        ))}
      </div>
    </section>
  );
}

function QueuePanel({
  q,
  elapsed,
  onChanged,
}: {
  q: QueueStatus;
  elapsed: number;
  onChanged: () => void;
}) {
  const longest = q.waiting.length > 0 ? q.longestWaitSeconds + elapsed : 0;
  return (
    <section
      className="az-card cf-card"
      aria-label={`Queue ${q.name}`}
      data-testid={`queue-${q.name}`}
    >
      <div className="cf-card__head cf-card__head--top">
        <div>
          <h2 className="cf-card__group-name">{q.name}</h2>
          <div className="cf-card__sub">
            {q.number ? (
              <>
                Number <span className="cf-mono">{q.number}</span> ·{" "}
              </>
            ) : null}
            {q.strategy}
          </div>
        </div>
        {!q.enabled && <Badge tone="warn">Disabled</Badge>}
      </div>
      <dl className="cf-stats">
        <div>
          <dt>Waiting</dt>
          <dd>{q.waiting.length}</dd>
        </div>
        <div>
          <dt>Longest wait</dt>
          <dd className="cf-mono">{mmss(longest)}</dd>
        </div>
        <div>
          <dt>Agents staffed</dt>
          <dd>{q.staffed}</dd>
        </div>
        <div>
          <dt>Available</dt>
          <dd>{q.available}</dd>
        </div>
      </dl>
      <h3 className="az-eyebrow">Waiting callers</h3>
      {q.waiting.length === 0 ? (
        <p className="cf-form__note">Nobody is waiting.</p>
      ) : (
        <table className="az-table" aria-label={`Callers waiting in ${q.name}`}>
          <thead>
            <tr>
              <th>#</th>
              <th>Caller</th>
              <th>Waiting</th>
              <th>Ringing</th>
            </tr>
          </thead>
          <tbody>
            {q.waiting.map((c) => (
              <tr key={c.callId}>
                <td className="cf-mono">{c.position}</td>
                <td>
                  <span className="cf-mono">{c.caller}</span>
                  {c.callerName ? ` ${c.callerName}` : ""}
                </td>
                <td className="cf-mono">{mmss(c.waitSeconds + elapsed)}</td>
                <td className="cf-mono">{c.offering || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <h3 className="az-eyebrow">Agents</h3>
      {q.agents.length === 0 ? (
        <p className="cf-form__note">The queue has no agents.</p>
      ) : (
        <table className="az-table" aria-label={`Agents of ${q.name}`}>
          <thead>
            <tr>
              <th>Agent</th>
              <th>State</th>
              <th>Calls</th>
              <th>
                <span className="az-sr-only">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {q.agents.map((a) => (
              <AgentRow key={a.extension} a={a} onChanged={onChanged} />
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

function AgentRow({
  a,
  onChanged,
}: {
  a: QueueStatusAgent;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  async function act(change: Parameters<typeof setQueueAgent>[1]) {
    setBusy(true);
    setError(null);
    try {
      await setQueueAgent(a.extension, change);
      onChanged();
    } catch (err: unknown) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }
  return (
    <tr>
      <td>
        <span className="cf-mono">{a.extension}</span> {a.name}
      </td>
      <td>
        <Badge tone={STATE_TONE[a.state]} dot>
          {STATE_LABEL[a.state]}
        </Badge>
        {a.paused && a.pauseReason ? ` (${a.pauseReason})` : ""}
        {a.wrapSeconds > 0 ? ` ${String(a.wrapSeconds)} s` : ""}
        {error && <span className="cf-form__error"> {error}</span>}
      </td>
      <td className="cf-mono">{a.callsAnswered}</td>
      <td>
        <Can>
          {a.dynamic && (
            <Button
              size="sm"
              variant="secondary"
              disabled={busy}
              aria-label={`${a.loggedIn ? "Log out" : "Log in"} agent ${a.extension}`}
              onClick={() => void act({ loggedIn: !a.loggedIn })}
            >
              {a.loggedIn ? "Log out" : "Log in"}
            </Button>
          )}
          {(a.loggedIn || !a.dynamic) && (
            <Button
              size="sm"
              variant="ghost"
              disabled={busy}
              aria-label={`${a.paused ? "Unpause" : "Pause"} agent ${a.extension}`}
              onClick={() =>
                void act(
                  a.paused
                    ? { paused: false }
                    : { paused: true, reason: "break" },
                )
              }
            >
              {a.paused ? "Unpause" : "Pause"}
            </Button>
          )}
        </Can>
      </td>
    </tr>
  );
}
