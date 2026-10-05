// Pieces the three Media pages share, built from design components: the page
// header, the now-playing card, the playback gate, the delete confirmation,
// the WAV drop field and the toast.

import {
  useCallback,
  useEffect,
  useId,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { errorMessage, isPlayableAudio, type Id } from "../../api";
import {
  Alert,
  Button,
  Icon,
  Meter,
  Modal,
} from "../../design/azrty/components";
import { formatDuration } from "../../format";
import "./media.css";

/** The design's page header: "Media" eyebrow, title, one-line description. */
export function MediaHeader({
  title,
  description,
  children,
}: {
  title: string;
  description: ReactNode;
  children?: ReactNode;
}) {
  return (
    <header className="media-head">
      <div>
        <span className="az-eyebrow media-head__eyebrow">Media</span>
        <h1 id="page-title" className="media-head__title">
          {title}
        </h1>
        <p className="media-head__desc">{description}</p>
      </div>
      {children}
    </header>
  );
}

/**
 * The playback gate: GET the audio route first and only play when it
 * answers with something an <audio> element can play (the 302 to the
 * presigned URL), so a missing object is an error, not a silent player.
 */
export function usePlayback(failure: (status: string) => string) {
  const [playing, setPlaying] = useState<Id | null>(null);
  const [checking, setChecking] = useState<Id | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function play(key: Id, path: string, name: string) {
    setError(null);
    setChecking(key);
    try {
      const res = await fetch(path, { credentials: "same-origin" });
      if (isPlayableAudio(res)) {
        setPlaying(key);
      } else {
        setPlaying(null);
        setError(
          `${failure(name)}: the audio is not available (HTTP ${res.status}).`,
        );
      }
    } catch (err: unknown) {
      setPlaying(null);
      setError(`${failure(name)}: ${errorMessage(err)}`);
    } finally {
      setChecking(null);
    }
  }

  const stop = useCallback(() => setPlaying(null), []);
  return { playing, checking, error, play, stop };
}

/**
 * The now-playing card: a stop button and a position meter over a hidden
 * <audio> element, which starts at once. `durationMs` is the server's
 * length; the element's own duration wins once it is known.
 */
export function NowPlaying({
  src,
  title,
  label,
  durationMs,
  onStop,
}: {
  src: string;
  /** What is playing (a caller, a name), shown in mono. */
  title: string;
  /** The audio element's accessible name. */
  label: string;
  durationMs?: number;
  onStop: () => void;
}) {
  const audioRef = useRef<HTMLAudioElement>(null);
  const [position, setPosition] = useState(0);
  const [length, setLength] = useState(
    durationMs !== undefined ? durationMs / 1000 : 0,
  );

  useEffect(() => {
    const audio = audioRef.current;
    if (!audio) return;
    try {
      void audio.play()?.catch(() => {
        // Autoplay refused or the source failed: the card stays, silent.
      });
    } catch {
      // Media playback is not available here (e.g. a test DOM).
    }
  }, [src]);

  const pct = length > 0 ? Math.min(100, (position / length) * 100) : 0;
  return (
    <div className="az-card media-player">
      <Button
        size="sm"
        icon="pause"
        aria-label={`Stop playing ${title}`}
        title="Stop"
        onClick={onStop}
      />
      <Meter
        value={Math.round(pct)}
        tone="sky"
        label={title}
        valueLabel={`${formatDuration(position * 1000)} / ${
          length > 0 ? formatDuration(length * 1000) : "—"
        }`}
      />
      <audio
        ref={audioRef}
        src={src}
        preload="auto"
        aria-label={label}
        onTimeUpdate={(e) => setPosition(e.currentTarget.currentTime)}
        onLoadedMetadata={(e) => {
          const d = e.currentTarget.duration;
          if (Number.isFinite(d) && d > 0) setLength(d);
        }}
        onEnded={onStop}
      />
    </div>
  );
}

/** A delete confirmation: the danger action, Cancel focused first. */
export function ConfirmDelete({
  title,
  description,
  confirmLabel,
  onConfirm,
  onClose,
}: {
  title: string;
  description: ReactNode;
  confirmLabel: string;
  /** Rejects with the error to show; resolves when done. */
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function confirm() {
    setBusy(true);
    setError(null);
    try {
      await onConfirm();
    } catch (err: unknown) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title={title}
      description={description}
      onClose={busy ? undefined : onClose}
      actions={
        <>
          <Button
            autoFocus
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={onClose}
          >
            Cancel
          </Button>
          <Button
            variant="danger"
            size="sm"
            icon="trash-2"
            disabled={busy}
            onClick={() => void confirm()}
          >
            {confirmLabel}
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not delete">
          {error}
        </Alert>
      )}
    </Modal>
  );
}

/**
 * The design's dashed WAV drop field: a file input stretched over the box,
 * so a click browses and a dropped file lands in the input.
 */
export function WavDrop({
  file,
  onFile,
  error,
  disabled,
}: {
  file: File | null;
  onFile: (file: File | null) => void;
  error?: string;
  disabled?: boolean;
}) {
  const id = useId();
  const hintId = `${id}-hint`;
  const errorId = `${id}-error`;
  const [over, setOver] = useState(false);
  return (
    <div
      className={[
        "media-drop",
        over && "media-drop--over",
        error && "media-drop--error",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      <Icon name="upload" size={24} />
      <label htmlFor={id}>
        <span className="media-drop__lead">Drop a WAV file</span> or browse
      </label>
      <span id={hintId} className="media-drop__hint">
        8 kHz mono PCM plays without conversion
      </span>
      {file && <span className="media-drop__file">{file.name}</span>}
      <input
        id={id}
        type="file"
        accept=".wav,audio/wav,audio/x-wav,audio/wave"
        disabled={disabled}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? `${errorId} ${hintId}` : hintId}
        onDragEnter={() => setOver(true)}
        onDragLeave={() => setOver(false)}
        onDrop={() => setOver(false)}
        onChange={(e) => onFile(e.target.files?.[0] ?? null)}
      />
      {error && (
        <p id={errorId} className="media-field-error">
          {error}
        </p>
      )}
    </div>
  );
}

/** Client checks for an announcement WAV before anything is sent. */
export function checkWav(file: File | null, maxBytes: number): string | null {
  if (!file) return "Choose a WAV file.";
  if (file.size === 0) return "The file is empty.";
  if (file.size > maxBytes) return "The file is larger than 10 MB.";
  return null;
}

/** The design's toast: a short confirmation, bottom right, for 3 s. */
export function useToast(): [ReactNode, (message: string) => void] {
  const [message, setMessage] = useState<string | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);
  const show = useCallback((text: string) => {
    clearTimeout(timer.current);
    setMessage(text);
    timer.current = setTimeout(() => setMessage(null), 3000);
  }, []);
  const node = (
    <div role="status" aria-live="polite">
      {message && (
        <div className="az-card media-toast">
          <Icon name="circle-check" size={16} />
          {message}
        </div>
      )}
    </div>
  );
  return [node, show];
}
