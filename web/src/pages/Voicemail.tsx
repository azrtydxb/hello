import {
  useEffect,
  useId,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent as ReactKeyboardEvent,
} from "react";
import {
  deleteVoicemailMessage,
  errorMessage,
  fieldErrors,
  getVoicemailBox,
  isPlayableAudio,
  listExtensions,
  listVoicemailMessages,
  markMessageHeard,
  updateVoicemailBox,
  voicemailAudioPath,
  type Extension,
  type FieldError,
  type Id,
  type VoicemailBox,
  type VoicemailMessage,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";
import { Field, fieldId, FormError, mapFieldErrors } from "../forms";
import { formatDuration, formatTime } from "../format";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready" };

/** One box's data: the box settings plus its message list. */
type BoxState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; box: VoicemailBox };

/**
 * Voicemail: pick a box (extension), list its messages, play them in the
 * browser, mark them heard/unheard, delete them, and edit the box settings.
 */
export function Voicemail() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [selectedId, setSelectedId] = useState<string>("");
  const [unheardOnly, setUnheardOnly] = useState(false);
  const [settingsFor, setSettingsFor] = useState<Extension | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then((items) => {
        if (controller.signal.aborted) return;
        setExtensions(items);
        setList({ status: "ready" });
        // Preselect the first box so the page shows something at once.
        setSelectedId((prev) =>
          prev === "" && items.length > 0 && items[0]
            ? String(items[0].id)
            : prev,
        );
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  const selected = extensions.find((e) => String(e.id) === selectedId) ?? null;

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Voicemail</h1>
      {list.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading extensions…
        </p>
      )}
      {list.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load the extensions.</strong>
          <p>{list.message}</p>
        </div>
      )}
      {list.status === "ready" && extensions.length === 0 && (
        <p className="muted">No extensions yet, so there are no boxes.</p>
      )}
      {list.status === "ready" && extensions.length > 0 && selected && (
        <>
          <div className="fields">
            <div className="field">
              <label htmlFor="voicemail-box">Box</label>
              <select
                id="voicemail-box"
                value={selectedId}
                onChange={(e) => setSelectedId(e.target.value)}
              >
                {extensions.map((e) => (
                  <option key={String(e.id)} value={String(e.id)}>
                    {e.number} — {e.name}
                  </option>
                ))}
              </select>
            </div>
            <div className="field checkbox">
              <input
                id="unheard-only"
                type="checkbox"
                checked={unheardOnly}
                onChange={(e) => setUnheardOnly(e.target.checked)}
              />
              <label htmlFor="unheard-only">Unheard only</label>
            </div>
            <button
              type="button"
              className="align-end"
              onClick={() => setSettingsFor(selected)}
            >
              Box settings for {selected.number}
            </button>
          </div>
          <BoxPanel
            key={`${String(selected.id)}:${unheardOnly ? "unheard" : "all"}`}
            extension={selected}
            unheardOnly={unheardOnly}
          />
        </>
      )}
      {settingsFor && (
        <BoxSettingsDialog
          extension={settingsFor}
          onClose={() => setSettingsFor(null)}
        />
      )}
    </section>
  );
}

