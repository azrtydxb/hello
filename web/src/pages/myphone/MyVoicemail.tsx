import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type Id } from "../../api";
import {
  deleteMyMessage,
  getMyVoicemail,
  listMyMessages,
  markMyMessageHeard,
  myMessageAudioPath,
  updateMyVoicemail,
  type SelfExtension,
  type SelfMessage,
  type SelfVoicemailBox,
} from "../../api/self";
import {
  Alert,
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Input,
  PageHeader,
  Spinner,
  Table,
  type TableColumn,
  useToast,
} from "../../design/azrty/components";
import { formatDuration, formatTime } from "../../format";
import { mapFieldErrors } from "../../forms";
import { NowPlaying, usePlayback } from "../media/MediaParts";
import { GreetingRecorder } from "./GreetingRecorder";
import { WithExtension } from "./useMyExtensions";

/** The user's voicemail: messages, PIN and greetings. */
export function MyVoicemail() {
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="My phone"
        title="Voicemail"
        description="Listen to, download and delete your messages, and set your PIN and greetings."
      />
      <WithExtension>{(ext) => <Box ext={ext} />}</WithExtension>
    </section>
  );
}

function Box({ ext }: { ext: SelfExtension }) {
  const [messages, setMessages] = useState<SelfMessage[] | null>(null);
  const [box, setBox] = useState<SelfVoicemailBox | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<SelfMessage | null>(null);
  const playback = usePlayback(
    (who) => `Could not play the message from ${who}`,
  );
  const toast = useToast();

  useEffect(() => {
    const ctl = new AbortController();
    Promise.all([
      listMyMessages(ext.id, ctl.signal),
      getMyVoicemail(ext.id, ctl.signal),
    ])
      .then(([m, b]) => {
        setMessages(m);
        setBox(b);
      })
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setError(errorMessage(err));
      });
    return () => ctl.abort();
  }, [ext.id]);

  async function heard(m: SelfMessage, value: boolean) {
    setError(null);
    try {
      await markMyMessageHeard(m.id, value);
      setMessages(
        (l) => l && l.map((x) => (x.id === m.id ? { ...x, heard: value } : x)),
      );
    } catch (err) {
      setError(`Could not mark the message: ${errorMessage(err)}`);
    }
  }

  async function remove(m: SelfMessage) {
    await deleteMyMessage(m.id);
    setMessages((l) => l && l.filter((x) => x.id !== m.id));
    if (String(playback.playing) === String(m.id)) playback.stop();
    setDeleting(null);
    toast.show("Message deleted.");
  }

  const items = messages ?? [];
  const playing = items.find((m) => String(m.id) === String(playback.playing));
  const columns: TableColumn<SelfMessage>[] = [
    {
      key: "play",
      label: <span className="visually-hidden">Play</span>,
      width: 44,
      primary: false,
      render: (m) => {
        const on = String(playback.playing) === String(m.id);
        return (
          <IconButton
            icon={on ? "pause" : "play"}
            label={
              on
                ? `Stop the message from ${m.caller}`
                : `Play the message from ${m.caller}`
            }
            disabled={playback.checking !== null}
            onClick={() =>
              on
                ? playback.stop()
                : void playback.play(m.id, myMessageAudioPath(m.id), m.caller)
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
          <a
            className="az-btn az-btn--ghost az-btn--sm"
            href={myMessageAudioPath(m.id, true)}
            download
            aria-label={`Download the message from ${m.caller}`}
          >
            Download
          </a>
          <IconButton
            icon={m.heard ? "mail" : "check-check"}
            label={`Mark the message from ${m.caller} ${m.heard ? "unheard" : "heard"}`}
            onClick={() => void heard(m, !m.heard)}
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
    <div className="pf-stack">
      {error && <Alert tone="bad">{error}</Alert>}
      {playback.error && <Alert tone="bad">{playback.error}</Alert>}
      {playing && (
        <NowPlaying
          key={String(playing.id)}
          src={myMessageAudioPath(playing.id)}
          title={playing.caller}
          label={`Playback of the message from ${playing.caller}`}
          durationMs={playing.durationMs}
          onStop={playback.stop}
        />
      )}
      {messages === null && !error && <Spinner label="Loading messages…" />}
      {messages !== null && items.length === 0 && (
        <EmptyState
          icon="inbox"
          title="No messages"
          description={`Messages left for ${ext.number} appear here.`}
        />
      )}
      {items.length > 0 && (
        <Table
          caption={`Messages for ${ext.number}`}
          columns={columns}
          rows={items}
          rowKey={(m) => String(m.id)}
        />
      )}
      {box && (
        <Settings
          extensionId={ext.id}
          box={box}
          onSaved={(b) => {
            setBox(b);
            toast.show("Voicemail settings saved.");
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete message?"
          description={`The message from ${deleting.caller} and its audio are removed. This cannot be undone.`}
          confirmLabel="Delete message"
          onConfirm={() => remove(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

/** PIN and greetings (upload a WAV file or record one). */
function Settings({
  extensionId,
  box,
  onSaved,
}: {
  extensionId: Id;
  box: SelfVoicemailBox;
  onSaved: (b: SelfVoicemailBox) => void;
}) {
  const [pin, setPin] = useState("");
  const [greeting, setGreeting] = useState<Blob | null>(null);
  const [unreachable, setUnreachable] = useState<Blob | null>(null);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    if (pin === "" && !greeting && !unreachable) {
      setFormError("Enter a new PIN or choose a greeting first.");
      return;
    }
    setBusy(true);
    setErrors({});
    setFormError(null);
    try {
      const saved = await updateMyVoicemail(extensionId, {
        pin: pin || undefined,
        greeting: greeting ?? undefined,
        unreachable: unreachable ?? undefined,
      });
      setPin("");
      setGreeting(null);
      setUnreachable(null);
      onSaved(saved);
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), ["pin"]);
      setErrors(byKey);
      if (unmatched.length > 0 || Object.keys(byKey).length === 0)
        setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={(e) => void onSubmit(e)}
      noValidate
      className="az-card pf-stack"
      aria-label="Voicemail settings"
    >
      <h2 className="vm-box__title">Settings</h2>
      {formError && <Alert tone="bad">{formError}</Alert>}
      <Input
        label="PIN"
        type="password"
        inputMode="numeric"
        autoComplete="off"
        hint={
          box.hasPin
            ? "A PIN is set. Enter 4 to 10 digits to change it."
            : "No PIN is set yet. Enter 4 to 10 digits."
        }
        value={pin}
        error={errors.pin}
        onChange={(e) => setPin(e.target.value)}
      />
      <fieldset className="pf-stack">
        <legend className="az-field__label">Greeting</legend>
        <p className="az-field__hint">
          {box.hasGreeting
            ? "A greeting is set."
            : "The standard greeting is used."}{" "}
          Played when you are busy or do not answer.
        </p>
        <input
          type="file"
          accept=".wav,audio/wav"
          aria-label="Upload a greeting (WAV)"
          onChange={(e) => setGreeting(e.target.files?.[0] ?? null)}
        />
        <GreetingRecorder onRecorded={setGreeting} disabled={busy} />
      </fieldset>
      <fieldset className="pf-stack">
        <legend className="az-field__label">Unreachable greeting</legend>
        <p className="az-field__hint">
          {box.hasUnreachableGreeting
            ? "A greeting is set."
            : "The standard greeting is used."}{" "}
          Played when your phone is not registered.
        </p>
        <input
          type="file"
          accept=".wav,audio/wav"
          aria-label="Upload an unreachable greeting (WAV)"
          onChange={(e) => setUnreachable(e.target.files?.[0] ?? null)}
        />
      </fieldset>
      <div>
        <Button type="submit" icon="save" disabled={busy}>
          Save settings
        </Button>
      </div>
    </form>
  );
}
