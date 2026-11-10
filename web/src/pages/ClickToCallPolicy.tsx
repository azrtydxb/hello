import { useEffect, useState, type FormEvent } from "react";
import { ApiError, errorMessage } from "../api";
import {
  getClickToCall,
  putClickToCall,
  type ClickToCallSettings,
} from "../api/originate";
import {
  Alert,
  Button,
  Input,
  Select,
  Spinner,
  Switch,
  useToast,
} from "../design/azrty/components";
import "./platform/platform.css";

const DESTINATION_OPTIONS = [
  { value: "inherit", label: "Whatever the outbound routes allow" },
  {
    value: "restricted",
    label: "Internal numbers and the country prefixes below only",
  },
];

/** The prefixes a free-text field holds: split on commas and whitespace. */
export function parsePrefixes(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((p) => p.trim())
    .filter((p) => p !== "");
}

/**
 * Click-to-call policy (administrators): which numbers a click-to-call may
 * dial and whether it may ask the phone to answer by itself. Both default to
 * the cautious choice.
 */
export function ClickToCallPolicy() {
  const [settings, setSettings] = useState<ClickToCallSettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    getClickToCall(ctl.signal)
      .then(setSettings)
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setError(errorMessage(err));
      });
    return () => ctl.abort();
  }, []);
  if (error)
    return (
      <Alert tone="bad" title="Could not load the click-to-call policy">
        {error}
      </Alert>
    );
  if (!settings) return <Spinner label="Loading the click-to-call policy…" />;
  return <PolicyForm initial={settings} />;
}

function PolicyForm({ initial }: { initial: ClickToCallSettings }) {
  const [destinations, setDestinations] = useState(initial.destinations);
  const [prefixes, setPrefixes] = useState(initial.countryPrefixes.join(", "));
  const [autoAnswer, setAutoAnswer] = useState(initial.autoAnswerAllowed);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setFormError(null);
    try {
      const next = await putClickToCall({
        destinations,
        countryPrefixes: parsePrefixes(prefixes),
        autoAnswerAllowed: autoAnswer,
      });
      setDestinations(next.destinations);
      setPrefixes(next.countryPrefixes.join(", "));
      setAutoAnswer(next.autoAnswerAllowed);
      toast.show("Click-to-call policy saved.");
    } catch (err) {
      setFormError(err instanceof ApiError ? err.message : errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={(e) => void onSubmit(e)}
      noValidate
      className="az-card pf-stack"
      aria-label="Click-to-call policy"
    >
      <h2>Click-to-call</h2>
      <p className="az-field__hint">
        A click-to-call rings the person&apos;s own phone first and dials the
        number when they answer. Emergency numbers are never dialled this way.
      </p>
      {formError && (
        <Alert tone="bad" title="Not saved">
          {formError}
        </Alert>
      )}
      <Select
        label="Destinations"
        value={destinations}
        options={DESTINATION_OPTIONS}
        onChange={(e) =>
          setDestinations(e.target.value as ClickToCallSettings["destinations"])
        }
      />
      {destinations === "restricted" && (
        <Input
          label="Country prefixes"
          hint="Separated by commas, for example +971, 00971. A number that leaves through an outbound route must start with one of them."
          value={prefixes}
          onChange={(e) => setPrefixes(e.target.value)}
        />
      )}
      <Switch
        label="Allow a request to ask the phone to answer by itself"
        hint="Uses the phone's auto-answer headers; off by default."
        checked={autoAnswer}
        onChange={(e) => setAutoAnswer(e.target.checked)}
      />
      <div>
        <Button type="submit" disabled={busy}>
          Save
        </Button>
      </div>
    </form>
  );
}
