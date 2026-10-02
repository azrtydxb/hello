import type { TraceStep } from "../api";

/** Trace steps in step order (by `n`, not by array position). */
export function sortedSteps(trace: readonly TraceStep[]): TraceStep[] {
  return [...trace].sort((a, b) => a.n - b.n);
}

/** A routing trace as an ordered list, one sentence per step. */
export function TraceList({
  trace,
  labelledBy,
}: {
  trace: readonly TraceStep[];
  labelledBy: string;
}) {
  if (trace.length === 0) {
    return <p className="muted">No trace was recorded.</p>;
  }
  return (
    <ol className="trace" aria-labelledby={labelledBy}>
      {sortedSteps(trace).map((s) => (
        <li key={s.n} value={s.n}>
          {s.text}
        </li>
      ))}
    </ol>
  );
}
