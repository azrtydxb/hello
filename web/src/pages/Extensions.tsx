import { useEffect, useState, type FormEvent } from "react";
import {
  createExtension,
  deleteExtension,
  errorMessage,
  EXTENSION_NUMBER_PATTERN,
  listExtensions,
  updateExtension,
  type Extension,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";

const NUMBER_HINT = "2 to 10 digits.";

interface Errors {
  number?: string;
  name?: string;
}

function validate(number: string, name: string): Errors {
  const errors: Errors = {};
  if (!EXTENSION_NUMBER_PATTERN.test(number)) {
    errors.number = "The number must be 2 to 10 digits (0–9 only).";
  }
  if (name.trim() === "") errors.name = "Enter a name.";
  return errors;
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
  const [errors, setErrors] = useState<Errors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerError(null);
    const found = validate(number, name);
    setErrors(found);
    if (found.number || found.name) return;
    setBusy(true);
    try {
      const ext = await createExtension({ number, name: name.trim() });
      onCreated(ext);
      setNumber("");
      setName("");
    } catch (err) {
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
  const [errors, setErrors] = useState<Errors>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const base = `edit-${String(ext.id)}`;

  async function onSave() {
    setServerError(null);
    const found = validate(number, name);
    setErrors(found);
    if (found.number || found.name) return;
    const patch: { number?: string; name?: string } = {};
    if (number !== ext.number) patch.number = number;
    if (name.trim() !== ext.name) patch.name = name.trim();
    if (!patch.number && patch.name === undefined) {
      onCancel();
      return;
    }
    setBusy(true);
    try {
      onSaved(await updateExtension(ext.id, patch));
    } catch (err) {
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
