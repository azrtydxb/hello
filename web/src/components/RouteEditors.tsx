import { useId } from "react";
import type { Schedule, ScheduleWindow, Transform } from "../api";
import { Button, IconButton, Input, Switch } from "../design/azrty/components";
import { fieldId, type ErrorMap } from "../forms";
import { Checkbox } from "../pages/callflow/ui";

/** The form's view of a Transform: every field as typed. */
export interface TransformDraft {
  strip: string;
  prefix: string;
  regex: string;
  template: string;
}

/** A Transform as editable strings. */
export function transformDraft(
  t: Transform | undefined | null,
): TransformDraft {
  return {
    strip: t?.strip ? String(t.strip) : "",
    prefix: t?.prefix ?? "",
    regex: t?.regex ?? "",
    template: t?.template ?? "",
  };
}

/** Back to the API shape, omitting empty fields ({} is identity). */
export function toTransform(d: TransformDraft): Transform {
  const t: Transform = {};
  if (d.strip.trim() !== "") t.strip = Number(d.strip);
  if (d.prefix !== "") t.prefix = d.prefix;
  if (d.regex !== "") t.regex = d.regex;
  if (d.template !== "") t.template = d.template;
  return t;
}

export const TRANSFORM_FIELDS = [
  "strip",
  "prefix",
  "regex",
  "template",
] as const;

/** Field keys a TransformEditor at `base` shows, for error mapping. */
export function transformKeys(base: string): string[] {
  return [base, ...TRANSFORM_FIELDS.map((f) => `${base}.${f}`)];
}

/** Client-side checks for a transform at `base`. */
export function validateTransform(
  base: string,
  d: TransformDraft,
): Record<string, string> {
  const e: Record<string, string> = {};
  if (d.strip.trim() !== "" && !/^[0-9]+$/.test(d.strip.trim())) {
    e[`${base}.strip`] = "Enter a whole number of digits (0 or more).";
  }
  if (d.regex.length > 500) e[`${base}.regex`] = "At most 500 characters.";
  if (d.template !== "" && d.regex === "") {
    e[`${base}.template`] = "A template needs a regex.";
  }
  return e;
}

/** Editor for a number rewrite: strip, then prefix, then regex → template. */
export function TransformEditor({
  form,
  base,
  legend,
  value,
  errors,
  onChange,
}: {
  form: string;
  base: string;
  legend: string;
  value: TransformDraft;
  errors: ErrorMap;
  onChange: (v: TransformDraft) => void;
}) {
  const id = (f: string) => fieldId(form, `${base}.${f}`);
  const groupError = errors[base];
  const groupErrorId = `${fieldId(form, base)}-error`;
  const set = (f: keyof TransformDraft, v: string) =>
    onChange({ ...value, [f]: v });
  return (
    <fieldset
      className="cf-form__section"
      aria-describedby={groupError ? groupErrorId : undefined}
    >
      <legend className="az-eyebrow">{legend}</legend>
      {groupError && (
        <p id={groupErrorId} className="cf-form__error">
          {groupError}
        </p>
      )}
      <p className="cf-form__note">
        Applied in order: strip leading digits, add the prefix, then replace a
        regex match with the template (<code>{"${1}"}</code> refers to a group).
        Leave all empty to keep the number unchanged.
      </p>
      <div className="cf-two">
        <Input
          id={id("strip")}
          label="Strip digits"
          mono
          inputMode="numeric"
          value={value.strip}
          error={errors[`${base}.strip`]}
          onChange={(e) => set("strip", e.target.value)}
        />
        <Input
          id={id("prefix")}
          label="Prefix"
          mono
          value={value.prefix}
          error={errors[`${base}.prefix`]}
          onChange={(e) => set("prefix", e.target.value)}
        />
        <Input
          id={id("regex")}
          label="Regex"
          mono
          spellCheck={false}
          value={value.regex}
          error={errors[`${base}.regex`]}
          onChange={(e) => set("regex", e.target.value)}
        />
        <Input
          id={id("template")}
          label="Template"
          mono
          spellCheck={false}
          value={value.template}
          error={errors[`${base}.template`]}
          onChange={(e) => set("template", e.target.value)}
        />
      </div>
    </fieldset>
  );
}

export const DAY_NAMES = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
] as const;

function localTimeZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC";
  } catch {
    return "UTC";
  }
}

function timeZones(): string[] {
  try {
    return Intl.supportedValuesOf("timeZone");
  } catch {
    return [];
  }
}

/** The schedule a newly enabled editor starts with: weekdays 09:00–17:00 here. */
export function newSchedule(): Schedule {
  return {
    timeZone: localTimeZone(),
    windows: [{ days: [1, 2, 3, 4, 5], start: "09:00", end: "17:00" }],
  };
}

/** Field keys a ScheduleEditor shows, for error mapping. */
export function scheduleKeys(s: Schedule | null): string[] {
  if (!s) return ["schedule"];
  return [
    "schedule",
    "schedule.timeZone",
    "schedule.windows",
    ...s.windows.flatMap((_, i) => [
      `schedule.windows[${i}]`,
      `schedule.windows[${i}].days`,
      `schedule.windows[${i}].start`,
      `schedule.windows[${i}].end`,
    ]),
  ];
}

