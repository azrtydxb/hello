import { useState } from "react";
import {
  errorMessage,
  fieldErrors,
  SIP_USERNAME_PATTERN,
  type Announcement,
} from "../api";
import { announcementAudioPath } from "../api/media";
import {
  createGeneratedAnnouncement,
  generateAnnouncement,
  MAX_TTS_CHARS,
  saveAnnouncementPrompt,
} from "../api/tts";
import { Alert, Badge, Button, Input, Modal } from "../design/azrty/components";
import { formatTime } from "../format";
import { mapFieldErrors } from "../forms";
import { ModelVoiceFields, useTtsModels } from "../pages/media/TtsParts";

export const NAME_HINT =
  "Letters, digits, . _ - · up to 64; destinations use this name.";

/**
 * The text prompt editor: write the text, pick a voice, and generate the audio
 * once. The audio is stored and played from storage afterwards; saving the
 * text alone never calls the provider, and generating needs a click.
 */
export function PromptEditor({
  announcement,
  onClose,
  onDone,
}: {
  announcement: Announcement | null;
  onClose: () => void;
  onDone: (a: Announcement, message: string) => void;
}) {
  const [name, setName] = useState("");
  const [text, setText] = useState(announcement?.sourceText ?? "");
  const [modelId, setModelId] = useState(announcement?.modelId ?? "");
  const [voiceId, setVoiceId] = useState(announcement?.voiceId ?? "");
  const [errors, setErrors] = useState<{ name?: string; text?: string }>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { models, error: modelsError } = useTtsModels();
  const formId = `ann-prompt-${String(announcement?.id ?? "new")}`;

  const generated = announcement?.generated === true;
  // Edited in this form, or saved earlier and not generated since.
  const dirty =
    announcement !== null &&
    generated &&
    (announcement.textEdited === true ||
      text.trim() !== (announcement.sourceText ?? "") ||
      modelId !== (announcement.modelId ?? "") ||
      voiceId !== (announcement.voiceId ?? ""));
  const prompt = {
    text: text.trim(),
    voiceId: voiceId || undefined,
    modelId: modelId || undefined,
  };

  function validate(): boolean {
    const found: { name?: string; text?: string } = {};
    if (!announcement && !SIP_USERNAME_PATTERN.test(name.trim())) {
      found.name = "Enter a name of 1-64 of A-Z a-z 0-9 . _ -.";
    }
    if (prompt.text === "") found.text = "Write the text to speak.";
    else if ([...prompt.text].length > MAX_TTS_CHARS) {
      found.text = `At most ${String(MAX_TTS_CHARS)} characters.`;
    }
    setErrors(found);
    return !found.name && !found.text;
  }

  async function run(action: () => Promise<void>) {
    setFormError(null);
    if (!validate()) return;
    setBusy(true);
    try {
      await action();
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), ["name", "text"]);
      setErrors(mapped.byKey);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  const onSave = () =>
    run(async () => {
      if (!announcement) return;
      onDone(
        await saveAnnouncementPrompt(announcement.id, prompt),
        `Text of ${announcement.name} saved; generate it to hear the change.`,
      );
    });

  const onGenerate = () =>
    run(async () => {
      if (!announcement) {
        const res = await createGeneratedAnnouncement(name.trim(), prompt);
        onDone(
          res.announcement,
          `Announcement ${res.announcement.name} generated.`,
        );
        return;
      }
      // An explicit click on an unchanged prompt means "again".
      const res = await generateAnnouncement(announcement.id, {
        ...prompt,
        force: generated && !dirty,
      });
      onDone(
        res.announcement,
        res.regenerated
          ? `Audio of ${announcement.name} generated.`
          : `${announcement.name} is already generated from this text.`,
      );
    });

  return (
    <Modal
      title={
        announcement ? `Text of ${announcement.name}` : "New prompt from text"
      }
      description="The audio is generated once through OpenRouter and stored; calls play the stored audio and never reach the provider."
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
          {announcement && (
            <Button
              variant="secondary"
              size="sm"
              disabled={busy}
              onClick={() => void onSave()}
            >
              Save text
            </Button>
          )}
          <Button
            size="sm"
            icon="wand-sparkles"
            disabled={busy}
            onClick={() => void onGenerate()}
          >
            {!announcement
              ? "Generate announcement"
              : generated
                ? "Regenerate"
                : "Generate"}
          </Button>
        </>
      }
    >
      <form
        id={formId}
        className="media-modal-form"
        onSubmit={(e) => e.preventDefault()}
        noValidate
      >
        {!announcement && (
          <Input
            id={`${formId}-name`}
            label="Prompt name"
            mono
            placeholder="support-closed"
            value={name}
            error={errors.name}
            hint={NAME_HINT}
            onChange={(e) => setName(e.target.value)}
          />
        )}
        <div className={errors.text ? "az-field az-field--error" : "az-field"}>
          <label className="az-field__label" htmlFor={`${formId}-text`}>
            Text to speak
          </label>
          <textarea
            id={`${formId}-text`}
            className="az-input media-textarea"
            rows={5}
            value={text}
            aria-invalid={errors.text ? true : undefined}
            onChange={(e) => setText(e.target.value)}
          />
          <span className="az-field__hint">
            {errors.text ??
              `${String([...text].length)} / ${String(MAX_TTS_CHARS)} characters`}
          </span>
        </div>
        <ModelVoiceFields
          idPrefix={formId}
          models={models}
          modelId={modelId}
          voiceId={voiceId}
          onModel={setModelId}
          onVoice={setVoiceId}
          disabled={busy}
        />
        {modelsError && (
          <Alert tone="warn" title="Could not load the voice list">
            {modelsError}
          </Alert>
        )}
        {announcement && (
          <div className="media-prompt-status">
            {dirty ? (
              <Badge tone="warn">Text edited since the last generation</Badge>
            ) : generated ? (
              <Badge tone="info">
                Generated
                {announcement.generatedAt
                  ? ` ${formatTime(announcement.generatedAt)}`
                  : ""}
              </Badge>
            ) : (
              <Badge tone="outline">Not generated from text</Badge>
            )}
            {!generated && (
              <span className="media-hint">
                Generating replaces the current audio.
              </span>
            )}
          </div>
        )}
        {announcement && (
          <audio
            className="media-audio"
            controls
            preload="none"
            aria-label={`Preview of ${announcement.name}`}
            src={`${announcementAudioPath(announcement.id)}?v=${encodeURIComponent(announcement.updatedAt ?? "")}`}
          />
        )}
        {formError && (
          <Alert tone="bad" title="Could not generate the prompt">
            {formError}
          </Alert>
        )}
      </form>
    </Modal>
  );
}
