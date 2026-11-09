// AI on the canvas (spec flow-canvas S-19): ask the assistant about this
// canvas (explain a flow, why a traced call went where it did) or for a
// change. A change comes back as a proposal whose configuration batch is
// drawn on the canvas as an overlay; the operator applies or discards it.
// Nothing is applied without that click. Viewers may look at proposals but
// not ask; with no model configured the panel says so.

import { useCallback, useEffect, useRef, useState } from "react";
import { Link } from "react-router";
import { errorMessage } from "../../../api";
import {
  applyProposal,
  batchActionOf,
  createSession,
  dismissProposal,
  getAIStatus,
  getProposal,
  listProposals,
  MESSAGE_LIMIT,
  type AIMessageContext,
  type AIProposal,
  type AIStatus,
} from "../../../api/aiagent";
import { streamMessage, type StreamEvent } from "../../../api/aistream";
import type { FlowGraph } from "../../../api/flow";
import type { FlowTraceQuery } from "../../../api/flowTrace";
import { AIOff } from "../../../components/ai/AIOff";
import { PlainText } from "../../../components/ai/PlainText";
import { ProposalStatusBadge } from "../../../components/ai/ProposalCard";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  Spinner,
} from "../../../design/azrty/components";
import { useCan } from "../../../role";
import { overlayCounts } from "./overlay";
import "./flowai.css";

export interface FlowAIPanelProps {
  canvasId: number | string;
  graph: FlowGraph;
  /** The node selected on the canvas, sent as context. */
  selected: string | null;
  /** The call traced on the canvas, sent as context. */
  trace: FlowTraceQuery | null;
  /** The proposal drawn on the canvas, or null. */
  preview: AIProposal | null;
  onPreview: (p: AIProposal | null) => void;
  /** An applied proposal: the canvas reloads and places what it created. */
  onApplied: (p: AIProposal) => Promise<void>;
  onClose: () => void;
}

type Status =
  { s: "loading" } | { s: "error"; m: string } | { s: "ok"; v: AIStatus };

interface Exchange {
  question: string;
  answer: string;
  step: string;
  done: boolean;
  error?: string;
  proposalId?: string | null;
}

/** The question context the canvas sends with every message. */
export function contextOf(
  canvasId: number | string,
  selected: string | null,
  trace: FlowTraceQuery | null,
): AIMessageContext {
  const c: AIMessageContext = { canvasId: Number(canvasId) };
  if (selected) c.nodes = [selected];
  if (trace) {
    c.trace = { from: trace.from, number: trace.number };
    if (trace.callerId) c.trace.callerId = trace.callerId;
    if (trace.at) c.trace.at = trace.at;
    if (trace.digits) c.trace.digits = trace.digits;
  }
  return c;
}

export function FlowAIPanel(props: FlowAIPanelProps) {
  const { onClose } = props;
  const [status, setStatus] = useState<Status>({ s: "loading" });
  useEffect(() => {
    const c = new AbortController();
    getAIStatus(c.signal).then(
      (v) => setStatus({ s: "ok", v }),
      (e: unknown) => {
        if (!c.signal.aborted) setStatus({ s: "error", m: errorMessage(e) });
      },
    );
    return () => c.abort();
  }, []);
  // Escape closes the panel, as it closes the node and trace panels.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      if (document.querySelector('[aria-modal="true"]')) return;
      onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  return (
    <aside className="flow-panel" aria-label="Ask AI">
      <Drawer
        inline
        open
        title="Ask AI"
        description="Explain this flow, ask why a call went somewhere, or describe a change. A change is shown on the canvas first; nothing is applied until you apply it."
        onClose={onClose}
        width="100%"
      >
        {status.s === "loading" && <Spinner label="Checking the AI agent…" />}
        {status.s === "error" && (
          <Alert tone="bad" title="Could not load the AI status">
            {status.m}
          </Alert>
        )}
        {status.s === "ok" && !status.v.enabled && (
          <AIOff reason={status.v.reason} />
        )}
        {status.s === "ok" && status.v.enabled && <Enabled {...props} />}
      </Drawer>
    </aside>
  );
}

