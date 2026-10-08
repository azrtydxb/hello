import { Link } from "react-router";
import { Badge, type BadgeTone } from "../../design/azrty/components";
import type { AIFinding, Severity } from "../../api/aiagent";
import { PlainText } from "./PlainText";

const SEV: Record<Severity, BadgeTone> = {
  info: "info",
  warning: "warn",
  critical: "bad",
};

function show(v: unknown): string {
  return typeof v === "string" ? v : JSON.stringify(v);
}

/** One finding: evidence, explanation (or "Not explained") and its actions. */
export function FindingCard({
  finding: f,
  actions,
}: {
  finding: AIFinding;
  actions?: React.ReactNode;
}) {
  const evidence = Object.entries(f.evidence ?? {});
  return (
    <article className="aix-card" aria-label={`Finding: ${f.title}`}>
      <div className="aix-card__head">
        <h2 className="aix-card__title">{f.title}</h2>
        <Badge tone={SEV[f.severity]}>{f.severity}</Badge>
        <Badge tone="outline">{f.status}</Badge>
      </div>
      <p className="aix-muted">
        {f.type} · {f.subject} · seen {f.occurrences}× · last{" "}
        {new Date(f.lastSeen).toLocaleString()}
      </p>
      {evidence.length > 0 && (
        <table className="aix-diff" aria-label="Evidence">
          <thead>
            <tr>
              <th scope="col">Evidence</th>
              <th scope="col">Value</th>
            </tr>
          </thead>
          <tbody>
            {evidence.map(([k, v]) => (
              <tr key={k}>
                <th scope="row" className="aix-mono">
                  {k}
                </th>
                <td className="aix-mono">{show(v)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {f.explanation ? (
        <dl className="aix-expl">
          <dt>Summary</dt>
          <dd>
            <PlainText text={f.explanation.summary} />
          </dd>
          <dt>Likely cause</dt>
          <dd>
            <PlainText text={f.explanation.likelyCause} />
          </dd>
          <dt>Next step</dt>
          <dd>
            <PlainText text={f.explanation.nextStep} />
          </dd>
        </dl>
      ) : (
        <p className="aix-muted">Not explained</p>
      )}
      {f.status === "dismissed" && f.dismissReason && (
        <p className="aix-muted">
          Dismissed: <PlainText text={f.dismissReason} className="aix-inline" />
        </p>
      )}
      <div className="aix-actions">
        {f.proposalId && (
          <Link to={`/ai/proposals/${f.proposalId}`}>View proposal</Link>
        )}
        {actions}
      </div>
    </article>
  );
}
