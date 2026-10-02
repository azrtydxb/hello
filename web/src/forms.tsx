import type { ReactNode } from "react";
import type { FieldError } from "./api";

/** Error messages keyed by form field key (a JSON path such as "destinations[0].host"). */
export type ErrorMap = Readonly<Record<string, string>>;

/** Props a Field hands its control so the label, error and hint are wired up. */
export interface ControlProps {
  id: string;
  "aria-invalid"?: true;
  "aria-describedby"?: string;
}

// Server paths are relative to the item being saved and use the API's JSON
// names ("numberTransform.template", "trunks[0]"), so they are matched as
// they are. A path into some other item ("outbound[3].match") matches no
// field and is reported at form level.
function candidates(path: string): string[] {
  let p = path.trim();
  // "a.b[2].c" -> "a.b[2].c", "a.b[2]", "a.b", "a"
  const out: string[] = [];
  while (p) {
    out.push(p);
    const cut = Math.max(p.lastIndexOf("."), p.lastIndexOf("["));
    if (cut <= 0) break;
    p = p.slice(0, cut);
  }
  return out;
}

/**
 * Assign each server FieldError to the closest field the form shows, matching
 * case-insensitively and falling back to parent paths. Errors with no
 * matching field are returned in `unmatched`, for a form-level summary.
 */
export function mapFieldErrors(
  fields: readonly FieldError[],
  known: readonly string[],
): { byKey: ErrorMap; unmatched: FieldError[] } {
  const lower = new Map(known.map((k) => [k.toLowerCase(), k]));
  const byKey: Record<string, string> = {};
  const unmatched: FieldError[] = [];
  for (const fe of fields) {
    const key = candidates(fe.path)
      .map((c) => lower.get(c.toLowerCase()))
      .find((k) => k !== undefined);
    if (key === undefined) {
      unmatched.push(fe);
    } else {
      byKey[key] = byKey[key] ? `${byKey[key]} ${fe.message}` : fe.message;
    }
  }
  return { byKey, unmatched };
}

/** A DOM-safe id for a field key. */
export function fieldId(form: string, key: string): string {
  return `${form}-${key.replace(/[^A-Za-z0-9_-]+/g, "-")}`;
}

interface FieldProps {
  id: string;
  label: ReactNode;
  error?: string;
  hint?: ReactNode;
  className?: string;
  children: (props: ControlProps) => ReactNode;
}

/** A labelled control whose error (or hint) is linked by aria-describedby. */
export function Field({
  id,
  label,
  error,
  hint,
  className,
  children,
}: FieldProps) {
  const describedBy = error ? `${id}-error` : hint ? `${id}-hint` : undefined;
  return (
    <div className={className ? `field ${className}` : "field"}>
      <label htmlFor={id}>{label}</label>
      {children({
        id,
        "aria-invalid": error ? true : undefined,
        "aria-describedby": describedBy,
      })}
      {error ? (
        <p id={`${id}-error`} className="field-error">
          {error}
        </p>
      ) : (
        hint && (
          <p id={`${id}-hint`} className="hint">
            {hint}
          </p>
        )
      )}
    </div>
  );
}

/** A form-level error: the message plus any field errors with no field on screen. */
export function FormError({
  message,
  unmatched,
}: {
  message: string | null;
  unmatched: readonly FieldError[];
}) {
  if (!message && unmatched.length === 0) return null;
  return (
    <div role="alert" className="error">
      {message && <p>{message}</p>}
      {unmatched.length > 0 && (
        <ul>
          {unmatched.map((fe, i) => (
            <li key={i}>
              <code>{fe.path}</code>: {fe.message}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/** "1, 2,3" -> ["1","2","3"] (blank entries dropped). */
export function splitList(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((v) => v.trim())
    .filter((v) => v !== "");
}
