import { Link } from "react-router";
import { Badge, type BadgeTone } from "../../design/azrty/components";
import type { AIProposal, ProposalStatus } from "../../api/aiagent";
import { PlainText } from "./PlainText";

const TONES: Record<ProposalStatus, BadgeTone> = {
  open: "info",
  applied: "good",
  failed: "bad",
  stale: "warn",
  dismissed: "neutral",
  superseded: "neutral",
};

export function ProposalStatusBadge({ status }: { status: ProposalStatus }) {
  return <Badge tone={TONES[status]}>{status}</Badge>;
}

/** A proposal in a list or inline in a chat; a delete is marked. */
export function ProposalCard({ proposal }: { proposal: AIProposal }) {
  const n = proposal.actions.length;
  return (
    <article
      className={`aix-card${proposal.hasDelete ? " aix-card--delete" : ""}`}
      aria-label={`Proposal: ${proposal.title}`}
    >
      <div className="aix-card__head">
        <Link to={`/ai/proposals/${proposal.id}`} className="aix-card__title">
          {proposal.title}
        </Link>
        <ProposalStatusBadge status={proposal.status} />
        {proposal.hasDelete && (
          <Badge tone="bad" icon="trash-2">
            Deletes
          </Badge>
        )}
      </div>
      <PlainText text={proposal.rationale} className="aix-muted" />
      <p className="aix-muted">
        {n} {n === 1 ? "change" : "changes"} · from {proposal.source} ·{" "}
        {new Date(proposal.createdAt).toLocaleString()}
      </p>
    </article>
  );
}
