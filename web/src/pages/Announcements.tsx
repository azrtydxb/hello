import { useEffect, useState, type FormEvent } from "react";
import {
  deleteAnnouncement,
  errorMessage,
  fieldErrors,
  listAnnouncements,
  MAX_ANNOUNCEMENT_BYTES,
  SIP_USERNAME_PATTERN,
  uploadAnnouncement,
  type Announcement,
} from "../api";
import {
  announcementAudioPath,
  replaceAnnouncement,
  wasReplaced,
} from "../api/media";
import {
  createGeneratedAnnouncement,
  generateAnnouncement,
  MAX_TTS_CHARS,
  saveAnnouncementPrompt,
} from "../api/tts";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Input,
  Modal,
  PageHeader,
  Spinner,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { mapFieldErrors } from "../forms";
import { checkWav, NowPlaying, usePlayback, WavDrop } from "./media/MediaParts";
import { ModelVoiceFields, useTtsModels } from "./media/TtsParts";
import { Can } from "../role";

const NAME_HINT =
  "Letters, digits, . _ - · up to 64; destinations use this name.";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Announcement[] };

const byName = (a: Announcement, b: Announcement) =>
  a.name.localeCompare(b.name);

/**
 * Announcements: named WAV prompts that ring groups and routes play. Upload,
 * play, replace the audio of one, delete.
 */
