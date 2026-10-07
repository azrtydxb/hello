import { useEffect, useState } from "react";
import { Link } from "react-router";
import {
  deleteRecording,
  errorMessage,
  listExtensions,
  recordingAudioPath,
  type Extension,
  type RecordingOrigin,
} from "../api";
import {
  listCallRecordings,
  recordingDownloadPath,
  type CallRecording,
  type CallRecordingPage,
} from "../api/media";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Icon,
  IconButton,
  PageHeader,
  Select,
  Spinner,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { formatDuration, formatTime } from "../format";
import { NowPlaying, usePlayback } from "./media/MediaParts";
import { Can } from "../role";

export const PAGE_SIZE = 50;

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; page: CallRecordingPage };

/** How each origin reads in the "Started by" badge. */
const ORIGIN_LABEL: Record<RecordingOrigin, string> = {
  default: "default",
  dtmf: "dtmf (*1)",
  api: "api",
};

/** The parties of the recording's call, or "—" before its CDR exists. */
function callLabel(r: CallRecording): string {
  if (!r.source && !r.destination) return "—";
  return `${r.source || "—"} → ${r.destination || "—"}`;
}

/** Recordings: paged list, extension filter, playback, download, delete. */
export function Recordings() {
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [extensionsError, setExtensionsError] = useState<string | null>(null);
  const [filter, setFilter] = useState("");
  const [cursors, setCursors] = useState<string[]>([""]);
  const [state, setState] = useState<ListState>({ status: "loading" });
  const [deleting, setDeleting] = useState<CallRecording | null>(null);
  const toast = useToast();
  const showToast = toast.show;
  const playback = usePlayback(() => "Could not play the recording");
  const before = cursors[cursors.length - 1] ?? "";

  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then((items) => setExtensions(items))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setExtensionsError(errorMessage(err));
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    listCallRecordings(
      {
        extension: filter || undefined,
        before: before || undefined,
        limit: PAGE_SIZE,
      },
      controller.signal,
    )
      .then((page) => setState({ status: "ready", page }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [filter, before]);

  async function onDelete(rec: CallRecording) {
    await deleteRecording(rec.id);
    setState((prev) =>
      prev.status === "ready"
        ? {
            status: "ready",
            page: {
              items: prev.page.items.filter((r) => r.id !== rec.id),
              next: prev.page.next,
            },
          }
        : prev,
    );
    if (String(playback.playing) === String(rec.id)) playback.stop();
    setDeleting(null);
    showToast(`Recording ${rec.correlationId} deleted.`);
  }

  const page = state.status === "ready" ? state.page : undefined;
  const playingRec =
    page?.items.find((r) => String(r.id) === String(playback.playing)) ?? null;

  const columns: TableColumn<CallRecording>[] = [
    {
      key: "play",
      label: <span className="visually-hidden">Play</span>,
      width: 44,
      primary: false,
      render: (r) => {
        const isPlaying = String(playback.playing) === String(r.id);
        return (
          <IconButton
            icon={isPlaying ? "pause" : "play"}
            label={
              isPlaying
                ? `Stop the recording ${r.correlationId}`
                : `Play the recording ${r.correlationId}`
            }
            disabled={playback.checking !== null}
            onClick={() =>
              isPlaying
                ? playback.stop()
                : void playback.play(
                    r.id,
                    recordingAudioPath(r.id),
                    r.correlationId,
                  )
            }
          />
        );
      },
    },
    {
      key: "recorded",
      label: "Recorded",
      render: (r) => formatTime(r.createdAt),
    },
    {
      key: "call",
      label: "Call",
      render: (r) =>
        r.source || r.destination ? (
          <span>
            <span className="rec-call">{r.source || "—"}</span> →{" "}
            <span className="rec-call">{r.destination || "—"}</span>
          </span>
        ) : (
          "—"
        ),
    },
    {
      key: "correlation",
      label: "Correlation ID",
      mono: true,
      render: (r) =>
        r.cdrId !== undefined && r.cdrId !== null ? (
          <Link to={`/history/${encodeURIComponent(String(r.cdrId))}`}>
            {r.correlationId}
          </Link>
        ) : (
          r.correlationId
        ),
    },
    {
      key: "origin",
      label: "Started by",
      render: (r) => (
        <Badge tone="outline">
          {ORIGIN_LABEL[r.initiatedBy] ?? r.initiatedBy}
        </Badge>
      ),
    },
    {
      key: "length",
      label: "Length",
      align: "right",
      mono: true,
      render: (r) => formatDuration(r.durationMs),
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (r) => (
        <div className="media-actions">
          <a
            className="az-iconbtn"
            href={recordingDownloadPath(r.id)}
            download={`recording-${String(r.id)}.wav`}
            aria-label={`Download the recording ${r.correlationId}`}
            title="Download"
          >
            <Icon name="download" size={16} />
          </a>
          <Can>
            <IconButton
              icon="trash-2"
              label={`Delete the recording ${r.correlationId}`}
              onClick={() => setDeleting(r)}
            />
          </Can>
        </div>
      ),
    },
  ];

  const options = [
    { value: "", label: "All extensions" },
    ...extensions.map((e) => ({
      value: e.number,
      label: `${e.number} ${e.name}`,
    })),
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Media"
        title="Recordings"
        description={
          <>
            Anchored calls recorded by default, by{" "}
            <span className="media-mono">*1</span> mid-call, or through the API.
          </>
        }
        actions={
          <>
            <Select
              size="sm"
              className="rec-filter"
              aria-label="Filter by extension"
              options={options}
              value={filter}
              onChange={(e) => {
                setFilter(e.target.value);
                setCursors([""]);
              }}
            />
          </>
        }
      />
      <div className="media-stack">
        {extensionsError && (
          <Alert tone="warn" title="Could not load the extension filter">
            {extensionsError}
          </Alert>
        )}
        {playback.error && <Alert tone="bad">{playback.error}</Alert>}
        {playingRec && (
          <NowPlaying
            key={String(playingRec.id)}
            src={recordingAudioPath(playingRec.id)}
            title={callLabel(playingRec)}
            label={`Playback of the recording ${playingRec.correlationId}`}
            durationMs={playingRec.durationMs}
            onStop={playback.stop}
          />
        )}
        {state.status === "loading" && <Spinner label="Loading recordings…" />}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load recordings">
            {state.message}
          </Alert>
        )}
        {page && page.items.length === 0 && (
          <EmptyState
            icon="mic"
            title={
              filter ? "No recordings for this extension" : "No recordings yet"
            }
            description="Anchored calls are recorded by default; *1 toggles recording mid-call."
          />
        )}
        {page && page.items.length > 0 && (
          <Table
            caption="Recordings"
            columns={columns}
            rows={page.items}
            rowKey={(r) => String(r.id)}
          />
        )}
      </div>
      <nav className="media-pager" aria-label="Recording pages">
        {cursors.length > 1 && (
          <Button
            variant="secondary"
            size="sm"
            icon="chevron-left"
            disabled={state.status === "loading"}
            onClick={() => setCursors(cursors.slice(0, -1))}
          >
            Newer
          </Button>
        )}
        <Button
          variant="secondary"
          size="sm"
          iconRight="chevron-right"
          disabled={!page?.next || state.status === "loading"}
          onClick={() => {
            if (page?.next) setCursors([...cursors, page.next]);
          }}
        >
          Older
        </Button>
      </nav>
      {deleting && (
        <ConfirmDialog
          title="Delete recording?"
          description={`The recording of ${deleting.correlationId} and its audio are removed. This cannot be undone.`}
          confirmLabel="Delete recording"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}
