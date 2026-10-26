import { useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type FieldError } from "../../api";
import {
  createCalendar,
  deleteCalendar,
  importCalendar,
  listCalendars,
  putCalendar,
  type Calendar,
  type CalendarEntry,
} from "../../api/timecond";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  Drawer,
  EmptyState,
  IconButton,
  Input,
  Select,
  Spinner,
  useRestoreFocus,
  useToast,
} from "../../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../../forms";
import { Can } from "../../role";
import { FormAlert } from "../callflow/ui";
import { HHMM, HHMM_END, NAME_PATTERN, useLoadedList } from "./shared";

const FORM_ID = "calendar-form";
const MAX_IMPORT = 1 << 20;

const IMPORT_HINT = `One entry per line: a date, a range or a yearly date, then optional words and the label.

2026-12-25 Christmas Day
2026-12-24..2026-12-26 Christmas break
07-21 National day
2026-06-02 14:00-15:00 Staff meeting
2026-11-29 open=10:00-13:00,14:00-18:00 Shopping Sunday

An iCalendar (.ics) file can be pasted too.`;

/** How an entry reads on a card: "2026-12-25 Christmas Day (yearly)". */
function entryText(e: CalendarEntry): string {
  const dates = e.end && e.end !== e.start ? `${e.start} to ${e.end}` : e.start;
  const times = e.allDay ? "" : ` ${e.startTime}-${e.endTime}`;
  const open =
    e.mode === "open"
      ? ` open ${e.hours.map((h) => `${h.start}-${h.end}`).join(", ")}`
      : "";
  return `${dates}${times}${e.label ? ` · ${e.label}` : ""}${open}${e.yearly ? " (yearly)" : ""}`;
}

