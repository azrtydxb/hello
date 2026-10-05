/**
 * Building blocks the call-flow pages (Trunks, Routes, Route tester, Ring
 * groups) share: the design's page header, its toast, a confirm dialog, a
 * checkbox and the loading/error states. Design components and az- classes
 * only; layout lives in callflow.css.
 */
import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type InputHTMLAttributes,
  type ReactNode,
} from "react";
import type { FieldError } from "../../api";
import {
  Alert,
  Button,
  Icon,
  Modal,
  Spinner,
} from "../../design/azrty/components";
import "./callflow.css";

/** The design's page header: eyebrow, title, one-line description, actions. */
export function PageHeader({
  eyebrow = "Call flow",
  title,
  description,
  actions,
  back,
}: {
  eyebrow?: string;
  title: string;
  description: ReactNode;
  actions?: ReactNode;
  /** Shown above the title instead of the eyebrow (e.g. a back link). */
  back?: ReactNode;
}) {
  return (
    <div className="cf-head">
      <div>
        {back ?? <span className="az-eyebrow cf-head__eyebrow">{eyebrow}</span>}
        <h1 id="page-title" className="cf-head__title">
          {title}
        </h1>
        <p className="cf-head__desc">{description}</p>
      </div>
      {actions && <div className="cf-head__actions">{actions}</div>}
    </div>
  );
}

/** A short confirmation in the corner, announced politely; gone after 3 s. */
export function useToast() {
  const [message, setMessage] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const show = useCallback((msg: string) => {
    clearTimeout(timer.current);
    setMessage(msg);
    timer.current = setTimeout(() => setMessage(null), 3000);
  }, []);
  const node = (
    // The live region is always mounted so the message is announced.
    <div className="az-toast-stack" role="status" aria-live="polite">
      {message && (
        <div className="az-toast az-toast--good">
          <Icon name="circle-check" size={16} />
          <div className="az-toast__body">{message}</div>
        </div>
      )}
    </div>
  );
  return { show, node };
}

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

/** "Delete X?" as the design's modal: Cancel, then the destructive action. */
export function ConfirmDialog({
  title,
  description,
  confirmLabel,
  onConfirm,
  onClose,
}: {
  title: string;
  description: ReactNode;
  confirmLabel: string;
  /** Resolves when done; a rejection's message is shown in the dialog. */
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  useRestoreFocus();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
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
            disabled={busy}
            onClick={onClose}
          >
            Cancel
          </Button>
          <Button
            variant="danger"
            icon="trash-2"
            disabled={busy}
            onClick={() => {
              setBusy(true);
              setError(null);
              onConfirm().catch((err: unknown) => {
                setError(err instanceof Error ? err.message : String(err));
                setBusy(false);
              });
            }}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not delete">
          {error}
        </Alert>
      )}
    </Modal>
  );
}

/** The design system's checkbox (az-check with a box), for multi-choice rows. */
export function Checkbox({
  label,
  hint,
  ...rest
}: Omit<InputHTMLAttributes<HTMLInputElement>, "type"> & {
  label: ReactNode;
  hint?: ReactNode;
}) {
  return (
    <label className="az-check">
      <input type="checkbox" className="az-check__input" {...rest} />
      <span className="az-check__box">
        <Icon name="check" size={12} />
      </span>
      <span className="az-check__text">
        <span className="az-check__label">{label}</span>
        {hint && <span className="az-check__hint">{hint}</span>}
      </span>
    </label>
  );
}

/** A form-level error: the message plus field errors no field on screen shows. */
export function FormAlert({
  message,
  unmatched,
}: {
  message: string | null;
  unmatched: readonly FieldError[];
}) {
  if (!message && unmatched.length === 0) return null;
  return (
    <Alert tone="bad" title={message ?? "Could not save"}>
      {unmatched.length > 0 && (
        <ul className="cf-plain">
          {unmatched.map((fe, i) => (
            <li key={i}>
              <code>{fe.path}</code>: {fe.message}
            </li>
          ))}
        </ul>
      )}
    </Alert>
  );
}

/** "Loading trunks…" with the design's spinner, announced politely. */
export function Loading({ what }: { what: string }) {
  return (
    <div className="cf-loading" role="status" aria-live="polite">
      <Spinner size={16} />
      Loading {what}…
    </div>
  );
}

/** A section's own heading row inside a drawer or card. */
export function SectionLabel({
  children,
  id,
}: {
  children: ReactNode;
  id?: string;
}) {
  return (
    <span className="az-eyebrow cf-section-label" id={id}>
      {children}
    </span>
  );
}
