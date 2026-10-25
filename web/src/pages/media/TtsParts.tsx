// Pieces the text-to-speech forms share: the model and voice pickers.

import { useEffect, useState } from "react";
import { errorMessage } from "../../api";
import { DEFAULT_TTS_MODEL, listTtsModels, type TtsModel } from "../../api/tts";
import { Input, Select } from "../../design/azrty/components";

/**
 * Load the ElevenLabs models and voices OpenRouter offers. The lists only
 * feed the pickers: when they cannot be loaded the pickers become plain
 * fields, so a prompt can still be written.
 */
export function useTtsModels(enabled = true) {
  const [models, setModels] = useState<TtsModel[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    if (!enabled) return;
    const controller = new AbortController();
    listTtsModels(controller.signal)
      .then(setModels)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [enabled]);
  return { models, error };
}

/**
 * Model and voice pickers. An empty value means "the default from the
 * text-to-speech settings".
 */
export function ModelVoiceFields({
  idPrefix,
  models,
  modelId,
  voiceId,
  onModel,
  onVoice,
  disabled,
}: {
  idPrefix: string;
  models: TtsModel[] | null;
  modelId: string;
  voiceId: string;
  onModel: (id: string) => void;
  onVoice: (id: string) => void;
  disabled?: boolean;
}) {
  const current = models?.find((m) => m.id === (modelId || DEFAULT_TTS_MODEL));
  return (
    <>
      {models && models.length > 0 ? (
        <Select
          id={`${idPrefix}-model`}
          label="Model"
          value={modelId}
          disabled={disabled}
          options={[
            { value: "", label: "Default model" },
            ...models.map((m) => ({ value: m.id, label: m.name || m.id })),
          ]}
          onChange={(e) => {
            onModel(e.target.value);
            onVoice("");
          }}
        />
      ) : (
        <Input
          id={`${idPrefix}-model`}
          label="Model"
          mono
          value={modelId}
          disabled={disabled}
          placeholder="default"
          onChange={(e) => onModel(e.target.value)}
        />
      )}
      {current && current.voices.length > 0 ? (
        <Select
          id={`${idPrefix}-voice`}
          label="Voice"
          value={voiceId}
          disabled={disabled}
          options={[
            { value: "", label: "Default voice" },
            ...current.voices.map((v) => ({ value: v, label: v })),
          ]}
          onChange={(e) => onVoice(e.target.value)}
        />
      ) : (
        <Input
          id={`${idPrefix}-voice`}
          label="Voice"
          mono
          value={voiceId}
          disabled={disabled}
          placeholder="default"
          onChange={(e) => onVoice(e.target.value)}
        />
      )}
    </>
  );
}
