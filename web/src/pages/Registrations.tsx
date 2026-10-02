import { listRegistrations } from "../api";
import { LiveStatus } from "../components/LiveStatus";
import { formatTime } from "../format";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";

/** Registrations: live SIP bindings from GET /api/v1/registrations, refreshed every 5s. */
export function Registrations() {
  const state = usePolling(listRegistrations, LIVE_REFRESH_MS);
  const items = state.status === "loading" ? undefined : state.data;

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Registrations</h1>
      <LiveStatus state={state} what="registrations" />
      {items && items.length === 0 && (
        <p className="muted">No devices are registered.</p>
      )}
      {items && items.length > 0 && (
        <table aria-labelledby="page-title">
          <thead>
            <tr>
              <th scope="col">Extension</th>
              <th scope="col">Device</th>
              <th scope="col">Contact</th>
              <th scope="col">Source</th>
              <th scope="col">User agent</th>
              <th scope="col">Node</th>
              <th scope="col">Expires</th>
            </tr>
          </thead>
          <tbody>
            {items.map((b) => (
              <tr key={`${b.aor} ${b.contactUri}`}>
                <th scope="row">{b.extension}</th>
                <td>
                  <code>{b.device}</code>
                </td>
                <td className="wrap">
                  <code>{b.contactUri}</code>
                </td>
                <td>
                  {b.source} ({b.transport})
                </td>
                <td>{b.userAgent || "—"}</td>
                <td>{b.receivedNode}</td>
                <td>{formatTime(b.expires)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
