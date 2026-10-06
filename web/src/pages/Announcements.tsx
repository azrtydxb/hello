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
  Alert,
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
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (a) => (
        <div className="media-actions">
          <IconButton
            icon="replace"
            label={`Replace the audio of ${a.name}`}
            onClick={() => setReplacing(a)}
          />
          <IconButton
            icon="trash-2"
            label={`Delete announcement ${a.name}`}
            onClick={() => setDeleting(a)}
          />
        </div>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Media"
        title="Announcements"
        description="Named prompts that ring groups and routes can play. WAV, up to 10 MB."
      />
      <div className="ann-grid">
        <UploadCard
          onUploaded={(a) => {
            upsert(a);
            showToast(`Announcement ${a.name} uploaded.`);
          }}
        />
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
          <Button
            type="submit"
            size="sm"
            icon="replace"
            form={formId}
            disabled={busy}
          >
            Replace audio
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
