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
  listVoicemailBoxes,
  type VoicemailBoxDetail,
  type VoicemailBoxSummary,
} from "../api/media";
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
  PageHeader,
  Spinner,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { formatDuration, formatTime } from "../format";
import { mapFieldErrors } from "../forms";
import { NowPlaying, usePlayback } from "./media/MediaParts";

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
    onToast(`Message from ${msg.caller} deleted`);
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
          {m.caller || "—"}
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
      render: (m) =>
        m.emailStatus ? (
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
          <IconButton
            icon={m.heard ? "mail" : "check-check"}
            label={`Mark the message from ${m.caller} ${m.heard ? "unheard" : "heard"}`}
            onClick={() => void onMarkHeard(m, !m.heard)}
          />
          <IconButton
            icon="trash-2"
            label={`Delete the message from ${m.caller}`}
            onClick={() => setDeleting(m)}
          />
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
            {d ? d.email || "No email" : "—"}
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
          onSaved={(value) => {
            setDetail({ status: "ready", value });
            setSettingsOpen(false);
            onToast(`Box ${box.number} saved`);
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
  unreachable?: string;
}

/** Box settings: email address, PIN and the two greetings. */
function BoxSettings({
  box,
  detail,
  onClose,
  onSaved,
}: {
  box: VoicemailBoxSummary;
  detail: VoicemailBoxDetail;
  onClose: () => void;
  onSaved: (detail: VoicemailBoxDetail) => void;
}) {
  const [email, setEmail] = useState(detail.email);
  const [password, setPassword] = useState("");
  const [greeting, setGreeting] = useState<File | null>(null);
  const [unreachable, setUnreachable] = useState<File | null>(null);
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
      // The PUT answers with the box as GET sends it (the detail shape).
      const saved = (await updateVoicemailBox(box.extensionId, {
        email: address,
        ...(password !== "" ? { password } : {}),
        ...(greeting ? { greeting } : {}),
        ...(unreachable ? { unreachable } : {}),
      })) as unknown as VoicemailBoxDetail;
      onSaved({ ...detail, ...saved });
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
          <Button type="submit" size="sm" form={formId} disabled={busy}>
            Save box settings
          </Button>
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
          hint="Where messages are emailed; leave empty for none."
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
        <FileField
          id={`${formId}-greeting`}
          label="Greeting audio (WAV)"
          error={errors.greeting}
          hint={
            detail.greetingObject
              ? "A greeting is set; uploading replaces it."
              : "Played before the beep."
          }
          onFile={setGreeting}
        />
        <FileField
          id={`${formId}-unreachable`}
          label="Unreachable greeting audio (WAV)"
          error={errors.unreachable}
          hint={
            detail.unreachableObject
              ? "An unreachable greeting is set; uploading replaces it."
              : "Played when the extension is unreachable."
          }
          onFile={setUnreachable}
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
