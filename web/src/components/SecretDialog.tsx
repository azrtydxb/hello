import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";

interface SecretDialogProps {
  title: string;
  /** Who the secret belongs to, e.g. the device's SIP username. */
  subject: string;
  secret: string;
  onClose: () => void;
}

type CopyState = "idle" | "copied" | "unavailable";

/**
 * Modal dialog showing a one-time secret. Focus moves into it on open, Tab
 * stays inside it, Escape closes it, and focus returns where it was.
 */
export function SecretDialog({
  title,
  subject,
  secret,
  onClose,
}: SecretDialogProps) {
  const titleId = useId();
  const noteId = useId();
  const inputId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const copyRef = useRef<HTMLButtonElement>(null);
  const [copy, setCopy] = useState<CopyState>("idle");

  useEffect(() => {
    const previous =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    copyRef.current?.focus();
    return () => {
      if (previous && previous.isConnected) previous.focus();
    };
  }, []);

  async function onCopy() {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("no clipboard");
      await navigator.clipboard.writeText(secret);
      setCopy("copied");
    } catch {
      setCopy("unavailable");
    }
  }

  function onKeyDown(e: KeyboardEvent<HTMLDivElement>) {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab" || !dialogRef.current) return;
    const focusable = Array.from(
      dialogRef.current.querySelectorAll<HTMLElement>("button, input"),
    ).filter((el) => !el.hasAttribute("disabled"));
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (!first || !last) return;
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  return (
    <div className="backdrop">
      <div
        ref={dialogRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        aria-describedby={noteId}
        onKeyDown={onKeyDown}
      >
        <h2 id={titleId}>{title}</h2>
        <p id={noteId} className="warning">
          Copy the SIP secret for <strong>{subject}</strong> now.{" "}
          <strong>You will not see this again</strong>; if it is lost, rotate
          the secret to get a new one.
        </p>
        <label htmlFor={inputId}>SIP secret</label>
        <input
          id={inputId}
          className="secret"
          readOnly
          value={secret}
          spellCheck={false}
          autoComplete="off"
          onFocus={(e) => e.currentTarget.select()}
        />
        <p className="dialog-status" role="status" aria-live="polite">
          {copy === "copied" && "Copied to the clipboard."}
          {copy === "unavailable" &&
            "Copying is not available here: select the secret and copy it manually."}
        </p>
        <div className="actions">
          <button
            ref={copyRef}
            type="button"
            className="primary"
            onClick={() => void onCopy()}
          >
            Copy secret
          </button>
          <button type="button" onClick={onClose}>
            Done
          </button>
        </div>
      </div>
    </div>
  );
}
