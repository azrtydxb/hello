import { useCallback, useState } from "react";
import { errorMessage } from "../api";
import {
  acknowledgeFinding,
  dismissFinding,
  FINDING_STATUSES,
  listFindings,
  SEVERITIES,
  type AIFinding,
} from "../api/aiagent";
import { AIGate } from "../components/ai/AIGate";
import { FindingCard } from "../components/ai/FindingCard";
import {
  Alert,
  Button,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
} from "../design/azrty/components";
import { Can } from "../role";
import { usePolling } from "../usePolling";
import "../components/ai/ai.css";

const ALL = "";

function Findings() {
  const [status, setStatus] = useState("open");
  const [severity, setSeverity] = useState(ALL);
  const load = useCallback(
    (signal: AbortSignal) => listFindings({ status, severity }, signal),
    [status, severity],
  );
  const state = usePolling(load, 15_000);
  const [dismissing, setDismissing] = useState<AIFinding | null>(null);
  const [reason, setReason] = useState("");
  const [error, setError] = useState<string | null>(null);
  const data = state.status === "loading" ? undefined : state.data;

  async function act(fn: () => Promise<unknown>) {
    setError(null);
    try {
      await fn();
      state.reload();
    } catch (e) {
      setError(errorMessage(e));
    }
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI"
        title="Findings"
        description={
          data
            ? `Health score ${data.healthScore} of 100. Detected from live state and call history.`
            : "Detected from live state and call history."
        }
      />
      <div className="aix-stack">
        <div className="aix-filters">
          <Select
            label="Status"
            value={status}
            onChange={(e) => setStatus(e.target.value)}
            options={[{ value: ALL, label: "All" }, ...FINDING_STATUSES]}
          />
          <Select
            label="Severity"
            value={severity}
            onChange={(e) => setSeverity(e.target.value)}
            options={[{ value: ALL, label: "All" }, ...SEVERITIES]}
          />
        </div>
        {error && (
          <Alert tone="bad" title="Action failed">
            {error}
          </Alert>
        )}
        {state.status === "loading" && <Spinner label="Loading findings…" />}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load findings">
            {state.message}
          </Alert>
        )}
        {data && data.items.length === 0 && (
          <EmptyState
            icon="circle-check"
            title="No findings"
            description="Nothing matches these filters."
          />
        )}
        {data?.items.map((f) => (
          <FindingCard
            key={f.id}
            finding={f}
            actions={
              <Can>
                {f.status === "open" && (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => void act(() => acknowledgeFinding(f.id))}
                  >
                    Acknowledge
                  </Button>
                )}
                {(f.status === "open" || f.status === "acknowledged") && (
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => {
                      setReason("");
                      setDismissing(f);
                    }}
                  >
                    Dismiss
                  </Button>
                )}
              </Can>
            }
          />
        ))}
      </div>
      {dismissing && (
        <Modal
          title={`Dismiss "${dismissing.title}"?`}
          description="It opens again if its severity rises."
          onClose={() => setDismissing(null)}
          actions={
            <>
              <Button variant="ghost" onClick={() => setDismissing(null)}>
                Cancel
              </Button>
              <Button
                disabled={reason.trim() === ""}
                onClick={() => {
                  const f = dismissing;
                  setDismissing(null);
                  void act(() => dismissFinding(f.id, reason.trim()));
                }}
              >
                Dismiss finding
              </Button>
            </>
          }
        >
          <Input
            label="Reason"
            value={reason}
            maxLength={500}
            onChange={(e) => setReason(e.target.value)}
            autoFocus
          />
        </Modal>
      )}
    </section>
  );
}

/** Findings: what the detectors found, explained by the model where it could. */
export function AIFindings() {
  return (
    <AIGate>
      <Findings />
    </AIGate>
  );
}