const HHMM = /^([01][0-9]|2[0-3]):[0-5][0-9]$/;

/** Client-side checks for a schedule. */
export function validateSchedule(s: Schedule | null): Record<string, string> {
  const e: Record<string, string> = {};
  if (!s) return e;
  if (s.timeZone.trim() === "") e["schedule.timeZone"] = "Enter a time zone.";
  if (s.windows.length === 0)
    e["schedule.windows"] = "Add at least one window.";
  s.windows.forEach((w, i) => {
    if (w.days.length === 0) e[`schedule.windows[${i}].days`] = "Pick a day.";
    if (!HHMM.test(w.start)) e[`schedule.windows[${i}].start`] = "Use HH:MM.";
    if (!HHMM.test(w.end)) e[`schedule.windows[${i}].end`] = "Use HH:MM.";
  });
  return e;
}

/** Editor for an optional weekly schedule in one time zone. */
export function ScheduleEditor({
  form,
  value,
  errors,
  onChange,
}: {
  form: string;
  value: Schedule | null;
  errors: ErrorMap;
  onChange: (v: Schedule | null) => void;
}) {
  const listId = useId();
  const id = (k: string) => fieldId(form, k);
  const setWindow = (i: number, patch: Partial<ScheduleWindow>) =>
    value &&
    onChange({
      ...value,
      windows: value.windows.map((w, j) => (j === i ? { ...w, ...patch } : w)),
    });
  const groupError = errors.schedule ?? errors["schedule.windows"];
  const groupErrorId = `${id("schedule")}-error`;

  return (
    <fieldset
      className="cf-form__section"
      aria-describedby={groupError ? groupErrorId : undefined}
    >
      <legend className="az-eyebrow">Schedule</legend>
      <Switch
        id={id("schedule.on")}
        label="Only during these times"
        labelPosition="end"
        checked={value !== null}
        onChange={(e) => onChange(e.target.checked ? newSchedule() : null)}
      />
      {groupError && (
        <p id={groupErrorId} className="cf-form__error">
          {groupError}
        </p>
      )}
      {value === null && <p className="cf-form__note">Always open.</p>}
      {value !== null && (
        <>
          <Input
            id={id("schedule.timeZone")}
            label="Time zone"
            list={listId}
            value={value.timeZone}
            error={errors["schedule.timeZone"]}
            hint="IANA name, e.g. Asia/Dubai."
            onChange={(e) => onChange({ ...value, timeZone: e.target.value })}
          />
          <datalist id={listId}>
            {timeZones().map((tz) => (
              <option key={tz} value={tz} />
            ))}
          </datalist>
          {value.windows.map((w, i) => {
            const n = i + 1;
            const daysKey = `schedule.windows[${i}].days`;
            const daysError =
              errors[daysKey] ?? errors[`schedule.windows[${i}]`];
            return (
              <fieldset
                key={i}
                className="cf-window"
                aria-describedby={
                  daysError ? `${id(daysKey)}-error` : undefined
                }
              >
                <legend>Window {n}</legend>
                <div className="cf-days">
                  {DAY_NAMES.map((day, d) => (
                    <Checkbox
                      key={day}
                      aria-label={day}
                      label={<span aria-hidden="true">{day.slice(0, 3)}</span>}
                      checked={w.days.includes(d)}
                      onChange={(e) =>
                        setWindow(i, {
                          days: e.target.checked
                            ? [...w.days, d].sort((a, b) => a - b)
                            : w.days.filter((x) => x !== d),
                        })
                      }
                    />
                  ))}
                </div>
                {daysError && (
                  <p id={`${id(daysKey)}-error`} className="cf-form__error">
                    {daysError}
                  </p>
                )}
                <div className="cf-row">
                  <div className="cf-row__fields">
                    <Input
                      id={id(`schedule.windows[${i}].start`)}
                      label={`Start ${n}`}
                      type="time"
                      size="sm"
                      value={w.start}
                      error={errors[`schedule.windows[${i}].start`]}
                      onChange={(e) => setWindow(i, { start: e.target.value })}
                    />
                    <Input
                      id={id(`schedule.windows[${i}].end`)}
                      label={`End ${n}`}
                      type="time"
                      size="sm"
                      value={w.end}
                      error={errors[`schedule.windows[${i}].end`]}
                      hint="Before the start runs past midnight."
                      onChange={(e) => setWindow(i, { end: e.target.value })}
                    />
                  </div>
                  <IconButton
                    icon="trash-2"
                    label={`Remove window ${n}`}
                    onClick={() =>
                      onChange({
                        ...value,
                        windows: value.windows.filter((_, j) => j !== i),
                      })
                    }
                  />
                </div>
              </fieldset>
            );
          })}
          <div>
            <Button
              variant="ghost"
              size="sm"
              icon="plus"
              onClick={() =>
                onChange({
                  ...value,
                  windows: [
                    ...value.windows,
                    { days: [1, 2, 3, 4, 5], start: "09:00", end: "17:00" },
                  ],
                })
              }
            >
              Add window
            </Button>
          </div>
        </>
      )}
    </fieldset>
  );
}
