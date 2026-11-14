import { useEffect, useState, type FormEvent } from "react";
import {
  errorMessage,
  fieldErrors,
  listExtensions,
  listQueues,
  listRingGroups,
  type Extension,
  type Queue,
  type RingGroup,
} from "../../api";
import {
  listInboxAgents,
  type InboxAgent,
  type MessagingAddress,
} from "../../api/inbox";
import {
  getRouting,
  putRouting,
  ROUTE_KIND_LABEL,
  type AfterHoursAction,
  type RouteKind,
  type ScheduleDay,
} from "../../api/inboxRouting";
import {
  Alert,
  Button,
  Input,
  Modal,
  Select,
  Spinner,
  useRestoreFocus,
} from "../../design/azrty/components";

const DAYS = [
  "Sunday",
  "Monday",
  "Tuesday",
  "Wednesday",
  "Thursday",
  "Friday",
  "Saturday",
];

type Choices = {
  queues: Queue[];
  users: InboxAgent[];
  extensions: Extension[];
  groups: RingGroup[];
};

/** The target picker of a route kind: the kind, then what it names. */
function RoutePicker({
  idPrefix,
  label,
  kind,
  target,
  choices,
  errors,
  onKind,
  onTarget,
}: {
  idPrefix: string;
  label: string;
  kind: RouteKind;
  target: string;
  choices: Choices;
  errors: Record<string, string>;
  onKind: (k: RouteKind) => void;
  onTarget: (t: string) => void;
}) {
  const options: Record<
    Exclude<RouteKind, "inbox">,
    { value: string; label: string }[]
  > = {
    queue: choices.queues
      .filter((q) => q.textEnabled)
      .map((q) => ({ value: String(q.id), label: q.name })),
    user: choices.users.map((u) => ({
      value: String(u.id),
      label: u.displayName || u.username,
    })),
    extension: choices.extensions.map((e) => ({
      value: String(e.id),
      label: e.name ? `${e.number} · ${e.name}` : e.number,
    })),
    ring_group: choices.groups.map((g) => ({
      value: String(g.id),
      label: g.name,
    })),
  };
  return (
    <div className="cf-two">
      <Select
        id={`${idPrefix}-kind`}
        label={label}
        value={kind}
        error={errors[`${idPrefix}Kind`]}
        options={(Object.keys(ROUTE_KIND_LABEL) as RouteKind[]).map((k) => ({
          value: k,
          label: ROUTE_KIND_LABEL[k],
        }))}
        onChange={(e) => {
          onKind(e.target.value as RouteKind);
          onTarget("");
        }}
      />
      {kind !== "inbox" && (
        <Select
          id={`${idPrefix}-target`}
          label="Which"
          value={target}
          error={errors[`${idPrefix}Target`]}
          hint={
            kind === "queue" && options.queue.length === 0
              ? "No queue takes texts yet: turn on texting on a queue first."
              : undefined
          }
          options={[{ value: "", label: "Choose…" }, ...options[kind]]}
          onChange={(e) => onTarget(e.target.value)}
        />
      )}
    </div>
  );
}

/**
 * How one number routes: where its new conversations go, when it is open and
 * what it does outside those hours. A queue assigns by its strategy; a user
 * or an extension's user gets every conversation; a ring group's users share
 * the pool and the first to reply takes it.
 */
