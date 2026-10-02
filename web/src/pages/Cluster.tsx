import { useCallback, useEffect, useRef, useState } from "react";
import {
  ApiError,
  drainNode,
  errorMessage,
  getCluster,
  undrainNode,
  type ClusterMember,
  type ClusterStatus,
} from "../api";
import { LiveStatus } from "../components/LiveStatus";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";

const STATE_LABEL: Record<string, string> = {
  JOINING: "Joining",
  READY: "Ready",
  DRAINING: "Draining",
  UNHEALTHY: "Unhealthy",
  OFFLINE: "Offline",
};

const KIND_LABEL: Record<string, string> = {
  sip: "SIP",
  control: "Control",
};

/** How far a node's configuration lags the current revision. */
export function revisionLag(current: number, member: ClusterMember): number {
  return Math.max(0, current - member.configRevision);
}

function lagText(lag: number): string {
  if (lag === 0) return "current";
  return `${lag} behind`;
}

/** Seconds between the heartbeat and `now`, as "4 s ago" / "3 min ago". */
export function heartbeatAge(heartbeat: string, now: Date): string {
  const t = new Date(heartbeat).getTime();
  if (Number.isNaN(t)) return "—";
  const s = Math.max(0, Math.round((now.getTime() - t) / 1000));
  if (s < 120) return `${s} s ago`;
  const m = Math.round(s / 60);
  if (m < 120) return `${m} min ago`;
  return `${Math.round(m / 60)} h ago`;
}

const canDrain = (m: ClusterMember) =>
  m.state !== "DRAINING" && m.state !== "OFFLINE";

