import { useRef, useState } from "react";
import { errorMessage } from "../../api";
import { Alert, Button } from "../../design/azrty/components";
import { canRecord, recordingToWav } from "./wav";

/**
 * Records a greeting from the microphone and hands it over as a PCM WAV
 * (8 kHz mono), ready to upload. Uploading a ready-made WAV file stays the
 * other way in; this is shown only where the browser can record.
 */
export function GreetingRecorder({
  onRecorded,
  disabled,
}: {
  onRecorded: (wav: Blob) => void;
  disabled?: boolean;
}) {
  const [recording, setRecording] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [preview, setPreview] = useState<string | null>(null);
  const recorder = useRef<MediaRecorder | null>(null);

  if (!canRecord()) {
    return (
      <p className="az-field__hint">
        This browser cannot record; upload a WAV file instead.
      </p>
    );
  }

  async function start() {
    setError(null);
    try {
      const stream = await navigator.mediaDevices.getUserMedia({ audio: true });
      const chunks: Blob[] = [];
      const rec = new MediaRecorder(stream);
      rec.ondataavailable = (e) => chunks.push(e.data);
      rec.onstop = () => {
        stream.getTracks().forEach((t) => t.stop());
        recordingToWav(new Blob(chunks, { type: rec.mimeType }))
          .then((wav) => {
            setPreview((old) => {
              if (old) URL.revokeObjectURL(old);
              return URL.createObjectURL(wav);
            });
            onRecorded(wav);
          })
          .catch((err: unknown) =>
            setError(`Could not process the recording: ${errorMessage(err)}`),
          );
      };
      recorder.current = rec;
      rec.start();
      setRecording(true);
    } catch (err) {
      setError(`Could not start recording: ${errorMessage(err)}`);
    }
  }

  function stop() {
    recorder.current?.stop();
    setRecording(false);
  }

  return (
    <div className="pf-stack">
      {error && <Alert tone="bad">{error}</Alert>}
      <div>
        {recording ? (
          <Button variant="danger" icon="square" onClick={stop}>
            Stop recording
          </Button>
        ) : (
          <Button
            variant="secondary"
            icon="mic"
            disabled={disabled}
            onClick={() => void start()}
          >
            Record a greeting
          </Button>
        )}
      </div>
      {preview && (
        <audio controls src={preview} aria-label="Your recorded greeting" />
      )}
    </div>
  );
}