export function Announcements() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [replacing, setReplacing] = useState<Announcement | null>(null);
  const [deleting, setDeleting] = useState<Announcement | null>(null);
  // The text prompt editor: "new" for a prompt from text, or the announcement.
  const [prompting, setPrompting] = useState<Announcement | "new" | null>(null);
  const toast = useToast();
  const showToast = toast.show;
  const playback = usePlayback((name) => `Could not play ${name}`);

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

  /** Put one announcement into the list (new, or replacing its old row). */
  function upsert(a: Announcement) {
    setList((prev) =>
      prev.status === "ready"
        ? {
            status: "ready",
            items: [
              ...prev.items.filter((i) => String(i.id) !== String(a.id)),
              a,
            ].sort(byName),
          }
        : prev,
    );
  }

  async function onDelete(a: Announcement) {
    await deleteAnnouncement(a.id);
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: prev.items.filter((i) => i.id !== a.id) }
        : prev,
    );
    if (String(playback.playing) === String(a.id)) playback.stop();
    setDeleting(null);
    showToast(`Announcement ${a.name} deleted.`);
  }

  const items = list.status === "ready" ? list.items : null;
  const playingAnn =
    items?.find((a) => String(a.id) === String(playback.playing)) ?? null;

  const columns: TableColumn<Announcement>[] = [
    {
      key: "play",
      label: <span className="visually-hidden">Play</span>,
      width: 44,
      primary: false,
      render: (a) => {
        const isPlaying = String(playback.playing) === String(a.id);
        return (
          <IconButton
            icon={isPlaying ? "pause" : "play"}
            label={isPlaying ? `Stop ${a.name}` : `Play ${a.name}`}
            disabled={playback.checking !== null}
            onClick={() =>
              isPlaying
                ? playback.stop()
                : void playback.play(a.id, announcementAudioPath(a.id), a.name)
            }
          />
        );
      },
    },
    {
      key: "name",
      label: "Name",
      mono: true,
      render: (a) => <span className="az-table__primary">{a.name}</span>,
    },
    {
      key: "uploaded",
      label: "Uploaded",
      render: (a) => formatTime(a.createdAt),
    },
    {
      key: "updated",
      label: "Updated",
      render: (a) => (wasReplaced(a) ? formatTime(a.updatedAt) : "—"),
    },
    {
      key: "source",
      label: "Source",
      render: (a) =>
        a.textEdited ? (
          <Badge tone="warn">Text edited</Badge>
        ) : a.generated ? (
          <Badge tone="info">Generated</Badge>
        ) : (
          <Badge tone="outline">Uploaded</Badge>
        ),
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (a) => (
        <div className="media-actions">
          <Can>
            <IconButton
              icon="wand-sparkles"
              label={`Edit the text of ${a.name}`}
              onClick={() => setPrompting(a)}
            />
          </Can>
          <Can>
            <IconButton
              icon="replace"
              label={`Replace the audio of ${a.name}`}
              onClick={() => setReplacing(a)}
            />
          </Can>
          <Can>
            <IconButton
              icon="trash-2"
              label={`Delete announcement ${a.name}`}
              onClick={() => setDeleting(a)}
            />
          </Can>
        </div>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Media"
        title="Announcements"
        description="Named prompts that ring groups and routes can play. Upload a WAV (up to 10 MB), or write the text and generate it once; either plays from storage."
        actions={
          <Can>
            <Button
              variant="secondary"
              icon="wand-sparkles"
              onClick={() => setPrompting("new")}
            >
              New prompt from text
            </Button>
          </Can>
        }
      />
      <div className="ann-grid">
        <Can>
          <UploadCard
            onUploaded={(a) => {
              upsert(a);
              showToast(`Announcement ${a.name} uploaded.`);
            }}
          />
        </Can>
        <div className="ann-list media-stack">
          {playback.error && <Alert tone="bad">{playback.error}</Alert>}
          {playingAnn && (
            <NowPlaying
              key={String(playingAnn.id)}
              src={announcementAudioPath(playingAnn.id)}
              title={playingAnn.name}
              label={`Playback of ${playingAnn.name}`}
              onStop={playback.stop}
            />
          )}
          {list.status === "loading" && (
            <Spinner label="Loading announcements…" />
          )}
          {list.status === "error" && (
            <Alert tone="bad" title="Could not load announcements">
              {list.message}
            </Alert>
          )}
          {items && items.length === 0 && (
            <EmptyState
              icon="megaphone"
              title="No announcements yet"
              description="Upload a WAV to name it in ring groups and routes."
            />
          )}
          {items && items.length > 0 && (
            <Table
              caption="Announcements"
              columns={columns}
              rows={items}
              rowKey={(a) => String(a.id)}
            />
          )}
        </div>
      </div>
      {replacing && (
        <ReplaceAudio
          announcement={replacing}
          onClose={() => setReplacing(null)}
          onReplaced={(a) => {
            upsert(a);
            setReplacing(null);
            if (String(playback.playing) === String(a.id)) playback.stop();
            showToast(`Audio of ${a.name} replaced.`);
          }}
        />
      )}
      {prompting && (
        <PromptEditor
          announcement={prompting === "new" ? null : prompting}
          onClose={() => setPrompting(null)}
          onDone={(a, message) => {
            upsert(a);
            setPrompting(null);
            if (String(playback.playing) === String(a.id)) playback.stop();
            showToast(message);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title="Delete announcement?"
          description={`${deleting.name} and its audio are removed; destinations that name it skip the step at call time.`}
          confirmLabel="Delete announcement"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

/** The design's upload card: name, WAV drop field, upload button. */
function UploadCard({ onUploaded }: { onUploaded: (a: Announcement) => void }) {
  const [name, setName] = useState("");
  const [file, setFile] = useState<File | null>(null);
  const [errors, setErrors] = useState<{ name?: string; file?: string }>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // A new key clears the file input after an upload.
  const [dropKey, setDropKey] = useState(0);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    const found: { name?: string; file?: string } = {};
    const trimmed = name.trim();
    if (!SIP_USERNAME_PATTERN.test(trimmed)) {
      found.name = "Enter a name of 1-64 of A-Z a-z 0-9 . _ -.";
    }
    const fileError = checkWav(file, MAX_ANNOUNCEMENT_BYTES);
    if (fileError) found.file = fileError;
    setErrors(found);
    if (found.name || found.file || !file) return;
    setBusy(true);
    try {
      const created = await uploadAnnouncement(trimmed, file);
      onUploaded(created);
      setName("");
      setFile(null);
      setDropKey((k) => k + 1);
      setErrors({});
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), ["name", "file"]);
      setErrors(mapped.byKey);
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      className="az-card ann-upload"
      aria-labelledby="ann-upload-title"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="ann-upload-title" className="ann-upload__title">
        Upload
      </h2>
      <Input
        id="ann-name"
        label="Name"
        mono
        placeholder="support-closed"
        value={name}
        error={errors.name}
        hint={NAME_HINT}
        onChange={(e) => setName(e.target.value)}
      />
      <WavDrop
        key={dropKey}
        file={file}
        onFile={setFile}
        error={errors.file}
        disabled={busy}
      />
      {formError && (
        <Alert tone="bad" title="Could not upload">
          {formError}
        </Alert>
      )}
      <Button type="submit" icon="upload" block disabled={busy}>
        Upload announcement
      </Button>
    </form>
  );
}

/** Replace an announcement's audio: same name, new WAV. */
function ReplaceAudio({
  announcement,
  onClose,
  onReplaced,
}: {
  announcement: Announcement;
  onClose: () => void;
  onReplaced: (a: Announcement) => void;
}) {
  const [file, setFile] = useState<File | null>(null);
  const [error, setError] = useState<string | undefined>(undefined);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const formId = `ann-replace-${String(announcement.id)}`;

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    const fileError = checkWav(file, MAX_ANNOUNCEMENT_BYTES);
    setError(fileError ?? undefined);
    if (fileError || !file) return;
    setBusy(true);
    try {
      onReplaced(await replaceAnnouncement(announcement.id, file));
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), ["file"]);
      setError(mapped.byKey.file);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={`Replace ${announcement.name}`}
      description="Destinations keep the name; calls play the new audio from the next one on."
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
            <Button
              type="submit"
              size="sm"
              icon="replace"
              form={formId}
              disabled={busy}
            >
              Replace audio
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
        <WavDrop file={file} onFile={setFile} error={error} disabled={busy} />
        {formError && (
          <Alert tone="bad" title="Could not replace the audio">
            {formError}
          </Alert>
        )}
      </form>
    </Modal>
  );
}

/**
 * The text prompt editor: write the text, pick a voice, and generate the audio
 * once. The audio is stored and played from storage afterwards; saving the
 * text alone never calls the provider, and generating needs a click.
 */
function PromptEditor({
  announcement,
  onClose,
  onDone,
}: {
  announcement: Announcement | null;
  onClose: () => void;
  onDone: (a: Announcement, message: string) => void;
}) {
  const [name, setName] = useState("");
  const [text, setText] = useState(announcement?.sourceText ?? "");
  const [modelId, setModelId] = useState(announcement?.modelId ?? "");
  const [voiceId, setVoiceId] = useState(announcement?.voiceId ?? "");
  const [errors, setErrors] = useState<{ name?: string; text?: string }>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { models, error: modelsError } = useTtsModels();
  const formId = `ann-prompt-${String(announcement?.id ?? "new")}`;

  const generated = announcement?.generated === true;
  // Edited in this form, or saved earlier and not generated since.
  const dirty =
    announcement !== null &&
    generated &&
    (announcement.textEdited === true ||
      text.trim() !== (announcement.sourceText ?? "") ||
      modelId !== (announcement.modelId ?? "") ||
      voiceId !== (announcement.voiceId ?? ""));
  const prompt = {
    text: text.trim(),
    voiceId: voiceId || undefined,
    modelId: modelId || undefined,
  };

  function validate(): boolean {
    const found: { name?: string; text?: string } = {};
    if (!announcement && !SIP_USERNAME_PATTERN.test(name.trim())) {
      found.name = "Enter a name of 1-64 of A-Z a-z 0-9 . _ -.";
    }
    if (prompt.text === "") found.text = "Write the text to speak.";
    else if ([...prompt.text].length > MAX_TTS_CHARS) {
      found.text = `At most ${String(MAX_TTS_CHARS)} characters.`;
    }
    setErrors(found);
    return !found.name && !found.text;
  }

  async function run(action: () => Promise<void>) {
    setFormError(null);
    if (!validate()) return;
    setBusy(true);
    try {
      await action();
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), ["name", "text"]);
      setErrors(mapped.byKey);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  const onSave = () =>
    run(async () => {
      if (!announcement) return;
      onDone(
        await saveAnnouncementPrompt(announcement.id, prompt),
        `Text of ${announcement.name} saved; generate it to hear the change.`,
      );
    });

  const onGenerate = () =>
    run(async () => {
      if (!announcement) {
        const res = await createGeneratedAnnouncement(name.trim(), prompt);
        onDone(
          res.announcement,
          `Announcement ${res.announcement.name} generated.`,
        );
        return;
      }
      // An explicit click on an unchanged prompt means "again".
      const res = await generateAnnouncement(announcement.id, {
        ...prompt,
        force: generated && !dirty,
      });
      onDone(
        res.announcement,
        res.regenerated
          ? `Audio of ${announcement.name} generated.`
          : `${announcement.name} is already generated from this text.`,
      );
    });

  return (
    <Modal
      title={
        announcement ? `Text of ${announcement.name}` : "New prompt from text"
      }
      description="The audio is generated once through OpenRouter and stored; calls play the stored audio and never reach the provider."
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
          {announcement && (
            <Button
              variant="secondary"
              size="sm"
              disabled={busy}
              onClick={() => void onSave()}
            >
              Save text
            </Button>
          )}
          <Button
            size="sm"
            icon="wand-sparkles"
            disabled={busy}
            onClick={() => void onGenerate()}
          >
            {!announcement
              ? "Generate announcement"
              : generated
                ? "Regenerate"
                : "Generate"}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="media-modal-form"
        onSubmit={(e) => e.preventDefault()}
        noValidate
      >
        {!announcement && (
          <Input
            id={`${formId}-name`}
            label="Prompt name"
            mono
            placeholder="support-closed"
            value={name}
            error={errors.name}
            hint={NAME_HINT}
            onChange={(e) => setName(e.target.value)}
          />
        )}
        <div className={errors.text ? "az-field az-field--error" : "az-field"}>
          <label className="az-field__label" htmlFor={`${formId}-text`}>
            Text to speak
          </label>
          <textarea
            id={`${formId}-text`}
            className="az-input media-textarea"
            rows={5}
            value={text}
            aria-invalid={errors.text ? true : undefined}
            onChange={(e) => setText(e.target.value)}
          />
          <span className="az-field__hint">
            {errors.text ??
              `${String([...text].length)} / ${String(MAX_TTS_CHARS)} characters`}
          </span>
        </div>
        <ModelVoiceFields
          idPrefix={formId}
          models={models}
          modelId={modelId}
          voiceId={voiceId}
          onModel={setModelId}
          onVoice={setVoiceId}
          disabled={busy}
        />
        {modelsError && (
          <Alert tone="warn" title="Could not load the voice list">
            {modelsError}
          </Alert>
        )}
        {announcement && (
          <div className="media-prompt-status">
            {dirty ? (
              <Badge tone="warn">Text edited since the last generation</Badge>
            ) : generated ? (
              <Badge tone="info">
                Generated
                {announcement.generatedAt
                  ? ` ${formatTime(announcement.generatedAt)}`
                  : ""}
              </Badge>
            ) : (
              <Badge tone="outline">Not generated from text</Badge>
            )}
            {!generated && (
              <span className="media-hint">
                Generating replaces the current audio.
              </span>
            )}
          </div>
        )}
        {announcement && (
          <audio
            className="media-audio"
            controls
            preload="none"
            aria-label={`Preview of ${announcement.name}`}
            src={`${announcementAudioPath(announcement.id)}?v=${encodeURIComponent(announcement.updatedAt ?? "")}`}
          />
        )}
        {formError && (
          <Alert tone="bad" title="Could not generate the prompt">
            {formError}
          </Alert>
        )}
      </form>
    </Modal>
  );
}
