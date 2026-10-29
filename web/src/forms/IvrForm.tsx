import { useState, type FormEvent } from "react";
import {
  createIvrMenu,
  errorMessage,
  fieldErrors,
  hasNoTarget,
  putIvrMenu,
  type FieldError,
  type Id,
  type IvrDestination,
  type IvrMenu,
  type IvrMenuFields,
  type IvrOption,
} from "../api";
import {
  Alert,
  Button,
  Drawer,
  IconButton,
  Input,
  Select,
  Switch,
  useRestoreFocus,
} from "../design/azrty/components";
import {
  DestinationPicker,
  type DestinationChoices,
} from "../components/IvrDestination";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { FormAlert } from "../pages/callflow/ui";
import {
  announcementAudioPath,
  PROMPT_LANGUAGE_LABELS,
  promptLanguageLabel,
} from "../api/media";
import { NowPlaying, usePlayback } from "../pages/media/MediaParts";
import { Can } from "../role";

export const FORM_ID = "ivr-form";
export const NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;
export const NUMBER_PATTERN = /^([0-9]{2,16})?$/;
export const DIGITS = [
  "1",
  "2",
  "3",
  "4",
  "5",
  "6",
  "7",
  "8",
  "9",
  "0",
  "*",
  "#",
];

interface Draft {
  name: string;
  number: string;
  enabled: boolean;
  greetingId: string;
  invalidPromptId: string;
  timeoutPromptId: string;
  digitTimeout: string;
  interDigitTimeout: string;
  maxRetries: string;
  allowDirectDial: boolean;
  promptLanguage: string;
  timeout: IvrDestination;
  invalid: IvrDestination;
  options: IvrOption[];
}

const draftOf = (m: IvrMenu | null): Draft =>
  m
    ? {
        name: m.name,
        number: m.number,
        enabled: m.enabled,
        greetingId: String(m.greetingId),
        invalidPromptId:
          m.invalidPromptId === null ? "" : String(m.invalidPromptId),
        timeoutPromptId:
          m.timeoutPromptId === null ? "" : String(m.timeoutPromptId),
        digitTimeout: String(m.digitTimeout),
        interDigitTimeout: String(m.interDigitTimeout),
        maxRetries: String(m.maxRetries),
        allowDirectDial: m.allowDirectDial,
        promptLanguage: m.promptLanguage ?? "",
        timeout: { ...m.timeout },
        invalid: { ...m.invalid },
        options: m.options.map((o) => ({ ...o })),
      }
    : {
        name: "",
        number: "",
        enabled: true,
        greetingId: "",
        invalidPromptId: "",
        timeoutPromptId: "",
        digitTimeout: "5",
        interDigitTimeout: "3",
        maxRetries: "3",
        allowDirectDial: false,
        promptLanguage: "",
        timeout: { kind: "hangup", target: "" },
        invalid: { kind: "hangup", target: "" },
        options: [],
      };

const KEYS = [
  "name",
  "number",
  "enabled",
  "greetingId",
  "invalidPromptId",
  "timeoutPromptId",
  "digitTimeout",
  "interDigitTimeout",
  "maxRetries",
  "allowDirectDial",
  "promptLanguage",
  "timeout",
  "invalid",
  "options",
];

const whole = (s: string) => /^[0-9]{1,3}$/.test(s);

