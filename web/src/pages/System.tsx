import { useEffect, useState } from "react";
import {
  errorMessage,
  FEATURE_CODE_ACTIONS,
  FEATURE_CODE_PATTERN,
  fieldErrors,
  listFeatureCodes,
  listPresence,
  updateFeatureCodes,
  type DeviceState,
  type FeatureCode,
  type FeatureCodeAction,
  type FieldError,
} from "../api";
import {
  Alert,
  Badge,
  type BadgeTone,
  Button,
  IconButton,
  Input,
  LinkButton,
  LiveTag,
  PageHeader,
  Select,
  Spinner,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import "./platform/platform.css";

/** System: feature codes and live presence; API tokens live on AI access. */
export function System() {
  const toast = useToast();
  const showToast = toast.show;
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Platform"
        title="System"
        description="Feature codes and live presence."
      />
      <div className="pf-grid2 pf-grid2--wide">
        <FeatureCodes onSaved={() => showToast("Feature codes saved.")} />
        <div className="pf-stack" style={{ minWidth: 0 }}>
          <Presence />
          <TokensMoved />
        </div>
      </div>
      {toast.node}
    </section>
  );
}

// --- feature codes -------------------------------------------------------------

/** The feature-code editor: edit codes inline, save the whole list. */
function FeatureCodes({ onSaved }: { onSaved: () => void }) {
  const [list, setList] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready" }
  >({ status: "loading" });
  const [rows, setRows] = useState<FeatureCode[]>([]);
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listFeatureCodes(controller.signal)
      .then((items) => {
        setRows(items);
        setList({ status: "ready" });
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
    const seen = new Map<string, number>();
    rows.forEach((r, i) => {
      const code = r.code.trim();
      if (!FEATURE_CODE_PATTERN.test(code)) return;
      const n = seen.get(code);
      if (n !== undefined) {
        found[`items[${i}].code`] = `${code} is used twice.`;
        found[`items[${n}].code`] =
          found[`items[${n}].code`] ?? `${code} is used twice.`;
      }
      seen.set(code, i);
    });
    setErrors(found);
    return Object.keys(found).length > 0;
  }

  async function onSave() {
    setFormError(null);
    setUnmatched([]);
    if (validate()) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    const clean = rows.map((r) => ({
      ...r,
      code: r.code.trim(),
      argument: r.argument.trim(),
    }));
    try {
      await updateFeatureCodes(clean);
      setRows(clean);
      onSaved();
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
  }

  return (
    <form
      className="az-card pf-card pf-card--tight"
      aria-labelledby="feature-codes"
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        void onSave();
      }}
    >
      <div>
        <h2 className="pf-h3" id="feature-codes">
          Feature codes
        </h2>
        <p className="pf-head__desc">
          Dialled from a phone, e.g. <span className="pf-mono">*78</span> for
          DND on. Saving replaces the whole list.
        </p>
      </div>
      {list.status === "loading" && <Spinner label="Loading feature codes…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the feature codes">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && (
        <>
          <div>
            <div className="az-eyebrow pf-codes__head" aria-hidden="true">
              <span>Code</span>
              <span>Action</span>
              <span>Argument</span>
              <span />
            </div>
            {rows.length === 0 && (
              <p className="pf-muted">No feature codes. Add one below.</p>
            )}
            {rows.map((r, i) => (
              <div className="pf-codes__row" key={i}>
                <Input
                  id={`fc-code-${i}`}
                  aria-label={`Code ${i + 1}`}
                  size="sm"
                  mono
                  spellCheck={false}
                  value={r.code}
                  error={errors[`items[${i}].code`]}
                  onChange={(e) => setRow(i, { code: e.target.value })}
                />
                <Select
                  id={`fc-action-${i}`}
                  aria-label={`Action ${i + 1}`}
                  size="sm"
                  options={FEATURE_CODE_ACTIONS}
                  value={r.action}
                  error={errors[`items[${i}].action`]}
                  onChange={(e) =>
                    setRow(i, { action: e.target.value as FeatureCodeAction })
                  }
                />
                <Input
                  id={`fc-argument-${i}`}
                  aria-label={`Argument ${i + 1}`}
                  size="sm"
                  mono
                  placeholder="—"
                  spellCheck={false}
                  value={r.argument}
                  error={errors[`items[${i}].argument`]}
                  onChange={(e) => setRow(i, { argument: e.target.value })}
                />
                <IconButton
                  icon="x"
                  label={`Remove code ${r.code || i + 1}`}
                  onClick={() =>
                    setRows((prev) => prev.filter((_, j) => j !== i))
                  }
                />
              </div>
            ))}
          </div>
          {(formError || unmatched.length > 0) && (
            <Alert tone="bad" title={formError ?? "Could not save"}>
              {unmatched.length > 0 &&
                unmatched.map((fe) => `${fe.path}: ${fe.message}`).join("; ")}
            </Alert>
          )}
          <div className="pf-codes__actions">
            <Button
              variant="secondary"
              size="sm"
              icon="plus"
              onClick={() =>
                setRows((prev) => [
                  ...prev,
                  { code: "", action: "voicemail", argument: "" },
                ])
              }
            >
              Add code
            </Button>
            <Button type="submit" size="sm" disabled={busy}>
              Save feature codes
            </Button>
          </div>
        </>
      )}
    </form>
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

// --- presence ------------------------------------------------------------------

const PRESENCE: Record<string, [BadgeTone, string]> = {
  "on-call": ["info", "On call"],
  dnd: ["warn", "DND"],
  idle: ["outline", "Idle"],
  ringing: ["neutral", "Ringing"],
};

/** The live presence list, polled every few seconds. */
function Presence() {
  const presence = usePolling(listPresence, LIVE_REFRESH_MS);
  const items: DeviceState[] =
    presence.status === "loading" ? [] : (presence.data ?? []);
  return (
    <div className="az-card pf-card pf-card--tight">
      <div className="pf-cardhead">
        <h2 className="pf-h3" id="presence">
          Presence
        </h2>
        <LiveTag live={presence.status === "ready"} />
      </div>
      {presence.status === "loading" && <Spinner label="Loading presence…" />}
      {presence.status === "error" && (
        <Alert
          tone="bad"
          title={presence.data ? "Refresh failed" : "Could not load presence"}
        >
          {presence.message}
        </Alert>
      )}
      {presence.status !== "loading" &&
        (presence.status === "ready" || presence.data) &&
        (items.length === 0 ? (
          <p className="pf-muted">No presence known.</p>
        ) : (
          <ul className="pf-presence" aria-labelledby="presence">
            {items.map((d) => {
              const [tone, label] = PRESENCE[d.state] ?? ["outline", d.state];
              return (
                <li key={d.device}>
                  <span className="pf-mono">{d.device}</span>
                  <span className="pf-presence__ext">{d.extension || "—"}</span>
                  <Badge tone={tone} dot>
                    {label}
                  </Badge>
                </li>
              );
            })}
          </ul>
        ))}
    </div>
  );
}

// --- API tokens ----------------------------------------------------------------

/** API tokens moved to the AI access page, with their scopes and expiry. */
function TokensMoved() {
  return (
    <div className="az-card pf-card pf-card--tight">
      <div className="pf-cardhead">
        <div>
          <h2 className="pf-h3">API tokens</h2>
          <p className="pf-head__desc">
            Tokens, with their scopes and expiry, are on AI access.
          </p>
        </div>
        <LinkButton to="/ai" size="sm" icon="key-round">
          Manage tokens
        </LinkButton>
      </div>
    </div>
  );
}