export function RoutingDialog({
  address,
  onClose,
  onSaved,
}: {
  address: MessagingAddress;
  onClose: () => void;
  onSaved: () => void;
}) {
  useRestoreFocus();
  const [choices, setChoices] = useState<Choices | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [kind, setKind] = useState<RouteKind>("inbox");
  const [target, setTarget] = useState("");
  const [zone, setZone] = useState(
    Intl.DateTimeFormat().resolvedOptions().timeZone || "UTC",
  );
  const [days, setDays] = useState<ScheduleDay[]>([]);
  const [action, setAction] = useState<AfterHoursAction>("none");
  const [text, setText] = useState("");
  const [ahKind, setAhKind] = useState<RouteKind>("inbox");
  const [ahTarget, setAhTarget] = useState("");
  const [first, setFirst] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    const s = controller.signal;
    Promise.all([
      getRouting(address.id, s),
      listQueues(s),
      listInboxAgents(s),
      listExtensions(s),
      listRingGroups(s),
    ])
      .then(([r, queues, users, extensions, groups]) => {
        setChoices({ queues, users, extensions, groups });
        setKind(r.routeKind);
        setTarget(r.routeTarget);
        if (r.schedule.zone) setZone(r.schedule.zone);
        setDays(r.schedule.days);
        setAction(r.afterHours.action);
        setText(r.afterHours.text);
        setAhKind(r.afterHours.routeKind);
        setAhTarget(r.afterHours.routeTarget);
        setFirst(r.firstContactText);
      })
      .catch((err: unknown) => {
        if (!s.aborted) setLoadError(errorMessage(err));
      });
    return () => controller.abort();
  }, [address.id]);

  function setDay(i: number, patch: Partial<ScheduleDay>) {
    setDays((ds) => ds.map((d, j) => (j === i ? { ...d, ...patch } : d)));
  }

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setFields({});
    try {
      await putRouting(address.id, {
        routeKind: kind,
        routeTarget: kind === "inbox" ? "" : target,
        schedule: { zone: days.length > 0 ? zone.trim() : "", days },
        firstContactText: first.trim(),
        afterHours: {
          action,
          text: text.trim(),
          routeKind: ahKind,
          routeTarget: ahKind === "inbox" ? "" : ahTarget,
        },
      });
      onSaved();
    } catch (err: unknown) {
      setFields(
        Object.fromEntries(fieldErrors(err).map((f) => [f.path, f.message])),
      );
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  const errors = {
    route: fields.route,
    routeKind: fields.routeKind,
    routeTarget: fields.routeTarget,
    ahKind: fields["afterHours.routeKind"],
    ahTarget: fields["afterHours.routeTarget"],
  };

  return (
    <Modal
      title={`Routing of ${address.address}`}
      description="Where new conversations of this number go. A target that is gone or cannot take conversations sends them to the supervisors' pool instead."
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            type="submit"
            form="routing-form"
            disabled={busy || choices === null}
          >
            Save routing
          </Button>
        </>
      }
    >
      {loadError && (
        <Alert tone="bad" title="Could not load the routing">
          {loadError}
        </Alert>
      )}
      {!choices && !loadError && <Spinner label="Loading the routing…" />}
      {error && (
        <Alert tone="bad" title="Could not save the routing">
          {error}
        </Alert>
      )}
      {choices && (
        <form id="routing-form" className="cf-stack" onSubmit={save}>
          <RoutePicker
            idPrefix="route"
            label="New conversations go to"
            kind={kind}
            target={target}
            choices={choices}
            errors={{
              routeKind: errors.routeKind ?? "",
              routeTarget: errors.routeTarget ?? "",
            }}
            onKind={setKind}
            onTarget={setTarget}
          />
          <Input
            id="routing-first"
            label="First-contact text"
            hint="Sent once to a customer who texts this number for the first time. Empty sends nothing. Only a SIP trunk channel can send it today."
            value={first}
            maxLength={480}
            error={fields.firstContactText}
            onChange={(e) => setFirst(e.target.value)}
          />
          <fieldset className="cf-form__section">
            <legend className="az-eyebrow">Opening hours</legend>
            <p className="az-caption">
              No windows means always open. Outside the windows the after-hours
              action applies.
            </p>
            {days.length > 0 && (
              <Input
                id="routing-zone"
                label="Time zone"
                hint="An IANA name such as Europe/Brussels."
                value={zone}
                error={fields.schedule}
                onChange={(e) => setZone(e.target.value)}
              />
            )}
            {days.map((d, i) => (
              <div className="cf-two" key={i}>
                <Select
                  id={`routing-day-${String(i)}`}
                  label="Day"
                  value={String(d.day)}
                  options={DAYS.map((n, v) => ({ value: String(v), label: n }))}
                  onChange={(e) => setDay(i, { day: Number(e.target.value) })}
                />
                <Input
                  id={`routing-from-${String(i)}`}
                  label="From"
                  type="time"
                  value={d.from}
                  onChange={(e) => setDay(i, { from: e.target.value })}
                />
                <Input
                  id={`routing-to-${String(i)}`}
                  label="Until"
                  type="time"
                  value={d.to}
                  onChange={(e) => setDay(i, { to: e.target.value })}
                />
                <Button
                  size="sm"
                  variant="ghost"
                  onClick={() => setDays((ds) => ds.filter((_, j) => j !== i))}
                >
                  Remove window {i + 1}
                </Button>
              </div>
            ))}
            <Button
              size="sm"
              variant="secondary"
              icon="plus"
              onClick={() =>
                setDays((ds) => [...ds, { day: 1, from: "09:00", to: "17:00" }])
              }
            >
              Add a window
            </Button>
          </fieldset>
          {days.length > 0 && (
            <fieldset className="cf-form__section">
              <legend className="az-eyebrow">Outside the hours</legend>
              <Select
                id="routing-action"
                label="A new conversation"
                value={action}
                error={fields["afterHours.action"]}
                options={[
                  { value: "none", label: "Is routed as usual" },
                  { value: "reply", label: "Gets a reply, then is routed" },
                  { value: "route", label: "Goes somewhere else" },
                ]}
                onChange={(e) => setAction(e.target.value as AfterHoursAction)}
              />
              {action === "reply" && (
                <Input
                  id="routing-text"
                  label="Reply"
                  hint="Sent once, to a customer who has not opted out. Only a SIP trunk channel can send it today."
                  value={text}
                  maxLength={480}
                  error={fields["afterHours.text"]}
                  onChange={(e) => setText(e.target.value)}
                />
              )}
              {action === "route" && (
                <RoutePicker
                  idPrefix="after"
                  label="Goes to"
                  kind={ahKind}
                  target={ahTarget}
                  choices={choices}
                  errors={{
                    afterKind: errors.ahKind ?? "",
                    afterTarget: errors.ahTarget ?? "",
                  }}
                  onKind={setAhKind}
                  onTarget={setAhTarget}
                />
              )}
            </fieldset>
          )}
        </form>
      )}
    </Modal>
  );
}
