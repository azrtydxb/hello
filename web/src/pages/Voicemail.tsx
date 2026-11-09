import { useEffect, useState, type FormEvent } from "react";
import {
  deleteVoicemailMessage,
  errorMessage,
  fieldErrors,
  listVoicemailMessages,
  markMessageHeard,
  updateVoicemailBox,
  voicemailAudioPath,
  type FieldError,
  type Id,
  type VoicemailMessage,
} from "../api";
import {
  getVoicemailBoxDetail,
  greetingLabel,
  greetingStatus,
  isCustomGreeting,
  listVoicemailBoxes,
  previewDefaultGreeting,
  PROMPT_LANGUAGE_LABELS,
  promptLanguageLabel,
  revertGreeting,
  setBoxLanguage,
  type GreetingSlot,
  type VoicemailBoxDetail,
  type VoicemailBoxSummary,
} from "../api/media";
import { type Announcement, listAnnouncements } from "../api";
import { MAX_TTS_CHARS, putVoicemailGreeting } from "../api/tts";
import {
  Alert,
  Badge,
  type BadgeTone,
  Button,
  ConfirmDialog,
  EmptyState,
  Icon,
  IconButton,
  Input,
  Modal,
  navItemClassName,
  Select,
  PageHeader,
  Spinner,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { formatDuration, formatTime } from "../format";
import { mapFieldErrors } from "../forms";
import { NowPlaying, usePlayback } from "./media/MediaParts";
import { ModelVoiceFields, useTtsModels } from "./media/TtsParts";
import { Can } from "../role";

type Load<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; value: T };

/** How the email column reads a message's delivery state. */
function emailTone(status: string | undefined): BadgeTone {
  if (status === "sent") return "outline";
  if (status === "failed") return "bad";
  return "neutral";
}

/**
 * Voicemail: the boxes on the left with their unheard counts; the selected
 * box's settings summary, now-playing card and messages on the right.
 */
