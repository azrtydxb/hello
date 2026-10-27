import { useEffect, useState, type FormEvent } from "react";
import {
  errorMessage,
  fieldErrors,
  IVR_DESTINATION_LABEL,
  hasNoTarget,
  listAnnouncements,
  listExtensions,
  listIvrMenus,
  listPagingGroups,
  listRingGroups,
  type FieldError,
} from "../api";
import {
  createTimeCondition,
  deleteTimeCondition,
  evaluateTimeCondition,
  listSchedules,
  listTimeConditions,
  putTimeCondition,
  setTimeConditionOverride,
  type Override,
  type TcDestination,
  type TcEvaluation,
  type TimeCondition,
} from "../api/timecond";
import {
  DestinationPicker,
  type DestinationChoices,
} from "../components/IvrDestination";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Input,
  PageHeader,
  SegmentedControl,
  Select,
  Spinner,
  Tabs,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { Can } from "../role";
import { FormAlert } from "./callflow/ui";
import { Calendars } from "./timecond/Calendars";
import { Schedules } from "./timecond/Schedules";
import { NAME_PATTERN, useLoadedList, useQuietList } from "./timecond/shared";

type Tab = "conditions" | "schedules" | "calendars";

const FORM_ID = "time-condition-form";

const destText = (d: TcDestination): string => {
  switch (d.kind) {
    case "hangup":
      return IVR_DESTINATION_LABEL.hangup;
    case "directory":
      return IVR_DESTINATION_LABEL.directory;
    case "announcement":
      return `Announcement ${d.target}, then ${
        d.thenVoicemail ? `voicemail ${d.thenVoicemail}` : "hang up"
      }`;
    default:
      return `${IVR_DESTINATION_LABEL[d.kind]} ${d.target}`;
  }
};

/**
 * Time and date routing: time conditions send a call to one place when the
 * schedule is open, another when it is closed and a third on holidays, with a
 * manual day/night override. Schedules hold the weekly hours; calendars hold
 * the holidays.
 */
export function TimeConditions() {
  const [tab, setTab] = useState<Tab>("conditions");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Time conditions"
        description="Route calls by the time of day and the date: open hours to one destination, after hours or holidays to another. Send calls to a time condition from an inbound route, an IVR key, a ring group's failover or a feature code."
      />
      <Tabs<Tab>
        aria-label="Time conditions, schedules and calendars"
        className="cf-tabs"
        idPrefix="tc"
        value={tab}
        onChange={setTab}
        items={[
          { id: "conditions", label: "Time conditions" },
          { id: "schedules", label: "Schedules" },
          { id: "calendars", label: "Calendars" },
        ]}
      />
      <div
        role="tabpanel"
        id={`tc-panel-${tab}`}
        aria-labelledby={`tc-tab-${tab}`}
      >
        {tab === "conditions" && <Conditions />}
        {tab === "schedules" && <Schedules />}
        {tab === "calendars" && <Calendars />}
      </div>
    </section>
  );
}

const OVERRIDE_OPTIONS = [
  { value: "auto" as const, label: "Auto" },
  { value: "open" as const, label: "Open" },
  { value: "closed" as const, label: "Closed" },
];

