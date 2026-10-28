// The diff the user confirms before anything is applied (spec S-13): every
// change field by field, the warnings the batch introduces, and one button.
// Apply is never sent without it.

import type { ReactNode } from "react";
import type { BatchResult, BatchWarning } from "../../api/configBatch";
import { Alert, ConfirmDialog } from "../../design/azrty/components";

export interface ReviewItem {
  title: string;
  result?: BatchResult;
}

const show = (v: unknown): string => {
  if (v === undefined) return "";
  if (typeof v === "string") return v === "" ? '""' : v;
  return JSON.stringify(v);
};

export function ReviewDialog({
  title,
  description,
  items,
  warnings,
  skipped,
  confirmLabel,
  onConfirm,
  onClose,
  children,
}: {
  title: string;
  description: ReactNode;
  items: ReviewItem[];
  warnings: BatchWarning[];
  skipped?: string[];
  confirmLabel: string;
  onConfirm: () => Promise<void>;
  onClose: () => void;
  children?: ReactNode;
}) {
  return (
    <ConfirmDialog
      title={title}
      description={description}
      confirmLabel={confirmLabel}
      confirmIcon="check"
      confirmVariant="primary"
      errorTitle="Could not apply"
      onConfirm={onConfirm}
      onClose={onClose}
    >
      <div className="flow-review">
        {children}
        {items.map((it, i) => (
          <section key={i} aria-label={it.title}>
            <h3>{it.title}</h3>
            {it.result && it.result.diff.length > 0 ? (
              <table className="flow-diff">
                <thead>
                  <tr>
                    <th scope="col">Field</th>
                    <th scope="col">Before</th>
                    <th scope="col">After</th>
                  </tr>
                </thead>
                <tbody>
                  {it.result.diff.map((d) => (
                    <tr key={d.path}>
                      <td>{d.path}</td>
                      <td className="flow-diff__before">{show(d.before)}</td>
                      <td className="flow-diff__after">{show(d.after)}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p>
                {it.result?.status === "unchanged"
                  ? "No change."
                  : it.result?.status === "created"
                    ? "A new object."
                    : it.result?.status === "deleted"
                      ? "The object is removed."
                      : "No field changes."}
              </p>
            )}
          </section>
        ))}
        {warnings.length > 0 && (
          <Alert tone="warn" title="Warnings this introduces">
            <ul>
              {warnings.map((w, i) => (
                <li key={i}>{w.message}</li>
              ))}
            </ul>
          </Alert>
        )}
        {skipped && skipped.length > 0 && (
          <Alert tone="info" title="Not put back">
            <ul>
              {skipped.map((s, i) => (
                <li key={i}>{s}</li>
              ))}
            </ul>
          </Alert>
        )}
      </div>
    </ConfirmDialog>
  );
}