function validateDest(
  out: Record<string, string>,
  key: string,
  d: IvrDestination,
) {
  if (hasNoTarget(d.kind)) return;
  if (d.target.trim() === "") out[`${key}.target`] = "Choose a target.";
  else if (
    d.kind === "external" &&
    !/^\+?[0-9*#]{2,32}$/.test(d.target.trim())
  ) {
    out[`${key}.target`] = "Use 2 to 32 digits, optionally starting with +.";
  }
}

function validate(d: Draft): Record<string, string> {
  const out: Record<string, string> = {};
  if (!NAME_PATTERN.test(d.name)) {
    out.name = "Use 1 to 64 letters, digits, dots, dashes or underscores.";
  }
  if (!NUMBER_PATTERN.test(d.number)) {
    out.number = "Leave empty, or use 2 to 16 digits.";
  }
  if (d.greetingId === "") out.greetingId = "Choose the greeting.";
  if (!whole(d.digitTimeout) || +d.digitTimeout < 1 || +d.digitTimeout > 60) {
    out.digitTimeout = "Use 1 to 60 seconds.";
  }
  if (
    !whole(d.interDigitTimeout) ||
    +d.interDigitTimeout < 1 ||
    +d.interDigitTimeout > 30
  ) {
    out.interDigitTimeout = "Use 1 to 30 seconds.";
  }
  if (!whole(d.maxRetries) || +d.maxRetries > 10) {
    out.maxRetries = "Use 0 to 10.";
  }
  validateDest(out, "timeout", d.timeout);
  validateDest(out, "invalid", d.invalid);
  const seen = new Set<string>();
  d.options.forEach((o, i) => {
    if (seen.has(o.digit)) out[`options[${i}].digit`] = "Each key once.";
    seen.add(o.digit);
    validateDest(out, `options[${i}]`, o);
  });
  return out;
}

export function IvrDrawer({
  menu,
  choices,
  onClose,
  commit,
  onSaved,
}: {
  menu: IvrMenu | null;
  choices: DestinationChoices;
  onClose: () => void;
  /** The canvas stages the body in its draft instead of saving it here. */
  commit?: (body: object) => Promise<void>;
  onSaved: (m: IvrMenu, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(menu));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const playback = usePlayback((name) => `Could not play ${name}`);
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  const promptOptions = (blank: string) => [
    { value: "", label: blank },
    ...choices.announcements.map((a) => ({
      value: String(a.id),
      label: a.name,
    })),
  ];
  const free = DIGITS.filter((x) => !d.options.some((o) => o.digit === x));

  /** A prompt picker with a preview of the chosen announcement. */
  const prompt = (
    field: "greetingId" | "invalidPromptId" | "timeoutPromptId",
    label: string,
    blank: string,
    hint?: string,
  ) => {
    const chosen = choices.announcements.find((a) => String(a.id) === d[field]);
    const isPlaying = chosen && String(playback.playing) === String(chosen.id);
    return (
      <div className="cf-add">
        <Select
          id={`ivr-${field}`}
          label={label}
          value={d[field]}
          error={errors[field]}
          hint={hint}
          options={promptOptions(blank)}
          onChange={(e) => {
            playback.stop();
            set(field, e.target.value);
          }}
        />
        <Button
          variant="secondary"
          size="sm"
          icon={isPlaying ? "square" : "play"}
          disabled={!chosen || playback.checking !== null}
          aria-label={`${isPlaying ? "Stop" : "Preview"} ${label.toLowerCase()}`}
          onClick={() =>
            chosen &&
            (isPlaying
              ? playback.stop()
              : void playback.play(
                  chosen.id,
                  announcementAudioPath(chosen.id),
                  chosen.name,
                ))
          }
        >
          {isPlaying ? "Stop" : "Preview"}
        </Button>
      </div>
    );
  };

  const playingAnn = choices.announcements.find(
    (a) => String(a.id) === String(playback.playing),
  );

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found = validate(d);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    const id = (v: string): Id | null => {
      if (v === "") return null;
      return choices.announcements.find((a) => String(a.id) === v)?.id ?? v;
    };
    try {
      const body: IvrMenuFields = {
        name: d.name,
        number: d.number,
        enabled: d.enabled,
        greetingId: id(d.greetingId) as Id,
        invalidPromptId: id(d.invalidPromptId),
        timeoutPromptId: id(d.timeoutPromptId),
        digitTimeout: Number(d.digitTimeout),
        interDigitTimeout: Number(d.interDigitTimeout),
        maxRetries: Number(d.maxRetries),
        allowDirectDial: d.allowDirectDial,
        promptLanguage: d.promptLanguage,
        timeout: d.timeout,
        invalid: d.invalid,
        options: d.options,
      };
      if (commit) {
        await commit(body);
        onClose();
        return;
      }
      onSaved(
        menu ? await putIvrMenu(menu.id, body) : await createIvrMenu(body),
        menu === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  function setOption(i: number, next: Partial<IvrOption>) {
    set(
      "options",
      d.options.map((o, j) => (j === i ? { ...o, ...next } : o)),
    );
  }

  return (
    <Drawer
      title={menu ? `IVR menu ${menu.name}` : "New IVR menu"}
      description="Plays the greeting, then sends the caller where the pressed key points."
      onClose={busy ? undefined : onClose}
      width={640}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {menu ? "Save menu" : "Create menu"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={menu ? `Edit IVR menu ${menu.name}` : "New IVR menu"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id="ivr-name"
            label="Name"
            autoFocus
            value={d.name}
            error={errors.name}
            onChange={(e) => set("name", e.target.value)}
          />
          <Input
            id="ivr-number"
            label="Number"
            mono
            value={d.number}
            error={errors.number}
            hint="Optional: dial this to reach the menu directly."
            onChange={(e) => set("number", e.target.value)}
          />
        </div>
        <Switch
          label="Enabled"
          hint="A disabled menu answers its number with Not Found"
          labelPosition="end"
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Prompts</legend>
          {prompt("greetingId", "Greeting", "Choose…")}
          {prompt(
            "invalidPromptId",
            "Invalid choice prompt",
            "None: replay the greeting",
            "Played before the greeting repeats after a wrong key.",
          )}
          {prompt(
            "timeoutPromptId",
            "Timeout prompt",
            "None: replay the greeting",
            "Played before the greeting repeats after no key.",
          )}
          {playback.error && <Alert tone="bad">{playback.error}</Alert>}
          {playingAnn && (
            <NowPlaying
              key={String(playingAnn.id)}
              src={announcementAudioPath(playingAnn.id)}
              title={playingAnn.name}
              label={`Playback of ${playingAnn.name}`}
              onStop={playback.stop}
            />
          )}
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Keys</legend>
          {errors.options && <p className="cf-form__error">{errors.options}</p>}
          {d.options.length === 0 && (
            <p className="cf-form__note">No keys yet.</p>
          )}
          {d.options.map((o, i) => (
            <div
              className="cf-row"
              key={i}
              role="group"
              aria-label={`Key ${o.digit}`}
            >
              <div className="cf-row__fields cf-row__fields--key">
                <Select
                  id={`ivr-option-${i}-digit`}
                  label="Key"
                  value={o.digit}
                  error={errors[`options[${i}].digit`]}
                  options={[o.digit, ...free]}
                  onChange={(e) => setOption(i, { digit: e.target.value })}
                />
                <DestinationPicker
                  id={`ivr-option-${i}`}
                  label="Goes to"
                  value={o}
                  choices={choices}
                  kindError={errors[`options[${i}].kind`]}
                  targetError={errors[`options[${i}].target`]}
                  onChange={(next) => setOption(i, next)}
                />
              </div>
              <IconButton
                icon="trash-2"
                label={`Remove key ${o.digit}`}
                onClick={() =>
                  set(
                    "options",
                    d.options.filter((_, j) => j !== i),
                  )
                }
              />
            </div>
          ))}
          <Button
            variant="secondary"
            size="sm"
            icon="plus"
            disabled={free.length === 0}
            onClick={() =>
              set("options", [
                ...d.options,
                { digit: free[0]!, kind: "hangup", target: "" },
              ])
            }
          >
            Add key
          </Button>
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Timing</legend>
          <div className="cf-two">
            <Input
              id="ivr-digit-timeout"
              label="Wait for a key (seconds)"
              value={d.digitTimeout}
              error={errors.digitTimeout}
              onChange={(e) => set("digitTimeout", e.target.value)}
            />
            <Input
              id="ivr-inter-digit-timeout"
              label="Between digits (seconds)"
              value={d.interDigitTimeout}
              error={errors.interDigitTimeout}
              onChange={(e) => set("interDigitTimeout", e.target.value)}
            />
          </div>
          <Input
            id="ivr-max-retries"
            label="Repeats before giving up"
            value={d.maxRetries}
            error={errors.maxRetries}
            hint="How often the menu repeats after silence or a wrong key."
            onChange={(e) => set("maxRetries", e.target.value)}
          />
          <Switch
            label="Allow dialling an extension"
            hint="Digits that are not a key dial an extension number; # ends the number."
            labelPosition="end"
            checked={d.allowDirectDial}
            onChange={(e) => set("allowDirectDial", e.target.checked)}
          />
          <Select
            id="ivr-language"
            label="Prompt language"
            hint="The language of the spoken prompts this menu adds, such as the dial-by-name directory. Default follows the system language."
            value={d.promptLanguage}
            error={errors.promptLanguage}
            options={[
              { value: "", label: "Default" },
              ...Object.keys(PROMPT_LANGUAGE_LABELS).map((code) => ({
                value: code,
                label: promptLanguageLabel(code),
              })),
            ]}
            onChange={(e) => set("promptLanguage", e.target.value)}
          />
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">
            When the caller does not choose
          </legend>
          <DestinationPicker
            id="ivr-timeout"
            label="After silence"
            value={d.timeout}
            choices={choices}
            selfName={d.name}
            kindError={errors["timeout.kind"]}
            targetError={errors["timeout.target"]}
            onChange={(next) => set("timeout", next)}
          />
          <DestinationPicker
            id="ivr-invalid"
            label="After wrong keys"
            value={d.invalid}
            choices={choices}
            selfName={d.name}
            kindError={errors["invalid.kind"]}
            targetError={errors["invalid.target"]}
            onChange={(next) => set("invalid", next)}
          />
        </fieldset>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