export function Voicemail() {
  const [boxes, setBoxes] = useState<Load<VoicemailBoxSummary[]>>({
    status: "loading",
  });
  const [selectedId, setSelectedId] = useState<string>("");
  const toast = useToast();
  const showToast = toast.show;

  useEffect(() => {
    const controller = new AbortController();
    listVoicemailBoxes(controller.signal)
      .then((items) => {
        setBoxes({ status: "ready", value: items });
        setSelectedId((prev) =>
          prev === "" && items[0] ? String(items[0].id) : prev,
        );
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setBoxes({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  /** Keep a box's unheard count in step with what the panel changed. */
  function adjustUnheard(boxId: Id, delta: number, totalDelta = 0) {
    setBoxes((prev) =>
      prev.status === "ready"
        ? {
            status: "ready",
            value: prev.value.map((b) =>
              String(b.id) === String(boxId)
                ? {
                    ...b,
                    unheard: Math.max(0, b.unheard + delta),
                    total: Math.max(0, b.total + totalDelta),
                  }
                : b,
            ),
          }
        : prev,
    );
  }

  const list = boxes.status === "ready" ? boxes.value : [];
  const selected = list.find((b) => String(b.id) === selectedId) ?? null;

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Media"
        title="Voicemail"
        description="One box per extension. Messages are stored in object storage and can be emailed."
      />
      {boxes.status === "loading" && <Spinner label="Loading boxes…" />}
      {boxes.status === "error" && (
        <Alert tone="bad" title="Could not load the voicemail boxes">
          {boxes.message}
        </Alert>
      )}
      {boxes.status === "ready" && list.length === 0 && (
        <EmptyState
          icon="voicemail"
          title="No voicemail boxes"
          description="Every extension gets a box when it is created. Add an extension first."
        />
      )}
      {list.length > 0 && (
        <div className="vm-grid">
          <ul className="az-card vm-boxes" aria-label="Voicemail boxes">
            {list.map((b) => {
              const active = String(b.id) === selectedId;
              return (
                <li key={String(b.id)}>
                  <button
                    type="button"
                    className={navItemClassName(active)}
                    aria-current={active ? "true" : undefined}
                    onClick={() => setSelectedId(String(b.id))}
                  >
                    <span className="media-mono">{b.number}</span>
                    <span className="vm-boxes__name">{b.name}</span>
                    {b.unheard > 0 && (
                      <Badge tone="info">
                        {b.unheard}
                        <span className="visually-hidden"> unheard</span>
                      </Badge>
                    )}
                  </button>
                </li>
              );
            })}
          </ul>
          {selected && (
            <BoxPanel
              key={String(selected.id)}
              box={selected}
              onUnheard={(delta, totalDelta) =>
                adjustUnheard(selected.id, delta, totalDelta)
              }
              onToast={showToast}
            />
          )}
        </div>
      )}
      {toast.node}
    </section>
  );
}

/** One box: its summary card, now-playing card and message table. */
function BoxPanel({
  box,
  onUnheard,
  onToast,
}: {
  box: VoicemailBoxSummary;
  onUnheard: (delta: number, totalDelta?: number) => void;
  onToast: (message: string) => void;
}) {
  const [detail, setDetail] = useState<Load<VoicemailBoxDetail>>({
    status: "loading",
  });
  const [messages, setMessages] = useState<Load<VoicemailMessage[]>>({
    status: "loading",
  });
  const [actionError, setActionError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<VoicemailMessage | null>(null);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const playback = usePlayback(
    (caller) => `Could not play the message from ${caller}`,
  );

  useEffect(() => {
    const controller = new AbortController();
    getVoicemailBoxDetail(box.extensionId, controller.signal)
      .then((value) => setDetail({ status: "ready", value }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setDetail({ status: "error", message: errorMessage(err) });
        }
      });
    listVoicemailMessages(box.id, {}, controller.signal)
      .then((value) => setMessages({ status: "ready", value }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setMessages({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [box.id, box.extensionId]);

  async function onMarkHeard(msg: VoicemailMessage, heard: boolean) {
    setActionError(null);
    try {
      await markMessageHeard(msg.id, heard);
      setMessages((prev) =>
        prev.status === "ready"
          ? {
              status: "ready",
              value: prev.value.map((m) =>
                m.id === msg.id ? { ...m, heard } : m,
              ),
            }
          : prev,
      );
      if (msg.heard !== heard) onUnheard(heard ? -1 : 1);
      onToast(
        `Message from ${msg.caller} marked ${heard ? "heard" : "unheard"}.`,
      );
    } catch (err: unknown) {
      setActionError(
        `Could not mark the message from ${msg.caller}: ${errorMessage(err)}`,
      );
    }
  }

  async function onDelete(msg: VoicemailMessage) {
    await deleteVoicemailMessage(msg.id);
    setMessages((prev) =>
      prev.status === "ready"
        ? { status: "ready", value: prev.value.filter((m) => m.id !== msg.id) }
        : prev,
    );
    onUnheard(msg.heard ? 0 : -1, -1);
    if (String(playback.playing) === String(msg.id)) playback.stop();
    setDeleting(null);
    onToast(`Message from ${msg.caller} deleted.`);
  }

  const items = messages.status === "ready" ? messages.value : [];
  const playingMessage =
    items.find((m) => String(m.id) === String(playback.playing)) ?? null;
  const d = detail.status === "ready" ? detail.value : null;

  const columns: TableColumn<VoicemailMessage>[] = [
    {
      key: "play",
      label: <span className="visually-hidden">Play</span>,
      width: 44,
      primary: false,
      render: (m) => {
        const isPlaying = String(playback.playing) === String(m.id);
        return (
          <IconButton
            icon={isPlaying ? "pause" : "play"}
            label={
              isPlaying
                ? `Stop the message from ${m.caller}`
                : `Play the message from ${m.caller}`
            }
            disabled={playback.checking !== null}
            onClick={() =>
              isPlaying
                ? playback.stop()
                : void playback.play(m.id, voicemailAudioPath(m.id), m.caller)
            }
          />
        );
      },
    },
    {
      key: "caller",
      label: "Caller",
      mono: true,
      render: (m) => (
        <span className={m.heard ? undefined : "media-unheard"}>
          {m.callerName
            ? `${m.callerName} (${m.caller || "—"})`
            : m.caller || "—"}
        </span>
      ),
    },
    {
      key: "received",
      label: "Received",
      render: (m) => formatTime(m.createdAt),
    },
    {
      key: "length",
      label: "Length",
      align: "right",
      mono: true,
      render: (m) => formatDuration(m.durationMs),
    },
    {
      key: "email",
      label: "Email",
      // Without SMTP every message stays "pending" forever; say so once in
      // the box summary instead of on each row.
      render: (m) =>
        m.emailStatus && d?.emailDelivery !== false ? (
          <Badge tone={emailTone(m.emailStatus)}>{m.emailStatus}</Badge>
        ) : (
          "—"
        ),
    },
    {
      key: "status",
      label: "Status",
      render: (m) => (m.heard ? "Heard" : "New"),
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (m) => (
        <div className="media-actions">
          <Can>
            <IconButton
              icon={m.heard ? "mail" : "check-check"}
              label={`Mark the message from ${m.caller} ${m.heard ? "unheard" : "heard"}`}
              onClick={() => void onMarkHeard(m, !m.heard)}
            />
          </Can>
          <Can>
            <IconButton
              icon="trash-2"
              label={`Delete the message from ${m.caller}`}
              onClick={() => setDeleting(m)}
            />
          </Can>
        </div>
      ),
    },
  ];

  return (
    <div className="media-stack">
      <div className="az-card vm-box">
        <div>
          <span className="az-eyebrow">Box</span>
          <h2 className="vm-box__title">
            <span className="media-mono">{box.number}</span> · {box.name}
          </h2>
        </div>
        <ul className="vm-box__facts" aria-label="Box settings summary">
          <li>
            <Icon name="mail" size={14} />
            {d
              ? d.emailDelivery === false
                ? "Email not configured"
                : d.email || "No email"
              : "—"}
          </li>
          <li>
            <Icon name="lock" size={14} />
            {d ? (d.hasPassword ? "PIN set" : "PIN not set") : "PIN —"}
          </li>
          <li>
            <Icon name="audio-lines" size={14} />
            {d ? greetingLabel(d) : "—"}
          </li>
        </ul>
        <Button
          variant="secondary"
          size="sm"
          icon="settings-2"
          className="vm-box__settings"
          disabled={!d}
          onClick={() => setSettingsOpen(true)}
        >
          Box settings
        </Button>
      </div>
      {detail.status === "error" && (
        <Alert tone="bad" title="Could not load the box settings">
          {detail.message}
        </Alert>
      )}
      {actionError && <Alert tone="bad">{actionError}</Alert>}
      {playback.error && <Alert tone="bad">{playback.error}</Alert>}
      {playingMessage && (
        <NowPlaying
          key={String(playingMessage.id)}
          src={voicemailAudioPath(playingMessage.id)}
          title={playingMessage.caller}
          label={`Playback of the message from ${playingMessage.caller}`}
          durationMs={playingMessage.durationMs}
          onStop={playback.stop}
        />
      )}
      {messages.status === "loading" && <Spinner label="Loading messages…" />}
      {messages.status === "error" && (
        <Alert tone="bad" title="Could not load the messages">
          {messages.message}
        </Alert>
      )}
      {messages.status === "ready" && items.length === 0 && (
        <EmptyState
          icon="inbox"
          title="No messages"
          description={`Messages left for ${box.number} appear here.`}
        />
      )}
      {items.length > 0 && (
        <Table
          caption={`Messages in box ${box.number}`}
          columns={columns}
          rows={items}
          rowKey={(m) => String(m.id)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete message?"
          description={`The message from ${deleting.caller} and its audio are removed. This cannot be undone.`}
          confirmLabel="Delete message"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {settingsOpen && d && (
        <BoxSettings
          box={box}
          detail={d}
          onClose={() => setSettingsOpen(false)}
          onGreeting={(value, message) => {
            setDetail({ status: "ready", value });
            onToast(message);
          }}
          onSaved={(value) => {
            setDetail({ status: "ready", value });
            setSettingsOpen(false);
            onToast(`Box ${box.number} saved.`);
          }}
        />
      )}
    </div>
  );
}

interface SettingsErrors {
  email?: string;
  password?: string;
  greeting?: string;
  busy?: string;
  unreachable?: string;
  name?: string;
}

/** Box settings: email address, PIN and the greetings. */
function BoxSettings({
  box,
  detail,
  onClose,
  onSaved,
  onGreeting,
}: {
  box: VoicemailBoxSummary;
  detail: VoicemailBoxDetail;
  onClose: () => void;
  onSaved: (detail: VoicemailBoxDetail) => void;
  /** A greeting was set from an announcement or text; the modal stays open. */
  onGreeting: (detail: VoicemailBoxDetail, message: string) => void;
}) {
  const [email, setEmail] = useState(detail.email);
  const [password, setPassword] = useState("");
  // "" is "Default": the box inherits the deployment language.
  const [language, setLanguage] = useState(detail.promptLanguage ?? "");
  const [greeting, setGreeting] = useState<File | null>(null);
  const [busyFile, setBusyFile] = useState<File | null>(null);
  const [unreachable, setUnreachable] = useState<File | null>(null);
  const [nameFile, setNameFile] = useState<File | null>(null);
  const [errors, setErrors] = useState<SettingsErrors>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const formId = `vm-settings-${String(box.id)}`;

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    const found: SettingsErrors = {};
    const address = email.trim();
    if (address !== "" && !address.includes("@")) {
      found.email = "Enter an email address, or leave it empty.";
    }
    if (password !== "" && password.length < 4) {
      found.password = "The PIN must be at least 4 characters.";
    }
    setErrors(found);
    if (found.email || found.password) return;
    setBusy(true);
    try {
      // The language has its own route: the box PUT may be multipart.
      if (language !== (detail.promptLanguage ?? "")) {
        await setBoxLanguage(box.id, language);
      }
      // The PUT answers with the box as GET sends it (the detail shape).
      const saved = (await updateVoicemailBox(box.extensionId, {
        email: address,
        ...(password !== "" ? { password } : {}),
        ...(greeting ? { greeting } : {}),
        ...(busyFile ? { busy: busyFile } : {}),
        ...(unreachable ? { unreachable } : {}),
        ...(nameFile ? { name: nameFile } : {}),
      })) as unknown as VoicemailBoxDetail;
      onSaved({ ...detail, ...saved, promptLanguage: language });
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), [
        "email",
        "password",
        "greeting",
        "busy",
        "unreachable",
        "name",
      ]);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={`Box settings for ${box.number}`}
      description={`${box.name}. Leave the PIN empty to keep the current one.`}
      onClose={busy ? undefined : onClose}
      actions={
        <>
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={onClose}
          >
            Cancel
          </Button>
          <Can>
            <Button type="submit" size="sm" form={formId} disabled={busy}>
              Save box settings
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={formId}
        className="media-modal-form"
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id={`${formId}-email`}
          label="Email address"
          type="email"
          value={email}
          autoFocus
          error={errors.email}
          hint={
            detail.emailDelivery === false
              ? "Email delivery is not configured on this server; the address is kept for when it is."
              : "Where messages are emailed; leave empty for none."
          }
          onChange={(e) => setEmail(e.target.value)}
        />
        <Input
          id={`${formId}-password`}
          label="New PIN"
          type="password"
          autoComplete="new-password"
          value={password}
          error={errors.password}
          hint={
            detail.hasPassword
              ? "A PIN is set; leave empty to keep it."
              : "No PIN is set."
          }
          onChange={(e) => setPassword(e.target.value)}
        />
        <Select
          id={`${formId}-language`}
          label="Prompt language"
          hint="The language of the spoken prompts for this box: the default greeting and the retrieval menus. Default follows the system language, English unless set."
          value={language}
          disabled={busy}
          onChange={(e) => setLanguage(e.target.value)}
          options={[
            { value: "", label: "Default" },
            ...Object.keys(PROMPT_LANGUAGE_LABELS).map((code) => ({
              value: code,
              label: promptLanguageLabel(code),
            })),
          ]}
        />
        <GreetingSource
          id={`${formId}-greeting`}
          slot="greeting"
          label="Greeting"
          error={errors.greeting}
          box={box}
          detail={detail}
          disabled={busy}
          onFile={setGreeting}
          onApplied={onGreeting}
        />
        <GreetingSource
          id={`${formId}-busy`}
          slot="busy"
          label="Busy greeting"
          error={errors.busy}
          box={box}
          detail={detail}
          disabled={busy}
          onFile={setBusyFile}
          onApplied={onGreeting}
        />
        <GreetingSource
          id={`${formId}-unreachable`}
          slot="unreachable"
          label="Unreachable greeting"
          error={errors.unreachable}
          box={box}
          detail={detail}
          disabled={busy}
          onFile={setUnreachable}
          onApplied={onGreeting}
        />
        <GreetingSource
          id={`${formId}-name`}
          slot="name"
          label="Recorded name"
          error={errors.name}
          box={box}
          detail={detail}
          disabled={busy}
          onFile={setNameFile}
          onApplied={onGreeting}
        />
        {(formError || unmatched.length > 0) && (
          <Alert tone="bad" title="Could not save the box">
            {formError}
            {unmatched.map((fe) => (
              <span key={fe.path} className="media-mono">
                {" "}
                {fe.path}: {fe.message}
              </span>
            ))}
          </Alert>
        )}
      </form>
    </Modal>
  );
}

/** A labelled WAV file input in the design system's field layout. */
function FileField({
  id,
  label,
  hint,
  error,
  onFile,
}: {
  id: string;
  label: string;
  hint: string;
  error?: string;
  onFile: (file: File | null) => void;
}) {
  return (
    <div className={error ? "az-field az-field--error" : "az-field"}>
      <label className="az-field__label" htmlFor={id}>
        {label}
      </label>
      <input
        id={id}
        className="az-input"
        type="file"
        accept=".wav,audio/wav,audio/x-wav,audio/wave"
        aria-invalid={error ? true : undefined}
        aria-describedby={`${id}-hint`}
        onChange={(e) => onFile(e.target.files?.[0] ?? null)}
      />
      <span className="az-field__hint" id={`${id}-hint`}>
        {error || hint}
      </span>
    </div>
  );
}

type GreetingMode = "upload" | "announcement" | "text";

/**
 * One greeting's source: upload a WAV (saved with the box), pick an existing
 * announcement, or write text and generate it once. The last two apply at
 * once, on their own button; generating is always an explicit click and the
 * same text, voice and model never generate twice.
 */
function GreetingSource({
  id,
  slot,
  label,
  error,
  box,
  detail,
  disabled,
  onFile,
  onApplied,
}: {
  id: string;
  slot: GreetingSlot;
  label: string;
  error?: string;
  box: VoicemailBoxSummary;
  detail: VoicemailBoxDetail;
  disabled?: boolean;
  onFile: (file: File | null) => void;
  onApplied: (detail: VoicemailBoxDetail, message: string) => void;
}) {
  const object = {
    greeting: detail.greetingObject,
    busy: detail.busyObject,
    unreachable: detail.unreachableObject,
    name: detail.nameObject,
  }[slot];
  const record = {
    greeting: detail.greetingTts,
    busy: detail.busyTts,
    unreachable: detail.unreachableTts,
    name: null,
  }[slot];
  // Only the three greetings can be written as text or picked from an
  // announcement; a recorded name is audio only.
  const spoken = slot !== "name";
  const [mode, setMode] = useState<GreetingMode>("upload");
  const [announcements, setAnnouncements] = useState<Announcement[] | null>(
    null,
  );
  const [annId, setAnnId] = useState("");
  const [text, setText] = useState(record?.text ?? "");
  const [modelId, setModelId] = useState(record?.model ?? "");
  const [voiceId, setVoiceId] = useState(record?.voice ?? "");
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { models, error: modelsError } = useTtsModels(mode === "text");

  useEffect(() => {
    if (mode !== "announcement" || announcements) return;
    const controller = new AbortController();
    listAnnouncements(controller.signal)
      .then(setAnnouncements)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setProblem(errorMessage(err));
      });
    return () => controller.abort();
  }, [mode, announcements]);

  async function apply(
    body:
      | { source: "announcement"; announcementId: string }
      | { source: "text"; text: string; voiceId?: string; modelId?: string },
    done: (regenerated: boolean) => string,
  ) {
    if (slot === "name") return;
    setProblem(null);
    setBusy(true);
    try {
      const res = await putVoicemailGreeting(box.extensionId, {
        slot,
        ...body,
      });
      onApplied({ ...detail, ...res.box }, done(res.regenerated));
    } catch (err: unknown) {
      setProblem(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const trimmed = text.trim();
  const tooLong = [...trimmed].length > MAX_TTS_CHARS;
  const edited =
    record !== null &&
    record !== undefined &&
    (record.text !== trimmed ||
      record.model !== modelId ||
      record.voice !== voiceId);
  const current = object
    ? record
      ? "Generated from text."
      : slot === "name"
        ? "A name is recorded."
        : "A greeting is set."
    : {
        greeting: "Played before the beep when nobody answers.",
        busy: "Played before the beep when the extension is busy.",
        unreachable: "Played when the extension is unreachable.",
        name: "Spoken in the default greeting; the display name is used without one.",
      }[slot];
  const [preview, setPreview] = useState<string | null>(null);
  const [previewing, setPreviewing] = useState(false);

  useEffect(
    () => () => {
      if (preview) URL.revokeObjectURL(preview);
    },
    [preview],
  );

  async function onPreview() {
    if (slot === "name") return;
    setProblem(null);
    setPreviewing(true);
    try {
      const blob = await previewDefaultGreeting(box.extensionId, slot);
      setPreview(URL.createObjectURL(blob));
    } catch (err: unknown) {
      setProblem(errorMessage(err));
    } finally {
      setPreviewing(false);
    }
  }

  async function onRevert() {
    setProblem(null);
    setBusy(true);
    try {
      const res = await revertGreeting(box.extensionId, slot);
      setPreview(null);
      onApplied(
        { ...detail, ...res },
        `${label} of box ${box.number} is back to the default.`,
      );
    } catch (err: unknown) {
      setProblem(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <fieldset className="media-modal-form" style={{ border: 0, padding: 0 }}>
      <legend className="az-field__label">{label}</legend>
      <div className="vm-greeting-status">
        <Badge tone={isCustomGreeting(detail, slot) ? "good" : "info"}>
          {greetingStatus(detail, slot)}
        </Badge>
        {spoken && (
          <Button
            size="sm"
            variant="secondary"
            icon="volume-2"
            disabled={disabled || busy || previewing}
            onClick={() => void onPreview()}
          >
            {`Preview default ${label.toLowerCase()}`}
          </Button>
        )}
        {isCustomGreeting(detail, slot) && (
          <Button
            size="sm"
            variant="secondary"
            icon="rotate-ccw"
            disabled={disabled || busy}
            onClick={() => void onRevert()}
          >
            {`Revert ${label.toLowerCase()} to default`}
          </Button>
        )}
      </div>
      {preview && (
        <audio
          controls
          autoPlay
          src={preview}
          aria-label={`Preview of the default ${label.toLowerCase()}`}
        />
      )}
      {spoken && (
        <Select
          id={`${id}-source`}
          label={`${label} source`}
          value={mode}
          disabled={disabled || busy}
          options={[
            { value: "upload", label: "Upload a WAV" },
            { value: "announcement", label: "Use an existing announcement" },
            { value: "text", label: "Generate from text" },
          ]}
          onChange={(e) => {
            setMode(e.target.value as GreetingMode);
            setProblem(null);
          }}
        />
      )}
      {mode === "upload" && (
        <FileField
          id={id}
          label={`${label} audio (WAV)`}
          error={error}
          hint={
            object
              ? `${current} Uploading replaces it; stereo is mixed to mono and the sample rate is kept (up to 48 kHz).`
              : current
          }
          onFile={onFile}
        />
      )}
      {mode === "announcement" && (
        <>
          <Select
            id={`${id}-announcement`}
            label="Announcement"
            value={annId}
            disabled={disabled || busy || !announcements}
            options={[
              { value: "", label: announcements ? "Choose…" : "Loading…" },
              ...(announcements ?? []).map((a) => ({
                value: String(a.id),
                label: a.name,
              })),
            ]}
            onChange={(e) => setAnnId(e.target.value)}
            hint={`${current} The audio is copied, so later changes to the announcement do not alter the greeting.`}
          />
          <Button
            size="sm"
            variant="secondary"
            disabled={disabled || busy || annId === ""}
            onClick={() =>
              void apply(
                { source: "announcement", announcementId: annId },
                () => `${label} of box ${box.number} set.`,
              )
            }
          >
            Use this announcement
          </Button>
        </>
      )}
      {mode === "text" && (
        <>
          <div className="az-field">
            <label className="az-field__label" htmlFor={`${id}-text`}>
              Text to speak
            </label>
            <textarea
              id={`${id}-text`}
              className="az-input media-textarea"
              rows={4}
              value={text}
              disabled={disabled || busy}
              onChange={(e) => setText(e.target.value)}
            />
            <span className="az-field__hint">
              {tooLong
                ? `At most ${String(MAX_TTS_CHARS)} characters.`
                : `${current} Generated once; the stored audio plays from then on.`}
            </span>
          </div>
          <ModelVoiceFields
            idPrefix={id}
            models={models}
            modelId={modelId}
            voiceId={voiceId}
            onModel={setModelId}
            onVoice={setVoiceId}
            disabled={disabled || busy}
          />
          {modelsError && (
            <Alert tone="warn" title="Could not load the voice list">
              {modelsError}
            </Alert>
          )}
          {edited && (
            <Badge tone="warn">Text edited since the last generation</Badge>
          )}
          <Button
            size="sm"
            icon="wand-sparkles"
            disabled={disabled || busy || trimmed === "" || tooLong}
            onClick={() =>
              void apply(
                {
                  source: "text",
                  text: trimmed,
                  voiceId: voiceId || undefined,
                  modelId: modelId || undefined,
                },
                (regenerated) =>
                  regenerated
                    ? `${label} of box ${box.number} generated.`
                    : `${label} of box ${box.number} is already generated from this text.`,
              )
            }
          >
            {record ? "Regenerate greeting" : "Generate greeting"}
          </Button>
        </>
      )}
      {problem && (
        <Alert tone="bad" title="Could not set the greeting">
          {problem}
        </Alert>
      )}
    </fieldset>
  );
}
