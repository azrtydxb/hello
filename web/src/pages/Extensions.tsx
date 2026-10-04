import { useEffect, useState, type FormEvent } from "react";
import {
  createExtension,
  deleteExtension,
  errorMessage,
  fieldErrors,
  EXTENSION_NUMBER_PATTERN,
  listExtensions,
  updateExtension,
  type Extension,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";
import { mapFieldErrors } from "../forms";

const NUMBER_HINT = "2 to 10 digits.";

const EXTERNAL_NUMBER_PATTERN = /^(\+?[0-9]{2,20})?$/;
const EXTERNAL_HINT = "Optional; presented to carriers, e.g. +97142000101.";

interface Errors {
  number?: string;
  name?: string;
  externalNumber?: string;
  dnd?: string;
  forwardAlways?: string;
  forwardBusy?: string;
  forwardNoAnswer?: string;
  voicemailEnabled?: string;
}

const hasErrors = (e: Errors) =>
  Boolean(
    e.number ||
    e.name ||
    e.externalNumber ||
    e.dnd ||
    e.forwardAlways ||
    e.forwardBusy ||
    e.forwardNoAnswer ||
    e.voicemailEnabled,
  );

/** A forward target: empty = off, or 2–20 digits with an optional +. */
const FORWARD_PATTERN = /^(\+?[0-9]{2,20})?$/;
const FORWARD_HINT =
  "Empty = off; or 2 to 20 digits, optionally starting with +.";
const FORWARD_FIELDS = [
  { key: "forwardAlways", label: "Forward always", source: "always" },
  { key: "forwardBusy", label: "Forward busy", source: "busy" },
  { key: "forwardNoAnswer", label: "Forward no-answer", source: "noAnswer" },
] as const;

function validate(
  number: string,
  name: string,
  external: string,
  forwards: { always: string; busy: string; noAnswer: string },
): Errors {
  const errors: Errors = {};
  if (!EXTENSION_NUMBER_PATTERN.test(number)) {
    errors.number = "The number must be 2 to 10 digits (0–9 only).";
  }
  if (name.trim() === "") errors.name = "Enter a name.";
  if (!EXTERNAL_NUMBER_PATTERN.test(external.trim())) {
    errors.externalNumber =
      "Use 2 to 20 digits, optionally starting with +, or leave it empty.";
  }
  const targets = [
    ["forwardAlways", forwards.always],
    ["forwardBusy", forwards.busy],
    ["forwardNoAnswer", forwards.noAnswer],
  ] as const;
  for (const [key, value] of targets) {
    if (!FORWARD_PATTERN.test(value.trim())) {
      errors[key as keyof Errors] = FORWARD_HINT;
    }
  }
  return errors;
}

/** Server field errors on the extension's editable fields. */
function serverFieldErrors(err: unknown): Errors {
  return mapFieldErrors(fieldErrors(err), [
    "number",
    "name",
    "externalNumber",
    "dnd",
    "forwardAlways",
    "forwardBusy",
    "forwardNoAnswer",
    "voicemailEnabled",
  ]).byKey;
}

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Extension[] };

/** Extensions: list, create, edit and delete. */
export function Extensions() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [actionError, setActionError] = useState<string | null>(null);
  const [editing, setEditing] = useState<Extension["id"] | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  function replaceItems(fn: (items: Extension[]) => Extension[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );
  }

  async function onDelete(ext: Extension) {
    setActionError(null);
    try {
      await deleteExtension(ext.id);
      replaceItems((items) => items.filter((i) => i.id !== ext.id));
    } catch (err) {
      setActionError(`Could not delete ${ext.number}: ${errorMessage(err)}`);
    }
  }

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Extensions</h1>
      <CreateExtension
        onCreated={(ext) => replaceItems((items) => [...items, ext])}
      />

      <h2 id="extensions-list">All extensions</h2>
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      {list.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading extensions…
        </p>
      )}
      {list.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load extensions.</strong>
          <p>{list.message}</p>
        </div>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <p className="muted">No extensions yet.</p>
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <table aria-labelledby="extensions-list">
          <thead>
            <tr>
              <th scope="col">Number</th>
              <th scope="col">Name</th>
              <th scope="col">External number</th>
              <th scope="col">DND</th>
              <th scope="col">Voicemail</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.items.map((ext) =>
              editing === ext.id ? (
                <EditRow
                  key={ext.id}
                  ext={ext}
                  onCancel={() => setEditing(null)}
                  onSaved={(saved) => {
                    replaceItems((items) =>
                      items.map((i) => (i.id === saved.id ? saved : i)),
                    );
                    setEditing(null);
                  }}
                />
              ) : (
                <tr key={ext.id}>
                  <th scope="row">{ext.number}</th>
                  <td>{ext.name}</td>
                  <td>{ext.externalNumber || "—"}</td>
                  <td>{ext.dnd ? "Yes" : "No"}</td>
                  <td>{ext.voicemailEnabled === false ? "No" : "Yes"}</td>
                  <td className="row-actions">
                    <button
                      type="button"
                      aria-label={`Edit extension ${ext.number}`}
                      onClick={() => {
                        setActionError(null);
                        setEditing(ext.id);
                      }}
                    >
                      Edit
                    </button>
                    <ConfirmButton
                      label="Delete"
                      accessibleLabel={`Delete extension ${ext.number}`}
                      prompt={`Delete ${ext.number} and its devices?`}
                      confirmLabel="Delete extension"
                      onConfirm={() => onDelete(ext)}
                    />
                  </td>
                </tr>
              ),
            )}
          </tbody>
        </table>
      )}
    </section>
  );
}

