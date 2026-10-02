import { useEffect, useState } from "react";
import { errorMessage, listCdrs, type CdrPage } from "../api";
import { formatDuration, formatTime } from "../format";

export const PAGE_SIZE = 50;

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; page: CdrPage };

/** Call History: CDRs newest first, paged with the `next` cursor. */
export function History() {
  // Cursors of the pages visited: "" is the newest page; the last is shown.
  const [cursors, setCursors] = useState<string[]>([""]);
  const [state, setState] = useState<State>({ status: "loading" });
  const before = cursors[cursors.length - 1] ?? "";

  useEffect(() => {
    const controller = new AbortController();
    listCdrs(
      { before: before || undefined, limit: PAGE_SIZE },
      controller.signal,
    )
      .then((page) => setState({ status: "ready", page }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [before]);

  function go(next: string[]) {
    setState({ status: "loading" });
    setCursors(next);
  }

  const page = state.status === "ready" ? state.page : undefined;

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Call History</h1>
      {state.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading call history…
        </p>
      )}
      {state.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load call history.</strong>
          <p>{state.message}</p>
        </div>
      )}
      {page && page.items.length === 0 && (
        <p className="muted">
          No calls recorded{cursors.length > 1 ? " before these" : ""}.
        </p>
      )}
      {page && page.items.length > 0 && (
        <table aria-labelledby="page-title">
          <thead>
            <tr>
              <th scope="col">Started</th>
              <th scope="col">From</th>
              <th scope="col">To</th>
              <th scope="col">Status</th>
              <th scope="col">Duration</th>
              <th scope="col">Billable</th>
              <th scope="col">Ended by</th>
              <th scope="col">Reason</th>
              <th scope="col">Node</th>
            </tr>
          </thead>
          <tbody>
            {page.items.map((cdr) => (
              <tr key={cdr.id}>
                <th scope="row">{formatTime(cdr.startTime)}</th>
                <td>{cdr.source}</td>
                <td>{cdr.destination}</td>
                <td>{cdr.finalStatus}</td>
                <td>{formatDuration(cdr.durationMs)}</td>
                <td>{formatDuration(cdr.billableMs)}</td>
                <td>{cdr.terminationSide}</td>
                <td>{cdr.failureReason || "—"}</td>
                <td>{cdr.sipNode}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <nav className="pager" aria-label="Call history pages">
        <button
          type="button"
          disabled={cursors.length === 1 || state.status === "loading"}
          onClick={() => go(cursors.slice(0, -1))}
        >
          Newer
        </button>
        <button
          type="button"
          disabled={!page?.next}
          onClick={() => page?.next && go([...cursors, page.next])}
        >
          Older
        </button>
      </nav>
    </section>
  );
}
