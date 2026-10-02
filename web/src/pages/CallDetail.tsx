import { useEffect, useState } from "react";
import { Link, useParams } from "react-router";
import { errorMessage, getCdr, type CdrDetail } from "../api";
import { TraceList } from "../components/TraceList";
import { formatDuration, formatTime } from "../format";

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; cdr: CdrDetail };

const DIRECTION_LABEL: Record<string, string> = {
  internal: "Internal",
  inbound: "Inbound",
  outbound: "Outbound",
};

/** A failed call: no answer, or a final status outside 2xx. */
export function isFailed(cdr: CdrDetail): boolean {
  return !cdr.answerTime || cdr.finalStatus < 200 || cdr.finalStatus >= 300;
}

/** One call's record: the CDR fields, its routing trace and, if it failed, why. */
export function CallDetail() {
  const { id = "" } = useParams();
  const [state, setState] = useState<State>({ status: "loading" });

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

  return (
    <section aria-labelledby="page-title">
      <p>
        <Link to="/history">← Call History</Link>
      </p>
      <h1 id="page-title">Call {id}</h1>
      {state.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading call…
        </p>
      )}
      {state.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load this call.</strong>
          <p>{state.message}</p>
        </div>
      )}
      {state.status === "ready" && <Detail cdr={state.cdr} />}
    </section>
  );
}

function Detail({ cdr }: { cdr: CdrDetail }) {
  const failed = isFailed(cdr);
  const rewritten =
    cdr.rewrittenDestination &&
    cdr.rewrittenDestination !== cdr.originalDestination;
  return (
    <>
      {cdr.explanation && (
        <div className={failed ? "error" : "notice"} role="note">
          <strong>{failed ? "Why it failed: " : "Note: "}</strong>
          {cdr.explanation}
        </div>
      )}
      <dl className="facts">
        <dt>Direction</dt>
        <dd>{DIRECTION_LABEL[cdr.direction] ?? cdr.direction ?? "—"}</dd>
        <dt>From</dt>
        <dd>{cdr.source}</dd>
        <dt>Destination</dt>
        <dd>
          {rewritten ? (
            <>
              {cdr.originalDestination} → {cdr.rewrittenDestination}
            </>
          ) : (
            cdr.originalDestination || cdr.destination
          )}
        </dd>
        <dt>Route</dt>
        <dd>{cdr.route || "—"}</dd>
        <dt>Trunk</dt>
        <dd>{cdr.trunk || "—"}</dd>
        <dt>Final status</dt>
        <dd>
          {cdr.finalStatus}
          {failed ? " (failed)" : ""}
        </dd>
        {cdr.failureReason && (
          <>
            <dt>Failure reason</dt>
            <dd>{cdr.failureReason}</dd>
          </>
        )}
        <dt>Ended by</dt>
        <dd>{cdr.terminationSide}</dd>
        <dt>Started</dt>
        <dd>{formatTime(cdr.startTime)}</dd>
        <dt>Rang</dt>
        <dd>{formatTime(cdr.ringTime)}</dd>
        <dt>Answered</dt>
        <dd>{formatTime(cdr.answerTime)}</dd>
        <dt>Ended</dt>
        <dd>{formatTime(cdr.endTime)}</dd>
        <dt>Duration</dt>
        <dd>{formatDuration(cdr.durationMs)}</dd>
        <dt>Billable</dt>
        <dd>{formatDuration(cdr.billableMs)}</dd>
        <dt>SIP node</dt>
        <dd>{cdr.sipNode}</dd>
        <dt>Media</dt>
        <dd>{cdr.mediaMode}</dd>
        <dt>SIP Call-ID</dt>
        <dd>
          <code>{cdr.sipCallId}</code>
        </dd>
        <dt>Correlation ID</dt>
        <dd>
          <code>{cdr.correlationId}</code>
        </dd>
      </dl>
      <h2 id="trace-title">Routing trace</h2>
      <TraceList trace={cdr.trace} labelledBy="trace-title" />
    </>
  );
}
