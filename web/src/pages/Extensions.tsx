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
}

const hasErrors = (e: Errors) =>
  Boolean(e.number || e.name || e.externalNumber);

function validate(number: string, name: string, external: string): Errors {
  const errors: Errors = {};
  if (!EXTENSION_NUMBER_PATTERN.test(number)) {
    errors.number = "The number must be 2 to 10 digits (0–9 only).";
  }
  if (name.trim() === "") errors.name = "Enter a name.";
  if (!EXTERNAL_NUMBER_PATTERN.test(external.trim())) {
    errors.externalNumber =
      "Use 2 to 20 digits, optionally starting with +, or leave it empty.";
  }
  return errors;
}

/** Server field errors on number, name or externalNumber. */
function serverFieldErrors(err: unknown): Errors {
  return mapFieldErrors(fieldErrors(err), ["number", "name", "externalNumber"])
    .byKey;
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
    const found = validate(number, name, external);
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
  const [errors, setErrors] = useState<Errors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const base = `edit-${String(ext.id)}`;

  async function onSave() {
    setServerError(null);
    const found = validate(number, name, external);
    setErrors(found);
    if (hasErrors(found)) return;
    const patch: { number?: string; name?: string; externalNumber?: string } =
      {};
    if (number !== ext.number) patch.number = number;
    if (name.trim() !== ext.name) patch.name = name.trim();
    if (external.trim() !== (ext.externalNumber ?? "")) {
      patch.externalNumber = external.trim();
    }
    if (Object.keys(patch).length === 0) {
      onCancel();
      return;
    }
    setBusy(true);
    try {
      onSaved(await updateExtension(ext.id, patch));
    } catch (err) {
      setErrors(serverFieldErrors(err));
      setServerError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <tr
      onKeyDown={(e) => {
        if (e.key === "Escape") onCancel();
        if (e.key === "Enter" && e.target instanceof HTMLInputElement) {
          e.preventDefault();
          void onSave();
        }
      }}
    >
      <th scope="row">
        <label className="visually-hidden" htmlFor={`${base}-number`}>
          Number for extension {ext.number}
        </label>
        <input
          id={`${base}-number`}
          inputMode="numeric"
          value={number}
          autoFocus
          onChange={(e) => setNumber(e.target.value)}
          aria-invalid={errors.number ? true : undefined}
          aria-describedby={errors.number ? `${base}-number-error` : undefined}
        />
        {errors.number && (
          <p id={`${base}-number-error`} className="field-error">
            {errors.number}
          </p>
        )}
      </th>
      <td>
        <label className="visually-hidden" htmlFor={`${base}-name`}>
          Name for extension {ext.number}
        </label>
        <input
          id={`${base}-name`}
          value={name}
          onChange={(e) => setName(e.target.value)}
          aria-invalid={errors.name ? true : undefined}
          aria-describedby={errors.name ? `${base}-name-error` : undefined}
        />
        {errors.name && (
          <p id={`${base}-name-error`} className="field-error">
            {errors.name}
          </p>
        )}
      </td>
      <td>
        <label className="visually-hidden" htmlFor={`${base}-external`}>
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
        {serverError && (
          <p role="alert" className="error">
            Could not save: {serverError}
          </p>
        )}
      </td>
      <td className="row-actions">
        <button
          type="button"
          className="primary"
          disabled={busy}
          onClick={() => void onSave()}
        >
          Save
        </button>
        <button type="button" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
      </td>
    </tr>
  );
}
