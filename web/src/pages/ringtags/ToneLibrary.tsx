import { useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../../api";
import {
  deleteRingtone,
  listRingtones,
  uploadRingtone,
  type Ringtone,
} from "../../api/ringtag";
import {
  Alert,
  Button,
  ConfirmDialog,
  EmptyState,
  Input,
  Spinner,
  useToast,
} from "../../design/azrty/components";
import { Can } from "../../role";
import { FormAlert } from "../callflow/ui";
import { useLoadedList } from "../timecond/shared";

/** The name form the API takes (1 to 32 of a-z, 0-9 and dashes). */
const TONE_NAME = /^[a-z0-9][a-z0-9-]{0,31}$/;
const MAX_BYTES = 512 * 1024;
const MAX_TONES = 64;

const seconds = (ms: number) => `${(ms / 1000).toFixed(1)} s`;

/**
 * The tone library: WAV files phones download and the ring map can choose.
 * A file is PCM, 8 or 16 kHz, mono, 16-bit, at most 30 s and 512 KiB. A tone
 * a ring map choice uses cannot be deleted.
 */
export function ToneLibrary() {
  const [state, replace] = useLoadedList(listRingtones);
  const [deleting, setDeleting] = useState<Ringtone | null>(null);
  const [name, setName] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [busy, setBusy] = useState(false);
  const [nameError, setNameError] = useState<string | undefined>();
  const [fileError, setFileError] = useState<string | undefined>();
  const [formError, setFormError] = useState<string | null>(null);
  // Changing the key empties the file input after an upload.
  const [round, setRound] = useState(0);
  const toast = useToast();

  async function onUpload(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    const badName = TONE_NAME.test(name)
      ? undefined
      : "Use 1 to 32 lowercase letters, digits or dashes.";
    const badFile = !file
      ? "Choose a WAV file."
      : file.size > MAX_BYTES
        ? "The file is larger than 512 KiB."
        : undefined;
    setNameError(badName);
    setFileError(badFile);
    if (badName || badFile || !file) return;
    setBusy(true);
    try {
      const saved = await uploadRingtone(name, file);
      replace((items) =>
        [...items, saved].sort((a, b) => a.name.localeCompare(b.name)),
      );
      setName("");
      setFile(null);
      setRound((n) => n + 1);
      toast.show(`Tone ${saved.name} added.`);
    } catch (err: unknown) {
      for (const f of fieldErrors(err)) {
        if (f.path === "name") setNameError(f.message);
        if (f.path === "file") setFileError(f.message);
      }
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function onDelete(t: Ringtone) {
    try {
      await deleteRingtone(t.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${t.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((items) => items.filter((x) => x.id !== t.id));
    setDeleting(null);
    toast.show(`Tone ${t.name} deleted.`);
  }

  const full = state.status === "ready" && state.items.length >= MAX_TONES;
  return (
    <div className="cf-stack">
      <Can>
        <form
          className="cf-stack"
          onSubmit={(e) => void onUpload(e)}
          noValidate
        >
          <Input
            id="tone-name"
            label="Tone name"
            value={name}
            error={nameError}
            hint="The name the ring map shows: lowercase letters, digits and dashes."
            disabled={busy || full}
            onChange={(e) => setName(e.target.value)}
          />
          <Input
            id="tone-file"
            key={round}
            label="WAV file"
            type="file"
            accept=".wav,audio/wav,audio/x-wav,audio/wave"
            error={fileError}
            hint="PCM, 8 or 16 kHz, mono, 16-bit, at most 30 seconds and 512 KiB."
            disabled={busy || full}
            onChange={(e) => setFile(e.target.files?.[0] ?? null)}
          />
          <FormAlert message={formError} unmatched={[]} />
          <div>
            <Button type="submit" icon="upload" disabled={busy || full}>
              Upload tone
            </Button>
          </div>
        </form>
      </Can>
      {state.status === "loading" && <Spinner label="Loading the tones…" />}
      {state.status === "error" && (
        <Alert tone="bad" title="Could not load the tones">
          {state.message}
        </Alert>
      )}
      {state.status === "ready" && state.items.length === 0 && (
        <EmptyState
          title="No custom tones"
          description="Upload a WAV file to choose it for a ring tag on Yealink and Snom phones."
        />
      )}
      {state.status === "ready" && state.items.length > 0 && (
        <ul className="cf-grid cf-grid--groups cf-plain" aria-label="Tones">
          {state.items.map((t) => (
            <li
              className="az-card cf-card"
              key={t.id}
              aria-label={`Tone ${t.name}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <div style={{ minWidth: 0 }}>
                  <h2 className="cf-card__group-name">{t.name}</h2>
                  <div className="cf-card__sub">
                    {seconds(t.durationMs)} · {t.sampleRate / 1000} kHz ·{" "}
                    {Math.max(1, Math.round(t.sizeBytes / 1024))} KiB
                    {t.usedBy > 0 &&
                      ` · used by ${t.usedBy} ring map ${t.usedBy === 1 ? "choice" : "choices"}`}
                  </div>
                </div>
              </div>
              <Can>
                <div className="cf-card__foot">
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete tone ${t.name}`}
                    onClick={() => setDeleting(t)}
                  >
                    Delete
                  </Button>
                </div>
              </Can>
            </li>
          ))}
        </ul>
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete tone ${deleting.name}?`}
          description="A tone that a ring map choice uses cannot be deleted; change the choice first."
          confirmLabel="Delete tone"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </div>
  );
}
