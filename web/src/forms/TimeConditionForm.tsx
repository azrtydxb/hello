import { useState, type FormEvent } from "react";
import {
  errorMessage,
  fieldErrors,
  hasNoTarget,
  type FieldError,
} from "../api";
import {
  createTimeCondition,
  putTimeCondition,
  type TcDestination,
  type TimeCondition,
} from "../api/timecond";
import {
  DestinationPicker,
  type DestinationChoices,
} from "../components/IvrDestination";
import {
  Button,
  Checkbox,
  Drawer,
  Input,
  Select,
  useRestoreFocus,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { Can } from "../role";
import { FormAlert } from "../pages/callflow/ui";
import { NAME_PATTERN } from "../pages/timecond/shared";

export const FORM_ID = "time-condition-form";

// --- edit --------------------------------------------------------------------

interface Draft {
  name: string;
  scheduleId: string;
  toggleNumber: string;
  open: TcDestination;
  closed: TcDestination;
  holidayOn: boolean;
  holiday: TcDestination;
}

const HANGUP: TcDestination = { kind: "hangup", target: "" };

const draftOf = (t: TimeCondition | null): Draft => ({
  name: t?.name ?? "",
  scheduleId: t ? String(t.scheduleId) : "",
  toggleNumber: t?.toggleNumber ?? "",
  open: t ? { ...t.open } : { ...HANGUP },
  closed: t ? { ...t.closed } : { ...HANGUP },
  holidayOn: Boolean(t?.holiday),
  holiday: t?.holiday ? { ...t.holiday } : { ...HANGUP },
});

const KEYS = [
  "name",
  "scheduleId",
  "toggleNumber",
  "open",
  "closed",
  "holiday",
  "open.kind",
  "open.target",
  "open.thenVoicemail",
  "closed.kind",
  "closed.target",
  "closed.thenVoicemail",
  "holiday.kind",
  "holiday.target",
  "holiday.thenVoicemail",
];

function validateDest(
  out: Record<string, string>,
  key: string,
  d: TcDestination,
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
  if (d.scheduleId === "") out.scheduleId = "Choose the schedule.";
  if (!/^[0-9]{0,4}$/.test(d.toggleNumber)) {
    out.toggleNumber = "Leave empty, or use 1 to 4 digits.";
  }
  validateDest(out, "open", d.open);
  validateDest(out, "closed", d.closed);
  if (d.holidayOn) validateDest(out, "holiday", d.holiday);
  return out;
}

/** A destination picker that also offers the voicemail after an announcement. */
function TcDestinationPicker({
  id,
  label,
  value,
  choices,
  selfName,
  errors,
  path,
  onChange,
}: {
  id: string;
  label: string;
  value: TcDestination;
  choices: DestinationChoices;
  selfName: string;
  errors: ErrorMap;
  path: string;
  onChange: (next: TcDestination) => void;
}) {
  return (
    <div className="cf-stack">
      <DestinationPicker
        id={id}
        label={label}
        value={value}
        choices={choices}
        selfName={selfName}
        kindError={errors[`${path}.kind`]}
        targetError={errors[`${path}.target`]}
        onChange={(next) =>
          onChange({
            kind: next.kind,
            target: next.target,
            ...(next.kind === "announcement" && value.thenVoicemail
              ? { thenVoicemail: value.thenVoicemail }
              : {}),
          })
        }
      />
      {value.kind === "announcement" && (
        <Select
          id={`${id}-then`}
          label={`${label}: afterwards`}
          value={value.thenVoicemail ?? ""}
          error={errors[`${path}.thenVoicemail`]}
          options={[
            { value: "", label: "Hang up" },
            ...choices.extensions.map((e) => ({
              value: e.number,
              label: `Voicemail of ${e.number}${e.name ? ` · ${e.name}` : ""}`,
            })),
          ]}
          onChange={(e) => {
            const { thenVoicemail: _drop, ...rest } = value;
            void _drop;
            onChange(
              e.target.value === ""
                ? rest
                : { ...rest, thenVoicemail: e.target.value },
            );
          }}
        />
      )}
    </div>
  );
}

export function ConditionDrawer({
  item,
  choices,
  schedules,
  onClose,
  commit,
  onSaved,
}: {
  item: TimeCondition | null;
  choices: DestinationChoices;
  schedules: ReadonlyArray<{ id: number | string; name: string }>;
  onClose: () => void;
  /** The canvas stages the body in its draft instead of saving it here. */
  commit?: (body: object) => Promise<void>;
  onSaved: (t: TimeCondition, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(item));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  const scheduleOptions = [
    { value: "", label: "Choose…" },
    ...(d.scheduleId !== "" &&
    !schedules.some((s) => String(s.id) === d.scheduleId)
      ? [{ value: d.scheduleId, label: `${d.scheduleId} (not found)` }]
      : []),
    ...schedules.map((s) => ({ value: String(s.id), label: s.name })),
  ];

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
    const sched = schedules.find((s) => String(s.id) === d.scheduleId);
    const body = {
      name: d.name,
      scheduleId: sched?.id ?? d.scheduleId,
      toggleNumber: d.toggleNumber,
      open: d.open,
      closed: d.closed,
      holiday: d.holidayOn ? d.holiday : null,
    };
    try {
      if (commit) {
        await commit(body);
        onClose();
        return;
      }
      onSaved(
        item
          ? await putTimeCondition(item.id, body)
          : await createTimeCondition(body),
        item === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={item ? `Time condition ${item.name}` : "New time condition"}
      description="Sends the call to one destination while the schedule is open and another while it is closed."
      onClose={busy ? undefined : onClose}
      width={640}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {item ? "Save time condition" : "Create time condition"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={
          item ? `Edit time condition ${item.name}` : "New time condition"
        }
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id="tc-name"
            label="Name"
            autoFocus
            value={d.name}
            error={errors.name}
            onChange={(e) => set("name", e.target.value)}
          />
          <Select
            id="tc-schedule"
            label="Schedule"
            value={d.scheduleId}
            error={errors.scheduleId}
            options={scheduleOptions}
            onChange={(e) => set("scheduleId", e.target.value)}
          />
        </div>
        <Input
          id="tc-toggle"
          label="Toggle number"
          mono
          value={d.toggleNumber}
          error={errors.toggleNumber}
          hint={`Optional, 1 to 4 digits. Dial *28${d.toggleNumber || "<n>"} from a phone to switch between day and night mode.`}
          onChange={(e) => set("toggleNumber", e.target.value)}
        />
        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Destinations</legend>
          <TcDestinationPicker
            id="tc-open"
            label="When open"
            path="open"
            value={d.open}
            choices={choices}
            selfName={d.name}
            errors={errors}
            onChange={(next) => set("open", next)}
          />
          <TcDestinationPicker
            id="tc-closed"
            label="When closed"
            path="closed"
            value={d.closed}
            choices={choices}
            selfName={d.name}
            errors={errors}
            onChange={(next) => set("closed", next)}
          />
          <Checkbox
            label="A different destination on holidays"
            hint="Without it, a holiday goes to the closed destination."
            checked={d.holidayOn}
            onChange={(e) => set("holidayOn", e.target.checked)}
          />
          {d.holidayOn && (
            <TcDestinationPicker
              id="tc-holiday"
              label="On a holiday"
              path="holiday"
              value={d.holiday}
              choices={choices}
              selfName={d.name}
              errors={errors}
              onChange={(next) => set("holiday", next)}
            />
          )}
        </fieldset>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
