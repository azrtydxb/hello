import { useEffect, useState } from "react";
import { errorMessage, type Id } from "../../api";
import {
  listSettingOverrides,
  OVERRIDE_KEY,
  putSettingOverrides,
  SECRET_MASK,
  type OverrideScope,
  type SettingOverride,
} from "../../api/provSettings";
import { Alert, Button, Checkbox, Input } from "../../design/azrty/components";
import { Can, useCan } from "../../role";

interface Row {
  key: string;
  value: string;
  secret: boolean;
  /** The row came from the server, so a secret's value is the stored one. */
  stored: boolean;
}

const toRows = (items: SettingOverride[]): Row[] =>
  items.map((o) => ({
    key: o.key,
    value: o.value,
    secret: o.secret,
    stored: true,
  }));

/** The first problem with the rows, or null (the server checks them again). */
export function rowProblem(rows: Row[]): string | null {
  const seen = new Set<string>();
  for (const r of rows) {
    if (!OVERRIDE_KEY.test(r.key))
      return `"${r.key}" is not a Grandstream P-value (P71) or a Yealink key (features.text_message.enable)`;
    if (seen.has(r.key)) return `${r.key} is listed twice`;
    seen.add(r.key);
    if (
      [...r.value].some((c) => c.charCodeAt(0) < 32 || c.charCodeAt(0) === 127)
    )
      return `${r.key}: the value must be one line`;
  }
  return null;
}

/**
 * Extra vendor settings of a phone group or a phone, on top of the template.
 * The order is the template, then the group, then the phone.
 */
export function SettingOverrides({
  scope,
  id,
  title,
  onSaved,
}: {
  scope: OverrideScope;
  id: Id;
  title: string;
  onSaved?: () => void;
}) {
  const [rows, setRows] = useState<Row[] | null>(null);
  const [saved, setSaved] = useState("");
  const [loadError, setLoadError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const admin = useCan("admin");

  useEffect(() => {
    const controller = new AbortController();
    listSettingOverrides(scope, id, controller.signal)
      .then((items) => {
        const r = toRows(items);
        setRows(r);
        setSaved(JSON.stringify(r));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setLoadError(errorMessage(err));
      });
    return () => controller.abort();
  }, [scope, id]);

  if (loadError)
    return (
      <Alert tone="warn" title="Could not load the setting overrides">
        {loadError}
      </Alert>
    );
  if (!rows) return null;

  const problem = rowProblem(rows);
  const change = (i: number, patch: Partial<Row>) =>
    setRows(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  async function onSave() {
    if (!rows) return;
    setBusy(true);
    setError(null);
    try {
      const items = await putSettingOverrides(
        scope,
        id,
        rows.map((r) => ({
          key: r.key,
          secret: r.secret,
          // A secret that was not retyped keeps the stored value.
          value:
            r.secret && r.stored && r.value === SECRET_MASK ? null : r.value,
        })),
      );
      const next = toRows(items);
      setRows(next);
      setSaved(JSON.stringify(next));
      onSaved?.();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="prov-stack">
      <h2 className="inv-section-title">{title}</h2>
      <p className="prov-muted">
        Vendor keys the template does not write. A key is a Grandstream P-value
        (<span className="cf-mono">P71</span>) or a Yealink key (
        <span className="cf-mono">features.text_message.enable</span>). The
        template&apos;s value is replaced by the group&apos;s, and that by the
        phone&apos;s. Keys Hello owns (the account, the provisioning URL, the CA
        and the admin password) are refused. Only use a key you can find in the
        vendor&apos;s documentation: Hello does not check that the phone knows
        it. The phone picks up a change at its next resync.
      </p>
      {rows.length === 0 && (
        <p className="prov-muted">No setting is overridden.</p>
      )}
      {rows.map((r, i) => (
        <div className="prov-row" key={i}>
          <Input
            id={`override-key-${String(i)}`}
            label="Key"
            value={r.key}
            disabled={!admin || busy}
            onChange={(e) => change(i, { key: e.target.value.trim() })}
          />
          <Input
            id={`override-value-${String(i)}`}
            label="Value"
            type={r.secret ? "password" : "text"}
            value={r.value}
            disabled={!admin || busy}
            onChange={(e) => change(i, { value: e.target.value })}
          />
          <Checkbox
            label="Secret"
            checked={r.secret}
            disabled={!admin || busy || r.stored}
            onChange={(e) => change(i, { secret: e.target.checked })}
          />
          <Can min="admin">
            <Button
              type="button"
              variant="secondary"
              size="sm"
              disabled={busy}
              onClick={() => setRows(rows.filter((_, j) => j !== i))}
            >
              Remove {r.key || "row"}
            </Button>
          </Can>
        </div>
      ))}
      {problem && (
        <Alert tone="warn" title="Check the settings">
          {problem}
        </Alert>
      )}
      {error && (
        <Alert tone="bad" title="Could not save the setting overrides">
          {error}
        </Alert>
      )}
      <Can min="admin">
        <div className="prov-row">
          <Button
            type="button"
            variant="secondary"
            size="sm"
            icon="plus"
            disabled={busy || rows.length >= 200}
            onClick={() =>
              setRows([
                ...rows,
                { key: "", value: "", secret: false, stored: false },
              ])
            }
          >
            Add a setting
          </Button>
          <Button
            type="button"
            variant="secondary"
            disabled={
              busy || problem !== null || JSON.stringify(rows) === saved
            }
            onClick={() => void onSave()}
          >
            Save settings
          </Button>
        </div>
      </Can>
    </div>
  );
}
