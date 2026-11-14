import { useEffect, useMemo, useRef, useState } from "react";
import {
  getRecordingManifest,
  recordingVideoPath,
  type CallRecording,
  type RecordingVideoStream,
} from "../../api/media";
import { recordingAudioPath, errorMessage } from "../../api";
import { Alert, Badge, Button, Icon } from "../../design/azrty/components";
import { formatDuration } from "../../format";

/** A video is kept within this of the audio's clock, in seconds. */
export const MAX_DRIFT_SECONDS = 0.1;
/** How often the drift is checked while playing, in ms. */
export const SYNC_EVERY_MS = 2000;

/**
 * Where a stream's video is when the audio is at `audioSeconds`: the audio's
 * time minus the stream's offset, or null before the stream has begun.
 */
export function videoTimeFor(
  audioSeconds: number,
  offsetMs: number,
): number | null {
  const t = audioSeconds - offsetMs / 1000;
  return t < 0 ? null : t;
}

/** Whether a video that is at `videoSeconds` must be moved to `wanted`. */
export function needsResync(videoSeconds: number, wanted: number): boolean {
  return Math.abs(videoSeconds - wanted) > MAX_DRIFT_SECONDS;
}

/**
 * The parts of one leg in the order they play, and which one the audio's time
 * falls in: the last part that has begun, or none before the first. `gap` is
 * true when that part has ended and the next has not begun: the stretch the
 * recording lost to a take-over.
 */
export function partAt(
  parts: RecordingVideoStream[],
  audioMs: number,
): { part: RecordingVideoStream | null; gap: boolean } {
  let current: RecordingVideoStream | null = null;
  for (const p of parts) {
    if (p.offsetMs <= audioMs) current = p;
  }
  if (!current) return { part: null, gap: false };
  const ended = current.offsetMs + current.durationMs;
  const hasNext = parts.some((p) => p.offsetMs > current.offsetMs);
  return { part: current, gap: hasNext && audioMs > ended + 500 };
}

/** The streams of one leg, by the time they begin. */
function legParts(
  streams: RecordingVideoStream[],
  leg: "caller" | "callee",
): RecordingVideoStream[] {
  return streams
    .filter((s) => s.leg === leg && s.state !== "purged")
    .sort((a, b) => a.offsetMs - b.offsetMs || a.part - b.part);
}

type Load =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; streams: RecordingVideoStream[] };

/**
 * Plays a recording that has video: the audio (the WAV) is the master clock and
 * each video, muted, follows it. The caller's picture is large and the callee's
 * small in a corner (or the only one is large); a stream that begins later than
 * the audio shows nothing until its time, a take-over's lost stretch shows a
 * "recording gap" marker, and a stream the browser cannot play offers its file as
 * a download.
 */
