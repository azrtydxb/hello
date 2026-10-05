import { useEffect, useState, type FormEvent } from "react";
import {
  createRingGroup,
  deleteRingGroup,
  errorMessage,
  fieldErrors,
  listExtensions,
  listRingGroups,
  updateRingGroup,
  EXTENSION_NUMBER_PATTERN,
  RING_STRATEGIES,
  RING_STRATEGY_LABEL,
  SIP_USERNAME_PATTERN,
  type Extension,
  type FailureKind,
  type FieldError,
  type Id,
  type RingGroup,
  type RingGroupFields,
  type RingStrategy,
} from "../api";
import {
  Alert,
  Avatar,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Icon,
  IconButton,
  Input,
  PageHeader,
  Select,
  Spinner,
  Switch,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { UNKNOWN } from "./callflow/format";
import { FormAlert } from "./callflow/ui";

const FORM_ID = "group-form";

type ListState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: T[] };

const findExt = (extensionId: Id, extensions: readonly Extension[]) =>
  extensions.find((e) => String(e.id) === String(extensionId));

/** The extension's number, or `#id` when the list has not loaded it. */
function extLabel(extensionId: Id, extensions: readonly Extension[]): string {
  return findExt(extensionId, extensions)?.number ?? `#${String(extensionId)}`;
}

/** "Rings 25 s · respects DND", "Hunt · 5 s between members · rings 60 s". */
function groupMeta(g: RingGroup): string {
  return [
    g.hunt ? "Hunt" : null,
    g.memberDelay > 0 ? `${g.memberDelay} s between members` : null,
    `${g.hunt ? "rings" : "Rings"} ${g.ringTimeout} s`,
    g.ignoreDnd ? "ignores DND" : "respects DND",
  ]
    .filter(Boolean)
    .join(" · ");
}

/** What a member's row says on the right: its weight or its ring time. */
function memberExtra(g: RingGroup, m: RingGroup["members"][number]): string {
  if (g.strategy === "weighted") return `weight ${m.weight}`;
  if (m.delay > 0) return `${m.delay} s`;
  return UNKNOWN;
}

function failureText(g: RingGroup): string {
  switch (g.failureKind) {
    case "voicemail":
      return `Voicemail box ${g.failureTarget || UNKNOWN}`;
    case "announcement":
      return `Announcement ${g.failureTarget || UNKNOWN}`;
    case "external":
      return `External ${g.failureTarget || UNKNOWN}`;
    default:
      return "Hang up";
  }
}

/**
 * Ring and hunt groups as cards: strategy, members in ring order and the
 * failure destination; created and edited in a drawer.
 */
