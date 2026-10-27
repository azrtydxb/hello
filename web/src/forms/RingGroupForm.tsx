import { listTimeConditions, type TimeCondition } from "../api/timecond";
import { useEffect, useState, type FormEvent } from "react";
import { QueueSelect } from "../components/QueueSelect";
import {
  createRingGroup,
  errorMessage,
  fieldErrors,
  listIvrMenus,
  listRingGroups,
  updateRingGroup,
  EXTENSION_NUMBER_PATTERN,
  RING_STRATEGIES,
  RING_STRATEGY_LABEL,
  type Extension,
  type FailureKind,
  type IvrMenu,
  type FieldError,
  type Id,
  type RingGroup,
  type RingGroupFields,
  type RingStrategy,
} from "../api";
import {
  Avatar,
  Button,
  Drawer,
  IconButton,
  Input,
  Select,
  Switch,
  useRestoreFocus,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { FormAlert } from "../pages/callflow/ui";
import { Can } from "../role";
import { MOHSelect } from "../pages/media/MohSelect";

export const FORM_ID = "group-form";

export const findExt = (
  extensionId: Id | null,
  extensions: readonly Extension[],
) =>
  extensionId === null
    ? undefined
    : extensions.find((e) => String(e.id) === String(extensionId));

/** The extension's number, or `#id` when the list has not loaded it. */
export function extLabel(
  extensionId: Id | null,
  extensions: readonly Extension[],
): string {
  return findExt(extensionId, extensions)?.number ?? `#${String(extensionId)}`;
}

/** The member extension's number, or `#id` when the list has not loaded it. */
export function memberLabel(
  m: { extensionId: Id | null },
  extensions: readonly Extension[],
): string {
  return extLabel(m.extensionId, extensions);
}

/** A member as the form edits it: numbers stay strings while typing. */
interface MemberDraft {
  extensionId: Id | null;
  weight: string;
  delay: string;
}

interface GroupDraft {
  name: string;
  strategy: RingStrategy;
  hunt: boolean;
  ringTimeout: string;
  memberDelay: string;
  ignoreDnd: boolean;
  failureKind: FailureKind;
  failureTarget: string;
  mohClassId: Id | null;
  mohRingback: boolean;
  members: MemberDraft[];
}

function groupDraft(group: RingGroup | null): GroupDraft {
  return {
    name: group?.name ?? "",
    strategy: group?.strategy ?? "ring-all",
    hunt: group?.hunt ?? false,
    ringTimeout: group ? String(group.ringTimeout) : "30",
    memberDelay: group ? String(group.memberDelay) : "0",
    ignoreDnd: group?.ignoreDnd ?? false,
    failureKind: group?.failureKind ?? "none",
    failureTarget: group?.failureTarget ?? "",
    mohClassId: group?.mohClassId ?? null,
    mohRingback: group?.mohRingback ?? false,
    members: (group?.members ?? [])
      .slice()
      .sort((a, b) => a.position - b.position)
      .map((m) => ({
        extensionId: m.extensionId,
        weight: String(m.weight),
        delay: String(m.delay),
      })),
  };
}

function groupBody(d: GroupDraft): RingGroupFields {
  return {
    name: d.name.trim(),
    strategy: d.strategy,
    hunt: d.hunt,
    ringTimeout: Number(d.ringTimeout),
    memberDelay: Number(d.memberDelay),
    ignoreDnd: d.ignoreDnd,
    failureKind: d.failureKind,
    failureTarget: d.failureKind === "none" ? "" : d.failureTarget.trim(),
    mohClassId: d.mohClassId,
    mohRingback: d.mohRingback,
    members: d.members.map((m, i) => ({
      extensionId: m.extensionId,
      position: i + 1,
      weight: Number(m.weight),
      delay: Number(m.delay),
    })),
  };
}

/** Field keys the form shows, for mapping server field errors. */
function groupKeys(d: GroupDraft): string[] {
  return [
    "name",
    "strategy",
    "hunt",
    "ringTimeout",
    "memberDelay",
    "ignoreDnd",
    "failureKind",
    "failureTarget",
    "mohClassId",
    "mohRingback",
    "members",
    ...d.members.flatMap((_, i) => [
      `members[${i}]`,
      `members[${i}].extensionId`,
      `members[${i}].position`,
      `members[${i}].weight`,
      `members[${i}].delay`,
    ]),
  ];
}

const NATURAL = /^[0-9]+$/;

function validateGroup(d: GroupDraft): Record<string, string> {
  const e: Record<string, string> = {};
  if (d.name.trim() === "") e.name = "Enter a name.";
  if (
    !NATURAL.test(d.ringTimeout) ||
    Number(d.ringTimeout) < 1 ||
    Number(d.ringTimeout) > 3600
  ) {
    e.ringTimeout = "Use a whole number of seconds, 1–3600.";
  }
  if (!NATURAL.test(d.memberDelay) || Number(d.memberDelay) > 3600) {
    e.memberDelay = "Use a whole number of seconds, 0–3600.";
  }
  if (d.members.length === 0) e.members = "Add at least one member.";
  d.members.forEach((m, i) => {
    if (!NATURAL.test(m.weight)) {
      e[`members[${i}].weight`] = "Use a whole number, 0 or more.";
    }
    if (!NATURAL.test(m.delay) || Number(m.delay) > 3600) {
      e[`members[${i}].delay`] = "Use a whole number of seconds, 0–3600.";
    }
  });
  if (d.failureKind === "external") {
    if (d.failureTarget.trim() === "") {
      e.failureTarget = "Enter the number to call.";
    } else if (!/^\+?[0-9]{2,20}$/.test(d.failureTarget.trim())) {
      e.failureTarget = "Use 2 to 20 digits, optionally starting with +.";
    }
  }
  if (d.failureKind === "ivr" && d.failureTarget.trim() === "") {
    e.failureTarget = "Choose the IVR menu.";
  }
  if (d.failureKind === "queue" && d.failureTarget.trim() === "") {
    e.failureTarget = "Choose the call queue.";
  }
  if (d.failureKind === "time_condition" && d.failureTarget.trim() === "") {
    e.failureTarget = "Choose the time condition.";
  }
  if (d.failureKind === "extension" && d.failureTarget.trim() === "") {
    e.failureTarget = "Choose the extension to ring.";
  }
  if (d.failureKind === "ring_group") {
    if (d.failureTarget.trim() === "") {
      e.failureTarget = "Choose the ring group to ring.";
    } else if (d.failureTarget.trim() === d.name.trim()) {
      e.failureTarget = "A group cannot fall back to itself.";
    }
  }
  if (d.failureKind === "voicemail") {
    if (d.failureTarget.trim() === "") {
      e.failureTarget = "Enter the extension whose box takes the call.";
    } else if (!EXTENSION_NUMBER_PATTERN.test(d.failureTarget.trim())) {
      e.failureTarget = "Use an extension number (2 to 10 digits).";
    }
  }
  return e;
}

/** A target select's options: a stored value the list lacks stays selectable. */
function choices(
  options: { value: string; label: string }[],
  current: string,
): { value: string; label: string }[] {
  return [
    { value: "", label: "Choose…" },
    ...(current !== "" && !options.some((o) => o.value === current)
      ? [{ value: current, label: `${current} (not found)` }]
      : []),
    ...options,
  ];
}

/** The create/edit drawer for one group. */
export function RingGroupDrawer({
  group,
  extensions,
  extReady,
  onClose,
  commit,
  onSaved,
}: {
  group: RingGroup | null;
  extensions: Extension[];
  extReady: boolean;
  onClose: () => void;
  /** The canvas stages the body in its draft instead of saving it here. */
  commit?: (body: object) => Promise<void>;
  onSaved: (g: RingGroup, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => groupDraft(group));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [pick, setPick] = useState("");
  const [menus, setMenus] = useState<IvrMenu[]>([]);
  const [timeConds, setTimeConds] = useState<TimeCondition[]>([]);
  const [groups, setGroups] = useState<RingGroup[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    listRingGroups(controller.signal)
      .then(setGroups)
      .catch(() => undefined); // the picker stays empty; saving still works
    listTimeConditions(controller.signal)
      .then(setTimeConds)
      .catch(() => undefined); // the picker stays empty; saving still works
    listIvrMenus(controller.signal)
      .then(setMenus)
      .catch(() => undefined); // the picker stays empty; saving still works
    return () => controller.abort();
  }, []);
  const set = <K extends keyof GroupDraft>(k: K, v: GroupDraft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));
  const fid = (k: string) => `group-${k.replace(/[^A-Za-z0-9_-]+/g, "-")}`;

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found = validateGroup(d);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      const body = groupBody(d);
      if (commit) {
        await commit(body);
        onClose();
        return;
      }
      onSaved(
        group
          ? await updateRingGroup(group.id, body)
          : await createRingGroup(body),
        group === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), groupKeys(d));
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  const swap = (i: number, j: number) => {
    const next = [...d.members];
    const a = next[i];
    const b = next[j];
    if (a === undefined || b === undefined) return;
    next[i] = b;
    next[j] = a;
    set("members", next);
  };
  const setMember = (i: number, patch: Partial<MemberDraft>) =>
    set(
      "members",
      d.members.map((x, j) => (j === i ? { ...x, ...patch } : x)),
    );

  const available = extensions.filter(
    (e) => !d.members.some((m) => String(m.extensionId) === String(e.id)),
  );

  return (
    <Drawer
      title={group ? `Ring group ${group.name}` : "New ring group"}
      description="Members ring in the order shown; the failure destination takes calls nobody answers."
      onClose={busy ? undefined : onClose}
      width={560}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {group ? "Save group" : "Create group"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={group ? `Edit ring group ${group.name}` : "New ring group"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id={fid("name")}
            label="Name"
            autoFocus
            value={d.name}
            error={errors.name}
            onChange={(e) => set("name", e.target.value)}
          />
          <Select
            id={fid("strategy")}
            label="Strategy"
            value={d.strategy}
            error={errors.strategy}
            options={RING_STRATEGIES.map((s) => ({
              value: s,
              label: RING_STRATEGY_LABEL[s],
            }))}
            onChange={(e) => set("strategy", e.target.value as RingStrategy)}
          />
        </div>
        <div className="cf-two">
          <Input
            id={fid("ringTimeout")}
            label="Ring timeout (s)"
            inputMode="numeric"
            value={d.ringTimeout}
            error={errors.ringTimeout}
            hint="How long the group rings before the failure destination."
            onChange={(e) => set("ringTimeout", e.target.value)}
          />
          <Input
            id={fid("memberDelay")}
            label="Member delay (s)"
            inputMode="numeric"
            value={d.memberDelay}
            error={errors.memberDelay}
            hint="Pause before starting the next member."
            onChange={(e) => set("memberDelay", e.target.value)}
          />
        </div>
        <div className="cf-field-group">
          <Switch
            label="Hunt"
            hint="One member at a time, moving on when one does not answer or is busy"
            labelPosition="end"
            checked={d.hunt}
            onChange={(e) => set("hunt", e.target.checked)}
          />
          <Switch
            label="Ignore DND"
            hint="Ring members even when they are on do not disturb"
            labelPosition="end"
            checked={d.ignoreDnd}
            onChange={(e) => set("ignoreDnd", e.target.checked)}
          />
        </div>
        <MOHSelect
          id={fid("mohClassId")}
          label="Music on hold"
          hint="What callers to this group hear when it holds them. Default uses the global class."
          value={d.mohClassId}
          error={errors.mohClassId}
          onChange={(v) => set("mohClassId", v)}
        />
        <Switch
          label="Play music instead of ringback"
          hint="The caller hears the class while the group rings, not a ringing tone"
          labelPosition="end"
          checked={d.mohRingback}
          onChange={(e) => set("mohRingback", e.target.checked)}
        />

        <fieldset
          className="cf-form__section"
          aria-describedby={
            errors.members ? `${fid("members")}-error` : undefined
          }
        >
          <legend className="az-eyebrow">Members, in ring order</legend>
          {errors.members && (
            <p id={`${fid("members")}-error`} className="cf-form__error">
              {errors.members}
            </p>
          )}
          {d.members.length === 0 && (
            <p className="cf-form__note">No members chosen.</p>
          )}
          <ol className="cf-rows">
            {d.members.map((m, i) => {
              const label = memberLabel(m, extensions);
              const name = findExt(m.extensionId, extensions)?.name;
              return (
                <li className="cf-row" key={String(m.extensionId)}>
                  <div className="cf-stack" style={{ gap: 8, minWidth: 0 }}>
                    <span className="cf-row__label">
                      <span className="cf-member__pos">{i + 1}</span>
                      <Avatar name={name || label} size={26} />
                      <span>
                        <span className="cf-mono">{label}</span> {name ?? ""}
                      </span>
                    </span>
                    <div className="cf-row__fields">
                      <Input
                        id={fid(`members[${i}].weight`)}
                        label={`Weight for ${label}`}
                        mono
                        size="sm"
                        inputMode="numeric"
                        value={m.weight}
                        error={errors[`members[${i}].weight`]}
                        onChange={(e) =>
                          setMember(i, { weight: e.target.value })
                        }
                      />
                      <Input
                        id={fid(`members[${i}].delay`)}
                        label={`Delay for ${label} (s)`}
                        mono
                        size="sm"
                        inputMode="numeric"
                        value={m.delay}
                        error={errors[`members[${i}].delay`]}
                        onChange={(e) =>
                          setMember(i, { delay: e.target.value })
                        }
                      />
                    </div>
                  </div>
                  <div className="cf-row__actions">
                    <IconButton
                      icon="arrow-up"
                      label={`Ring ${label} earlier`}
                      disabled={i === 0}
                      onClick={() => swap(i, i - 1)}
                    />
                    <IconButton
                      icon="arrow-down"
                      label={`Ring ${label} later`}
                      disabled={i === d.members.length - 1}
                      onClick={() => swap(i, i + 1)}
                    />
                    <IconButton
                      icon="trash-2"
                      label={`Remove ${label}`}
                      onClick={() =>
                        set(
                          "members",
                          d.members.filter((_, j) => j !== i),
                        )
                      }
                    />
                  </div>
                </li>
              );
            })}
          </ol>
          <div className="cf-add">
            <Select
              id={fid("member-pick")}
              label="Add member"
              size="sm"
              value={pick}
              disabled={!extReady}
              options={[
                {
                  value: "",
                  label: extReady ? "Choose…" : "Loading…",
                },
                ...available.map((e) => ({
                  value: `ext:${String(e.id)}`,
                  label: e.name ? `${e.number} · ${e.name}` : e.number,
                })),
              ]}
              onChange={(e) => setPick(e.target.value)}
            />
            <Button
              variant="secondary"
              size="sm"
              icon="plus"
              disabled={pick === ""}
              onClick={() => {
                const chosen = extensions.find(
                  (x) => String(x.id) === pick.slice("ext:".length),
                );
                if (!chosen) return;
                set("members", [
                  ...d.members,
                  {
                    extensionId: chosen.id,
                    weight: "1",
                    delay: "0",
                  },
                ]);
                setPick("");
              }}
            >
              Add
            </Button>
          </div>
        </fieldset>

        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">If nobody answers</legend>
          <Select
            id={fid("failureKind")}
            label="When no one answers"
            value={d.failureKind}
            error={errors.failureKind}
            options={[
              { value: "none", label: "Hang up" },
              { value: "voicemail", label: "Voicemail" },
              { value: "external", label: "External number" },
              { value: "ivr", label: "IVR menu" },
              { value: "queue", label: "Call queue" },
              { value: "time_condition", label: "Time condition" },
              { value: "extension", label: "Extension" },
              { value: "ring_group", label: "Ring group" },
            ]}
            onChange={(e) =>
              setD((prev) => ({
                ...prev,
                failureKind: e.target.value as FailureKind,
                failureTarget: "", // a target belongs to its kind
              }))
            }
          />
          {d.failureKind === "extension" && (
            <Select
              id={fid("failureTarget")}
              label="Extension"
              value={d.failureTarget}
              error={errors.failureTarget}
              hint="Rings the extension as a new call: its own forwarding and voicemail apply."
              options={choices(
                extensions.map((x) => ({
                  value: x.number,
                  label: x.name ? `${x.number} · ${x.name}` : x.number,
                })),
                d.failureTarget,
              )}
              onChange={(e) => set("failureTarget", e.target.value)}
            />
          )}
          {d.failureKind === "ring_group" && (
            <Select
              id={fid("failureTarget")}
              label="Ring group"
              value={d.failureTarget}
              error={errors.failureTarget}
              hint="Rings the other group with its own strategy and fallback."
              options={choices(
                groups
                  .filter((x) => x.name !== d.name.trim())
                  .map((x) => ({ value: x.name, label: x.name })),
                d.failureTarget,
              )}
              onChange={(e) => set("failureTarget", e.target.value)}
            />
          )}
          {d.failureKind === "ivr" && (
            <Select
              id={fid("failureTarget")}
              label="IVR menu"
              value={d.failureTarget}
              error={errors.failureTarget}
              options={[
                { value: "", label: "Choose…" },
                ...(d.failureTarget !== "" &&
                !menus.some((m) => m.name === d.failureTarget)
                  ? [
                      {
                        value: d.failureTarget,
                        label: `${d.failureTarget} (not found)`,
                      },
                    ]
                  : []),
                ...menus.map((m) => ({ value: m.name, label: m.name })),
              ]}
              onChange={(e) => set("failureTarget", e.target.value)}
            />
          )}
          {d.failureKind === "queue" && (
            <QueueSelect
              id={fid("failureTarget")}
              label="Call queue"
              value={d.failureTarget}
              error={errors.failureTarget}
              onChange={(target) => set("failureTarget", target)}
            />
          )}
          {d.failureKind === "time_condition" && (
            <Select
              id={fid("failureTarget")}
              label="Time condition"
              value={d.failureTarget}
              error={errors.failureTarget}
              options={[
                { value: "", label: "Choose…" },
                ...(d.failureTarget !== "" &&
                !timeConds.some((t) => t.name === d.failureTarget)
                  ? [
                      {
                        value: d.failureTarget,
                        label: `${d.failureTarget} (not found)`,
                      },
                    ]
                  : []),
                ...timeConds.map((t) => ({ value: t.name, label: t.name })),
              ]}
              onChange={(e) => set("failureTarget", e.target.value)}
            />
          )}
          {d.failureKind !== "none" &&
            d.failureKind !== "ivr" &&
            d.failureKind !== "queue" &&
            d.failureKind !== "time_condition" &&
            d.failureKind !== "extension" &&
            d.failureKind !== "ring_group" && (
              <Input
                id={fid("failureTarget")}
                label={
                  d.failureKind === "voicemail"
                    ? "Box extension"
                    : "External number"
                }
                mono
                value={d.failureTarget}
                error={errors.failureTarget}
                hint={
                  d.failureKind === "voicemail"
                    ? "The extension whose box takes the call."
                    : "2 to 20 digits, optionally starting with +."
                }
                onChange={(e) => set("failureTarget", e.target.value)}
              />
            )}
        </fieldset>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
