import { useEffect, useState } from "react";
import { useParams } from "react-router";
import { errorMessage } from "../api";
import { getCallRecord, type CallRecord } from "../api/calls";
import {
  Alert,
  Badge,
  LinkButton,
  PageHeader,
  type Property,
  PropertyList,
  Spinner,
} from "../design/azrty/components";
import { formatDuration } from "../format";
import {
  clock,
  day,
  directionOf,
  isFailed,
  RoutingTrace,
  statusTone,
} from "./calls/common";

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; cdr: CallRecord };

/** One call's record: timeline, the CDR fields, its routing trace and, if it failed, why. */
export function CallDetail() {
  const { id = "" } = useParams();
  const [state, setState] = useState<State>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    getCallRecord(id, controller.signal)
      .then((cdr) => setState({ status: "ready", cdr }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [id]);

  const cdr = state.status === "ready" ? state.cdr : undefined;
  const back = (
    <LinkButton
      to="/history"
      variant="ghost"
      size="sm"
      icon="arrow-left"
      className="calls-back"
    >
      Call history
    </LinkButton>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        back={back}
        title={
          <>
            Call {id}
            {cdr && (
              <Badge tone={statusTone(cdr.finalStatus)} className="calls-mono">
                {cdr.finalStatus}
              </Badge>
            )}
          </>
        }
        description={cdr && <Subtitle cdr={cdr} />}
        actions={cdr && <Actions cdr={cdr} />}
      />
      {state.status === "loading" && <Spinner label="Loading call" />}
      {state.status === "error" && (
        <Alert tone="bad" title="Could not load this call">
          {state.message}
        </Alert>
      )}
      {cdr && <Detail cdr={cdr} />}
    </section>
  );
}

function Subtitle({ cdr }: { cdr: CallRecord }) {
  return (
    <>
      {directionOf(cdr).label} · {day(cdr.startTime)} {clock(cdr.startTime)} ·{" "}
      <span className="calls-mono">{cdr.source}</span> →{" "}
      <span className="calls-mono">
        {cdr.originalDestination || cdr.destination}
      </span>
    </>
  );
}

function Actions({ cdr }: { cdr: CallRecord }) {
  const number = cdr.originalDestination || cdr.destination;
  const retest = new URLSearchParams({ from: cdr.source, number });
  return (
    <>
      <LinkButton to="/diagnostics?tab=trace" icon="activity">
        SIP trace
      </LinkButton>
      <LinkButton to={`/routes/test?${retest.toString()}`} icon="flask-conical">
        Re-test this number
      </LinkButton>
    </>
  );
}

interface Moment {
  label: string;
  value: string;
  tone: "muted" | "good" | "bad" | "off";
}

/** Started, rang, answered and ended, each "—" when it did not happen. */
export function timeline(cdr: CallRecord): Moment[] {
  const failed = isFailed(cdr);
  return [
    { label: "Started", value: clock(cdr.startTime), tone: "muted" },
    {
      label: "Rang",
      value: clock(cdr.ringTime),
      tone: cdr.ringTime ? "muted" : "off",
    },
    {
      label: "Answered",
      value: clock(cdr.answerTime),
      tone: cdr.answerTime ? "good" : "off",
    },
    {
      label: "Ended",
      value: failed
        ? clock(cdr.endTime)
        : `after ${formatDuration(cdr.durationMs)}`,
      tone: failed ? "bad" : "muted",
    },
  ];
}

function Detail({ cdr }: { cdr: CallRecord }) {
  const failed = isFailed(cdr);
  const orig = cdr.originalDestination || cdr.destination;
  const rewritten =
    cdr.rewrittenDestination && cdr.rewrittenDestination !== orig;
  const explanation = cdr.explanation || cdr.failureReason;
  const facts: Property[] = [
    { label: "Direction", value: directionOf(cdr).label },
    { label: "From", value: cdr.source, mono: true },
    {
      label: "Destination",
      value: rewritten ? `${orig} → ${cdr.rewrittenDestination}` : orig,
      mono: true,
    },
    { label: "Route", value: cdr.route || "—" },
    { label: "Trunk", value: cdr.trunk || "—", mono: !!cdr.trunk },
    {
      label: "Final status",
      value: `${cdr.finalStatus}${failed ? " (failed)" : ""}`,
    },
    ...(cdr.failureReason
      ? [{ label: "Failure reason", value: cdr.failureReason }]
      : []),
    {
      label: "Ended by",
      value: cdr.terminationSide || "—",
    },
    { label: "Duration", value: formatDuration(cdr.durationMs) },
    { label: "Billable", value: formatDuration(cdr.billableMs) },
    { label: "SIP node", value: cdr.sipNode || "—", mono: true },
    { label: "Media", value: cdr.mediaMode || "—" },
    { label: "SIP Call-ID", value: cdr.sipCallId || "—", mono: true },
    { label: "Correlation ID", value: cdr.correlationId || "—", mono: true },
  ];

  return (
    <>
      {failed && explanation && (
        <Alert tone="bad" title="Why it failed" className="calls-detail-alert">
          {explanation}
        </Alert>
      )}
      {!failed && cdr.note && (
        <Alert tone="info" title="Note" className="calls-detail-alert">
          {cdr.note}
        </Alert>
      )}
      <div className="calls-detail">
        <div className="calls-col">
          <div className="az-card calls-card">
            <h2 className="calls-card__title" id="timeline-title">
              Timeline
            </h2>
            <dl className="calls-timeline" aria-labelledby="timeline-title">
              {timeline(cdr).map((m) => (
                <div key={m.label} className="calls-timeline__step">
                  <span className="calls-timeline__rail" aria-hidden="true">
                    <span className={`az-dot calls-dot--${m.tone}`} />
                    <span className="calls-timeline__line" />
                  </span>
                  <dt className="az-eyebrow">{m.label}</dt>
                  <dd className="calls-timeline__value">{m.value}</dd>
                </div>
              ))}
            </dl>
          </div>
          <div className="az-card calls-card">
            <h2 className="calls-card__title">Record</h2>
            <PropertyList items={facts} />
          </div>
        </div>
        <div className="az-card calls-card">
          <h2 className="calls-card__title" id="trace-title">
            Routing trace
          </h2>
          <RoutingTrace
            trace={cdr.trace}
            failed={failed}
            labelledBy="trace-title"
          />
        </div>
      </div>
    </>
  );
}