export function RecordingPlayer({
  recording,
  title,
  onStop,
}: {
  recording: CallRecording;
  title: string;
  onStop: () => void;
}) {
  const [load, setLoad] = useState<Load>({ status: "loading" });
  const audioRef = useRef<HTMLAudioElement>(null);
  const [now, setNow] = useState(0);
  const [failed, setFailed] = useState<Set<string>>(new Set());

  useEffect(() => {
    const controller = new AbortController();
    getRecordingManifest(recording.id, controller.signal)
      .then((m) => setLoad({ status: "ready", streams: m.streams }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setLoad({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [recording.id]);

  const streams = useMemo(
    () => (load.status === "ready" ? load.streams : []),
    [load],
  );
  const caller = useMemo(() => legParts(streams, "caller"), [streams]);
  const callee = useMemo(() => legParts(streams, "callee"), [streams]);
  const large = caller.length > 0 ? caller : callee;
  const small = caller.length > 0 ? callee : [];

  return (
    <div className="az-card media-player media-player--video">
      <div className="media-player__row">
        <Button
          size="sm"
          icon="pause"
          aria-label={`Stop playing ${title}`}
          title="Stop"
          onClick={onStop}
        />
        <span className="media-mono">{title}</span>
        <Badge tone="outline">
          <Icon name="video" size={12} /> video
        </Badge>
        <span className="media-mono">
          {formatDuration(now * 1000)} / {formatDuration(recording.durationMs)}
        </span>
      </div>
      {load.status === "loading" && (
        <p className="media-hint">Loading the video…</p>
      )}
      {load.status === "error" && (
        <Alert tone="warn" title="The video could not be loaded">
          {load.message} The audio plays on.
        </Alert>
      )}
      {load.status === "ready" && large.length === 0 && (
        <Alert tone="warn" title="No video was stored">
          The call carried video but nothing of it was recorded (see the call's
          trace). The audio plays on.
        </Alert>
      )}
      {large.length > 0 && (
        <div className="media-video">
          <SlavedVideo
            recordingId={recording.id}
            parts={large}
            audioRef={audioRef}
            now={now}
            label={`Video of the ${large[0]?.leg ?? "caller"}`}
            failed={failed}
            onFail={(id) => setFailed((f) => new Set(f).add(id))}
            className="media-video__main"
          />
          {small.length > 0 && (
            <SlavedVideo
              recordingId={recording.id}
              parts={small}
              audioRef={audioRef}
              now={now}
              label={`Video of the ${small[0]?.leg ?? "callee"}`}
              failed={failed}
              onFail={(id) => setFailed((f) => new Set(f).add(id))}
              className="media-video__pip"
            />
          )}
        </div>
      )}
      <audio
        ref={audioRef}
        src={recordingAudioPath(recording.id)}
        controls
        autoPlay
        preload="auto"
        aria-label={`Playback of the recording ${recording.correlationId}`}
        onTimeUpdate={(e) => setNow(e.currentTarget.currentTime)}
        onEnded={onStop}
      />
    </div>
  );
}

/**
 * One <video>, muted, slaved to the audio element: its time is the audio's minus
 * the stream's offset, set on play, seek and every two seconds when it drifts
 * more than 100 ms. Parts of the leg follow one another in the same element.
 */
function SlavedVideo({
  recordingId,
  parts,
  audioRef,
  now,
  label,
  failed,
  onFail,
  className,
}: {
  recordingId: number | string;
  parts: RecordingVideoStream[];
  audioRef: React.RefObject<HTMLAudioElement | null>;
  now: number;
  label: string;
  failed: Set<string>;
  onFail: (streamId: string) => void;
  className: string;
}) {
  const videoRef = useRef<HTMLVideoElement>(null);
  const { part, gap } = partAt(parts, now * 1000);
  const active = part ?? parts[0];
  const src = active ? recordingVideoPath(recordingId, active.id) : undefined;
  const activeId = active ? String(active.id) : "";

  // Keep the video on the audio's clock.
  useEffect(() => {
    const audio = audioRef.current;
    const video = videoRef.current;
    if (!audio || !video || !part) return;
    const follow = () => {
      const want = videoTimeFor(audio.currentTime, part.offsetMs);
      if (want === null) return;
      if (needsResync(video.currentTime, want)) {
        try {
          video.currentTime = want;
        } catch {
          // Not seekable yet: the next tick tries again.
        }
      }
      if (audio.paused) {
        video.pause();
      } else if (video.paused) {
        void video.play()?.catch(() => undefined);
      }
    };
    follow();
    const events = ["play", "pause", "seeked", "ratechange"] as const;
    events.forEach((e) => audio.addEventListener(e, follow));
    const timer = window.setInterval(follow, SYNC_EVERY_MS);
    return () => {
      events.forEach((e) => audio.removeEventListener(e, follow));
      window.clearInterval(timer);
    };
  }, [audioRef, part, src]);

  if (!active) return null;
  if (failed.has(activeId)) {
    return (
      <div className={`${className} media-video__failed`}>
        <p>
          This browser cannot play the{" "}
          {active.codec === "h264" ? "H.264" : "VP8"} video.
        </p>
        <a
          className="az-button az-button--sm"
          href={recordingVideoPath(recordingId, active.id, true)}
          download
        >
          Download the video
        </a>
      </div>
    );
  }
  return (
    <div className={className}>
      {part ? (
        <video
          key={activeId}
          ref={videoRef}
          src={src}
          muted
          playsInline
          preload="auto"
          aria-label={label}
          onError={() => onFail(activeId)}
        />
      ) : (
        <p className="media-hint">
          The video begins at {formatDuration(active.offsetMs)}.
        </p>
      )}
      {gap && (
        <span className="media-video__gap" role="status">
          recording gap
        </span>
      )}
    </div>
  );
}
