import {
  useCallback,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { Icon } from "./Icon";

/** How long a toast stays up, as in the console design. */
export const TOAST_MS = 3000;

/** Props for <Toast>. */
export interface ToastProps {
  /** The confirmation to show; null shows nothing. */
  message: ReactNode | null;
}

/**
 * The toast stack, bottom right: one short confirmation of a change. The
 * live region is always mounted so a new message is announced politely.
 */
export function Toast({ message }: ToastProps) {
  return (
    <div className="az-toast-stack" role="status" aria-live="polite">
      {message && (
        <div className="az-toast az-toast--good">
          <Icon name="circle-check" size={16} />
          <div className="az-toast__body">{message}</div>
        </div>
      )}
    </div>
  );
}

/** What useToast returns: `show(message)` and the node to render once. */
export interface ToastHandle {
  /** Shows `message`, replacing any toast still up, for TOAST_MS. */
  show: (message: string) => void;
  /** The <Toast> to render somewhere in the page. */
  node: ReactNode;
}

/** A page's toast: `toast.show("Trunk saved.")`, then render `toast.node`. */
export function useToast(): ToastHandle {
  const [message, setMessage] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const show = useCallback((text: string) => {
    clearTimeout(timer.current);
    setMessage(text);
    timer.current = setTimeout(() => setMessage(null), TOAST_MS);
  }, []);
  return { show, node: <Toast message={message} /> };
}