function CreateExtension({
  onCreated,
}: {
  onCreated: (ext: Extension) => void;
}) {
  const [number, setNumber] = useState("");
  const [name, setName] = useState("");
  const [external, setExternal] = useState("");
  const [errors, setErrors] = useState<Errors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerError(null);
    const found = validate(number, name, external, {
      always: "",
      busy: "",
      noAnswer: "",
    });
    setErrors(found);
    if (hasErrors(found)) return;
    setBusy(true);
    try {
      const ext = await createExtension({
        number,
        name: name.trim(),
        // Sent only when given, so a create without one stays the Phase 1 shape.
        ...(external.trim() ? { externalNumber: external.trim() } : {}),
      });
      onCreated(ext);
      setNumber("");
      setName("");
      setExternal("");
    } catch (err) {
      setErrors(serverFieldErrors(err));
      setServerError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      className="inline-form"
      aria-labelledby="new-extension"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="new-extension">New extension</h2>
      <div className="fields">
        <div className="field">
          <label htmlFor="ext-number">Number</label>
          <input
            id="ext-number"
            inputMode="numeric"
            value={number}
            onChange={(e) => setNumber(e.target.value)}
            aria-invalid={errors.number ? true : undefined}
            aria-describedby={
              errors.number ? "ext-number-error" : "ext-number-hint"
            }
          />
          {errors.number ? (
            <p id="ext-number-error" className="field-error">
              {errors.number}
            </p>
          ) : (
            <p id="ext-number-hint" className="hint">
              {NUMBER_HINT}
            </p>
          )}
        </div>
        <div className="field">
          <label htmlFor="ext-name">Name</label>
          <input
            id="ext-name"
            value={name}
            onChange={(e) => setName(e.target.value)}
            aria-invalid={errors.name ? true : undefined}
            aria-describedby={errors.name ? "ext-name-error" : undefined}
          />
          {errors.name && (
            <p id="ext-name-error" className="field-error">
              {errors.name}
            </p>
          )}
        </div>
        <div className="field">
          <label htmlFor="ext-external">External number</label>
          <input
            id="ext-external"
            inputMode="tel"
            value={external}
            onChange={(e) => setExternal(e.target.value)}
            aria-invalid={errors.externalNumber ? true : undefined}
            aria-describedby={
              errors.externalNumber ? "ext-external-error" : "ext-external-hint"
            }
          />
          {errors.externalNumber ? (
            <p id="ext-external-error" className="field-error">
              {errors.externalNumber}
            </p>
          ) : (
            <p id="ext-external-hint" className="hint">
              {EXTERNAL_HINT}
            </p>
          )}
        </div>
      </div>
      {serverError && (
        <p role="alert" className="error">
          Could not create the extension: {serverError}
        </p>
      )}
      <button type="submit" className="primary" disabled={busy}>
        Create extension
      </button>
    </form>
  );
}

