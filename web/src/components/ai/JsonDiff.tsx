import { Badge } from "../../design/azrty/components";

type Flat = Map<string, string>;

function isObj(v: unknown): v is Record<string, unknown> {
  return typeof v === "object" && v !== null && !Array.isArray(v);
}

/** Dotted paths to leaf values; arrays and empty objects are leaves. */
export function flatten(v: unknown, prefix = "", out: Flat = new Map()): Flat {
  if (isObj(v) && Object.keys(v).length > 0) {
    for (const k of Object.keys(v).sort())
      flatten(v[k], prefix ? `${prefix}.${k}` : k, out);
  } else if (v !== undefined && v !== null) {
    out.set(prefix || "(value)", typeof v === "string" ? v : JSON.stringify(v));
  }
  return out;
}

/**
 * Before, after and (when given) the target as it is now. A field whose
 * current value differs from `before` is marked "changed since"; a field
 * the proposal removes is marked "removed" and one it adds "added".
 */
export function JsonDiff({
  before,
  after,
  current,
  hasCurrent = current !== undefined,
}: {
  before: unknown;
  after: unknown;
  current?: unknown;
  hasCurrent?: boolean;
}) {
  const b = flatten(before);
  const a = flatten(after);
  const c = flatten(current);
  const keys = [
    ...new Set([...b.keys(), ...a.keys(), ...(hasCurrent ? c.keys() : [])]),
  ].sort();
  if (keys.length === 0) return <p className="aix-muted">No fields.</p>;
  return (
    <table className="aix-diff">
      <thead>
        <tr>
          <th scope="col">Field</th>
          <th scope="col">Before</th>
          <th scope="col">After</th>
          {hasCurrent && <th scope="col">Now</th>}
        </tr>
      </thead>
      <tbody>
        {keys.map((k) => {
          const bv = b.get(k);
          const av = a.get(k);
          const cv = c.get(k);
          const kind =
            bv === undefined
              ? "added"
              : av === undefined
                ? "removed"
                : bv !== av
                  ? "changed"
                  : "same";
          const drift = hasCurrent && cv !== bv;
          return (
            <tr
              key={k}
              className={`aix-diff__row aix-diff__row--${kind}`}
              data-diff={kind}
            >
              <th scope="row" className="aix-mono">
                {k}
              </th>
              <td className="aix-mono">{bv ?? "—"}</td>
              <td className="aix-mono">
                {av ?? "—"}{" "}
                {kind !== "same" && (
                  <Badge tone={kind === "removed" ? "bad" : "info"}>
                    {kind}
                  </Badge>
                )}
              </td>
              {hasCurrent && (
                <td
                  className="aix-mono"
                  data-drift={drift ? "true" : undefined}
                >
                  {cv ?? "—"}{" "}
                  {drift && (
                    <Badge tone="warn" icon="triangle-alert">
                      changed since
                    </Badge>
                  )}
                </td>
              )}
            </tr>
          );
        })}
      </tbody>
    </table>
  );
}
