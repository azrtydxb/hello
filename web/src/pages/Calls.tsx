import { type CallHA, listCalls } from "../api";
import { LiveStatus } from "../components/LiveStatus";
import { formatTime } from "../format";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";

/** Active Calls: the live calls from GET /api/v1/calls, refreshed every 5s. */
export function Calls() {
  const state = usePolling(listCalls, LIVE_REFRESH_MS);
  const items = state.status === "loading" ? undefined : state.data;

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Active Calls</h1>
      <LiveStatus state={state} what="active calls" />
      {items && items.length === 0 && (
        <p className="muted">No calls in progress.</p>
      )}
      {items && items.length > 0 && (
        <table aria-labelledby="page-title">
          <thead>
            <tr>
              <th scope="col">From</th>
              <th scope="col">To</th>
              <th scope="col">State</th>
              <th scope="col">Started</th>
              <th scope="col">Answered</th>
              <th scope="col">Node</th>
              <th scope="col">Media</th>
              <th scope="col">HA</th>
            </tr>
          </thead>
          <tbody>
            {items.map((c) => (
              <tr key={c.id}>
                <th scope="row">{c.from}</th>
                <td>{c.to}</td>
                <td>{c.state}</td>
                <td>{formatTime(c.startedAt)}</td>
                <td>{formatTime(c.answeredAt)}</td>
                <td>{c.node}</td>
                <td>{c.media}</td>
                <td>
                  <HABadge ha={c.ha} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}

/**
 * The call's in-call HA state: a call a surviving node took over after its
 * owner died is badged, so operators see which calls survived a takeover.
 */
function HABadge({ ha }: { ha: CallHA }) {
  if (ha === "taken-over") {
    return (
      <span
        className="badge badge-taken-over"
        title="Re-homed onto this node after its owning node died"
      >
        Taken over
      </span>
    );
  }
  return <span className="muted">Owned</span>;
}
