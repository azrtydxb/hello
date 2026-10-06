/**
 * Building blocks the call-flow pages (Trunks, Routes, Route tester, Ring
 * groups) share beyond the design system: the form-level error. Layout lives in callflow.css.
 */
import type { FieldError } from "../../api";
import { Alert } from "../../design/azrty/components";
import "./callflow.css";

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
