import { useEffect, useState } from "react";
import {
  errorMessage,
  IVR_DESTINATION_LABEL,
  listAnnouncements,
  listExtensions,
  listIvrMenus,
  listPagingGroups,
  listRingGroups,
} from "../api";
import {
  deleteTimeCondition,
  evaluateTimeCondition,
  listSchedules,
  listTimeConditions,
  setTimeConditionOverride,
  type Override,
  type TcDestination,
  type TcEvaluation,
  type TimeCondition,
} from "../api/timecond";
import { type DestinationChoices } from "../components/IvrDestination";
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
  Spinner,
  Tabs,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { Can } from "../role";
import { Calendars } from "./timecond/Calendars";
import { Schedules } from "./timecond/Schedules";
import { useLoadedList, useQuietList } from "./timecond/shared";
import { ConditionDrawer } from "../forms/TimeConditionForm";

type Tab = "conditions" | "schedules" | "calendars";

const destText = (d: TcDestination): string => {
  switch (d.kind) {
    case "hangup":
      return IVR_DESTINATION_LABEL.hangup;
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
