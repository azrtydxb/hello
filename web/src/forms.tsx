import type { FieldError } from "./api";

/** Error messages keyed by form field key (a JSON path such as "destinations[0].host"). */
export type ErrorMap = Readonly<Record<string, string>>;

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

/** "1, 2,3" -> ["1","2","3"] (blank entries dropped). */
export function splitList(value: string): string[] {
  return value
    .split(/[\s,]+/)
    .map((v) => v.trim())
    .filter((v) => v !== "");
}
