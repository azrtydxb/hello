import { useEffect, useState } from "react";
import {
  errorMessage,
  FEATURE_CODE_ACTIONS,
  FEATURE_CODE_PATTERN,
  listFeatureCodes,
  listPresence,
  updateFeatureCodes,
  fieldErrors,
  type DeviceState,
  type FeatureCode,
  type FeatureCodeAction,
} from "../api";
import {
  Field,
  fieldId,
  FormError,
  mapFieldErrors,
  type ErrorMap,
} from "../forms";
import type { FieldError } from "../api";
import { usePolling, LIVE_REFRESH_MS } from "../usePolling";
import { formatTime } from "../format";

const LOADING = (
  <p role="status" aria-live="polite">
    Loading…
  </p>
);

/** System: feature-code list editor plus the live presence list. */
export function System() {
  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">System</h1>
      <FeatureCodes />
      <Presence />
    </section>
  );
}

/** The feature-code editor: edit codes inline, save the whole list. */
function FeatureCodes() {
  const [list, setList] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; items: FeatureCode[] }
  >({ status: "loading" });
  const [rows, setRows] = useState<FeatureCode[]>([]);
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState<string | null>(null);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const id = (k: string) => fieldId("feature-codes", k);

  useEffect(() => {
    const controller = new AbortController();
    listFeatureCodes(controller.signal)
      .then((items) => {
        setList({ status: "ready", items });
        setRows(items);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  function validate(): boolean {
    const found: Record<string, string> = {};
    rows.forEach((r, i) => {
      if (!FEATURE_CODE_PATTERN.test(r.code.trim())) {
        found[`items[${i}].code`] = "Use * plus 2–4 digits or # (or ##).";
      }
      if (r.code.trim() !== "" && r.code !== r.code.trim()) {
        found[`items[${i}].code`] =
          found[`items[${i}].code`] ?? "Remove the spaces.";
      }
      if (!FEATURE_CODE_ACTIONS.includes(r.action)) {
        found[`items[${i}].action`] = "Choose an action.";
      }
    });
    const dupes = new Map<string, number>();
    rows.forEach((r, i) => {
      const code = r.code.trim();
      if (!FEATURE_CODE_PATTERN.test(code)) return;
      const n = dupes.get(code);
      if (n !== undefined) {
        found[`items[${i}].code`] = `${code} is used twice.`;
        found[`items[${n}].code`] =
          found[`items[${n}].code`] ?? `${code} is used twice.`;
      }
      dupes.set(code, i);
    });
    setErrors(found);
    return Object.keys(found).length > 0;
  }

  async function onSave() {
    setFormError(null);
    if (validate()) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      await updateFeatureCodes(
        rows.map((r) => ({
          ...r,
          code: r.code.trim(),
          argument: r.argument.trim(),
        })),
      );
      setList((prev) =>
        prev.status === "ready"
          ? {
              ...prev,
              items: rows.map((r) => ({
                ...r,
                code: r.code.trim(),
                argument: r.argument.trim(),
              })),
            }
          : prev,
      );
      setStatus("Feature codes saved.");
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), keys(rows));
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  function setRow(i: number, patch: Partial<FeatureCode>) {
    setRows((prev) => prev.map((r, j) => (j === i ? { ...r, ...patch } : r)));
    setStatus(null);
  }

  return (
    <>
      <h2 id="feature-codes">Feature codes</h2>
      <p className="muted">
        Codes are dialed during a call, e.g. <code>*78</code> for DND on.
        Changing the list replaces every code, so edit it as a whole.
      </p>
      {list.status === "loading" && LOADING}
      {list.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load the feature codes.</strong>
          <p>{list.message}</p>
        </div>
      )}
      {list.status === "ready" && (
        <form
          className="inline-form"
          aria-labelledby="feature-codes"
          onSubmit={(e) => {
            e.preventDefault();
            void onSave();
          }}
        >
          {status && (
            <p role="status" aria-live="polite">
              {status}
            </p>
          )}
          {rows.map((r, i) => (
            <div className="fields" key={i}>
              <Field
                id={id(`items[${i}].code`)}
                label={`Code ${i + 1}`}
                error={errors[`items[${i}].code`]}
              >
                {(p) => (
                  <input
                    {...p}
                    className="narrow mono"
                    spellCheck={false}
                    value={r.code}
                    onChange={(e) => setRow(i, { code: e.target.value })}
                  />
                )}
              </Field>
              <Field
                id={id(`items[${i}].action`)}
                label={`Action ${i + 1}`}
                error={errors[`items[${i}].action`]}
              >
                {(p) => (
                  <select
                    {...p}
                    value={r.action}
                    onChange={(e) =>
                      setRow(i, { action: e.target.value as FeatureCodeAction })
                    }
                  >
                    {FEATURE_CODE_ACTIONS.map((a) => (
                      <option key={a} value={a}>
                        {a}
                      </option>
                    ))}
                  </select>
                )}
              </Field>
              <Field
                id={id(`items[${i}].argument`)}
                label={`Argument ${i + 1}`}
                error={errors[`items[${i}].argument`]}
                hint="Optional; e.g. the target of a forward."
              >
                {(p) => (
                  <input
                    {...p}
                    className="mono"
                    spellCheck={false}
                    value={r.argument}
                    onChange={(e) => setRow(i, { argument: e.target.value })}
                  />
                )}
              </Field>
              <button
                type="button"
                className="align-end"
                aria-label={`Remove code ${r.code || i + 1}`}
                onClick={() => {
                  setRows((prev) => prev.filter((_, j) => j !== i));
                  setStatus(null);
                }}
              >
                Remove
              </button>
            </div>
          ))}
          <p>
            <button
              type="button"
              onClick={() => {
                setRows((prev) => [
                  ...prev,
                  { code: "", action: "voicemail", argument: "" },
                ]);
                setStatus(null);
              }}
            >
              Add code
            </button>
          </p>
          <FormError message={formError} unmatched={unmatched} />
          <div className="actions start">
            <button type="submit" className="primary" disabled={busy}>
              Save feature codes
            </button>
          </div>
        </form>
      )}
    </>
  );
}

/** Field keys the editor shows, for mapping server field errors. */
function keys(rows: readonly FeatureCode[]): string[] {
  return [
    "items",
    ...rows.flatMap((_, i) => [
      `items[${i}]`,
      `items[${i}].code`,
      `items[${i}].action`,
      `items[${i}].argument`,
    ]),
  ];
}

/** The live presence list, polled every few seconds. */
function Presence() {
  const presence = usePolling(listPresence, LIVE_REFRESH_MS);
  return (
    <>
      <h2 id="presence">Presence</h2>
      <p className="muted">
        Device states from the cluster, refreshed every few seconds.
      </p>
      {presence.status === "loading" && LOADING}
      {presence.status === "error" && !presence.data && (
        <div role="alert" className="error">
          <strong>Could not load presence.</strong>
          <p>{presence.message}</p>
        </div>
      )}
      {presence.status === "ready" ||
      (presence.status === "error" && presence.data) ? (
        <PresenceTable items={presence.data ?? []} />
      ) : null}
      {presence.status === "error" && presence.data && (
        <p role="alert" className="error">
          Refresh failed: {presence.message}
        </p>
      )}
    </>
  );
}

function PresenceTable({ items }: { items: readonly DeviceState[] }) {
  return (
    <div className="table-wrap">
      <table aria-labelledby="presence">
        <thead>
          <tr>
            <th scope="col">Device</th>
            <th scope="col">Extension</th>
            <th scope="col">State</th>
            <th scope="col">Updated</th>
          </tr>
        </thead>
        <tbody>
          {items.length === 0 && (
            <tr>
              <td colSpan={4} className="muted">
                No presence known.
              </td>
            </tr>
          )}
          {items.map((d) => (
            <tr key={d.device}>
              <th scope="row">{d.device}</th>
              <td>{d.extension}</td>
              <td>{d.state}</td>
              <td>{formatTime(d.updatedAt)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