function Enabled({
  canvasId,
  graph,
  selected,
  trace,
  preview,
  onPreview,
  onApplied,
}: FlowAIPanelProps) {
  const canAsk = useCan("operator");
  const [sessionId, setSessionId] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const [exchanges, setExchanges] = useState<Exchange[]>([]);
  const [busy, setBusy] = useState(false);
  const ctl = useRef<AbortController | null>(null);
  useEffect(() => () => ctl.current?.abort(), []);

  const label = (id: string) =>
    graph.nodes.find((n) => n.id === id)?.label ?? id;

  const update = (patch: Partial<Exchange>) =>
    setExchanges((xs) =>
      xs.map((x, i) => (i === xs.length - 1 ? { ...x, ...patch } : x)),
    );

  const showProposal = useCallback(
    async (id: string) => {
      try {
        const p = await getProposal(id);
        if (batchActionOf(p)) onPreview(p);
      } catch {
        // The answer still links to it.
      }
    },
    [onPreview],
  );

  async function ask(question: string) {
    const content = question.trim();
    if (!content || busy) return;
    setBusy(true);
    setDraft("");
    setExchanges((xs) => [
      ...xs,
      { question: content, answer: "", step: "Working…", done: false },
    ]);
    const c = new AbortController();
    ctl.current = c;
    try {
      let sid = sessionId;
      if (!sid) {
        sid = (await createSession(`Call flow canvas ${canvasId}`)).id;
        setSessionId(sid);
      }
      const onEvent = (e: StreamEvent) => {
        switch (e.type) {
          case "tool":
            update({ step: e.label });
            break;
          case "thinking":
            update({ step: "Thinking…" });
            break;
          case "delta":
            setExchanges((xs) =>
              xs.map((x, i) =>
                i === xs.length - 1 ? { ...x, answer: x.answer + e.text } : x,
              ),
            );
            break;
          case "reset":
            update({ answer: "" });
            break;
          default:
            break;
        }
      };
      const out = await streamMessage(
        sid,
        content,
        onEvent,
        c.signal,
        contextOf(canvasId, selected, trace),
      );
      switch (out.kind) {
        case "done":
          update({
            answer: out.event.message.content,
            done: true,
            proposalId: out.event.proposalId,
          });
          if (out.event.proposalId) void showProposal(out.event.proposalId);
          break;
        case "error":
          update({ done: true, error: out.event.message });
          break;
        case "dropped":
          update({
            done: true,
            error: "The connection closed before the answer was complete.",
          });
          break;
        case "accepted":
          update({
            done: true,
            answer: "",
            error: "The answer is being prepared; see it in the AI assistant.",
          });
          break;
      }
    } catch (e) {
      update({
        done: true,
        error: c.signal.aborted ? "Stopped." : errorMessage(e),
      });
    } finally {
      setBusy(false);
      ctl.current = null;
    }
  }

  const explain = selected
    ? `Explain the call flow from ${label(selected)} (${selected}): where calls go and why.`
    : "Explain the call flows on this canvas: where calls arrive and where they end up.";
  const why = trace
    ? `Why did this call go where it went? It came from ${trace.from} to ${trace.number}${
        trace.digits ? ` with keys ${trace.digits}` : ""
      }${trace.at ? ` at ${trace.at}` : ""}.`
    : null;

  return (
    <div className="flow-ai">
      {preview && (
        <ProposalReview
          proposal={preview}
          graph={graph}
          canApply={canAsk}
          onPreview={onPreview}
          onApplied={onApplied}
        />
      )}
      {!canAsk && (
        <Alert tone="info">
          Asking the assistant needs the operator role. You can look at the
          proposals below on the canvas.
        </Alert>
      )}
      {canAsk && (
        <>
          <div
            className="flow-ai__quick"
            role="group"
            aria-label="Quick questions"
          >
            <Button
              variant="secondary"
              size="sm"
              icon="message-square"
              disabled={busy}
              onClick={() => void ask(explain)}
            >
              {selected
                ? "Explain from the selected node"
                : "Explain this canvas"}
            </Button>
            <Button
              variant="secondary"
              size="sm"
              icon="route"
              disabled={busy || !why}
              title={why ? undefined : "Trace a call first"}
              onClick={() => why && void ask(why)}
            >
              Why did this call go here?
            </Button>
          </div>
          <form
            className="flow-ai__composer"
            onSubmit={(e) => {
              e.preventDefault();
              void ask(draft);
            }}
          >
            <label htmlFor="flow-ai-message">Ask, or describe a change</label>
            <textarea
              id="flow-ai-message"
              value={draft}
              maxLength={MESSAGE_LIMIT}
              placeholder="For example: after 18:00 send the main number to the voicemail of 100"
              onChange={(e) => setDraft(e.target.value)}
            />
            <div className="flow-ai__actions">
              <Button
                type="submit"
                icon="send"
                size="sm"
                disabled={busy || draft.trim() === ""}
              >
                Send
              </Button>
              {busy && (
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  onClick={() => ctl.current?.abort()}
                >
                  Stop
                </Button>
              )}
            </div>
          </form>
        </>
      )}
      {exchanges.length > 0 && (
        <ol className="flow-ai__log" aria-label="Answers" aria-live="polite">
          {exchanges.map((x, i) => (
            <li key={i}>
              <p className="flow-ai__q">{x.question}</p>
              {x.answer && <PlainText text={x.answer} />}
              {!x.done && <p className="flow-ai__step">{x.step}</p>}
              {x.error && (
                <Alert tone="bad" title="No answer">
                  {x.error}
                </Alert>
              )}
              {x.proposalId && (
                <p>
                  <Link to={`/ai/proposals/${x.proposalId}`}>
                    The proposal with its full diff
                  </Link>
                </p>
              )}
            </li>
          ))}
        </ol>
      )}
      <OpenProposals current={preview?.id ?? null} onPreview={onPreview} />
    </div>
  );
}

