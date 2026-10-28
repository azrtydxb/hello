import { useState, type FormEvent } from "react";
import {
  createQueue,
  errorMessage,
  fieldErrors,
  hasNoTarget,
  putQueue,
  QUEUE_STRATEGIES,
  QUEUE_STRATEGY_LABEL,
  type Extension,
  type FieldError,
  type Id,
  type IvrDestination,
  type MOHClass,
  type Queue,
  type QueueFields,
  type QueueMember,
  type QueueStrategy,
} from "../api";
import {
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
import { Can } from "../role";

export const FORM_ID = "queue-form";
export const NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;
export const NUMBER_PATTERN = /^([0-9]{2,16})?$/;
export interface Choices extends DestinationChoices {
  all: readonly Extension[];
  mohClasses: readonly MOHClass[];
}

interface Draft {
  name: string;
  number: string;
  enabled: boolean;
  strategy: QueueStrategy;
  agentTimeout: string;
  retryDelay: string;
  wrapUp: string;
  maxCallers: string;
  maxWait: string;
  joinEmpty: boolean;
  leaveEmpty: boolean;
  timeout: IvrDestination;
  full: IvrDestination;
  noAgents: IvrDestination;
  mohClassId: string;
  announceFrequency: string;
  announcePosition: boolean;
  announceWait: boolean;
  periodicPromptId: string;
  positionPromptId: string;
  waitPromptId: string;
  minutesPromptId: string;
  digitsPrefix: string;
  breakoutKey: string;
  breakoutPromptId: string;
  breakoutVoicemail: string;
  members: QueueMember[];
}

const idStr = (v: Id | null | undefined) => (v == null ? "" : String(v));

const draftOf = (q: Queue | null): Draft => ({
  name: q?.name ?? "",
  number: q?.number ?? "",
  enabled: q?.enabled ?? true,
  strategy: q?.strategy ?? "ring_all",
  agentTimeout: String(q?.agentTimeout ?? 20),
  retryDelay: String(q?.retryDelay ?? 5),
  wrapUp: String(q?.wrapUp ?? 0),
  maxCallers: String(q?.maxCallers ?? 0),
  maxWait: String(q?.maxWait ?? 600),
  joinEmpty: q?.joinEmpty ?? true,
  leaveEmpty: q?.leaveEmpty ?? false,
  timeout: q?.timeout ?? { kind: "hangup", target: "" },
  full: q?.full ?? { kind: "hangup", target: "" },
  noAgents: q?.noAgents ?? { kind: "hangup", target: "" },
  mohClassId: idStr(q?.mohClassId),
  announceFrequency: String(q?.announceFrequency ?? 0),
  announcePosition: q?.announcePosition ?? false,
  announceWait: q?.announceWait ?? false,
  periodicPromptId: idStr(q?.periodicPromptId),
  positionPromptId: idStr(q?.positionPromptId),
  waitPromptId: idStr(q?.waitPromptId),
  minutesPromptId: idStr(q?.minutesPromptId),
  digitsPrefix: q?.digitsPrefix ?? "",
  breakoutKey: q?.breakoutKey ?? "",
  breakoutPromptId: idStr(q?.breakoutPromptId),
  breakoutVoicemail: q?.breakoutVoicemail ?? "",
  members: q?.members ?? [],
});

const KEYS = [
  "name",
  "number",
  "enabled",
  "strategy",
  "agentTimeout",
  "retryDelay",
  "wrapUp",
  "maxCallers",
  "maxWait",
  "joinEmpty",
  "leaveEmpty",
  "timeout",
  "full",
  "noAgents",
  "mohClassId",
  "announceFrequency",
  "announcePosition",
  "announceWait",
  "periodicPromptId",
  "positionPromptId",
  "waitPromptId",
  "minutesPromptId",
  "digitsPrefix",
  "breakoutKey",
  "breakoutPromptId",
  "breakoutVoicemail",
  "members",
];

const whole = (s: string) => /^[0-9]{1,5}$/.test(s);

function range(
  out: Record<string, string>,
  key: string,
  v: string,
  lo: number,
  hi: number,
) {
  if (!whole(v) || +v < lo || +v > hi) {
    out[key] = `Use ${String(lo)} to ${String(hi)}.`;
  }
}

function validateDest(
  out: Record<string, string>,
  key: string,
  d: IvrDestination,
) {
  if (!hasNoTarget(d.kind) && d.target.trim() === "") {
    out[`${key}.target`] = "Choose a target.";
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
  range(out, "agentTimeout", d.agentTimeout, 5, 120);
  range(out, "retryDelay", d.retryDelay, 0, 120);
  range(out, "wrapUp", d.wrapUp, 0, 600);
  range(out, "maxCallers", d.maxCallers, 0, 1000);
  range(out, "maxWait", d.maxWait, 0, 86400);
  range(out, "announceFrequency", d.announceFrequency, 0, 600);
  if (
    (d.announcePosition || d.announceWait) &&
    (+d.announceFrequency === 0 || d.digitsPrefix === "")
  ) {
    out.announceFrequency =
      "Set an interval and the digits prefix to announce numbers.";
  }
  if (d.announcePosition && d.positionPromptId === "") {
    out.positionPromptId = "Choose the position announcement.";
  }
  if (d.announceWait && (d.waitPromptId === "" || d.minutesPromptId === "")) {
    out.waitPromptId = "Choose the wait and minutes announcements.";
  }
  if (d.breakoutKey !== "" && !/^[0-9*#]$/.test(d.breakoutKey)) {
    out.breakoutKey = "One of 0-9, * or #.";
  }
  if (d.breakoutKey !== "" && d.breakoutVoicemail === "") {
    out.breakoutVoicemail = "Choose whose voicemail box takes the message.";
  }
  validateDest(out, "timeout", d.timeout);
  validateDest(out, "full", d.full);
  validateDest(out, "noAgents", d.noAgents);
  d.members.forEach((m, i) => {
    if (m.penalty < 0 || m.penalty > 99) {
      out[`members[${String(i)}].penalty`] = "Use 0 to 99.";
    }
  });
  return out;
}

export function QueueDrawer({
  queue,
  choices,
  onClose,
  commit,
  onSaved,
}: {
  queue: Queue | null;
  choices: Choices;
  onClose: () => void;
  /** The canvas stages the body in its draft instead of saving it here. */
  commit?: (body: object) => Promise<void>;
  onSaved: (q: Queue, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(queue));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [pick, setPick] = useState("");
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  const prompt = (
    field:
      | "periodicPromptId"
      | "positionPromptId"
      | "waitPromptId"
      | "minutesPromptId"
      | "breakoutPromptId",
    label: string,
  ) => (
    <Select
      id={`queue-${field}`}
      label={label}
      value={d[field]}
      error={errors[field]}
      options={[
        { value: "", label: "None" },
        ...choices.announcements.map((a) => ({
          value: String(a.id),
          label: a.name,
        })),
      ]}
      onChange={(e) => set(field, e.target.value)}
    />
  );
  const num = (
    field:
      | "agentTimeout"
      | "retryDelay"
      | "wrapUp"
      | "maxCallers"
      | "maxWait"
      | "announceFrequency",
    label: string,
    hint?: string,
  ) => (
    <Input
      id={`queue-${field}`}
      label={label}
      mono
      value={d[field]}
      error={errors[field]}
      hint={hint}
      onChange={(e) => set(field, e.target.value)}
    />
  );
  const free = choices.all.filter(
    (e) => !d.members.some((m) => String(m.extensionId) === String(e.id)),
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
    const prompt = (
      v: string,
      list: readonly { id: Id }[] = choices.announcements,
    ): Id | null => {
      if (v === "") return null;
      return list.find((a) => String(a.id) === v)?.id ?? v;
    };
    try {
      const body: QueueFields = {
        name: d.name,
        number: d.number,
        enabled: d.enabled,
        strategy: d.strategy,
        agentTimeout: Number(d.agentTimeout),
        retryDelay: Number(d.retryDelay),
        wrapUp: Number(d.wrapUp),
        maxCallers: Number(d.maxCallers),
        maxWait: Number(d.maxWait),
        joinEmpty: d.joinEmpty,
        leaveEmpty: d.leaveEmpty,
        timeout: d.timeout,
        full: d.full,
        noAgents: d.noAgents,
        mohClassId: prompt(d.mohClassId, choices.mohClasses),
        announceFrequency: Number(d.announceFrequency),
        announcePosition: d.announcePosition,
        announceWait: d.announceWait,
        periodicPromptId: prompt(d.periodicPromptId),
        positionPromptId: prompt(d.positionPromptId),
        waitPromptId: prompt(d.waitPromptId),
        minutesPromptId: prompt(d.minutesPromptId),
        digitsPrefix: d.digitsPrefix,
        breakoutKey: d.breakoutKey,
        breakoutPromptId: prompt(d.breakoutPromptId),
        breakoutVoicemail: d.breakoutVoicemail,
        members: d.members.map((m, i) => ({
          extensionId: m.extensionId,
          dynamic: m.dynamic,
          penalty: m.penalty,
          position: i + 1,
        })),
      };
      if (commit) {
        await commit(body);
        onClose();
        return;
      }
      onSaved(
        queue ? await putQueue(queue.id, body) : await createQueue(body),
        queue === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  function setMember(i: number, next: Partial<QueueMember>) {
    set(
      "members",
      d.members.map((m, j) => (j === i ? { ...m, ...next } : m)),
    );
  }

  return (
    <Drawer
      title={queue ? `Queue ${queue.name}` : "New queue"}
      description="Holds callers and offers them to agents."
      onClose={busy ? undefined : onClose}
      width={680}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {queue ? "Save queue" : "Create queue"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={queue ? `Edit queue ${queue.name}` : "New queue"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id="queue-name"
            label="Name"
            autoFocus
            value={d.name}
            error={errors.name}
            onChange={(e) => set("name", e.target.value)}
          />
          <Input
            id="queue-number"
            label="Number"
            mono
            value={d.number}
            error={errors.number}
            hint="Optional: dial or transfer to this number to join the queue."
            onChange={(e) => set("number", e.target.value)}
          />
        </div>
        <Switch
          label="Enabled"
          hint="A disabled queue answers its number with Not Found"
          labelPosition="end"
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Ringing</legend>
          <Select
            id="queue-strategy"
            label="Strategy"
            value={d.strategy}
            error={errors.strategy}
            options={QUEUE_STRATEGIES.map((s) => ({
              value: s,
              label: QUEUE_STRATEGY_LABEL[s],
            }))}
            onChange={(e) => set("strategy", e.target.value as QueueStrategy)}
          />
          <div className="cf-two">
            {num("agentTimeout", "Ring an agent for (s)")}
            {num("retryDelay", "Retry after (s)")}
          </div>
          <div className="cf-two">
            {num("wrapUp", "Wrap-up time (s)", "Left alone after a call.")}
            {num("maxCallers", "Callers at most", "0 for no limit.")}
          </div>
          {num("maxWait", "Wait at most (s)", "0 for no limit.")}
          <Switch
            label="Callers may join with no agent logged in"
            labelPosition="end"
            checked={d.joinEmpty}
            onChange={(e) => set("joinEmpty", e.target.checked)}
          />
          <Switch
            label="Callers leave when the last agent logs out"
            labelPosition="end"
            checked={d.leaveEmpty}
            onChange={(e) => set("leaveEmpty", e.target.checked)}
          />
          <Select
            id="queue-mohClassId"
            label="Music on hold"
            value={d.mohClassId}
            error={errors.mohClassId}
            options={[
              { value: "", label: "The default music" },
              ...choices.mohClasses.map((c) => ({
                value: String(c.id),
                label: c.name,
              })),
            ]}
            onChange={(e) => set("mohClassId", e.target.value)}
          />
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Agents</legend>
          {errors.members && <p className="cf-form__error">{errors.members}</p>}
          {d.members.length === 0 && (
            <p className="cf-form__note">No agents yet.</p>
          )}
          {d.members.map((m, i) => (
            <div
              className="cf-row"
              key={String(m.extensionId)}
              role="group"
              aria-label={`Agent ${m.number ?? ""}`}
            >
              <div className="cf-row__fields">
                <span className="cf-mono">
                  {m.number} {m.name}
                </span>
                <Switch
                  label="Logs in"
                  labelPosition="end"
                  checked={m.dynamic}
                  onChange={(e) => setMember(i, { dynamic: e.target.checked })}
                />
                <Input
                  id={`queue-member-${String(i)}-penalty`}
                  label="Penalty"
                  size="sm"
                  mono
                  value={String(m.penalty)}
                  error={errors[`members[${String(i)}].penalty`]}
                  onChange={(e) =>
                    setMember(i, { penalty: Number(e.target.value) || 0 })
                  }
                />
              </div>
              <IconButton
                icon="trash-2"
                label={`Remove agent ${m.number ?? ""}`}
                onClick={() =>
                  set(
                    "members",
                    d.members.filter((_, j) => j !== i),
                  )
                }
              />
            </div>
          ))}
          <div className="cf-add">
            <Select
              id="queue-add-agent"
              label="Add an agent"
              value={pick}
              options={[
                { value: "", label: "Choose an extension…" },
                ...free.map((e) => ({
                  value: String(e.id),
                  label: e.name ? `${e.number} · ${e.name}` : e.number,
                })),
              ]}
              onChange={(e) => setPick(e.target.value)}
            />
            <Button
              variant="secondary"
              icon="plus"
              disabled={pick === ""}
              onClick={() => {
                const e = choices.all.find((x) => String(x.id) === pick);
                if (!e) return;
                set("members", [
                  ...d.members,
                  {
                    extensionId: e.id,
                    number: e.number,
                    name: e.name,
                    dynamic: false,
                    penalty: 0,
                  },
                ]);
                setPick("");
              }}
            >
              Add agent
            </Button>
          </div>
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Announcements</legend>
          {num(
            "announceFrequency",
            "Announce every (s)",
            "0 for none. Position and wait time are spoken digit by digit from announcements named prefix0 to prefix9.",
          )}
          {prompt("periodicPromptId", "Periodic announcement")}
          <Switch
            label="Announce the position"
            labelPosition="end"
            checked={d.announcePosition}
            onChange={(e) => set("announcePosition", e.target.checked)}
          />
          {prompt("positionPromptId", "“You are caller number”")}
          <Switch
            label="Announce the estimated wait"
            labelPosition="end"
            checked={d.announceWait}
            onChange={(e) => set("announceWait", e.target.checked)}
          />
          {prompt("waitPromptId", "“The estimated wait is”")}
          {prompt("minutesPromptId", "“minutes”")}
          <Input
            id="queue-digitsPrefix"
            label="Digits prefix"
            mono
            value={d.digitsPrefix}
            error={errors.digitsPrefix}
            hint="Announcements named prefix0 to prefix9."
            onChange={(e) => set("digitsPrefix", e.target.value)}
          />
          <div className="cf-two">
            <Input
              id="queue-breakoutKey"
              label="Voicemail key"
              mono
              value={d.breakoutKey}
              error={errors.breakoutKey}
              hint="Empty for none."
              onChange={(e) => set("breakoutKey", e.target.value)}
            />
            <Select
              id="queue-breakoutVoicemail"
              label="Voicemail box of"
              value={d.breakoutVoicemail}
              error={errors.breakoutVoicemail}
              options={[
                { value: "", label: "Choose…" },
                ...choices.all.map((e) => ({
                  value: e.number,
                  label: e.name ? `${e.number} · ${e.name}` : e.number,
                })),
              ]}
              onChange={(e) => set("breakoutVoicemail", e.target.value)}
            />
          </div>
          {prompt("breakoutPromptId", "“Press 1 to leave a voicemail”")}
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">When a caller cannot be served</legend>
          <DestinationPicker
            id="queue-timeout"
            label="After the maximum wait"
            value={d.timeout}
            choices={choices}
            selfName={queue?.name}
            kindError={errors.timeout}
            targetError={errors["timeout.target"]}
            onChange={(next) => set("timeout", next)}
          />
          <DestinationPicker
            id="queue-full"
            label="When the queue is full"
            value={d.full}
            choices={choices}
            selfName={queue?.name}
            kindError={errors.full}
            targetError={errors["full.target"]}
            onChange={(next) => set("full", next)}
          />
          <DestinationPicker
            id="queue-noAgents"
            label="When no agent is logged in"
            value={d.noAgents}
            choices={choices}
            selfName={queue?.name}
            kindError={errors.noAgents}
            targetError={errors["noAgents.target"]}
            onChange={(next) => set("noAgents", next)}
          />
        </fieldset>

        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