function EditRow({
  ext,
  onCancel,
  onSaved,
}: {
  ext: Extension;
  onCancel: () => void;
  onSaved: (ext: Extension) => void;
}) {
  const [number, setNumber] = useState(ext.number);
  const [name, setName] = useState(ext.name);
  const [external, setExternal] = useState(ext.externalNumber ?? "");
  const [dnd, setDnd] = useState(ext.dnd ?? false);
  const [voicemailEnabled, setVoicemailEnabled] = useState(
    ext.voicemailEnabled !== false,
  );
  const [always, setAlways] = useState(ext.forwardAlways ?? "");
  const [busy, setBusyField] = useState(ext.forwardBusy ?? "");
  const [noAnswer, setNoAnswer] = useState(ext.forwardNoAnswer ?? "");
  const [errors, setErrors] = useState<Errors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const base = `edit-${String(ext.id)}`;

  async function onSave() {
    setServerError(null);
    const found = validate(number, name, external, {
      always,
      busy,
      noAnswer,
    });
    setErrors(found);
    if (hasErrors(found)) return;
    const patch: Parameters<typeof updateExtension>[1] = {};
    if (number !== ext.number) patch.number = number;
    if (name.trim() !== ext.name) patch.name = name.trim();
    if (external.trim() !== (ext.externalNumber ?? "")) {
      patch.externalNumber = external.trim();
    }
    if (dnd !== (ext.dnd ?? false)) patch.dnd = dnd;
    if (voicemailEnabled !== (ext.voicemailEnabled !== false)) {
      patch.voicemailEnabled = voicemailEnabled;
    }
    if (always.trim() !== (ext.forwardAlways ?? "")) {
      patch.forwardAlways = always.trim();
    }
    if (busy.trim() !== (ext.forwardBusy ?? "")) {
      patch.forwardBusy = busy.trim();
    }
    if (noAnswer.trim() !== (ext.forwardNoAnswer ?? "")) {
      patch.forwardNoAnswer = noAnswer.trim();
    }
    if (Object.keys(patch).length === 0) {
      onCancel();
      return;
    }
    setSaving(true);
    try {
      onSaved(await updateExtension(ext.id, patch));
    } catch (err) {
      setErrors(serverFieldErrors(err));
      setServerError(errorMessage(err));
      setSaving(false);
    }
  }

  return (
    <tr>
      <td colSpan={6}>
        <form
          className="inline-form"
          aria-label={`Edit extension ${ext.number}`}
          onSubmit={(e) => {
            e.preventDefault();
            void onSave();
          }}
          noValidate
        >
          <div className="fields">
            <div className="field">
              <label htmlFor={`${base}-number`}>
                Number for extension {ext.number}
              </label>
              <input
                id={`${base}-number`}
                inputMode="numeric"
                value={number}
                autoFocus
                onChange={(e) => setNumber(e.target.value)}
                aria-invalid={errors.number ? true : undefined}
                aria-describedby={
                  errors.number ? `${base}-number-error` : undefined
                }
              />
              {errors.number && (
                <p id={`${base}-number-error`} className="field-error">
                  {errors.number}
                </p>
              )}
            </div>
            <div className="field">
              <label htmlFor={`${base}-name`}>
                Name for extension {ext.number}
              </label>
              <input
                id={`${base}-name`}
                value={name}
                onChange={(e) => setName(e.target.value)}
                aria-invalid={errors.name ? true : undefined}
                aria-describedby={
                  errors.name ? `${base}-name-error` : undefined
                }
              />
              {errors.name && (
                <p id={`${base}-name-error`} className="field-error">
                  {errors.name}
                </p>
              )}
            </div>
            <div className="field">
              <label htmlFor={`${base}-external`}>
                External number for extension {ext.number}
              </label>
              <input
                id={`${base}-external`}
                inputMode="tel"
                value={external}
                onChange={(e) => setExternal(e.target.value)}
                aria-invalid={errors.externalNumber ? true : undefined}
                aria-describedby={
                  errors.externalNumber ? `${base}-external-error` : undefined
                }
              />
              {errors.externalNumber && (
                <p id={`${base}-external-error`} className="field-error">
                  {errors.externalNumber}
                </p>
              )}
            </div>
          </div>

          <fieldset className="group">
            <legend>Call features</legend>
            <div className="fields">
              <div className="field checkbox">
                <input
                  id={`${base}-dnd`}
                  type="checkbox"
                  checked={dnd}
                  onChange={(e) => setDnd(e.target.checked)}
                />
                <label htmlFor={`${base}-dnd`}>
                  DND for extension {ext.number}
                </label>
              </div>
              <div className="field checkbox">
                <input
                  id={`${base}-vm`}
                  type="checkbox"
                  checked={voicemailEnabled}
                  onChange={(e) => setVoicemailEnabled(e.target.checked)}
                />
                <label htmlFor={`${base}-vm`}>
                  Voicemail for extension {ext.number}
                </label>
              </div>
            </div>
            <div className="fields">
              {FORWARD_FIELDS.map((f) => {
                const key = f.key;
                const value = { always, busy, noAnswer }[f.source];
                const setter = {
                  always: setAlways,
                  busy: setBusyField,
                  noAnswer: setNoAnswer,
                }[f.source];
                const id = `${base}-${key}`;
                return (
                  <div className="field" key={key}>
                    <label htmlFor={id}>
                      {f.label} for extension {ext.number}
                    </label>
                    <input
                      id={id}
                      inputMode="tel"
                      value={value}
                      aria-invalid={errors[key] ? true : undefined}
                      aria-describedby={errors[key] ? `${id}-error` : undefined}
                      onChange={(e) => setter(e.target.value)}
                    />
                    {errors[key] && (
                      <p id={`${id}-error`} className="field-error">
                        {errors[key]}
                      </p>
                    )}
                  </div>
                );
              })}
            </div>
            <p className="hint">Forward targets: {FORWARD_HINT}</p>
          </fieldset>

          {serverError && (
            <p role="alert" className="error">
              Could not save: {serverError}
            </p>
          )}
          <div className="actions start">
            <button type="submit" className="primary" disabled={saving}>
              Save
            </button>
            <button type="button" disabled={saving} onClick={onCancel}>
              Cancel
            </button>
          </div>
        </form>
      </td>
    </tr>
  );
}
