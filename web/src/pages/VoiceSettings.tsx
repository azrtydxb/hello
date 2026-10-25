import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../api";
import {
  deleteTtsKey,
  getTtsSettings,
  saveTtsSettings,
  testTtsConnection,
  type TtsSettings,
} from "../api/tts";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Input,
  PageHeader,
  Spinner,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors } from "../forms";
import { useCan } from "../role";
import { ModelVoiceFields, useTtsModels } from "./media/TtsParts";
import "./media/media.css";

type Load =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; settings: TtsSettings };

/**
 * Settings, Voice (TTS): the text-to-speech provider. Prompt text is turned
 * into audio once, on request, through OpenRouter's ElevenLabs models; the
 * audio is stored and replayed from storage. The API key is write-only: it is
 * sealed on the server and never shown again.
 */
export function VoiceSettings() {
  const isAdmin = useCan("admin");
  const [load, setLoad] = useState<Load>({ status: "loading" });
  const toast = useToast();
  const { show } = toast;

  useEffect(() => {
    if (!isAdmin) return;
    const controller = new AbortController();
    getTtsSettings(controller.signal)
      .then((settings) => setLoad({ status: "ready", settings }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setLoad({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [isAdmin]);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Settings"
        title="Voice (TTS)"
        description="Text-to-speech provider: OpenRouter. Prompts are generated once from text and played from storage; a call never reaches the provider."
      />
      {!isAdmin && (
        <Alert tone="warn" title="Administrators only">
          The text-to-speech settings hold the provider key and are limited to
          administrators.
        </Alert>
      )}
      {isAdmin && load.status === "loading" && (
        <Spinner label="Loading the voice settings…" />
      )}
      {isAdmin && load.status === "error" && (
        <Alert tone="bad" title="Could not load the voice settings">
          {load.message}
        </Alert>
      )}
      {isAdmin && load.status === "ready" && (
        <SettingsForm
          settings={load.settings}
          onChange={(settings) => setLoad({ status: "ready", settings })}
          notify={show}
        />
      )}
      {toast.node}
    </section>
  );
}

function SettingsForm({
  settings,
  onChange,
  notify,
}: {
  settings: TtsSettings;
  onChange: (s: TtsSettings) => void;
  notify: (message: string) => void;
}) {
  const [apiKey, setApiKey] = useState("");
  const [model, setModel] = useState(settings.model);
  const [voice, setVoice] = useState(settings.voice);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [test, setTest] = useState<{
    tone: "good" | "bad";
    message: string;
  } | null>(null);
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const { models, error: modelsError } = useTtsModels(settings.configured);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setTest(null);
    const key = apiKey.trim();
    setBusy(true);
    try {
      const saved = await saveTtsSettings({
        ...(key !== "" ? { apiKey: key } : {}),
        model: model.trim() || undefined,
        voice: voice.trim(),
      });
      onChange(saved);
      setApiKey("");
      setErrors({});
      notify("Voice settings saved.");
    } catch (err: unknown) {
      setErrors(
        mapFieldErrors(fieldErrors(err), ["apiKey", "model", "voice"]).byKey,
      );
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function onTest() {
    setTest(null);
    setBusy(true);
    try {
      await testTtsConnection();
      setTest({ tone: "good", message: "OpenRouter accepted the stored key." });
    } catch (err: unknown) {
      setTest({ tone: "bad", message: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  }

  async function onRemove() {
    try {
      onChange(await deleteTtsKey());
      setRemoving(false);
      setTest(null);
      notify("The OpenRouter key was removed.");
    } catch (err: unknown) {
      setRemoving(false);
      setFormError(errorMessage(err));
    }
  }

  return (
    <form
      className="az-card ann-upload media-settings"
      aria-labelledby="voice-settings-title"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="voice-settings-title" className="ann-upload__title">
        OpenRouter{" "}
        <Badge tone={settings.configured ? "good" : "neutral"}>
          {settings.configured ? "Key stored" : "No key"}
        </Badge>
      </h2>
      <Input
        id="tts-key"
        label="OpenRouter API key"
        type="password"
        autoComplete="new-password"
        mono
        value={apiKey}
        error={errors.apiKey}
        hint={
          settings.configured
            ? `A key ending in ${settings.keyHint || "…"} is stored. It is never shown again; paste a new one to replace it, or leave this empty to keep it.`
            : "Paste the key from openrouter.ai. It is stored encrypted and cannot be read back."
        }
        onChange={(e) => setApiKey(e.target.value)}
      />
      <ModelVoiceFields
        idPrefix="tts-default"
        models={models}
        modelId={model}
        voiceId={voice}
        onModel={setModel}
        onVoice={setVoice}
        disabled={busy}
      />
      {errors.model && <Alert tone="bad">{errors.model}</Alert>}
      {errors.voice && <Alert tone="bad">{errors.voice}</Alert>}
      <p className="media-hint">
        The default model and voice are used by every prompt that does not pick
        its own.{" "}
        {settings.configured
          ? "Models and voices come from OpenRouter's ElevenLabs list."
          : "Save a key to load the list of voices."}
      </p>
      {settings.configured && modelsError && (
        <Alert tone="warn" title="Could not load the voice list">
          {modelsError} The model and voice can still be typed in.
        </Alert>
      )}
      {formError && (
        <Alert tone="bad" title="Could not save the voice settings">
          {formError}
        </Alert>
      )}
      {test && (
        <Alert tone={test.tone} title="Connection test">
          {test.message}
        </Alert>
      )}
      <div className="media-actions">
        <Button type="submit" icon="check" disabled={busy}>
          Save voice settings
        </Button>
        <Button
          variant="secondary"
          icon="plug"
          disabled={busy || !settings.configured}
          onClick={() => void onTest()}
        >
          Test connection
        </Button>
        <Button
          variant="secondary"
          icon="trash-2"
          disabled={busy || !settings.configured}
          onClick={() => setRemoving(true)}
        >
          Remove key
        </Button>
      </div>
      {removing && (
        <ConfirmDialog
          title="Remove the OpenRouter key?"
          description="Prompts already generated keep playing. New ones cannot be generated until a key is set again."
          confirmLabel="Remove key"
          onConfirm={() => onRemove()}
          onClose={() => setRemoving(false)}
        />
      )}
    </form>
  );
}