/** One box: its message table and playback bar. */
function BoxPanel({
  extension,
  unheardOnly,
}: {
  extension: Extension;
  unheardOnly: boolean;
}) {
  const [boxState, setBoxState] = useState<BoxState>({ status: "loading" });
  const [messages, setMessages] = useState<VoicemailMessage[] | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [checking, setChecking] = useState<Id | null>(null);
  const [playing, setPlaying] = useState<Id | null>(null);
  const [playError, setPlayError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    getVoicemailBox(extension.id, controller.signal)
      .then((box) => {
        if (controller.signal.aborted) return;
        setBoxState({ status: "ready", box });
        return listVoicemailMessages(
          box.id,
          { unheard: unheardOnly },
          controller.signal,
        )
          .then((items) => {
            if (!controller.signal.aborted) setMessages(items);
          })
          .catch((err: unknown) => {
            if (!controller.signal.aborted) {
              setListError(errorMessage(err));
            }
          });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setBoxState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [extension.id, unheardOnly]);

  /** The playback gate: confirm the audio answers before playing anything. */
  async function onPlay(msg: VoicemailMessage) {
    setPlayError(null);
    setChecking(msg.id);
    try {
      const res = await fetch(voicemailAudioPath(msg.id), {
        credentials: "same-origin",
      });
      if (isPlayableAudio(res)) {
        setPlaying(msg.id);
      } else {
        setPlaying(null);
        setPlayError(
          `Could not play the message from ${msg.caller}: the audio is not available (HTTP ${res.status}).`,
        );
      }
    } catch (err: unknown) {
      setPlaying(null);
      setPlayError(
        `Could not play the message from ${msg.caller}: ${errorMessage(err)}`,
      );
    } finally {
      setChecking(null);
    }
  }

  async function onMarkHeard(msg: VoicemailMessage, heard: boolean) {
    setActionError(null);
    try {
      await markMessageHeard(msg.id, heard);
      setMessages(
        (prev) =>
          prev?.map((m) => (m.id === msg.id ? { ...m, heard } : m)) ?? prev,
      );
    } catch (err: unknown) {
      setActionError(
        `Could not mark the message from ${msg.caller}: ${errorMessage(err)}`,
      );
    }
  }

  async function onDelete(msg: VoicemailMessage) {
    setActionError(null);
    try {
      await deleteVoicemailMessage(msg.id);
      setMessages((prev) => prev?.filter((m) => m.id !== msg.id) ?? prev);
      if (playing === msg.id) setPlaying(null);
    } catch (err: unknown) {
      setActionError(
        `Could not delete the message from ${msg.caller}: ${errorMessage(err)}`,
      );
    }
  }

  const playingMessage =
    messages?.find((m) => String(m.id) === String(playing)) ?? null;

  return (
    <>
      <h2 id="vm-messages">Messages</h2>
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      {playError && (
        <p role="alert" className="error">
          {playError}
        </p>
      )}
      {boxState.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading messages…
        </p>
      )}
      {boxState.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load the messages.</strong>
          <p>{boxState.message}</p>
        </div>
      )}
      {listError && (
        <div role="alert" className="error">
          <strong>Could not load the messages.</strong>
          <p>{listError}</p>
        </div>
      )}
      {messages !== null && messages.length === 0 && (
        <p className="muted">
          {unheardOnly ? "No unheard messages." : "No messages."}
        </p>
      )}
      {playingMessage && (
        <div className="playbar">
          <p className="muted">
            Playing the message from {playingMessage.caller}, received{" "}
            {formatTime(playingMessage.createdAt)}.
          </p>
          <audio
            controls
            src={voicemailAudioPath(playingMessage.id)}
            aria-label={`Playback of the message from ${playingMessage.caller}`}
          />
        </div>
      )}
      {messages !== null && messages.length > 0 && (
        <table aria-labelledby="vm-messages">
          <thead>
            <tr>
              <th scope="col">From</th>
              <th scope="col">Received</th>
              <th scope="col">Duration</th>
              <th scope="col">Heard</th>
              <th scope="col">Email</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {messages.map((msg) => (
              <tr key={String(msg.id)}>
                <th scope="row">{msg.caller}</th>
                <td>{formatTime(msg.createdAt)}</td>
                <td>{formatDuration(msg.durationMs)}</td>
                <td>{msg.heard ? "Yes" : "No"}</td>
                <td>{msg.emailStatus ?? "—"}</td>
                <td className="row-actions">
                  <button
                    type="button"
                    disabled={checking !== null}
                    onClick={() =>
                      playing === msg.id ? setPlaying(null) : void onPlay(msg)
                    }
                  >
                    {playing === msg.id
                      ? "Stop"
                      : `Play the message from ${msg.caller}`}
                  </button>
                  <button
                    type="button"
                    disabled={checking !== null}
                    aria-label={`Mark the message from ${msg.caller} ${msg.heard ? "unheard" : "heard"}`}
                    onClick={() => void onMarkHeard(msg, !msg.heard)}
                  >
                    {msg.heard ? "Mark unheard" : "Mark heard"}
                  </button>
                  <ConfirmButton
                    label="Delete"
                    accessibleLabel={`Delete the message from ${msg.caller}`}
                    prompt={`Delete the message from ${msg.caller}?`}
                    confirmLabel="Delete message"
                    onConfirm={() => onDelete(msg)}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </>
  );
}

/** The dialog's form errors: client checks plus server field errors. */
interface SettingsErrors {
  email?: string;
  password?: string;
  greeting?: string;
  unreachable?: string;
}

const EMAIL_HINT = "Where messages are emailed; leave empty for none.";

/** Modal box settings: password, email address, greeting uploads. */
function BoxSettingsDialog({
  extension,
  onClose,
}: {
  extension: Extension;
  onClose: () => void;
}) {
  const titleId = useId();
  const dialogRef = useRef<HTMLDivElement>(null);
  const [load, setLoad] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready" }
  >({ status: "loading" });
  const [box, setBox] = useState<VoicemailBox | null>(null);
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [greeting, setGreeting] = useState<File | null>(null);
  const [unreachable, setUnreachable] = useState<File | null>(null);
  const [errors, setErrors] = useState<SettingsErrors>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    getVoicemailBox(extension.id, controller.signal)
      .then((b) => {
        setBox(b);
        setEmail(b.email);
        setLoad({ status: "ready" });
      })
      .catch((err: unknown) => {
        setLoad({ status: "error", message: errorMessage(err) });
      });
    return () => controller.abort();
  }, [extension.id]);

  // Move focus into the dialog on open and back where it was on close.
  useEffect(() => {
    const previous =
      document.activeElement instanceof HTMLElement
        ? document.activeElement
        : null;
    dialogRef.current?.querySelector<HTMLInputElement>("input")?.focus();
    return () => {
      if (previous && previous.isConnected) previous.focus();
    };
  }, []);

  function onKeyDown(e: ReactKeyboardEvent<HTMLDivElement>) {
    if (e.key === "Escape") {
      e.stopPropagation();
      onClose();
      return;
    }
    if (e.key !== "Tab" || !dialogRef.current) return;
    const focusable = Array.from(
      dialogRef.current.querySelectorAll<HTMLElement>("button, input, select"),
    ).filter((el) => !el.hasAttribute("disabled"));
    const first = focusable[0];
    const last = focusable[focusable.length - 1];
    if (!first || !last) return;
    if (e.shiftKey && document.activeElement === first) {
      e.preventDefault();
      last.focus();
    } else if (!e.shiftKey && document.activeElement === last) {
      e.preventDefault();
      first.focus();
    }
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    const found: SettingsErrors = {};
    const address = email.trim();
    if (address !== "" && !address.includes("@")) {
      found.email = "Enter an email address, or leave it empty.";
    }
    if (password !== "" && password.length < 4) {
      found.password = "The password must be at least 4 characters.";
    }
    setErrors(found);
    if (found.email || found.password) return;
    setBusy(true);
    try {
      await updateVoicemailBox(extension.id, {
        email: address,
        ...(password !== "" ? { password } : {}),
        ...(greeting ? { greeting } : {}),
        ...(unreachable ? { unreachable } : {}),
      });
      onClose();
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), [
        "email",
        "password",
        "greeting",
        "unreachable",
      ]);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  const ready = load.status === "ready";
  return (
    <div className="backdrop">
      <div
        ref={dialogRef}
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        onKeyDown={onKeyDown}
      >
        <h2 id={titleId}>Box settings for {extension.number}</h2>
        {load.status === "error" && (
          <div role="alert" className="error">
            <strong>Could not load the box settings.</strong>
            <p>{load.message}</p>
          </div>
        )}
        <form onSubmit={(e) => void onSubmit(e)} noValidate>
          <Field
            id={fieldId("vm-settings", "email")}
            label="Email address"
            error={errors.email}
            hint={EMAIL_HINT}
          >
            {(p) => (
              <input
                {...p}
                type="email"
                value={email}
                disabled={!ready}
                onChange={(e) => setEmail(e.target.value)}
              />
            )}
          </Field>
          <Field
            id={fieldId("vm-settings", "password")}
            label="New password"
            error={errors.password}
            hint="Leave empty to keep the current one."
          >
            {(p) => (
              <input
                {...p}
                type="password"
                value={password}
                disabled={!ready}
                autoComplete="new-password"
                onChange={(e) => setPassword(e.target.value)}
              />
            )}
          </Field>
          <div className="field">
            <label htmlFor={fieldId("vm-settings", "greeting")}>
              Greeting audio (WAV)
            </label>
            <input
              id={fieldId("vm-settings", "greeting")}
              type="file"
              accept=".wav,audio/wav,audio/x-wav"
              disabled={!ready}
              onChange={(e) => setGreeting(e.target.files?.[0] ?? null)}
            />
            <p className="hint">
              {box?.hasGreeting
                ? "A greeting is set; uploading replaces it."
                : "Played before the beep."}
            </p>
          </div>
          <div className="field">
            <label htmlFor={fieldId("vm-settings", "unreachable")}>
              Unreachable greeting audio (WAV)
            </label>
            <input
              id={fieldId("vm-settings", "unreachable")}
              type="file"
              accept=".wav,audio/wav,audio/x-wav"
              disabled={!ready}
              onChange={(e) => setUnreachable(e.target.files?.[0] ?? null)}
            />
            <p className="hint">
              {box?.hasUnreachableGreeting
                ? "An unreachable greeting is set; uploading replaces it."
                : "Played when the extension is unreachable."}
            </p>
          </div>
          <FormError message={formError} unmatched={unmatched} />
          {!ready && load.status !== "error" && (
            <p role="status" aria-live="polite">
              Loading the box…
            </p>
          )}
          <div className="actions">
            <button type="submit" className="primary" disabled={busy || !ready}>
              Save box settings
            </button>
            <button type="button" disabled={busy} onClick={onClose}>
              Cancel
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}
