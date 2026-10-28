// Applied batches, newest first, with revert (spec S-14). A revert is shown
// as a dry run first: the same diff as an apply, then one confirmation.

import { useEffect, useState } from "react";
import { errorMessage } from "../../api";
import {
  listConfigBatches,
  revertConfigBatch,
  type BatchResponse,
  type BatchSummary,
} from "../../api/configBatch";
import {
  Alert,
  Badge,
  Button,
  Modal,
  Spinner,
} from "../../design/azrty/components";
import { ReviewDialog } from "./ReviewDialog";

export function HistoryDialog({
  canRevert,
  onClose,
  onReverted,
}: {
  canRevert: boolean;
  onClose: () => void;
  /** The configuration changed: reload the graph. */
  onReverted: () => void;
}) {
  const [items, setItems] = useState<BatchSummary[] | null>(null);
  const [error, setError] = useState("");
  const [preview, setPreview] = useState<{
    batch: BatchSummary;
    res: BatchResponse;
  } | null>(null);
  const [busy, setBusy] = useState("");

  useEffect(() => {
    const ctl = new AbortController();
    listConfigBatches(ctl.signal)
      .then(setItems)
      .catch((e: unknown) => {
        if (!ctl.signal.aborted) setError(errorMessage(e));
      });
    return () => ctl.abort();
  }, []);

  const startRevert = async (batch: BatchSummary) => {
    setBusy(batch.id);
    setError("");
    try {
      setPreview({ batch, res: await revertConfigBatch(batch.id, true) });
    } catch (e) {
      setError(`Cannot revert: ${errorMessage(e)}`);
    } finally {
      setBusy("");
    }
  };

  if (preview) {
    const when = new Date(preview.batch.createdAt).toLocaleString();
    return (
      <ReviewDialog
        title="Revert this change?"
        description={`Puts back what the change of ${when} by ${preview.batch.actor} did.`}
        items={preview.res.results.map((r) => ({
          title: `${r.status} ${r.node ?? r.operationId}`,
          result: r,
        }))}
        warnings={preview.res.warnings}
        skipped={preview.res.skipped}
        confirmLabel="Revert"
        onClose={() => setPreview(null)}
        onConfirm={async () => {
          await revertConfigBatch(preview.batch.id, false);
          onReverted();
          onClose();
        }}
      />
    );
  }

  return (
    <Modal
      title="Change history"
      description="Changes applied from the canvas, the API and the assistant."
      onClose={onClose}
      width={640}
    >
      {error && (
        <Alert tone="bad" title="History">
          {error}
        </Alert>
      )}
      {!items && !error && <Spinner label="Loading history" />}
      {items && items.length === 0 && <p>Nothing has been applied yet.</p>}
      {items && items.length > 0 && (
        <ul className="flow-panel__links" aria-label="Applied changes">
          {items.map((b) => (
            <li key={b.id} className="flow-item">
              <span>
                {new Date(b.createdAt).toLocaleString()} by {b.actor}:{" "}
                {b.opCount} {b.opCount === 1 ? "change" : "changes"}
                {b.via ? ` (${b.via})` : ""}{" "}
                {b.reverts && <Badge tone="info">Revert</Badge>}
                {b.revertedBy && <Badge tone="neutral">Reverted</Badge>}
              </span>
              {canRevert && !b.revertedBy && (
                <Button
                  size="sm"
                  variant="secondary"
                  icon="undo-2"
                  disabled={busy === b.id}
                  onClick={() => void startRevert(b)}
                  aria-label={`Revert the change of ${new Date(b.createdAt).toLocaleString()}`}
                >
                  Revert
                </Button>
              )}
            </li>
          ))}
        </ul>
      )}
    </Modal>
  );
}
