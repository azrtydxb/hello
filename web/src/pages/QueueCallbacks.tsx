import { useCallback, useState } from "react";
import { useParams } from "react-router";
import {
  cancelQueueCallback,
  errorMessage,
  listQueueCallbacks,
  type QueueCallback,
  type QueueCallbackStatus,
} from "../api";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  LinkButton,
  PageHeader,
  Spinner,
  useToast,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import { Can } from "../role";

const STATUS_LABEL: Record<QueueCallbackStatus, string> = {
  waiting: "Waiting",
  dialling: "Calling",
  connected: "Connected",
  failed: "Failed",
  cancelled: "Cancelled",
  expired: "Expired",
};

const STATUS_TONE: Record<
  QueueCallbackStatus,
  "good" | "warn" | "bad" | "info"
> = {
  waiting: "info",
  dialling: "info",
  connected: "good",
  failed: "bad",
  cancelled: "warn",
  expired: "warn",
};

const time = (iso: string) =>
  new Date(iso).toLocaleString(undefined, { hour12: false });

/**
 * The callback requests of one queue (GET /api/v1/queues/{id}/callbacks):
 * who asked to be called back, where in the queue their place is kept, how
 * many attempts were made and how it ended. An operator cancels an open one.
 */
export function QueueCallbacks() {
  const { id = "" } = useParams();
  const load = useCallback(
    (signal: AbortSignal) => listQueueCallbacks(id, signal),
    [id],
  );
  const state = usePolling(load, LIVE_REFRESH_MS);
  const toast = useToast();
  const [error, setError] = useState<string | null>(null);

  async function cancel(cb: QueueCallback) {
    setError(null);
    try {
      await cancelQueueCallback(id, cb.id);
      toast.show(`Callback to ${cb.number} cancelled.`);
      state.reload();
    } catch (err: unknown) {
      setError(`Could not cancel the callback: ${errorMessage(err)}`);
    }
  }

  const items: QueueCallback[] =
    state.status === "loading" ? [] : (state.data ?? []);
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Queue callbacks"
        description="Callers who asked to be called back instead of waiting. A request keeps the place the caller had in the queue; Hello rings the caller first and the agent after the caller has answered. Three attempts, two minutes apart; a request expires after two hours."
        actions={
          <LinkButton to="/queues" variant="secondary" icon="arrow-left">
            Queues
          </LinkButton>
        }
      />
      <div className="cf-stack">
        {state.status === "loading" && <Spinner label="Loading callbacks…" />}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load the callbacks">
            {state.message}
          </Alert>
        )}
        {error && (
          <Alert tone="bad" title="Cancel failed">
            {error}
          </Alert>
        )}
        {state.status !== "loading" && items.length === 0 && (
          <EmptyState
            icon="phone-call"
            title="No callback requests"
            description="Turn the callback on in the queue's settings; callers are then offered one when the wait is long."
          />
        )}
        {items.length > 0 && (
          <table className="az-table" aria-label="Callback requests">
            <thead>
              <tr>
                <th>Number</th>
                <th>Status</th>
                <th>Attempts</th>
                <th>Place kept from</th>
                <th>Next attempt</th>
                <th>Result</th>
                <th>
                  <span className="az-sr-only">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((cb) => {
                const open =
                  cb.status === "waiting" || cb.status === "dialling";
                return (
                  <tr key={String(cb.id)}>
                    <td>
                      <span className="cf-mono">{cb.number}</span>
                      {cb.callerName ? ` ${cb.callerName}` : ""}
                    </td>
                    <td>
                      <Badge tone={STATUS_TONE[cb.status]}>
                        {STATUS_LABEL[cb.status]}
                      </Badge>
                    </td>
                    <td className="cf-mono">{cb.attempts} of 3</td>
                    <td className="cf-mono">
                      {time(new Date(cb.positionKey).toISOString())}
                    </td>
                    <td className="cf-mono">
                      {cb.status === "waiting" ? time(cb.nextAttemptAt) : "—"}
                    </td>
                    <td>{cb.agent ? `Agent ${cb.agent}` : cb.result || "—"}</td>
                    <td>
                      {open && (
                        <Can>
                          <Button
                            variant="ghost"
                            size="sm"
                            icon="x"
                            aria-label={`Cancel callback to ${cb.number}`}
                            onClick={() => void cancel(cb)}
                          >
                            Cancel
                          </Button>
                        </Can>
                      )}
                    </td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </div>
      {toast.node}
    </section>
  );
}
