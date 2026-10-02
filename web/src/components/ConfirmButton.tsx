import { useEffect, useRef, useState, type KeyboardEvent } from "react";

interface ConfirmButtonProps {
  /** Text of the initial button, e.g. "Delete". */
  label: string;
  /** Accessible name of the initial button, e.g. "Delete extension 101". */
  accessibleLabel?: string;
  /** The question shown in place of the button, e.g. "Delete extension 101?". */
  prompt: string;
  confirmLabel: string;
  onConfirm: () => void | Promise<void>;
  disabled?: boolean;
}

/** A destructive action that asks for confirmation in place (no window.confirm). */
export function ConfirmButton({
  label,
  accessibleLabel,
  prompt,
  confirmLabel,
  onConfirm,
  disabled,
}: ConfirmButtonProps) {
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const cancelRef = useRef<HTMLButtonElement>(null);
  const triggerRef = useRef<HTMLButtonElement>(null);
  const returnFocus = useRef(false);

  useEffect(() => {
    if (confirming) {
      cancelRef.current?.focus();
    } else if (returnFocus.current) {
      returnFocus.current = false;
      triggerRef.current?.focus();
    }
  }, [confirming]);

  function cancel() {
    returnFocus.current = true;
    setConfirming(false);
  }

  async function confirm() {
    setBusy(true);
    try {
      await onConfirm();
    } finally {
      setBusy(false);
      setConfirming(false);
    }
  }

  function onKeyDown(e: KeyboardEvent) {
    if (e.key === "Escape") {
      e.stopPropagation();
      cancel();
    }
  }

  if (!confirming) {
    return (
      <button
        ref={triggerRef}
        type="button"
        className="danger"
        aria-label={accessibleLabel}
        disabled={disabled}
        onClick={() => setConfirming(true)}
      >
        {label}
      </button>
    );
  }
  return (
    <span
      className="confirm"
      role="group"
      aria-label={prompt}
      onKeyDown={onKeyDown}
    >
      <span className="confirm-prompt">{prompt}</span>
      <button
        type="button"
        className="danger"
        disabled={busy}
        onClick={() => void confirm()}
      >
        {confirmLabel}
      </button>
      <button ref={cancelRef} type="button" disabled={busy} onClick={cancel}>
        Cancel
      </button>
    </span>
  );
}
