import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type FieldError, type Id } from "../../api";
import {
  createSchedule,
  deleteSchedule,
  getDefaultTimeZone,
  listCalendars,
  listSchedules,
  putSchedule,
  type DayRange,
  type HoursSchedule,
} from "../../api/timecond";
import {
  Alert,
  Button,
  Checkbox,
  ConfirmDialog,
  Drawer,
  EmptyState,
  IconButton,
  Input,
  Spinner,
  useRestoreFocus,
  useToast,
} from "../../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../../forms";
import { Can } from "../../role";
import { FormAlert } from "../callflow/ui";
import {
  HHMM,
  HHMM_END,
  NAME_PATTERN,
  WEEK,
  useLoadedList,
  useQuietList,
} from "./shared";

const FORM_ID = "schedule-form";

/** "Mon-Fri 09:00-17:00" style summary of the weekly ranges. */
function summary(ranges: readonly DayRange[]): string {
  if (ranges.length === 0) return "Never open";
  return WEEK.map(({ day, label }) => {
    const rs = ranges.filter((r) => r.day === day);
    return rs.length === 0
      ? null
      : `${label.slice(0, 3)} ${rs.map((r) => `${r.start}-${r.end}`).join(", ")}`;
  })
    .filter((s) => s !== null)
    .join(" · ");
}