/** The calendars tab: holidays and exceptions the schedules consult. */
export function Calendars() {
  const [list, replace] = useLoadedList(listCalendars);
  const [deleting, setDeleting] = useState<Calendar | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; item: Calendar } | null
  >(null);
  const toast = useToast();

  async function onDelete(c: Calendar) {
    try {
      await deleteCalendar(c.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${c.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((items) => items.filter((x) => x.id !== c.id));
    setDeleting(null);
    toast.show(`Calendar ${c.name} deleted.`);
  }

  const add = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New calendar
      </Button>
    </Can>
  );
  const save = (saved: Calendar) =>
    replace((items) =>
      items.some((x) => x.id === saved.id)
        ? items.map((x) => (x.id === saved.id ? saved : x))
        : [...items, saved],
    );

  return (
    <div className="cf-stack">
      <div>{add}</div>
      {list.status === "loading" && <Spinner label="Loading calendars…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the calendars">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <EmptyState
          icon="calendar-days"
          title="No calendars yet"
          description="A calendar lists holidays and exceptions: single dates, ranges, yearly dates, whole days or part of a day."
          action={add}
        />
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <ul className="cf-grid cf-grid--groups cf-plain" aria-label="Calendars">
          {list.items.map((c) => (
            <li
              className="az-card cf-card"
              key={String(c.id)}
              aria-label={`Calendar ${c.name}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <div style={{ minWidth: 0 }}>
                  <h2 className="cf-card__group-name">{c.name}</h2>
                  <div className="cf-card__sub">
                    {c.entries.length} entries
                    {c.schedules.length > 0 &&
                      ` · used by ${c.schedules.join(", ")}`}
                  </div>
                </div>
                {c.schedules.length === 0 && <Badge tone="warn">Unused</Badge>}
              </div>
              <ul className="cf-members" aria-label={`Entries of ${c.name}`}>
                {c.entries.slice(0, 5).map((e, i) => (
                  <li className="cf-member" key={i}>
                    <span className="cf-member__who">{entryText(e)}</span>
                  </li>
                ))}
                {c.entries.length > 5 && (
                  <li className="cf-member">
                    <span className="cf-member__who">
                      and {c.entries.length - 5} more
                    </span>
                  </li>
                )}
              </ul>
              <div className="cf-card__foot">
                <Button
                  variant="secondary"
                  size="sm"
                  icon="pencil"
                  aria-label={`Edit calendar ${c.name}`}
                  onClick={() => setEditing({ kind: "edit", item: c })}
                >
                  Edit
                </Button>
                <Can>
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete calendar ${c.name}`}
                    onClick={() => setDeleting(c)}
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
        <CalendarDrawer
          key={editing.kind === "edit" ? String(editing.item.id) : "new"}
          item={editing.kind === "edit" ? editing.item : null}
          onClose={() => setEditing(null)}
          onImported={(c, n) => {
            save(c);
            toast.show(`Imported ${n} entries into ${c.name}.`);
          }}
          onSaved={(saved, created) => {
            save(saved);
            setEditing(null);
            toast.show(
              created
                ? `Calendar ${saved.name} created.`
                : `Calendar ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete calendar ${deleting.name}?`}
          description="A calendar that a schedule consults cannot be deleted."
          confirmLabel="Delete calendar"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

/** An entry as the form edits it: dates always carry a year (yearly ones 2000). */
interface EntryDraft extends CalendarEntry {
  key: number;
}

const YEARLY_YEAR = "2000";
const toFormDate = (v: string, yearly: boolean) =>
  yearly && v.length === 5 ? `${YEARLY_YEAR}-${v}` : v;
const fromFormDate = (v: string, yearly: boolean) =>
  yearly && v.length === 10 ? v.slice(5) : v;

let nextKey = 1;
const blankEntry = (): EntryDraft => ({
  key: nextKey++,
  label: "",
  start: "",
  end: "",
  yearly: false,
  allDay: true,
  startTime: "",
  endTime: "",
  mode: "closed",
  hours: [],
});

function entryDraft(e: CalendarEntry): EntryDraft {
  return {
    ...e,
    key: nextKey++,
    start: toFormDate(e.start, e.yearly),
    end: toFormDate(e.end, e.yearly),
    hours: e.hours.map((h) => ({ ...h })),
  };
}

function entryBody(e: EntryDraft): CalendarEntry {
  const { key: _key, ...rest } = e;
  void _key;
  return {
    ...rest,
    start: fromFormDate(e.start, e.yearly),
    end: fromFormDate(e.end || e.start, e.yearly),
    startTime: e.allDay ? "" : e.startTime,
    endTime: e.allDay ? "" : e.endTime,
    hours: e.mode === "open" ? e.hours : [],
  };
}

function validate(name: string, entries: EntryDraft[]): Record<string, string> {
  const out: Record<string, string> = {};
  if (!NAME_PATTERN.test(name)) {
    out.name = "Use 1 to 64 letters, digits, dots, dashes or underscores.";
  }
  entries.forEach((e, i) => {
    const p = `entries[${i}]`;
    if (!/^\d{4}-\d{2}-\d{2}$/.test(e.start))
      out[`${p}.start`] = "Pick a date.";
    if (e.end && !/^\d{4}-\d{2}-\d{2}$/.test(e.end))
      out[`${p}.end`] = "Pick a date.";
    if (!e.yearly && e.end && e.start && e.end < e.start) {
      out[`${p}.end`] = "Must not be before the start.";
    }
    if (e.mode === "closed" && !e.allDay) {
      if (!HHMM.test(e.startTime)) out[`${p}.startTime`] = "Use HH:MM.";
      if (!HHMM_END.test(e.endTime))
        out[`${p}.endTime`] = "Use HH:MM or 24:00.";
    }
    if (e.mode === "open") {
      if (e.hours.length === 0) out[`${p}.hours`] = "Add the open hours.";
      e.hours.forEach((h, j) => {
        if (!HHMM.test(h.start)) out[`${p}.hours[${j}].start`] = "Use HH:MM.";
        if (!HHMM_END.test(h.end))
          out[`${p}.hours[${j}].end`] = "Use HH:MM or 24:00.";
      });
    }
  });
  return out;
}

function CalendarDrawer({
  item,
  onClose,
  onSaved,
  onImported,
}: {
  item: Calendar | null;
  onClose: () => void;
  onSaved: (c: Calendar, created: boolean) => void;
  onImported: (c: Calendar, imported: number) => void;
}) {
  useRestoreFocus();
  const [name, setName] = useState(item?.name ?? "");
  const [description, setDescription] = useState(item?.description ?? "");
  const [entries, setEntries] = useState<EntryDraft[]>(() =>
    item ? item.entries.map(entryDraft) : [],
  );
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [text, setText] = useState("");
  const [replace, setReplace] = useState(false);
  const [importError, setImportError] = useState<string | null>(null);
  const [importing, setImporting] = useState(false);

  const setEntry = (key: number, next: Partial<EntryDraft>) =>
    setEntries((prev) =>
      prev.map((e) => (e.key === key ? { ...e, ...next } : e)),
    );

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found = validate(name, entries);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    const body = { name, description, entries: entries.map(entryBody) };
    try {
      onSaved(
        item ? await putCalendar(item.id, body) : await createCalendar(body),
        item === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), [
        "name",
        "description",
        "entries",
        ...entries.flatMap((_, i) => [
          `entries[${i}].start`,
          `entries[${i}].end`,
          `entries[${i}].startTime`,
          `entries[${i}].endTime`,
          `entries[${i}].hours`,
        ]),
      ]);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  async function onImport() {
    if (!item) return;
    setImportError(null);
    if (text.trim() === "") {
      setImportError("Paste the dates to import.");
      return;
    }
    if (text.length > MAX_IMPORT) {
      setImportError("At most 1 MiB.");
      return;
    }
    setImporting(true);
    try {
      const res = await importCalendar(item.id, text, replace);
      setEntries(res.calendar.entries.map(entryDraft));
      setText("");
      onImported(res.calendar, res.imported);
    } catch (err: unknown) {
      const fe = fieldErrors(err);
      setImportError(
        fe.length > 0
          ? fe.map((f) => `${f.path}: ${f.message}`).join("; ")
          : errorMessage(err),
      );
    } finally {
      setImporting(false);
    }
  }

  return (
    <Drawer
      title={item ? `Calendar ${item.name}` : "New calendar"}
      description="Holidays and exceptions. A closed entry closes the covered time; an open entry replaces the weekly hours of those days."
      onClose={busy ? undefined : onClose}
      width={720}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {item ? "Save calendar" : "Create calendar"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={item ? `Edit calendar ${item.name}` : "New calendar"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id="calendar-name"
            label="Name"
            autoFocus
            value={name}
            error={errors.name}
            onChange={(e) => setName(e.target.value)}
          />
          <Input
            id="calendar-description"
            label="Description"
            value={description}
            error={errors.description}
            onChange={(e) => setDescription(e.target.value)}
          />
        </div>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Entries</legend>
          {errors.entries && <p className="cf-form__error">{errors.entries}</p>}
          {entries.length === 0 && (
            <p className="cf-form__note">No entries yet.</p>
          )}
          {entries.map((e, i) => {
            const p = `entries[${i}]`;
            const n = i + 1;
            return (
              <div
                className="cf-stack"
                key={e.key}
                role="group"
                aria-label={`Entry ${n}`}
              >
                <div className="cf-row">
                  <Input
                    id={`cal-entry-${e.key}-label`}
                    label={`Label ${n}`}
                    value={e.label}
                    onChange={(ev) =>
                      setEntry(e.key, { label: ev.target.value })
                    }
                  />
                  <Input
                    id={`cal-entry-${e.key}-start`}
                    label={`First day ${n}`}
                    type="date"
                    value={e.start}
                    error={errors[`${p}.start`]}
                    onChange={(ev) =>
                      setEntry(e.key, { start: ev.target.value })
                    }
                  />
                  <Input
                    id={`cal-entry-${e.key}-end`}
                    label={`Last day ${n}`}
                    type="date"
                    value={e.end}
                    error={errors[`${p}.end`]}
                    hint="Empty: one day."
                    onChange={(ev) => setEntry(e.key, { end: ev.target.value })}
                  />
                  <IconButton
                    icon="trash-2"
                    label={`Remove entry ${n}`}
                    onClick={() =>
                      setEntries((prev) => prev.filter((x) => x.key !== e.key))
                    }
                  />
                </div>
                <div className="cf-row">
                  <Checkbox
                    label="Every year"
                    checked={e.yearly}
                    onChange={(ev) => {
                      const yearly = ev.target.checked;
                      setEntry(e.key, {
                        yearly,
                        start: yearly
                          ? toFormDate(e.start.slice(5), true)
                          : e.start,
                        end: yearly ? toFormDate(e.end.slice(5), true) : e.end,
                      });
                    }}
                  />
                  <Select
                    id={`cal-entry-${e.key}-mode`}
                    label={`Effect ${n}`}
                    size="sm"
                    value={e.mode}
                    options={[
                      { value: "closed", label: "Closed (holiday)" },
                      { value: "open", label: "Special open hours" },
                    ]}
                    onChange={(ev) =>
                      setEntry(e.key, {
                        mode: ev.target.value as "closed" | "open",
                        hours:
                          ev.target.value === "open" && e.hours.length === 0
                            ? [{ start: "09:00", end: "13:00" }]
                            : e.hours,
                      })
                    }
                  />
                  {e.mode === "closed" && (
                    <Checkbox
                      label="All day"
                      checked={e.allDay}
                      onChange={(ev) =>
                        setEntry(e.key, { allDay: ev.target.checked })
                      }
                    />
                  )}
                </div>
                {e.mode === "closed" && !e.allDay && (
                  <div className="cf-row">
                    <Input
                      id={`cal-entry-${e.key}-from`}
                      label={`Closed from ${n}`}
                      mono
                      value={e.startTime}
                      error={errors[`${p}.startTime`]}
                      onChange={(ev) =>
                        setEntry(e.key, { startTime: ev.target.value })
                      }
                    />
                    <Input
                      id={`cal-entry-${e.key}-to`}
                      label={`Closed until ${n}`}
                      mono
                      value={e.endTime}
                      error={errors[`${p}.endTime`]}
                      onChange={(ev) =>
                        setEntry(e.key, { endTime: ev.target.value })
                      }
                    />
                  </div>
                )}
                {e.mode === "open" && (
                  <div className="cf-stack">
                    {errors[`${p}.hours`] && (
                      <p className="cf-form__error">{errors[`${p}.hours`]}</p>
                    )}
                    {e.hours.map((h, j) => (
                      <div className="cf-row" key={j}>
                        <Input
                          id={`cal-entry-${e.key}-hours-${j}-start`}
                          label={`Open from ${n}.${j + 1}`}
                          mono
                          value={h.start}
                          error={errors[`${p}.hours[${j}].start`]}
                          onChange={(ev) =>
                            setEntry(e.key, {
                              hours: e.hours.map((x, k) =>
                                k === j ? { ...x, start: ev.target.value } : x,
                              ),
                            })
                          }
                        />
                        <Input
                          id={`cal-entry-${e.key}-hours-${j}-end`}
                          label={`Open until ${n}.${j + 1}`}
                          mono
                          value={h.end}
                          error={errors[`${p}.hours[${j}].end`]}
                          onChange={(ev) =>
                            setEntry(e.key, {
                              hours: e.hours.map((x, k) =>
                                k === j ? { ...x, end: ev.target.value } : x,
                              ),
                            })
                          }
                        />
                        <IconButton
                          icon="trash-2"
                          label={`Remove hours ${n}.${j + 1}`}
                          onClick={() =>
                            setEntry(e.key, {
                              hours: e.hours.filter((_, k) => k !== j),
                            })
                          }
                        />
                      </div>
                    ))}
                    <Button
                      variant="ghost"
                      size="sm"
                      icon="plus"
                      onClick={() =>
                        setEntry(e.key, {
                          hours: [...e.hours, { start: "14:00", end: "17:00" }],
                        })
                      }
                    >
                      Add hours
                    </Button>
                  </div>
                )}
              </div>
            );
          })}
          <Button
            variant="secondary"
            size="sm"
            icon="plus"
            onClick={() => setEntries((prev) => [...prev, blankEntry()])}
          >
            Add entry
          </Button>
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Import dates</legend>
          {item === null ? (
            <p className="cf-form__note">
              Save the calendar first, then import a list of dates into it.
            </p>
          ) : (
            <>
              <div className="az-field">
                <label className="az-field__label" htmlFor="calendar-import">
                  Dates to import
                </label>
                <textarea
                  id="calendar-import"
                  className="az-input cf-mono"
                  rows={7}
                  placeholder={IMPORT_HINT}
                  value={text}
                  onChange={(e) => setText(e.target.value)}
                />
              </div>
              <Checkbox
                label="Replace the existing entries"
                checked={replace}
                onChange={(e) => setReplace(e.target.checked)}
              />
              {importError && <Alert tone="bad">{importError}</Alert>}
              <Can>
                <Button
                  variant="secondary"
                  size="sm"
                  icon="upload"
                  disabled={importing || busy}
                  onClick={() => void onImport()}
                >
                  Import dates
                </Button>
              </Can>
            </>
          )}
        </fieldset>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
