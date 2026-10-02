import { useCallback, useEffect, useState, type FormEvent } from "react";
import {
  createTrunk,
  deleteTrunk,
  errorMessage,
  fieldErrors,
  listTrunks,
  listTrunkStatus,
  updateTrunk,
  type FieldError,
  type Trunk,
  type TrunkInput,
  type TrunkStatus,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";
import { LiveStatus } from "../components/LiveStatus";
import {
  Field,
  fieldId,
  FormError,
  mapFieldErrors,
  splitList,
  type ErrorMap,
} from "../forms";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";

const TRUNK_NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;
const FORM = "trunk";

interface DestinationDraft {
  host: string;
  port: string;
  priority: string;
  weight: string;
}

/** The form's view of a trunk; never holds the password. */
interface TrunkDraft {
  name: string;
  mode: "registration" | "ip";
  username: string;
  realm: string;
  fromDomain: string;
  registerExpires: string;
  optionsInterval: string;
  sourceCidrs: string;
  maxCalls: string;
  defaultCallerId: string;
  enabled: boolean;
  destinations: DestinationDraft[];
}

const NEW_DESTINATION: DestinationDraft = {
  host: "",
  port: "5060",
  priority: "0",
  weight: "1",
};

const NEW_TRUNK: TrunkDraft = {
  name: "",
  mode: "registration",
  username: "",
  realm: "",
  fromDomain: "",
  registerExpires: "3600",
  optionsInterval: "30",
  sourceCidrs: "",
  maxCalls: "0",
  defaultCallerId: "",
  enabled: true,
  destinations: [NEW_DESTINATION],
};

function toDraft(t: Trunk): TrunkDraft {
  return {
    name: t.name,
    mode: t.mode,
    username: t.username,
    realm: t.realm,
    fromDomain: t.fromDomain,
    registerExpires: String(t.registerExpires),
    optionsInterval: String(t.optionsInterval),
    sourceCidrs: (t.sourceCidrs ?? []).join("\n"),
    maxCalls: String(t.maxCalls),
    defaultCallerId: t.defaultCallerId,
    enabled: t.enabled,
    destinations: (t.destinations ?? []).map((d) => ({
      host: d.host,
      port: String(d.port),
      priority: String(d.priority),
      weight: String(d.weight),
    })),
  };
}

const isInt = (v: string) => /^-?[0-9]+$/.test(v.trim());

/** Client-side checks; the server validates everything again. */
function validate(d: TrunkDraft): ErrorMap {
  const e: Record<string, string> = {};
  if (!TRUNK_NAME_PATTERN.test(d.name)) {
    e.name = "Use 1 to 64 letters, digits, dots, underscores or hyphens.";
  }
  for (const key of [
    "registerExpires",
    "optionsInterval",
    "maxCalls",
  ] as const) {
    if (!isInt(d[key])) e[key] = "Enter a whole number.";
  }
  if (d.destinations.length === 0) e.destinations = "Add a destination.";
  d.destinations.forEach((dst, i) => {
    if (dst.host.trim() === "") e[`destinations[${i}].host`] = "Enter a host.";
    for (const f of ["port", "priority", "weight"] as const) {
      if (!isInt(dst[f])) e[`destinations[${i}].${f}`] = "Whole number.";
    }
  });
  return e;
}

function toInput(d: TrunkDraft): TrunkInput {
  return {
    name: d.name,
    mode: d.mode,
    username: d.username,
    realm: d.realm,
    fromDomain: d.fromDomain,
    registerExpires: Number(d.registerExpires),
    optionsInterval: Number(d.optionsInterval),
    sourceCidrs: splitList(d.sourceCidrs),
    maxCalls: Number(d.maxCalls),
    defaultCallerId: d.defaultCallerId,
    enabled: d.enabled,
    destinations: d.destinations.map((x) => ({
      host: x.host.trim(),
      port: Number(x.port),
      priority: Number(x.priority),
      weight: Number(x.weight),
    })),
  };
}

function knownKeys(d: TrunkDraft): string[] {
  return [
    "name",
    "mode",
    "username",
    "password",
    "realm",
    "fromDomain",
    "registerExpires",
    "optionsInterval",
    "sourceCidrs",
    "maxCalls",
    "defaultCallerId",
    "enabled",
    "destinations",
    ...d.destinations.flatMap((_, i) => [
      `destinations[${i}]`,
      ...["host", "port", "priority", "weight"].map(
        (f) => `destinations[${i}].${f}`,
      ),
    ]),
  ];
}

function formatLatency(ns: number | undefined): string {
  if (ns === undefined) return "";
  return `${(ns / 1e6).toFixed(ns < 1e7 ? 1 : 0)} ms`;
}

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Trunk[] };

type Editing = { kind: "new" } | { kind: "edit"; trunk: Trunk } | null;

