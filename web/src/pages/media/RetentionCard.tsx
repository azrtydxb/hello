import { useEffect, useState } from "react";
import { errorMessage } from "../../api";
import { getRecordingSettings, putRecordingSettings } from "../../api/media";
import { Alert, Button, Input } from "../../design/azrty/components";

/**
 * How long video recordings are kept (S-42), admin only. The audio keeps no
 * retention job; the video is large and goes after this many days.
 */
export function RetentionCard() {
  const [days, setDays] = useState("");
  const [saved, setSaved] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    getRecordingSettings(controller.signal)
      .then((s) => {
        setDays(String(s.videoRetentionDays));
        setSaved(s.videoRetentionDays);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setLoadError(errorMessage(err));
      });
    return () => controller.abort();
  }, []);

  const n = Number(days);
  const valid = Number.isInteger(n) && n >= 1 && n <= 3650;

  async function save() {
    setBusy(true);
    setError(null);
    try {
      const s = await putRecordingSettings(n);
      setSaved(s.videoRetentionDays);
    } catch (err: unknown) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="az-card rec-retention">
      <Input
        label="Keep video recordings for (days)"
        hint="Video is far larger than audio (up to about 1.35 GB an hour at the default ceiling); older video is removed daily. 1 to 3650."
        error={days !== "" && !valid ? "Whole days, 1 to 3650." : undefined}
        type="number"
        min={1}
        max={3650}
        value={days}
        onChange={(e) => setDays(e.target.value)}
      />
      <Button
        size="sm"
        disabled={!valid || busy || n === saved}
        onClick={() => void save()}
      >
        Save
      </Button>
      {loadError && (
        <p className="media-hint">
          The retention could not be read: {loadError}
        </p>
      )}
      {error && (
        <Alert tone="bad" title="Could not save the retention">
          {error}
        </Alert>
      )}
    </div>
  );
}
