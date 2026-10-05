import { useCallback, useEffect, useRef, useState } from "react";
import { Icon } from "../../design/azrty/components";

/** How long a toast stays up. */
export const TOAST_MS = 3000;

/** A confirmation toast: `show(message)` replaces any toast still showing. */
export function useToast() {
  const [message, setMessage] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  const show = useCallback((msg: string) => {
    clearTimeout(timer.current);
    setMessage(msg);
    timer.current = setTimeout(() => setMessage(null), TOAST_MS);
  }, []);
  useEffect(() => () => clearTimeout(timer.current), []);
  return { message, show };
}

/** The toast stack, bottom right; announced politely. */
export function Toast({ message }: { message: string | null }) {
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
