import { useEffect, useState } from "react";
import { Link, useParams } from "react-router";
import { errorMessage } from "../api";
import {
  applyProposal,
  DISMISS_REASONS,
  dismissProposal,
  getProposal,
  type AIProposal,
  type DismissReason,
} from "../api/aiagent";
import { AIGate } from "../components/ai/AIGate";
import { JsonDiff } from "../components/ai/JsonDiff";
import { PlainText } from "../components/ai/PlainText";
import { ProposalStatusBadge } from "../components/ai/ProposalCard";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
} from "../design/azrty/components";
import { Can } from "../role";
import "../components/ai/ai.css";

const isDelete = (op: string) => op.startsWith("delete");
const describe = (a: AIProposal["actions"][number]) =>
  `${a.operationId}${
    Object.keys(a.pathParams).length
      ? ` (${Object.entries(a.pathParams)
          .map(([k, v]) => `${k} ${v}`)
          .join(", ")})`
      : ""
  }`;

function Detail({ id }: { id: string }) {
  const [p, setP] = useState<AIProposal | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [dialog, setDialog] = useState<"apply" | "dismiss" | null>(null);
  const [understood, setUnderstood] = useState(false);
  const [reason, setReason] = useState<DismissReason>("not_needed");
  const [text, setText] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    const c = new AbortController();
    getProposal(id, c.signal).then(setP, (e: unknown) => {
      if (!c.signal.aborted) setLoadError(errorMessage(e));
    });
    return () => c.abort();
  }, [id]);

  async function run(fn: () => Promise<AIProposal>) {
    setBusy(true);
    setError(null);
    try {
      setP(await fn());
      setDialog(null);
    } catch (e) {
      setError(errorMessage(e));
      // A failed apply leaves the proposal failed: show its state.
      getProposal(id).then(setP, () => undefined);
      setDialog(null);
    } finally {
      setBusy(false);
    }
  }

  if (loadError)
    return (
      <Alert tone="bad" title="Could not load the proposal">
        {loadError}
      </Alert>
    );
  if (!p) return <Spinner label="Loading proposal…" />;
  const open = p.status === "open";
  const failedAt = p.failure;

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI · Proposals"
        title={p.title}
        description={<Link to="/ai/proposals">All proposals</Link>}
      />
      <div className="aix-stack">
        <div className="aix-card__head">
          <ProposalStatusBadge status={p.status} />
          <Badge tone="outline">from {p.source}</Badge>
          {p.hasDelete && (
            <Badge tone="bad" icon="trash-2">
              Deletes
            </Badge>
          )}
        </div>
        <PlainText text={p.rationale} />
        {p.hasDelete && (
          <Alert tone="bad" title="This proposal deletes configuration">
            Applying it removes the resources marked below. Check what refers to
            them first.
          </Alert>
        )}
        {error && (
          <Alert tone="bad" title="Action failed">
            {error}
          </Alert>
        )}
        {failedAt && (
          <Alert tone="bad" title={`Change ${failedAt.index + 1} failed`}>
            <PlainText
              text={`${failedAt.status} ${failedAt.code}: ${failedAt.message}`}
            />
            {failedAt.applied.length > 0 ? (
              <p>
                Already applied and not undone:{" "}
                {failedAt.applied
                  .map((i) => p.actions[i] && describe(p.actions[i]))
                  .filter(Boolean)
                  .join("; ")}
                .
              </p>
            ) : (
              <p>Nothing was applied.</p>
            )}
            <p>
              There is no retry: it would send the applied changes again. Make a
              new proposal.
            </p>
          </Alert>
        )}
        {p.status === "stale" && (
          <Alert tone="warn" title="Out of date">
            The configuration changed under this proposal. It cannot be applied.
          </Alert>
        )}
        {p.status === "dismissed" && (
          <p className="aix-muted">
            Dismissed: {p.dismissReason}
            {p.dismissText ? ` — ${p.dismissText}` : ""}
          </p>
        )}
        {p.actions.map((a, i) => (
          <article
            key={i}
            className={`aix-card${isDelete(a.operationId) ? " aix-card--delete" : ""}`}
            aria-label={`Change ${i + 1}`}
          >
            <div className="aix-card__head">
              <h2 className="aix-card__title">
                {i + 1}. {describe(a)}
              </h2>
              {isDelete(a.operationId) && (
                <Badge tone="bad" icon="trash-2">
                  Delete
                </Badge>
              )}
            </div>
            <JsonDiff
              before={a.before}
              after={isDelete(a.operationId) ? undefined : a.after}
              current={a.current}
              hasCurrent={"current" in a}
            />
          </article>
        ))}
        <Can>
          {open && (
            <div className="aix-actions">
              <Button
                icon="check"
                onClick={() => {
                  setUnderstood(false);
                  setDialog("apply");
                }}
              >
                Apply
              </Button>
              <Button variant="ghost" onClick={() => setDialog("dismiss")}>
                Dismiss
              </Button>
            </div>
          )}
        </Can>
      </div>
      {dialog === "apply" && (
        <Modal
          title="Apply this proposal?"
          description="These operations run in order with your credentials and are audited:"
          onClose={() => setDialog(null)}
          actions={
            <>
              <Button variant="ghost" onClick={() => setDialog(null)}>
                Cancel
              </Button>
              <Button
                variant={p.hasDelete ? "danger" : "primary"}
                disabled={busy || (p.hasDelete && !understood)}
                onClick={() => void run(() => applyProposal(p.id))}
              >
                {p.hasDelete ? "Apply and delete" : "Apply"}
              </Button>
            </>
          }
        >
          <ol className="aix-ops">
            {p.actions.map((a, i) => (
              <li key={i}>
                {isDelete(a.operationId) ? "Delete: " : ""}
                {describe(a)}
              </li>
            ))}
          </ol>
          {p.hasDelete && (
            <Checkbox
              label="I understand this deletes the resources listed above"
              checked={understood}
              onChange={(e) => setUnderstood(e.target.checked)}
            />
          )}
        </Modal>
      )}
      {dialog === "dismiss" && (
        <Modal
          title="Dismiss this proposal?"
          onClose={() => setDialog(null)}
          actions={
            <>
              <Button variant="ghost" onClick={() => setDialog(null)}>
                Cancel
              </Button>
              <Button
                disabled={busy}
                onClick={() =>
                  void run(() => dismissProposal(p.id, reason, text.trim()))
                }
              >
                Dismiss proposal
              </Button>
            </>
          }
        >
          <Select
            label="Reason"
            value={reason}
            onChange={(e) => setReason(e.target.value as DismissReason)}
            options={DISMISS_REASONS.map((r) => ({
              value: r.value,
              label: r.label,
            }))}
          />
          <Input
            label="Detail (optional)"
            value={text}
            maxLength={500}
            onChange={(e) => setText(e.target.value)}
          />
        </Modal>
      )}
    </section>
  );
}

/** One proposal with its diff, apply and dismiss. */
export function AIProposalDetail() {
  const { id = "" } = useParams();
  return (
    <AIGate>
      <Detail id={id} />
    </AIGate>
  );
}