/** The schedules tab: weekly open hours per time zone. */
export function Schedules() {
  const [list, replace] = useLoadedList(listSchedules);
  const [deleting, setDeleting] = useState<HoursSchedule | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; item: HoursSchedule } | null
  >(null);
  const toast = useToast();

  async function onDelete(s: HoursSchedule) {
    try {
      await deleteSchedule(s.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${s.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((items) => items.filter((x) => x.id !== s.id));
    setDeleting(null);
    toast.show(`Schedule ${s.name} deleted.`);
  }

  const add = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New schedule
      </Button>
    </Can>
  );

  return (
    <div className="cf-stack">
      <div>{add}</div>
      {list.status === "loading" && <Spinner label="Loading schedules…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the schedules">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <EmptyState
          icon="calendar-clock"
          title="No schedules yet"
          description="A schedule holds the weekly open hours of a business, in a time zone."
          action={add}
        />
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <ul className="cf-grid cf-grid--groups cf-plain" aria-label="Schedules">
          {list.items.map((s) => (
            <li
              className="az-card cf-card"
              key={String(s.id)}
              aria-label={`Schedule ${s.name}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <div style={{ minWidth: 0 }}>
                  <h2 className="cf-card__group-name">{s.name}</h2>
                  <div className="cf-card__sub">
                    <span className="cf-mono">{s.timeZone}</span>
                    {s.calendars.length > 0 &&
                      ` · holidays: ${s.calendars.join(", ")}`}
                  </div>
                </div>
              </div>
              <div className="cf-failure">{summary(s.ranges)}</div>
              <div className="cf-card__foot">
                <Button
                  variant="secondary"
                  size="sm"
                  icon="pencil"
                  aria-label={`Edit schedule ${s.name}`}
                  onClick={() => setEditing({ kind: "edit", item: s })}
                >
                  Edit
                </Button>
                <Can>
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete schedule ${s.name}`}
                    onClick={() => setDeleting(s)}
                  >
                    Delete
                  </Button>
                </Can>
              </div>
            </li>
          ))}
        </ul>
      )}
      {editing && (
        <ScheduleDrawer
          key={editing.kind === "edit" ? String(editing.item.id) : "new"}
          item={editing.kind === "edit" ? editing.item : null}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            replace((items) =>
              items.some((x) => x.id === saved.id)
                ? items.map((x) => (x.id === saved.id ? saved : x))
                : [...items, saved],
            );
            setEditing(null);
            toast.show(
              created
                ? `Schedule ${saved.name} created.`
                : `Schedule ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete schedule ${deleting.name}?`}
          description="A schedule that a time condition uses cannot be deleted."
          confirmLabel="Delete schedule"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

interface Draft {
  name: string;
  timeZone: string;
  ranges: DayRange[];
  calendarIds: Id[];
}

const KEYS = ["name", "timeZone", "ranges", "calendarIds"];

/** Zones the browser knows, for the datalist; empty where unsupported. */
function knownZones(): string[] {
  const intl = Intl as unknown as {
    supportedValuesOf?: (key: string) => string[];
  };
  try {
    return intl.supportedValuesOf?.("timeZone") ?? [];
  } catch {
    return [];
  }
}

function validate(d: Draft): Record<string, string> {
  const out: Record<string, string> = {};
  if (!NAME_PATTERN.test(d.name)) {
    out.name = "Use 1 to 64 letters, digits, dots, dashes or underscores.";
  }
  if (!/^[A-Za-z0-9_+/-]{1,64}$/.test(d.timeZone)) {
    out.timeZone = "Use an IANA time zone such as Asia/Dubai.";
  }
  d.ranges.forEach((r, i) => {
    if (!HHMM.test(r.start)) out[`ranges[${i}].start`] = "Use HH:MM.";
    if (!HHMM_END.test(r.end)) out[`ranges[${i}].end`] = "Use HH:MM or 24:00.";
    if (r.start === r.end) out[`ranges[${i}].end`] = "Must differ from the start.";
  });
  return out;
}

function ScheduleDrawer({
  item,
  onClose,
  onSaved,
}: {
  item: HoursSchedule | null;
  onClose: () => void;
  onSaved: (s: HoursSchedule, created: boolean) => void;
}) {
  useRestoreFocus();
  const calendars = useQuietList(listCalendars);
  const [d, setD] = useState<Draft>(() => ({
    name: item?.name ?? "",
    timeZone: item?.timeZone ?? "",
    ranges: item ? item.ranges.map((r) => ({ ...r })) : [],
    calendarIds: item ? [...item.calendarIds] : [],
  }));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const zones = knownZones();

  // A new schedule starts in the phones' time zone setting.
  useEffect(() => {
    if (item) return;
    const controller = new AbortController();
    getDefaultTimeZone(controller.signal)
      .then((tz) =>
        setD((prev) => (prev.timeZone === "" ? { ...prev, timeZone: tz } : prev)),
      )
      .catch(() => undefined);
    return () => controller.abort();
  }, [item]);

  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));
  const setRange = (index: number, next: Partial<DayRange>) =>
    set(
      "ranges",
      d.ranges.map((r, i) => (i === index ? { ...r, ...next } : r)),
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
    try {
      const body = { ...d, timeZone: d.timeZone.trim() };
      onSaved(
        item ? await putSchedule(item.id, body) : await createSchedule(body),
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

  /** Copies Monday's ranges to Tuesday to Friday. */
  function copyMonday() {
    const monday = d.ranges.filter((r) => r.day === 1);
    set("ranges", [
      ...d.ranges.filter((r) => r.day < 2 || r.day > 5),
      ...[2, 3, 4, 5].flatMap((day) => monday.map((r) => ({ ...r, day }))),
    ]);
  }

  return (
    <Drawer
      title={item ? `Schedule ${item.name}` : "New schedule"}
      description="Open hours per weekday, in one time zone. An end before the start runs past midnight."
      onClose={busy ? undefined : onClose}
      width={640}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {item ? "Save schedule" : "Create schedule"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={item ? `Edit schedule ${item.name}` : "New schedule"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id="schedule-name"
            label="Name"
            autoFocus
            value={d.name}
            error={errors.name}
            onChange={(e) => set("name", e.target.value)}
          />
          <div>
            <Input
              id="schedule-zone"
              label="Time zone"
              mono
              list="schedule-zones"
              value={d.timeZone}
              error={errors.timeZone}
              hint="Defaults to the phones' time zone setting."
              onChange={(e) => set("timeZone", e.target.value)}
            />
            <datalist id="schedule-zones">
              {zones.map((z) => (
                <option key={z} value={z} />
              ))}
            </datalist>
          </div>
        </div>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Open hours</legend>
          {errors.ranges && <p className="cf-form__error">{errors.ranges}</p>}
          {WEEK.map(({ day, label }) => {
            const rows = d.ranges
              .map((r, index) => ({ r, index }))
              .filter(({ r }) => r.day === day);
            return (
              <div
                key={day}
                role="group"
                aria-label={label}
                className="cf-stack"
              >
                <div className="cf-add">
                  <strong style={{ minWidth: 96 }}>{label}</strong>
                  {rows.length === 0 && <span>Closed</span>}
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="plus"
                    aria-label={`Add a range on ${label}`}
                    onClick={() =>
                      set("ranges", [
                        ...d.ranges,
                        { day, start: "09:00", end: "17:00" },
                      ])
                    }
                  >
                    Add range
                  </Button>
                  {day === 1 && rows.length > 0 && (
                    <Button variant="ghost" size="sm" onClick={copyMonday}>
                      Copy to Tuesday-Friday
                    </Button>
                  )}
                </div>
                {rows.map(({ r, index }) => (
                  <div className="cf-row" key={index}>
                    <Input
                      id={`schedule-range-${index}-start`}
                      label={`${label} start`}
                      mono
                      value={r.start}
                      error={errors[`ranges[${index}].start`]}
                      onChange={(e) => setRange(index, { start: e.target.value })}
                    />
                    <Input
                      id={`schedule-range-${index}-end`}
                      label={`${label} end`}
                      mono
                      value={r.end}
                      error={errors[`ranges[${index}].end`]}
                      onChange={(e) => setRange(index, { end: e.target.value })}
                    />
                    <IconButton
                      icon="trash-2"
                      label={`Remove ${label} range ${r.start}-${r.end}`}
                      onClick={() =>
                        set(
                          "ranges",
                          d.ranges.filter((_, i) => i !== index),
                        )
                      }
                    />
                  </div>
                ))}
              </div>
            );
          })}
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Holiday calendars</legend>
          {calendars.length === 0 && (
            <p className="cf-form__note">
              No calendars yet. Create one on the Calendars tab to close the
              schedule on holidays.
            </p>
          )}
          {calendars.map((c) => (
            <Checkbox
              key={String(c.id)}
              label={c.name}
              hint={`${c.entries.length} entries`}
              checked={d.calendarIds.some((x) => String(x) === String(c.id))}
              onChange={(e) =>
                set(
                  "calendarIds",
                  e.target.checked
                    ? [...d.calendarIds, c.id]
                    : d.calendarIds.filter((x) => String(x) !== String(c.id)),
                )
              }
            />
          ))}
        </fieldset>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
