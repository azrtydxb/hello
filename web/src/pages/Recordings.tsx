import { useEffect, useState } from "react";
import {
  deleteRecording,
  errorMessage,
  isPlayableAudio,
  listExtensions,
  listRecordings,
  recordingAudioPath,
  type Extension,
  type Id,
  type Recording,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";
import { formatDuration, formatTime } from "../format";

export const PAGE_SIZE = 50;

/** One page of recordings, as listRecordings returns it. */
type Page = { items: Recording[]; next: string };

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; page: Page };

/** Recordings: paged list, extension filter, playback, delete. */
export function Recordings() {
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [filter, setFilter] = useState("");
  const [applied, setApplied] = useState("");
  const [cursors, setCursors] = useState<string[]>([""]);
  const [state, setState] = useState<ListState>({ status: "loading" });
  const [actionError, setActionError] = useState<string | null>(null);
  const [checking, setChecking] = useState<Id | null>(null);
  const [playing, setPlaying] = useState<Id | null>(null);
  const [playError, setPlayError] = useState<string | null>(null);
  const before = cursors[cursors.length - 1] ?? "";

  // The extension picker doubles as the filter: an empty value lists all.
  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then((items) => {
        if (!controller.signal.aborted) setExtensions(items);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setActionError(errorMessage(err));
        }
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    listRecordings(
      {
        extension: applied || undefined,
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
  }, [applied, before]);

  async function onPlay(rec: Recording) {
    setPlayError(null);
    setChecking(rec.id);
    try {
      const res = await fetch(recordingAudioPath(rec.id), {
        credentials: "same-origin",
      });
      if (isPlayableAudio(res)) {
        setPlaying(rec.id);
      } else {
        setPlaying(null);
        setPlayError(
          "Could not play the recording: the audio is not available " +
            `(HTTP ${res.status}).`,
        );
      }
    } catch (err: unknown) {
      setPlaying(null);
      setPlayError(`Could not play the recording: ${errorMessage(err)}`);
    } finally {
      setChecking(null);
    }
  }

  async function onDelete(rec: Recording) {
    setActionError(null);
    try {
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
      if (playing === rec.id) setPlaying(null);
    } catch (err: unknown) {
      setActionError(`Could not delete the recording: ${errorMessage(err)}`);
    }
  }

  const page = state.status === "ready" ? state.page : undefined;
  const playingRec =
    page?.items.find((r) => String(r.id) === String(playing)) ?? null;

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Recordings</h1>
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      <div className="fields">
        <div className="field">
          <label htmlFor="rec-filter">Filter by extension</label>
          <select
            id="rec-filter"
            value={filter}
            onChange={(e) => {
              setFilter(e.target.value);
              setApplied(e.target.value);
              setCursors([""]);
            }}
          >
            <option value="">All extensions</option>
            {extensions.map((ext) => (
              <option key={String(ext.id)} value={ext.number}>
                {ext.number} — {ext.name}
              </option>
            ))}
          </select>
        </div>
      </div>
      {state.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading recordings…
        </p>
      )}
      {state.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load recordings.</strong>
          <p>{state.message}</p>
        </div>
      )}
      {playError && (
        <p role="alert" className="error">
          {playError}
        </p>
      )}
      {page && page.items.length === 0 && (
        <p className="muted">
          No recordings{applied ? " for this extension" : ""}.
        </p>
      )}
      {playingRec && (
        <div className="playbar">
          <p className="muted">
            Playing the recording of {playingRec.correlationId}, recorded{" "}
            {formatTime(playingRec.createdAt)}.
          </p>
          <audio
            controls
            src={recordingAudioPath(playingRec.id)}
            aria-label={`Playback of the recording ${playingRec.correlationId}`}
          />
        </div>
      )}
      {page && page.items.length > 0 && (
        <table aria-labelledby="page-title">
          <thead>
            <tr>
              <th scope="col">Call</th>
              <th scope="col">Recorded</th>
              <th scope="col">Started by</th>
              <th scope="col">Duration</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {page.items.map((rec) => (
              <tr key={String(rec.id)}>
                <th scope="row">{rec.correlationId}</th>
                <td>{formatTime(rec.createdAt)}</td>
                <td>{rec.initiatedBy}</td>
                <td>{formatDuration(rec.durationMs)}</td>
                <td className="row-actions">
                  <button
                    type="button"
                    disabled={checking !== null}
                    aria-label={`Play the recording ${rec.correlationId}`}
                    onClick={() =>
                      playing === rec.id ? setPlaying(null) : void onPlay(rec)
                    }
                  >
                    {playing === rec.id ? "Stop" : "Play"}
                  </button>
                  <ConfirmButton
                    label="Delete"
                    accessibleLabel={`Delete the recording ${rec.correlationId}`}
                    prompt={`Delete the recording ${rec.correlationId}?`}
                    confirmLabel="Delete recording"
                    onConfirm={() => onDelete(rec)}
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      <nav className="pager" aria-label="Recording pages">
        <button
          type="button"
          disabled={cursors.length === 1 || state.status === "loading"}
          onClick={() => setCursors(cursors.slice(0, -1))}
        >
          Newer
        </button>
        <button
          type="button"
          disabled={!page?.next || state.status === "loading"}
          onClick={() => {
            if (page?.next) setCursors([...cursors, page.next]);
          }}
        >
          Older
        </button>
      </nav>
    </section>
  );
}