function Conditions() {
  const [list, replace] = useLoadedList(listTimeConditions);
  const [deleting, setDeleting] = useState<TimeCondition | null>(null);
  const [evaluating, setEvaluating] = useState<TimeCondition | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; item: TimeCondition } | null
  >(null);
  const [busyId, setBusyId] = useState<string | null>(null);
  const [overrideError, setOverrideError] = useState<string | null>(null);
  const toast = useToast();
  const extensions = useQuietList(listExtensions);
  const ringGroups = useQuietList(listRingGroups);
  const announcements = useQuietList(listAnnouncements);
  const pagingGroups = useQuietList(listPagingGroups);
  const menus = useQuietList(listIvrMenus);
  const schedules = useQuietList(listSchedules);
  const timeConditions = list.status === "ready" ? list.items : [];
  const choices: DestinationChoices = {
    extensions,
    ringGroups,
    announcements,
    pagingGroups,
    menus,
    timeConditions,
  };

  async function onDelete(t: TimeCondition) {
    try {
      await deleteTimeCondition(t.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${t.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((items) => items.filter((x) => x.id !== t.id));
    setDeleting(null);
    toast.show(`Time condition ${t.name} deleted.`);
  }

  async function onOverride(t: TimeCondition, mode: Override) {
    setOverrideError(null);
    setBusyId(String(t.id));
    try {
      const saved = await setTimeConditionOverride(t.id, mode);
      replace((items) => items.map((x) => (x.id === saved.id ? saved : x)));
      toast.show(
        mode === "auto"
          ? `${t.name} follows its schedule again.`
          : `${t.name} is forced ${mode}.`,
      );
    } catch (err: unknown) {
      setOverrideError(`Could not change ${t.name}: ${errorMessage(err)}`);
    } finally {
      setBusyId(null);
    }
  }

  const add = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New time condition
      </Button>
    </Can>
  );

  return (
    <div className="cf-stack">
      <div>{add}</div>
      {overrideError && (
        <Alert tone="bad" title="Override not changed">
          {overrideError}
        </Alert>
      )}
      {list.status === "loading" && (
        <Spinner label="Loading time conditions…" />
      )}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the time conditions">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <EmptyState
          icon="clock"
          title="No time conditions yet"
          description="Create a schedule first, then a time condition that sends open and closed calls to different places."
          action={add}
        />
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <ul
          className="cf-grid cf-grid--groups cf-plain"
          aria-label="Time conditions"
        >
          {list.items.map((t) => (
            <li
              className="az-card cf-card"
              key={String(t.id)}
              aria-label={`Time condition ${t.name}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <div style={{ minWidth: 0 }}>
                  <h2 className="cf-card__group-name">{t.name}</h2>
                  <div className="cf-card__sub">
                    Schedule <span className="cf-mono">{t.scheduleName}</span>
                    {t.toggleNumber && (
                      <>
                        {" "}
                        · toggle{" "}
                        <span className="cf-mono">*28{t.toggleNumber}</span>
                      </>
                    )}
                  </div>
                </div>
                {t.override !== "auto" && (
                  <Badge tone="warn">Forced {t.override}</Badge>
                )}
              </div>
              <ul
                className="cf-members"
                aria-label={`Destinations of ${t.name}`}
              >
                <li className="cf-member">
                  <span className="cf-member__who">
                    <strong>Open</strong> {destText(t.open)}
                  </span>
                </li>
                <li className="cf-member">
                  <span className="cf-member__who">
                    <strong>Closed</strong> {destText(t.closed)}
                  </span>
                </li>
                <li className="cf-member">
                  <span className="cf-member__who">
                    <strong>Holiday</strong>{" "}
                    {t.holiday ? destText(t.holiday) : "as closed"}
                  </span>
                </li>
              </ul>
              <Can
                fallback={
                  <div className="cf-failure">Override: {t.override}</div>
                }
              >
                <div className="cf-failure">
                  <SegmentedControl<Override>
                    aria-label={`Day and night mode of ${t.name}`}
                    options={OVERRIDE_OPTIONS}
                    value={t.override}
                    onChange={(mode) => {
                      if (busyId === null && mode !== t.override) {
                        void onOverride(t, mode);
                      }
                    }}
                  />
                </div>
              </Can>
              <div className="cf-card__foot">
                <Button
                  variant="secondary"
                  size="sm"
                  icon="search"
                  aria-label={`What would ${t.name} do`}
                  onClick={() => setEvaluating(t)}
                >
                  What happens
                </Button>
                <Button
                  variant="secondary"
                  size="sm"
                  icon="pencil"
                  aria-label={`Edit time condition ${t.name}`}
                  onClick={() => setEditing({ kind: "edit", item: t })}
                >
                  Edit
                </Button>
                <Can>
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete time condition ${t.name}`}
                    onClick={() => setDeleting(t)}
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
        <ConditionDrawer
          key={editing.kind === "edit" ? String(editing.item.id) : "new"}
          item={editing.kind === "edit" ? editing.item : null}
          choices={choices}
          schedules={schedules}
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
                ? `Time condition ${saved.name} created.`
                : `Time condition ${saved.name} saved.`,
            );
          }}
        />
      )}
      {evaluating && (
        <EvaluateDrawer item={evaluating} onClose={() => setEvaluating(null)} />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete time condition ${deleting.name}?`}
          description="One that a route, ring group, IVR, feature code or another time condition names cannot be deleted."
          confirmLabel="Delete time condition"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

// --- what would happen -------------------------------------------------------

function EvaluateDrawer({
  item,
  onClose,
}: {
  item: TimeCondition;
  onClose: () => void;
}) {
  useRestoreFocus();
  const [at, setAt] = useState("");
  const [ignore, setIgnore] = useState(false);
  const [result, setResult] = useState<TcEvaluation | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function run(when: string, ignoreOverride: boolean) {
    setBusy(true);
    setError(null);
    try {
      setResult(await evaluateTimeCondition(item.id, when, ignoreOverride));
    } catch (err: unknown) {
      setResult(null);
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  useEffect(() => {
    const controller = new AbortController();
    evaluateTimeCondition(item.id, undefined, false, controller.signal)
      .then(setResult)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [item.id]);

  return (
    <Drawer
      title={`What ${item.name} would do`}
      description="Now, or at another time in the schedule's time zone."
      onClose={onClose}
      width={560}
      footer={
        <Button variant="secondary" onClick={onClose}>
          Close
        </Button>
      }
    >
      <div className="cf-form">
        <div className="cf-add">
          <Input
            id="tc-eval-at"
            label="At (schedule's time zone)"
            type="datetime-local"
            value={at}
            onChange={(e) => setAt(e.target.value)}
          />
          <Button
            variant="secondary"
            size="sm"
            disabled={busy}
            onClick={() => void run(at, ignore)}
          >
            Evaluate
          </Button>
          {at !== "" && (
            <Button
              variant="ghost"
              size="sm"
              onClick={() => {
                setAt("");
                void run("", ignore);
              }}
            >
              Now
            </Button>
          )}
        </div>
        <Checkbox
          label="Ignore the manual override"
          hint="Show what the schedule alone would do."
          checked={ignore}
          onChange={(e) => {
            setIgnore(e.target.checked);
            void run(at, e.target.checked);
          }}
        />
        {error && <Alert tone="bad">{error}</Alert>}
        {result && (
          <div role="status" aria-label="Evaluation" className="cf-stack">
            <p>
              <strong>{result.decision.verdict.local}</strong>
            </p>
            <p>
              Branch{" "}
              <Badge tone={result.decision.branch === "open" ? "good" : "warn"}>
                {result.decision.branch}
              </Badge>{" "}
              {result.decision.forced && <Badge tone="warn">forced</Badge>}
            </p>
            <p>{result.decision.why}</p>
            {result.decision.forced && (
              <p>Schedule alone: {result.decision.verdict.reason}</p>
            )}
            <p>
              Goes to <strong>{destText(result.resolution.destination)}</strong>
            </p>
            {result.resolution.steps.length > 1 && (
              <ol aria-label="Time conditions passed">
                {result.resolution.steps.map((s, i) => (
                  <li key={i}>
                    {s.condition}: {s.branch} → {destText(s.destination)}
                  </li>
                ))}
              </ol>
            )}
            {result.error && <Alert tone="bad">{result.error}</Alert>}
          </div>
        )}
      </div>
    </Drawer>
  );
}

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

function ConditionDrawer({
  item,
  choices,
  schedules,
  onClose,
  onSaved,
}: {
  item: TimeCondition | null;
  choices: DestinationChoices;
  schedules: ReadonlyArray<{ id: number | string; name: string }>;
  onClose: () => void;
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
