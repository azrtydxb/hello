import { useEffect, useState, type FormEvent } from "react";
import {
  deleteAnnouncement,
  errorMessage,
  listAnnouncements,
  MAX_ANNOUNCEMENT_BYTES,
  SIP_USERNAME_PATTERN,
  uploadAnnouncement,
  type Announcement,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";
import { formatTime } from "../format";

const NAME_HINT = "1-64 of A-Z a-z 0-9 . _ -; used by ring groups and codes.";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Announcement[] };

/** Announcements: named WAVs, uploaded as multipart, played by the PBX. */
export function Announcements() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [name, setName] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [errors, setErrors] = useState<{ name?: string; file?: string }>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    listAnnouncements(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    const found: { name?: string; file?: string } = {};
    const trimmed = name.trim();
    if (!SIP_USERNAME_PATTERN.test(trimmed)) {
      found.name = "Enter a name of 1-64 of A-Z a-z 0-9 . _ -.";
    }
    if (!file) {
      found.file = "Choose a WAV file.";
    } else if (file.size > MAX_ANNOUNCEMENT_BYTES) {
      found.file = "The file is larger than 10 MiB.";
    } else if (file.size === 0) {
      found.file = "The file is empty.";
    }
    setErrors(found);
    if (found.name || found.file) return;
    setBusy(true);
    try {
      const created = await uploadAnnouncement(trimmed, file!);
      setList((prev) =>
        prev.status === "ready"
          ? {
              status: "ready",
              items: [...prev.items, created].sort((a, b) =>
                a.name.localeCompare(b.name),
              ),
            }
          : prev,
      );
      setName("");
      setFile(null);
      setErrors({});
    } catch (err: unknown) {
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function onDelete(a: Announcement) {
    setActionError(null);
    try {
      await deleteAnnouncement(a.id);
      setList((prev) =>
        prev.status === "ready"
          ? { status: "ready", items: prev.items.filter((i) => i.id !== a.id) }
          : prev,
      );
    } catch (err: unknown) {
      setActionError(`Could not delete ${a.name}: ${errorMessage(err)}`);
    }
  }

  const items = list.status === "ready" ? list.items : null;

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Announcements</h1>
      <form
        className="inline-form"
        aria-labelledby="new-announcement"
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <h2 id="new-announcement">New announcement</h2>
        <div className="fields">
          <div className="field">
            <label htmlFor="ann-name">Name</label>
            <input
              id="ann-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              aria-invalid={errors.name ? true : undefined}
              aria-describedby={
                errors.name ? "ann-name-error" : "ann-name-hint"
              }
            />
            {errors.name ? (
              <p id="ann-name-error" className="field-error">
                {errors.name}
              </p>
            ) : (
              <p id="ann-name-hint" className="hint">
                {NAME_HINT}
              </p>
            )}
          </div>
          <div className="field">
            <label htmlFor="ann-file">Audio (WAV)</label>
            <input
              id="ann-file"
              type="file"
              accept=".wav,audio/wav,audio/x-wav"
              onChange={(e) => setFile(e.target.files?.[0] ?? null)}
              aria-invalid={errors.file ? true : undefined}
              aria-describedby={errors.file ? "ann-file-error" : undefined}
            />
            {errors.file && (
              <p id="ann-file-error" className="field-error">
                {errors.file}
              </p>
            )}
            <p className="hint">At most 10 MiB of WAV audio.</p>
          </div>
        </div>
        {formError && (
          <p role="alert" className="error">
            Could not upload: {formError}
          </p>
        )}
        <button type="submit" className="primary" disabled={busy}>
          Upload announcement
        </button>
      </form>

      <h2 id="announcements-list">All announcements</h2>
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      {list.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading announcements…
        </p>
      )}
      {list.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load announcements.</strong>
          <p>{list.message}</p>
        </div>
      )}
      {items && items.length === 0 && (
        <p className="muted">No announcements yet.</p>
      )}
      {items && items.length > 0 && (
        <table aria-labelledby="announcements-list">
          <thead>
            <tr>
              <th scope="col">Name</th>
              <th scope="col">Created</th>
              <th scope="col">Updated</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {items.map((a) => (
              <tr key={String(a.id)}>
                <th scope="row">{a.name}</th>
                <td>{formatTime(a.createdAt)}</td>
                <td>{formatTime(a.updatedAt)}</td>
                <td className="row-actions">
                  <ConfirmButton
                    label="Delete"
                    accessibleLabel={`Delete announcement ${a.name}`}
                    prompt={`Delete the announcement ${a.name}?`}
                    confirmLabel="Delete announcement"
                    onConfirm={() => onDelete(a)}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </section>
  );
}
