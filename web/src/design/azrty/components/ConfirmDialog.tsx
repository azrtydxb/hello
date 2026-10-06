import { useEffect, useState, type ReactNode } from "react";
import { Alert } from "./Alert";
import { Button, type ButtonVariant } from "./Button";
import { Modal } from "./Modal";

/**
 * Returns focus to whatever opened a dialog when the dialog closes. The
 * dialog moves focus in itself (autoFocus on its first control).
 */
export function useRestoreFocus() {
  // Read during render: autoFocus moves focus before any effect runs.
  const [opener] = useState(() =>
    document.activeElement instanceof HTMLElement
      ? document.activeElement
      : null,
  );
  useEffect(
    () => () => {
      if (opener && opener.isConnected) opener.focus();
    },
    [opener],
  );
}

/** Props for <ConfirmDialog>. */
export interface ConfirmDialogProps {
  /** The question: "Delete trunk carrier-primary?". */
  title: string;
  /** What happens, in one or two sentences. */
  description: ReactNode;
  /** The verb on the confirming button: "Delete trunk". */
  confirmLabel: string;
  /** Resolves when done; a rejection's message is shown in the dialog. */
  onConfirm: () => Promise<void>;
  onClose: () => void;
  /** Icon on the confirming button. */
  confirmIcon?: string;
  /** "danger" for destructive actions (the default). */
  confirmVariant?: ButtonVariant;
  /** Title of the alert shown when onConfirm rejects. */
  errorTitle?: string;
  children?: ReactNode;
}

/**
 * The confirmation every destructive action asks for: a modal with Cancel
 * (focused first) and the action. Busy while onConfirm runs; a failure stays
 * in the dialog so the viewer can retry or cancel. Focus returns to the
 * opener when it closes.
 */
export function ConfirmDialog({
  title,
  description,
  confirmLabel,
  onConfirm,
  onClose,
  confirmIcon = "trash-2",
  confirmVariant = "danger",
  errorTitle = "Could not delete",
  children,
}: ConfirmDialogProps) {
  useRestoreFocus();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function confirm() {
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
    } catch (err: unknown) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={title}
      description={description}
      onClose={busy ? undefined : onClose}
      actions={
        <>
          <Button
            autoFocus
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={onClose}
          >
            Cancel
          </Button>
          <Button
            variant={confirmVariant}
            size="sm"
            icon={confirmIcon}
            disabled={busy}
            onClick={() => void confirm()}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {children}
      {error && (
        <Alert tone="bad" title={errorTitle}>
          {error}
        </Alert>
      )}
    </Modal>
  );
}
