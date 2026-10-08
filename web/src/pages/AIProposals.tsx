import { useCallback, useState } from "react";
import { listProposals, PROPOSAL_STATUSES } from "../api/aiagent";
import { AIGate } from "../components/ai/AIGate";
import { ProposalCard } from "../components/ai/ProposalCard";
import {
  Alert,
  EmptyState,
  PageHeader,
  Select,
  Spinner,
} from "../design/azrty/components";
import { usePolling } from "../usePolling";
import "../components/ai/ai.css";

function Inbox() {
  const [status, setStatus] = useState("open");
  const load = useCallback(
    (s: AbortSignal) => listProposals(status, s),
    [status],
  );
  const state = usePolling(load, 15_000);
  const items = state.status === "loading" ? undefined : state.data;
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI"
        title="Proposals"
        description="Changes the assistant or the findings suggest. Nothing changes until an operator applies one."
      />
      <div className="aix-stack">
        <div className="aix-filters">
          <Select
            label="Status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            options={[{ value: "", label: "All" }, ...PROPOSAL_STATUSES]}
          />
        </div>
        {state.status === "loading" && <Spinner label="Loading proposals…" />}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load proposals">
            {state.message}
          </Alert>
        )}
        {items && items.length === 0 && (
          <EmptyState
            icon="inbox"
            title="No proposals"
            description="Nothing matches this status."
          />
        )}
        {items?.map((p) => (
          <ProposalCard key={p.id} proposal={p} />
        ))}
      </div>
    </section>
  );
}

/** Proposals inbox, filtered by status. */
export function AIProposals() {
  return (
    <AIGate>
      <Inbox />
    </AIGate>
  );
}
