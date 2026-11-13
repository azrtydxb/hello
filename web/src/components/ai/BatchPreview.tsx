import { Link } from "react-router";
import type { ProposalAction } from "../../api/aiagent";
import { Alert, Badge } from "../../design/azrty/components";

/** Where the canvas shows a batch proposal: its canvasId, else the list. */
export function canvasLinkOf(proposalId: string, a: ProposalAction): string {
  const body = a.body as { canvasId?: unknown } | undefined;
  const id = typeof body?.canvasId === "number" ? body.canvasId : null;
  return id === null
    ? "/flow"
    : `/flow/${id}?proposal=${encodeURIComponent(proposalId)}`;
}

/**
 * A proposal's configuration batch (spec flow-canvas S-19): each operation
 * as its dry run answered it, the warnings it introduces, and a link to see
 * it drawn on the call-flow canvas.
 */
export function BatchPreview({
  proposalId,
  action,
}: {
  proposalId: string;
  action: ProposalAction;
}) {
  const p = action.preview;
  const link = canvasLinkOf(proposalId, action);
  return (
    <div className="aix-stack">
      <p>
        <Link to={link}>
          {link === "/flow"
            ? "Open a call-flow canvas and show it from Ask AI"
            : "Show it on the call-flow canvas"}
        </Link>
      </p>
      {!p ? (
        <p className="aix-muted">No dry run was kept for this batch.</p>
      ) : (
        <>
          <ol className="aix-ops" aria-label="Batch operations">
            {p.results.map((r, i) => (
              <li key={i}>
                <Badge
                  tone={
                    r.status === "deleted"
                      ? "bad"
                      : r.status === "created"
                        ? "good"
                        : "outline"
                  }
                >
                  {r.status}
                </Badge>{" "}
                {r.operationId}
                {r.node ? ` ${r.node}` : ""}
                {r.diff.length > 0 &&
                  `: ${r.diff.map((d) => d.path).join(", ")}`}
              </li>
            ))}
          </ol>
          {p.warnings.length > 0 && (
            <Alert tone="warn" title="Warnings the batch introduces">
              <ul>
                {p.warnings.map((w, i) => (
                  <li key={i}>{w.message}</li>
                ))}
              </ul>
            </Alert>
          )}
        </>
      )}
    </div>
  );
}
