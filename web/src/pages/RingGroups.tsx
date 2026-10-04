import { useEffect, useState, type FormEvent } from "react";
import {
  createRingGroup,
  deleteRingGroup,
  errorMessage,
  fieldErrors,
  listExtensions,
  listRingGroups,
  updateRingGroup,
  RING_STRATEGIES,
  RING_STRATEGY_LABEL,
  type Extension,
  type FieldError,
  type Id,
  type RingGroup,
  type RingGroupFields,
  type RingStrategy,
  type FailureKind,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";
import {
  Field,
  fieldId,
  FormError,
  mapFieldErrors,
  type ErrorMap,
} from "../forms";
import { EXTENSION_NUMBER_PATTERN } from "../api";

type ListState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: T[] };

/** The extension's number, or `#id` when the list has not loaded it. */
function extLabel(extensionId: Id, extensions: readonly Extension[]): string {
  const ext = extensions.find((e) => String(e.id) === String(extensionId));
  return ext ? ext.number : `#${String(extensionId)}`;
}

/**
 * Ring and hunt groups: create, edit and delete groups with their strategy,
 * members (with per-member weight and delay) and failure destination.
 */
export function RingGroups() {
  const [list, setList] = useState<ListState<RingGroup>>({ status: "loading" });
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [extError, setExtError] = useState<string | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; group: RingGroup } | null
  >(null);

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
      .then(setExtensions)
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
    setActionError(null);
    try {
      await deleteRingGroup(group.id);
      replaceItems((items) => items.filter((g) => g.id !== group.id));
    } catch (err: unknown) {
      setActionError(`Could not delete ${group.name}: ${errorMessage(err)}`);
    }
  }

  const extensionLabel = (extensionId: Id) => {
    const ext = extensions.find((e) => String(e.id) === String(extensionId));
    return ext ? ext.number : `#${String(extensionId)}`;
  };

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Ring Groups</h1>
      {editing ? (
        <RingGroupForm
          key={editing.kind === "edit" ? String(editing.group.id) : "new"}
          group={editing.kind === "edit" ? editing.group : null}
          extensions={extensions}
          extReady={extensions.length > 0}
          onCancel={() => setEditing(null)}
          onSaved={(saved) => {
            replaceItems((items) =>
              items.some((g) => g.id === saved.id)
                ? items.map((g) => (g.id === saved.id ? saved : g))
                : [...items, saved],
            );
            setEditing(null);
          }}
        />
      ) : (
        <p>
          <button
            type="button"
            className="primary"
            onClick={() => setEditing({ kind: "new" })}
          >
            New ring group
          </button>
        </p>
      )}

      <h2 id="groups-list">All groups</h2>
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      {list.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading groups…
        </p>
      )}
      {list.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load the groups.</strong>
          <p>{list.message}</p>
        </div>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <p className="muted">No ring groups yet.</p>
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <div className="table-wrap">
          <table aria-labelledby="groups-list">
            <thead>
              <tr>
                <th scope="col">Name</th>
                <th scope="col">Strategy</th>
                <th scope="col">Hunt</th>
                <th scope="col">Members</th>
                <th scope="col">Timeout</th>
                <th scope="col">Ignore DND</th>
                <th scope="col">Failure destination</th>
                <th scope="col">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {list.items.map((group) => (
                <tr key={String(group.id)}>
                  <th scope="row">{group.name}</th>
                  <td>{RING_STRATEGY_LABEL[group.strategy]}</td>
                  <td>{group.hunt ? "Yes" : "No"}</td>
                  <td>
                    {group.members
                      .slice()
                      .sort((a, b) => a.position - b.position)
                      .map((m) => extensionLabel(m.extensionId))
                      .join(", ")}
                  </td>
                  <td>{group.ringTimeout}s</td>
                  <td>{group.ignoreDnd ? "Yes" : "No"}</td>
                  <td>
                    {group.failureKind === "none"
                      ? "Hang up"
                      : `${group.failureKind}: ${group.failureTarget}`}
                  </td>
                  <td className="row-actions">
                    <button
                      type="button"
                      aria-label={`Edit ring group ${group.name}`}
                      onClick={() => setEditing({ kind: "edit", group })}
                    >
                      Edit
                    </button>
                    <ConfirmButton
                      label="Delete"
                      accessibleLabel={`Delete ring group ${group.name}`}
                      prompt={`Delete ring group ${group.name}?`}
                      confirmLabel="Delete group"
                      onConfirm={() => onDelete(group)}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {extError && (
        <p role="alert" className="error">
          Could not load the extension list: {extError} The member picker needs
          it.
        </p>
      )}
    </section>
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
  if (d.failureKind === "voicemail" && d.failureTarget.trim() === "") {
    e.failureTarget = "Enter the extension whose box takes the call.";
  }
  if (
    d.failureKind === "voicemail" &&
    d.failureTarget.trim() !== "" &&
    !EXTENSION_NUMBER_PATTERN.test(d.failureTarget.trim())
  ) {
    e.failureTarget = "Use an extension number (2 to 10 digits).";
  }
  return e;
}

/** The create/edit form for one group. */
function RingGroupForm({
  group,
  extensions,
  extReady,
  onCancel,
  onSaved,
}: {
  group: RingGroup | null;
  extensions: Extension[];
  extReady: boolean;
  onCancel: () => void;
  onSaved: (g: RingGroup) => void;
}) {
  const form = "group";
  const [d, setD] = useState(() => groupDraft(group));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [pick, setPick] = useState("");
  const set = <K extends keyof GroupDraft>(k: K, v: GroupDraft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));
  const id = (k: string) => fieldId(form, k);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
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

  const available = extensions.filter(
    (e) => !d.members.some((m) => String(m.extensionId) === String(e.id)),
  );

  return (
    <form
      className="inline-form"
      aria-labelledby="group-form-title"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="group-form-title">
        {group ? `Edit ring group ${group.name}` : "New ring group"}
      </h2>
      <div className="fields">
        <Field id={id("name")} label="Name" error={errors.name}>
          {(p) => (
            <input
              {...p}
              value={d.name}
              onChange={(e) => set("name", e.target.value)}
            />
          )}
        </Field>
        <Field id={id("strategy")} label="Strategy" error={errors.strategy}>
          {(p) => (
            <select
              {...p}
              value={d.strategy}
              onChange={(e) => set("strategy", e.target.value as RingStrategy)}
            >
              {RING_STRATEGIES.map((s) => (
                <option key={s} value={s}>
                  {RING_STRATEGY_LABEL[s]}
                </option>
              ))}
            </select>
          )}
        </Field>
        <Field
          id={id("ringTimeout")}
          label="Ring timeout (seconds)"
          error={errors.ringTimeout}
          hint="How long the group rings before the failure destination."
        >
          {(p) => (
            <input
              {...p}
              className="narrow"
              inputMode="numeric"
              value={d.ringTimeout}
              onChange={(e) => set("ringTimeout", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("memberDelay")}
          label="Member delay (seconds)"
          error={errors.memberDelay}
          hint="Pause before starting the next member."
        >
          {(p) => (
            <input
              {...p}
              className="narrow"
              inputMode="numeric"
              value={d.memberDelay}
              onChange={(e) => set("memberDelay", e.target.value)}
            />
          )}
        </Field>
        <div className="field checkbox">
          <input
            id={id("hunt")}
            type="checkbox"
            checked={d.hunt}
            onChange={(e) => set("hunt", e.target.checked)}
          />
          <label htmlFor={id("hunt")}>Hunt (one member at a time)</label>
        </div>
        <div className="field checkbox">
          <input
            id={id("ignoreDnd")}
            type="checkbox"
            checked={d.ignoreDnd}
            onChange={(e) => set("ignoreDnd", e.target.checked)}
          />
          <label htmlFor={id("ignoreDnd")}>Ignore DND</label>
        </div>
      </div>

      <fieldset
        className="group"
        aria-describedby={errors.members ? `${id("members")}-error` : undefined}
      >
        <legend>Members, in ring order</legend>
        {errors.members && (
          <p id={`${id("members")}-error`} className="field-error">
            {errors.members}
          </p>
        )}
        {d.members.length === 0 && <p className="hint">No members chosen.</p>}
        <ol className="picked">
          {d.members.map((m, i) => {
            const label = extLabel(m.extensionId, extensions);
            return (
              <li key={String(m.extensionId)}>
                <span>{label}</span>
                <button
                  type="button"
                  aria-label={`Ring ${label} earlier`}
                  disabled={i === 0}
                  onClick={() => swap(i, i - 1)}
                >
                  <span aria-hidden="true">↑</span> Up
                </button>
                <button
                  type="button"
                  aria-label={`Ring ${label} later`}
                  disabled={i === d.members.length - 1}
                  onClick={() => swap(i, i + 1)}
                >
                  <span aria-hidden="true">↓</span> Down
                </button>
                <button
                  type="button"
                  aria-label={`Remove ${label}`}
                  onClick={() =>
                    set(
                      "members",
                      d.members.filter((_, j) => j !== i),
                    )
                  }
                >
                  Remove
                </button>
                <label
                  className="visually-hidden"
                  htmlFor={id(`members[${i}].weight`)}
                >
                  Weight for {label}
                </label>
                <input
                  id={id(`members[${i}].weight`)}
                  className="narrow"
                  inputMode="numeric"
                  value={m.weight}
                  aria-invalid={
                    errors[`members[${i}].weight`] ? true : undefined
                  }
                  aria-describedby={
                    errors[`members[${i}].weight`]
                      ? `${id(`members[${i}].weight`)}-error`
                      : undefined
                  }
                  onChange={(e) =>
                    set(
                      "members",
                      d.members.map((x, j) =>
                        j === i ? { ...x, weight: e.target.value } : x,
                      ),
                    )
                  }
                />
                {errors[`members[${i}].weight`] && (
                  <p
                    id={`${id(`members[${i}].weight`)}-error`}
                    className="field-error"
                  >
                    {errors[`members[${i}].weight`]}
                  </p>
                )}
                <label
                  className="visually-hidden"
                  htmlFor={id(`members[${i}].delay`)}
                >
                  Delay for {label}
                </label>
                <input
                  id={id(`members[${i}].delay`)}
                  className="narrow"
                  inputMode="numeric"
                  value={m.delay}
                  aria-invalid={
                    errors[`members[${i}].delay`] ? true : undefined
                  }
                  aria-describedby={
                    errors[`members[${i}].delay`]
                      ? `${id(`members[${i}].delay`)}-error`
                      : undefined
                  }
                  onChange={(e) =>
                    set(
                      "members",
                      d.members.map((x, j) =>
                        j === i ? { ...x, delay: e.target.value } : x,
                      ),
                    )
                  }
                />
                {errors[`members[${i}].delay`] && (
                  <p
                    id={`${id(`members[${i}].delay`)}-error`}
                    className="field-error"
                  >
                    {errors[`members[${i}].delay`]}
                  </p>
                )}
              </li>
            );
          })}
        </ol>
        <div className="fields">
          <div className="field">
            <label htmlFor={id("member-pick")}>Add member</label>
            <select
              id={id("member-pick")}
              value={pick}
              disabled={!extReady}
              onChange={(e) => setPick(e.target.value)}
            >
              <option value="">Choose…</option>
              {available.map((e) => (
                <option key={String(e.id)} value={String(e.id)}>
                  {e.number} — {e.name}
                </option>
              ))}
            </select>
          </div>
          <button
            type="button"
            className="align-end"
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
          </button>
        </div>
      </fieldset>

      <fieldset className="group">
        <legend>Failure destination</legend>
        <div className="fields">
          <Field
            id={id("failureKind")}
            label="When no one answers"
            error={errors.failureKind}
          >
            {(p) => (
              <select
                {...p}
                value={d.failureKind}
                onChange={(e) =>
                  set("failureKind", e.target.value as FailureKind)
                }
              >
                <option value="none">Hang up</option>
                <option value="voicemail">Voicemail</option>
                <option value="external">External number</option>
              </select>
            )}
          </Field>
          {d.failureKind !== "none" && (
            <Field
              id={id("failureTarget")}
              label={
                d.failureKind === "voicemail"
                  ? "Box extension"
                  : "External number"
              }
              error={errors.failureTarget}
              hint={
                d.failureKind === "voicemail"
                  ? "The extension whose box takes the call."
                  : "2 to 20 digits, optionally starting with +."
              }
            >
              {(p) => (
                <input
                  {...p}
                  value={d.failureTarget}
                  onChange={(e) => set("failureTarget", e.target.value)}
                />
              )}
            </Field>
          )}
        </div>
      </fieldset>

      <FormError message={formError} unmatched={unmatched} />
      <div className="actions start">
        <button type="submit" className="primary" disabled={busy}>
          {group ? "Save group" : "Create group"}
        </button>
        <button type="button" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}