/** Cluster: nodes, their state and load, dependencies, and drain controls. */
export function Cluster() {
  // Changing the loader restarts polling at once (after a drain or undrain).
  const [nonce, setNonce] = useState(0);
  const load = useCallback(
    (signal: AbortSignal) => {
      void nonce;
      return getCluster(signal);
    },
    [nonce],
  );
  const state = usePolling(load, LIVE_REFRESH_MS);
  const refresh = useCallback(() => setNonce((n) => n + 1), []);
  const [notice, setNotice] = useState<string | null>(null);

  const data: ClusterStatus | undefined =
    state.status === "loading" ? undefined : state.data;
  const now = state.status === "ready" ? state.updatedAt : new Date();
  const sipReady =
    data?.members.filter((m) => m.kind === "sip" && m.state === "READY")
      .length ?? 0;
  const hasSip = data?.members.some((m) => m.kind === "sip") ?? false;

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Cluster</h1>
      <LiveStatus state={state} what="cluster status" />
      <p className="muted" role="status" aria-live="polite">
        {notice}
      </p>
      {data && hasSip && sipReady === 0 && (
        <div role="alert" className="error">
          <strong>No SIP node is READY.</strong>
          <p>New calls are rejected (503) until a SIP node returns to READY.</p>
        </div>
      )}
      {data && (
        <>
          <h2 id="nodes-title">Nodes</h2>
          {data.members.length === 0 ? (
            <p className="muted">No nodes have reported in.</p>
          ) : (
            <div className="table-wrap">
              <table aria-labelledby="nodes-title">
                <thead>
                  <tr>
                    <th scope="col">Node</th>
                    <th scope="col">Kind</th>
                    <th scope="col">State</th>
                    <th scope="col">SIP address</th>
                    <th scope="col">Calls</th>
                    <th scope="col">Registrations</th>
                    <th scope="col">Version</th>
                    <th scope="col">Revision lag</th>
                    <th scope="col">Heartbeat</th>
                    <th scope="col">
                      <span className="visually-hidden">Actions</span>
                    </th>
                  </tr>
                </thead>
                <tbody>
                  {data.members.map((m) => (
                    <tr key={m.id}>
                      <th scope="row">{m.id}</th>
                      <td>{KIND_LABEL[m.kind] ?? m.kind}</td>
                      <td>
                        <span
                          className={`state state-${m.state.toLowerCase()}`}
                        >
                          {STATE_LABEL[m.state] ?? m.state}
                        </span>
                        {m.reason && (
                          <span className="reason"> — {m.reason}</span>
                        )}
                      </td>
                      <td>
                        {m.sipAddr ? <code>{m.sipAddr}</code> : "—"}
                        {m.transports && m.transports.length > 0 && (
                          <span className="muted">
                            {" "}
                            ({m.transports.join(", ")})
                          </span>
                        )}
                      </td>
                      <td>{m.activeCalls}</td>
                      <td>{m.registrations}</td>
                      <td>{m.version}</td>
                      <td>
                        {lagText(revisionLag(data.configRevision, m))}{" "}
                        <span className="muted">(rev {m.configRevision})</span>
                      </td>
                      <td>{heartbeatAge(m.heartbeat, now)}</td>
                      <td className="row-actions">
                        <DrainControl
                          member={m}
                          onDone={(message) => {
                            setNotice(message);
                            refresh();
                          }}
                        />
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </div>
          )}

          <h2 id="deps-title">Dependencies</h2>
          <table aria-labelledby="deps-title" className="deps">
            <thead>
              <tr>
                <th scope="col">Service</th>
                <th scope="col">Status</th>
                <th scope="col">Details</th>
              </tr>
            </thead>
            <tbody>
              <tr>
                <th scope="row">PostgreSQL</th>
                <td>
                  <UpDown up={data.postgres.up} />
                </td>
                <td>{data.postgres.error || "—"}</td>
              </tr>
              <tr>
                <th scope="row">Valkey</th>
                <td>
                  <UpDown up={data.valkey.up} />
                </td>
                <td>
                  {data.valkey.mode === "sentinel" ? "Sentinel" : "Single node"}
                  {data.valkey.primary && (
                    <>
                      , primary <code>{data.valkey.primary}</code>
                    </>
                  )}
                </td>
              </tr>
            </tbody>
          </table>

          <p>
            Configuration revision: <strong>{data.configRevision}</strong>
          </p>
        </>
      )}
    </section>
  );
}

function UpDown({ up }: { up: boolean }) {
  return (
    <span className={up ? "state state-up" : "state state-down"}>
      {up ? "Up" : "Down"}
    </span>
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
  const draining = !canDrain(m);
  const [step, setStep] = useState<Step>({ kind: "idle" });
  const [error, setError] = useState<string | null>(null);
  const firstAction = useRef<HTMLButtonElement>(null);
  const trigger = useRef<HTMLButtonElement>(null);
  const returnFocus = useRef(false);

  useEffect(() => {
    if (step.kind === "confirm" || step.kind === "warn") {
      firstAction.current?.focus();
    } else if (step.kind === "idle" && returnFocus.current) {
      returnFocus.current = false;
      trigger.current?.focus();
    }
  }, [step]);

  if (m.state === "OFFLINE") return null;

  function cancel() {
    returnFocus.current = true;
    setStep({ kind: "idle" });
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
    <div className="drain">
      {step.kind === "idle" && (
        <button
          ref={trigger}
          type="button"
          className={draining ? undefined : "danger"}
          aria-label={`${verb} ${m.id}`}
          onClick={() => setStep({ kind: "confirm" })}
        >
          {verb}
        </button>
      )}
      {step.kind === "confirm" && (
        <span
          className="confirm"
          role="group"
          aria-label={`${verb} ${m.id}?`}
          onKeyDown={(e) => e.key === "Escape" && cancel()}
        >
          <span className="confirm-prompt">
            {draining
              ? `Return ${m.id} to service?`
              : `Drain ${m.id}? It stops taking new calls; calls in progress continue.`}
          </span>
          <button
            ref={firstAction}
            type="button"
            className={draining ? "primary" : "danger"}
            onClick={() => void run(false)}
          >
            {draining ? "Undrain node" : "Drain node"}
          </button>
          <button type="button" onClick={cancel}>
            Cancel
          </button>
        </span>
      )}
      {step.kind === "warn" && (
        <div
          className="confirm warn-confirm"
          role="group"
          aria-label={`Drain ${m.id} anyway?`}
          onKeyDown={(e) => e.key === "Escape" && cancel()}
        >
          <p role="alert" className="warning">
            <strong>Warning:</strong> {step.message}
          </p>
          <span className="confirm-prompt">
            Draining anyway means new calls are rejected until a SIP node is
            READY again.
          </span>
          <button ref={firstAction} type="button" onClick={cancel}>
            Keep {m.id} in service
          </button>
          <button
            type="button"
            className="danger"
            onClick={() => void run(true)}
          >
            Drain anyway
          </button>
        </div>
      )}
      {step.kind === "busy" && (
        <span role="status" aria-live="polite">
          Working…
        </span>
      )}
      {error && (
        <p role="alert" className="error">
          Could not {verb.toLowerCase()} {m.id}: {error}
        </p>
      )}
    </div>
  );
}