export function RingGroups() {
  const [list, setList] = useState<ListState<RingGroup>>({ status: "loading" });
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [extReady, setExtReady] = useState(false);
  const [extError, setExtError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<RingGroup | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; group: RingGroup } | null
  >(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listRingGroups(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then((items) => {
        setExtensions(items);
        setExtReady(true);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setExtError(errorMessage(err));
        }
      });
    return () => controller.abort();
  }, []);

  function replaceItems(fn: (items: RingGroup[]) => RingGroup[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );
  }

  async function onDelete(group: RingGroup) {
    try {
      await deleteRingGroup(group.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${group.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replaceItems((items) => items.filter((g) => g.id !== group.id));
    setDeleting(null);
    toast.show(`Ring group ${group.name} deleted.`);
  }

  const newGroup = (
    <Button icon="plus" onClick={() => setEditing({ kind: "new" })}>
      New ring group
    </Button>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Ring groups"
        description="Ring several extensions for one call, then fall through to a failure destination."
        actions={newGroup}
      />
      <div className="cf-stack">
        {extError && (
          <Alert tone="warn" title="Could not load the extension list.">
            {extError} Members show by id, and the member picker needs it.
          </Alert>
        )}
        {list.status === "loading" && <Spinner label="Loading ring groups…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load the groups.">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.items.length === 0 && (
          <EmptyState
            icon="users-round"
            title="No ring groups yet."
            description="A ring group rings several extensions for one number, all at once or in turn."
            action={newGroup}
          />
        )}
        {list.status === "ready" && list.items.length > 0 && (
          <ul
            className="cf-grid cf-grid--groups cf-plain"
            aria-label="Ring groups"
          >
            {list.items.map((g) => (
              <GroupCard
                key={String(g.id)}
                group={g}
                extensions={extensions}
                onEdit={() => setEditing({ kind: "edit", group: g })}
                onDelete={() => setDeleting(g)}
              />
            ))}
          </ul>
        )}
      </div>

      {editing && (
        <RingGroupDrawer
          key={editing.kind === "edit" ? String(editing.group.id) : "new"}
          group={editing.kind === "edit" ? editing.group : null}
          extensions={extensions}
          extReady={extReady}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            replaceItems((items) =>
              items.some((g) => g.id === saved.id)
                ? items.map((g) => (g.id === saved.id ? saved : g))
                : [...items, saved],
            );
            setEditing(null);
            toast.show(
              created
                ? `Ring group ${saved.name} created.`
                : `Ring group ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ring group ${deleting.name}?`}
          description="Routes and forwards that send calls to it stop working until they are changed."
          confirmLabel="Delete group"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

function GroupCard({
  group: g,
  extensions,
  onEdit,
  onDelete,
}: {
  group: RingGroup;
  extensions: readonly Extension[];
  onEdit: () => void;
  onDelete: () => void;
}) {
  const titleId = `group-${String(g.id)}-name`;
  const members = g.members.slice().sort((a, b) => a.position - b.position);
  return (
    <li className="az-card cf-card" aria-labelledby={titleId}>
      <div className="cf-card__head cf-card__head--top">
        <div style={{ minWidth: 0 }}>
          <h2 className="cf-card__group-name" id={titleId}>
            {g.name}
          </h2>
          <div className="cf-card__sub">{groupMeta(g)}</div>
        </div>
        <Badge tone="pillar">
          {RING_STRATEGY_LABEL[g.strategy] ?? g.strategy}
        </Badge>
      </div>
      {members.length === 0 ? (
        <p className="cf-form__note">No members.</p>
      ) : (
        <ol
          className="cf-members"
          aria-label={`Members of ${g.name}, in ring order`}
        >
          {members.map((m, i) => {
            const ext = findExt(m.extensionId, extensions);
            return (
              <li className="cf-member" key={String(m.extensionId)}>
                <span className="cf-member__pos" aria-hidden="true">
                  {i + 1}
                </span>
                <span className="cf-member__who">
                  <Avatar name={ext?.name || ext?.number || "?"} size={26} />
                  <span>
                    <span className="cf-mono">
                      {ext?.number ?? `#${String(m.extensionId)}`}
                    </span>{" "}
                    {ext?.name ?? ""}
                  </span>
                </span>
                <span className="cf-member__extra">{memberExtra(g, m)}</span>
              </li>
            );
          })}
        </ol>
      )}
      <div className="cf-failure">
        <Icon name="corner-down-right" size={14} />
        If nobody answers: <strong>{failureText(g)}</strong>
      </div>
      <div className="cf-card__foot">
        <Button
          variant="secondary"
          size="sm"
          icon="pencil"
          aria-label={`Edit ring group ${g.name}`}
          onClick={onEdit}
        >
          Edit
        </Button>
        <Button
          variant="ghost"
          size="sm"
          icon="trash-2"
          className="cf-danger"
          aria-label={`Delete ring group ${g.name}`}
          onClick={onDelete}
        >
          Delete
        </Button>
      </div>
    </li>
  );
}

/** A member as the form edits it: numbers stay strings while typing. */
interface MemberDraft {
  extensionId: Id;
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
  if (d.failureKind === "voicemail") {
    if (d.failureTarget.trim() === "") {
      e.failureTarget = "Enter the extension whose box takes the call.";
    } else if (!EXTENSION_NUMBER_PATTERN.test(d.failureTarget.trim())) {
      e.failureTarget = "Use an extension number (2 to 10 digits).";
    }
  }
  if (d.failureKind === "announcement") {
    if (d.failureTarget.trim() === "") {
      e.failureTarget = "Enter the announcement's name.";
    } else if (!SIP_USERNAME_PATTERN.test(d.failureTarget.trim())) {
      e.failureTarget =
        "Use the announcement's name (1-64 of A-Z a-z 0-9 . _ -).";
    }
  }
  return e;
}

/** The create/edit drawer for one group. */
function RingGroupDrawer({
  group,
  extensions,
  extReady,
  onClose,
  onSaved,
}: {
  group: RingGroup | null;
  extensions: Extension[];
  extReady: boolean;
  onClose: () => void;
  onSaved: (g: RingGroup, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => groupDraft(group));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [pick, setPick] = useState("");
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
          <Button type="submit" form={FORM_ID} disabled={busy}>
            {group ? "Save group" : "Create group"}
          </Button>
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
              const label = extLabel(m.extensionId, extensions);
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
                { value: "", label: extReady ? "Choose…" : "Loading…" },
                ...available.map((e) => ({
                  value: String(e.id),
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
                const chosen = extensions.find((x) => String(x.id) === pick);
                if (!chosen) return;
                set("members", [
                  ...d.members,
                  { extensionId: chosen.id, weight: "1", delay: "0" },
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
              { value: "announcement", label: "Announcement" },
            ]}
            onChange={(e) => set("failureKind", e.target.value as FailureKind)}
          />
          {d.failureKind !== "none" && (
            <Input
              id={fid("failureTarget")}
              label={
                d.failureKind === "voicemail"
                  ? "Box extension"
                  : d.failureKind === "announcement"
                    ? "Announcement name"
                    : "External number"
              }
              mono
              value={d.failureTarget}
              error={errors.failureTarget}
              hint={
                d.failureKind === "voicemail"
                  ? "The extension whose box takes the call."
                  : d.failureKind === "announcement"
                    ? "The uploaded announcement's name."
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