/** Trunks: carrier accounts and IP peers, with live status every 5s. */
export function Trunks() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [editing, setEditing] = useState<Editing>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const status = usePolling(listTrunkStatus, LIVE_REFRESH_MS);

  useEffect(() => {
    const controller = new AbortController();
    listTrunks(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  const onSaved = useCallback((saved: Trunk, created: boolean) => {
    setList((prev) =>
      prev.status !== "ready"
        ? prev
        : {
            status: "ready",
            items: created
              ? [...prev.items, saved]
              : prev.items.map((t) => (t.id === saved.id ? saved : t)),
          },
    );
    setEditing(null);
    setNotice(`Trunk ${saved.name} saved.`);
  }, []);

  async function onDelete(t: Trunk) {
    setActionError(null);
    setNotice(null);
    try {
      await deleteTrunk(t.id);
      setList((prev) =>
        prev.status === "ready"
          ? { status: "ready", items: prev.items.filter((x) => x.id !== t.id) }
          : prev,
      );
    } catch (err) {
      setActionError(`Could not delete ${t.name}: ${errorMessage(err)}`);
    }
  }

  const statuses = status.status === "loading" ? undefined : status.data;
  const statusOf = (t: Trunk) =>
    statuses?.find((s) => String(s.trunkId) === String(t.id));

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Trunks</h1>
      <p className="muted" role="status" aria-live="polite">
        {notice}
      </p>
      {editing ? (
        <TrunkForm
          key={editing.kind === "edit" ? String(editing.trunk.id) : "new"}
          trunk={editing.kind === "edit" ? editing.trunk : null}
          onCancel={() => setEditing(null)}
          onSaved={onSaved}
        />
      ) : (
        <p>
          <button
            type="button"
            className="primary"
            disabled={list.status !== "ready"}
            onClick={() => {
              setNotice(null);
              setEditing({ kind: "new" });
            }}
          >
            New trunk
          </button>
        </p>
      )}

      <h2 id="trunks-list">All trunks</h2>
      <LiveStatus state={status} what="trunk status" />
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      {list.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading trunks…
        </p>
      )}
      {list.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load trunks.</strong>
          <p>{list.message}</p>
        </div>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <p className="muted">No trunks yet.</p>
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <div className="table-wrap">
          <table aria-labelledby="trunks-list">
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Mode</th>
                <th scope="col">Registration</th>
                <th scope="col">Destinations</th>
                <th scope="col">Calls</th>
                <th scope="col">Password</th>
                <th scope="col">Enabled</th>
                <th scope="col">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.items.map((t) => (
                <TrunkRow
                  key={t.id}
                  trunk={t}
                  status={statusOf(t)}
                  onEdit={() => {
                    setNotice(null);
                    setEditing({ kind: "edit", trunk: t });
                  }}
                  onDelete={() => onDelete(t)}
                />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function TrunkRow({
  trunk: t,
  status,
  onEdit,
  onDelete,
}: {
  trunk: Trunk;
  status: TrunkStatus | undefined;
  onEdit: () => void;
  onDelete: () => Promise<void>;
}) {
  const reg = status?.registration;
  return (
    <tr>
      <th scope="row">{t.name}</th>
      <td>{t.mode === "ip" ? "IP peer" : "Registration"}</td>
      <td>
        {t.mode === "ip" ? (
          <span className="muted">Not used</span>
        ) : reg ? (
          <span className={`state state-${reg.state}`}>
            {reg.state}
            {reg.lastCode ? ` (${reg.lastCode})` : ""}
          </span>
        ) : (
          <span className="muted">Unknown</span>
        )}
      </td>
      <td>
        {status && status.destinations.length > 0 ? (
          <ul className="plain">
            {status.destinations.map((d) => (
              <li key={d.destination}>
                <code>{d.destination}</code>{" "}
                <span className={d.up ? "state state-up" : "state state-down"}>
                  {d.up ? "up" : "down"}
                </span>
                {d.up && d.latencyNs !== undefined
                  ? `, ${formatLatency(d.latencyNs)}`
                  : ""}
                {!d.up && d.lastCode ? ` (${d.lastCode})` : ""}
              </li>
            ))}
          </ul>
        ) : (
          <span className="muted">No health data</span>
        )}
      </td>
      <td>
        {status?.activeCalls ?? 0}
        {t.maxCalls > 0 ? ` of ${t.maxCalls}` : " (no limit)"}
      </td>
      <td>{t.hasPassword ? "set" : "not set"}</td>
      <td>{t.enabled ? "Yes" : "No"}</td>
      <td className="row-actions">
        <button
          type="button"
          aria-label={`Edit trunk ${t.name}`}
          onClick={onEdit}
        >
          Edit
        </button>
        <ConfirmButton
          label="Delete"
          accessibleLabel={`Delete trunk ${t.name}`}
          prompt={`Delete trunk ${t.name}?`}
          confirmLabel="Delete trunk"
          onConfirm={onDelete}
        />
      </td>
    </tr>
  );
}

function TrunkForm({
  trunk,
  onCancel,
  onSaved,
}: {
  trunk: Trunk | null;
  onCancel: () => void;
  onSaved: (t: Trunk, created: boolean) => void;
}) {
  const [draft, setDraft] = useState<TrunkDraft>(() =>
    trunk ? toDraft(trunk) : NEW_TRUNK,
  );
  // The password is write-only: it starts empty, is sent only when the user
  // chose to set it, and is dropped with this component after saving.
  const [changingPassword, setChangingPassword] = useState(trunk === null);
  const [password, setPassword] = useState("");
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const set = <K extends keyof TrunkDraft>(key: K, value: TrunkDraft[K]) =>
    setDraft((d) => ({ ...d, [key]: value }));
  const setDest = (i: number, patch: Partial<DestinationDraft>) =>
    setDraft((d) => ({
      ...d,
      destinations: d.destinations.map((x, j) =>
        j === i ? { ...x, ...patch } : x,
      ),
    }));
  const id = (key: string) => fieldId(FORM, key);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = { ...validate(draft) };
    if (trunk && changingPassword && password === "") {
      found.password = "Enter the new password, or keep the current one.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    const body = toInput(draft);
    if (changingPassword && password !== "") body.password = password;
    setBusy(true);
    try {
      const saved = trunk
        ? await updateTrunk(trunk.id, body)
        : await createTrunk(body);
      setPassword("");
      onSaved(saved, trunk === null);
    } catch (err) {
      const mapped = mapFieldErrors(fieldErrors(err), knownKeys(draft));
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  const title = trunk ? `Edit trunk ${trunk.name}` : "New trunk";
  return (
    <form
      className="inline-form"
      aria-labelledby="trunk-form-title"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="trunk-form-title">{title}</h2>
      <div className="fields">
        <Field id={id("name")} label="Name" error={errors.name}>
          {(p) => (
            <input
              {...p}
              value={draft.name}
              autoComplete="off"
              onChange={(e) => set("name", e.target.value)}
            />
          )}
        </Field>
        <Field id={id("mode")} label="Mode" error={errors.mode}>
          {(p) => (
            <select
              {...p}
              value={draft.mode}
              onChange={(e) =>
                set("mode", e.target.value as TrunkDraft["mode"])
              }
            >
              <option value="registration">
                Registration (carrier account)
              </option>
              <option value="ip">IP peer</option>
            </select>
          )}
        </Field>
        <Field id={id("username")} label="Username" error={errors.username}>
          {(p) => (
            <input
              {...p}
              value={draft.username}
              autoComplete="off"
              onChange={(e) => set("username", e.target.value)}
            />
          )}
        </Field>
        <PasswordField
          id={id("password")}
          isNew={trunk === null}
          hasPassword={trunk?.hasPassword ?? false}
          changing={changingPassword}
          value={password}
          error={errors.password}
          onChange={setPassword}
          onChanging={(on) => {
            setChangingPassword(on);
            setPassword("");
          }}
        />
        <Field id={id("realm")} label="Realm" error={errors.realm}>
          {(p) => (
            <input
              {...p}
              value={draft.realm}
              onChange={(e) => set("realm", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("fromDomain")}
          label="From domain"
          error={errors.fromDomain}
        >
          {(p) => (
            <input
              {...p}
              value={draft.fromDomain}
              onChange={(e) => set("fromDomain", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("registerExpires")}
          label="Register expires (s)"
          error={errors.registerExpires}
          hint="60 to 86400."
        >
          {(p) => (
            <input
              {...p}
              inputMode="numeric"
              value={draft.registerExpires}
              onChange={(e) => set("registerExpires", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("optionsInterval")}
          label="OPTIONS interval (s)"
          error={errors.optionsInterval}
          hint="5 to 3600."
        >
          {(p) => (
            <input
              {...p}
              inputMode="numeric"
              value={draft.optionsInterval}
              onChange={(e) => set("optionsInterval", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("maxCalls")}
          label="Max calls"
          error={errors.maxCalls}
          hint="0 means no limit."
        >
          {(p) => (
            <input
              {...p}
              inputMode="numeric"
              value={draft.maxCalls}
              onChange={(e) => set("maxCalls", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("defaultCallerId")}
          label="Default caller ID"
          error={errors.defaultCallerId}
        >
          {(p) => (
            <input
              {...p}
              value={draft.defaultCallerId}
              onChange={(e) => set("defaultCallerId", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("sourceCidrs")}
          label="Source CIDRs"
          error={errors.sourceCidrs}
          hint="One per line, e.g. 203.0.113.0/24. Calls from these addresses are accepted as this trunk."
        >
          {(p) => (
            <textarea
              {...p}
              rows={3}
              value={draft.sourceCidrs}
              onChange={(e) => set("sourceCidrs", e.target.value)}
            />
          )}
        </Field>
        <div className="field checkbox">
          <input
            id={id("enabled")}
            type="checkbox"
            checked={draft.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
          />
          <label htmlFor={id("enabled")}>Enabled</label>
        </div>
      </div>

      <fieldset
        className="group"
        aria-describedby={
          errors.destinations ? `${id("destinations")}-error` : undefined
        }
      >
        <legend>Destinations</legend>
        {errors.destinations && (
          <p id={`${id("destinations")}-error`} className="field-error">
            {errors.destinations}
          </p>
        )}
        {draft.destinations.map((dst, i) => {
          const n = i + 1;
          const k = (f: string) => `destinations[${i}].${f}`;
          return (
            <div className="fields destination" key={i}>
              <Field
                id={id(k("host"))}
                label={`Host ${n}`}
                error={errors[k("host")] ?? errors[`destinations[${i}]`]}
              >
                {(p) => (
                  <input
                    {...p}
                    value={dst.host}
                    onChange={(e) => setDest(i, { host: e.target.value })}
                  />
                )}
              </Field>
              <Field
                id={id(k("port"))}
                label={`Port ${n}`}
                error={errors[k("port")]}
              >
                {(p) => (
                  <input
                    {...p}
                    className="narrow"
                    inputMode="numeric"
                    value={dst.port}
                    onChange={(e) => setDest(i, { port: e.target.value })}
                  />
                )}
              </Field>
              <Field
                id={id(k("priority"))}
                label={`Priority ${n}`}
                error={errors[k("priority")]}
              >
                {(p) => (
                  <input
                    {...p}
                    className="narrow"
                    inputMode="numeric"
                    value={dst.priority}
                    onChange={(e) => setDest(i, { priority: e.target.value })}
                  />
                )}
              </Field>
              <Field
                id={id(k("weight"))}
                label={`Weight ${n}`}
                error={errors[k("weight")]}
              >
                {(p) => (
                  <input
                    {...p}
                    className="narrow"
                    inputMode="numeric"
                    value={dst.weight}
                    onChange={(e) => setDest(i, { weight: e.target.value })}
                  />
                )}
              </Field>
              <button
                type="button"
                className="align-end"
                aria-label={`Remove destination ${n}`}
                onClick={() =>
                  setDraft((d) => ({
                    ...d,
                    destinations: d.destinations.filter((_, j) => j !== i),
                  }))
                }
              >
                Remove
              </button>
            </div>
          );
        })}
        <p className="hint">
          Lower priority is tried first; weight splits calls among equal
          priorities. Port 0 resolves SRV.
        </p>
        <button
          type="button"
          onClick={() =>
            setDraft((d) => ({
              ...d,
              destinations: [...d.destinations, NEW_DESTINATION],
            }))
          }
        >
          Add destination
        </button>
      </fieldset>

      <FormError message={formError} unmatched={unmatched} />
      <div className="actions start">
        <button type="submit" className="primary" disabled={busy}>
          {trunk ? "Save trunk" : "Create trunk"}
        </button>
        <button type="button" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}

/**
 * The write-only password: never pre-filled, never displayed. Editing an
 * existing trunk shows only whether one is set; changing it is explicit.
 */
function PasswordField({
  id,
  isNew,
  hasPassword,
  changing,
  value,
  error,
  onChange,
  onChanging,
}: {
  id: string;
  isNew: boolean;
  hasPassword: boolean;
  changing: boolean;
  value: string;
  error?: string;
  onChange: (v: string) => void;
  onChanging: (on: boolean) => void;
}) {
  const statusId = `${id}-status`;
  if (!changing) {
    return (
      <div className="field">
        <span className="label" id={`${id}-label`}>
          Password
        </span>
        <p id={statusId} className="password-state">
          {hasPassword ? "set" : "not set"}
        </p>
        <button
          type="button"
          aria-describedby={statusId}
          onClick={() => onChanging(true)}
        >
          {hasPassword ? "Change password" : "Set password"}
        </button>
      </div>
    );
  }
  return (
    <div className="field">
      <Field
        id={id}
        label={isNew ? "Password (optional)" : "New password"}
        error={error}
        hint="Write-only: it is stored encrypted and never shown again."
      >
        {(p) => (
          <input
            {...p}
            type="password"
            autoComplete="new-password"
            value={value}
            onChange={(e) => onChange(e.target.value)}
          />
        )}
      </Field>
      {!isNew && (
        <button type="button" onClick={() => onChanging(false)}>
          Keep current password
        </button>
      )}
    </div>
  );
}