/** Open proposals that carry a batch, to show on this canvas. */
function OpenProposals({
  current,
  onPreview,
}: {
  current: string | null;
  onPreview: (p: AIProposal | null) => void;
}) {
  const [items, setItems] = useState<AIProposal[] | null>(null);
  useEffect(() => {
    const c = new AbortController();
    listProposals("open", c.signal).then(
      (ps) => setItems(ps.filter((p) => batchActionOf(p))),
      () => setItems([]),
    );
    return () => c.abort();
  }, [current]);
  if (!items || items.length === 0) return null;
  return (
    <section className="flow-panel__section" aria-label="Open proposals">
      <h3>Open proposals</h3>
      <ul className="flow-ai__proposals">
        {items.map((p) => (
          <li key={p.id}>
            <span>{p.title}</span>
            <Button
              size="sm"
              variant={p.id === current ? "primary" : "secondary"}
              aria-pressed={p.id === current}
              onClick={() => onPreview(p.id === current ? null : p)}
            >
              {p.id === current ? "Hide" : "Show on canvas"}
            </Button>
          </li>
        ))}
      </ul>
    </section>
  );
}

/** The proposal drawn on the canvas, with Apply and Discard. */
export function ProposalReview({
  proposal,
  graph,
  canApply,
  onPreview,
  onApplied,
}: {
  proposal: AIProposal;
  graph: FlowGraph;
  canApply: boolean;
  onPreview: (p: AIProposal | null) => void;
  onApplied: (p: AIProposal) => Promise<void>;
}) {
  const [dialog, setDialog] = useState<"apply" | "discard" | null>(null);
  const [message, setMessage] = useState("");
  const action = batchActionOf(proposal);
  const prev = action?.preview;
  if (!action || !prev) {
    return (
      <Alert tone="warn" title={proposal.title}>
        This proposal has no configuration batch to show on the canvas.{" "}
        <Link to={`/ai/proposals/${proposal.id}`}>Open it</Link>.
      </Alert>
    );
  }
  const counts = overlayCounts(prev.flowDiff);
  const open = proposal.status === "open";
  const moved = graph.configRevision !== prev.baseRevision;
  const ops = prev.results;

  const apply = async () => {
    const p = await applyProposal(proposal.id);
    setDialog(null);
    if (p.status === "applied") {
      await onApplied(p);
      return;
    }
    onPreview(p);
    setMessage(
      p.status === "stale"
        ? "The configuration changed under this proposal, so nothing was applied. Ask again."
        : `Nothing was applied: ${p.failure?.message ?? p.status}.`,
    );
  };

  return (
    <section
      className="flow-panel__section flow-ai__review"
      aria-label={`Proposal: ${proposal.title}`}
    >
      <h3>
        {proposal.title} <ProposalStatusBadge status={proposal.status} />
      </h3>
      <PlainText text={proposal.rationale} className="aix-muted" />
      <p>
        <Badge tone="good">{counts.added} new</Badge>{" "}
        <Badge tone="warn">{counts.changed} changed</Badge>{" "}
        <Badge tone="bad">{counts.removed} deleted</Badge>{" "}
        <Badge tone="outline">{counts.connections} connections</Badge>
      </p>
      <ol className="flow-ai__ops" aria-label="Operations">
        {ops.map((r, i) => (
          <li key={i}>
            {r.status} {r.node ?? r.operationId}
            {r.diff.length > 0 &&
              ` (${r.diff.length} ${r.diff.length === 1 ? "field" : "fields"})`}
          </li>
        ))}
      </ol>
      {prev.warnings.length > 0 && (
        <Alert tone="warn" title="Warnings the change introduces">
          <ul>
            {prev.warnings.map((w, i) => (
              <li key={i}>{w.message}</li>
            ))}
          </ul>
        </Alert>
      )}
      {open && moved && (
        <Alert tone="info">
          The configuration changed since this was proposed. Applying it is
          refused if anything it changes changed too.
        </Alert>
      )}
      {message && <Alert tone="warn">{message}</Alert>}
      <div className="flow-ai__actions">
        {open && canApply && (
          <>
            <Button
              size="sm"
              icon="check"
              variant={proposal.hasDelete ? "danger" : "primary"}
              onClick={() => setDialog("apply")}
            >
              Apply
            </Button>
            <Button
              size="sm"
              variant="ghost"
              onClick={() => setDialog("discard")}
            >
              Discard
            </Button>
          </>
        )}
        <Button size="sm" variant="secondary" onClick={() => onPreview(null)}>
          Hide from canvas
        </Button>
        <Link to={`/ai/proposals/${proposal.id}`}>Full diff</Link>
      </div>
      {dialog === "apply" && (
        <ConfirmDialog
          title="Apply this proposal?"
          description={`${ops.length} ${ops.length === 1 ? "operation runs" : "operations run"} in one transaction with your credentials and are audited. The batch can be reverted from the history.`}
          confirmLabel={proposal.hasDelete ? "Apply and delete" : "Apply"}
          confirmIcon="check"
          confirmVariant={proposal.hasDelete ? "danger" : "primary"}
          errorTitle="Could not apply"
          onConfirm={apply}
          onClose={() => setDialog(null)}
        >
          <ol className="flow-ai__ops">
            {ops.map((r, i) => (
              <li key={i}>
                {r.status === "deleted" ? "Delete: " : ""}
                {r.operationId}
                {r.node ? ` ${r.node}` : ""}
              </li>
            ))}
          </ol>
        </ConfirmDialog>
      )}
      {dialog === "discard" && (
        <ConfirmDialog
          title="Discard this proposal?"
          description="It is dismissed as not needed and leaves the canvas. Nothing was applied, so nothing else changes."
          confirmLabel="Discard"
          confirmVariant="secondary"
          errorTitle="Could not discard"
          onConfirm={async () => {
            await dismissProposal(proposal.id, "not_needed");
            setDialog(null);
            onPreview(null);
          }}
          onClose={() => setDialog(null)}
        />
      )}
    </section>
  );
}
