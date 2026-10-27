import { useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type FieldError } from "../api";
import {
  createRingTag,
  deleteRingTag,
  evaluateRingRules,
  listRingRules,
  listRingTags,
  putRingRules,
  putRingTag,
  type CallerKind,
  type RingChoice,
  type RingRulePayload,
  type RingTag,
} from "../api/ringtag";
import type { DayRange } from "../api/timecond";
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
  PageHeader,
  Select,
  Spinner,
  Tabs,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { Can } from "../role";
import { moved } from "../useOrderedList";
import { FormAlert } from "./callflow/ui";
import { RingMap } from "./ringtags/RingMap";
import { HHMM, HHMM_END, WEEK, useLoadedList } from "./timecond/shared";

type Tab = "tags" | "rules" | "map" | "test";

/** The longest rule list the API takes. */
const MAX_RULES = 100;
const MAX_CUSTOM_TAGS = 32;
const TAG_NAME = /^[a-z0-9-]{1,32}$/;

/**
 * Ring tags: the tag a call rings an internal phone with (internal, external,
 * ring group, page, door, alarm, emergency callback and your own), and the
 * ordered rules that pick one. Hello sends the tag to the phone as an
 * Alert-Info header, and the ring map (provisioned into the phone) maps it to
 * a tone.
 */
export function RingTags() {
  const [tab, setTab] = useState<Tab>("tags");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Ringing"
        description="Tag calls so phones ring differently for a door, a customer or a colleague. A call's kind (page, alarm, door) comes first, then these rules in order, then the ring group's tag, then external for trunk calls and internal for the rest."
      />
      <Tabs<Tab>
        aria-label="Ringing: tags, rules, ring map and test"
        className="cf-tabs"
        idPrefix="rt"
        value={tab}
        onChange={setTab}
        items={[
          { id: "tags", label: "Tags" },
          { id: "rules", label: "Rules" },
          { id: "map", label: "Ring map" },
          { id: "test", label: "Test a call" },
        ]}
      />
      <div
        role="tabpanel"
        id={`rt-panel-${tab}`}
        aria-labelledby={`rt-tab-${tab}`}
      >
        {tab === "tags" && <Tags />}
        {tab === "rules" && <Rules />}
        {tab === "map" && <RingMap />}
        {tab === "test" && <TestCall />}
      </div>
    </section>
  );
}

// --- tags ------------------------------------------------------------------

function Tags() {
  const [list, replace] = useLoadedList(listRingTags);
  const [deleting, setDeleting] = useState<RingTag | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; item: RingTag } | null
  >(null);
  const toast = useToast();
  const custom =
    list.status === "ready" ? list.items.filter((t) => !t.builtin).length : 0;

  async function onDelete(t: RingTag) {
    try {
      await deleteRingTag(t.name);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${t.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((items) => items.filter((x) => x.name !== t.name));
    setDeleting(null);
    toast.show(`Ring tag ${t.name} deleted.`);
  }

  const add = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready" || custom >= MAX_CUSTOM_TAGS}
        onClick={() => setEditing({ kind: "new" })}
      >
        New ring tag
      </Button>
    </Can>
  );

  return (
    <div className="cf-stack">
      <div>{add}</div>
      {list.status === "loading" && <Spinner label="Loading ring tags…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the ring tags">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && (
        <ul className="cf-grid cf-grid--groups cf-plain" aria-label="Ring tags">
          {list.items.map((t) => (
            <li
              className="az-card cf-card"
              key={t.name}
              aria-label={`Ring tag ${t.name}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <div style={{ minWidth: 0 }}>
                  <h2 className="cf-card__group-name">{t.name}</h2>
                  <div className="cf-card__sub">
                    Bellcore-dr{t.bellcoreSlot}
                    {t.usedBy > 0 &&
                      ` · used by ${t.usedBy} ${t.usedBy === 1 ? "rule or group" : "rules and groups"}`}
                  </div>
                </div>
                {t.builtin && <Badge tone="neutral">Built in</Badge>}
              </div>
              {t.description && (
                <div className="cf-failure">{t.description}</div>
              )}
              <div className="cf-card__foot">
                <Button
                  variant="secondary"
                  size="sm"
                  icon="pencil"
                  aria-label={`Edit ring tag ${t.name}`}
                  onClick={() => setEditing({ kind: "edit", item: t })}
                >
                  Edit
                </Button>
                {!t.builtin && (
                  <Can>
                    <Button
                      variant="ghost"
                      size="sm"
                      icon="trash-2"
                      className="cf-danger"
                      aria-label={`Delete ring tag ${t.name}`}
                      onClick={() => setDeleting(t)}
                    >
                      Delete
                    </Button>
                  </Can>
                )}
              </div>
            </li>
          ))}
        </ul>
      )}
      {editing && (
        <TagDrawer
          key={editing.kind === "edit" ? editing.item.name : "new"}
          item={editing.kind === "edit" ? editing.item : null}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            replace((items) =>
              items.some((x) => x.name === saved.name)
                ? items.map((x) => (x.name === saved.name ? saved : x))
                : [...items, saved],
            );
            setEditing(null);
            toast.show(
              created
                ? `Ring tag ${saved.name} created.`
                : `Ring tag ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ring tag ${deleting.name}?`}
          description="A tag that a rule or a ring group uses cannot be deleted."
          confirmLabel="Delete ring tag"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

const TAG_FORM_ID = "ring-tag-form";
const TAG_KEYS = ["name", "bellcoreSlot", "description"];
const SLOTS = [1, 2, 3, 4, 5].map((n) => ({
  value: String(n),
  label: `Bellcore-dr${n}`,
}));

function TagDrawer({
  item,
  onClose,
  onSaved,
}: {
  item: RingTag | null;
  onClose: () => void;
  onSaved: (t: RingTag, created: boolean) => void;
}) {
  useRestoreFocus();
  const [name, setName] = useState(item?.name ?? "");
  const [slot, setSlot] = useState(String(item?.bellcoreSlot ?? 1));
  const [description, setDescription] = useState(item?.description ?? "");
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = {};
    if (!TAG_NAME.test(name)) {
      found.name = "Use 1 to 32 lowercase letters, digits or dashes.";
    }
    if (description.length > 200) {
      found.description = "Use at most 200 characters.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    const body = { bellcoreSlot: Number(slot), description };
    try {
      onSaved(
        item
          ? await putRingTag(item.name, body)
          : await createRingTag({ name, ...body }),
        item === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), TAG_KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={item ? `Ring tag ${item.name}` : "New ring tag"}
      description="The slot is the Bellcore-drN in the Alert-Info header, for phones that map only Bellcore-dr1 to dr5."
      onClose={busy ? undefined : onClose}
      width={520}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={TAG_FORM_ID} disabled={busy}>
              {item ? "Save ring tag" : "Create ring tag"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={TAG_FORM_ID}
        className="cf-form"
        aria-label={item ? `Edit ring tag ${item.name}` : "New ring tag"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id="ring-tag-name"
          label="Name"
          mono
          autoFocus={item === null}
          disabled={item !== null}
          value={name}
          error={errors.name}
          hint="Lowercase letters, digits and dashes. It cannot change later."
          onChange={(e) => setName(e.target.value)}
        />
        <Select
          id="ring-tag-slot"
          label="Bellcore slot"
          value={slot}
          error={errors.bellcoreSlot}
          options={SLOTS}
          onChange={(e) => setSlot(e.target.value)}
        />
        <Input
          id="ring-tag-description"
          label="Description"
          value={description}
          error={errors.description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}

// --- rules -----------------------------------------------------------------

interface RuleDraft {
  /** Stable key for the row while it moves. */
  key: number;
  name: string;
  tag: string;
  callerPattern: string;
  trunk: string;
  did: string;
  callerKind: "" | CallerKind;
  windowOn: boolean;
  timeZone: string;
  ranges: DayRange[];
}

let nextKey = 1;

function toDraft(r: RingRulePayload): RuleDraft {
  return {
    key: nextKey++,
    name: r.name,
    tag: r.tag,
    callerPattern: r.match.callerPattern ?? "",
    trunk: r.match.trunk ?? "",
    did: r.match.did ?? "",
    callerKind: r.match.callerKind ?? "",
    windowOn: r.match.window !== undefined,
    timeZone: r.match.window?.timeZone ?? "UTC",
    ranges: (r.match.window?.ranges ?? []).map((x) => ({ ...x })),
  };
}

function toPayload(d: RuleDraft): RingRulePayload {
  const match: RingRulePayload["match"] = {};
  if (d.callerPattern.trim()) match.callerPattern = d.callerPattern.trim();
  if (d.trunk.trim()) match.trunk = d.trunk.trim();
  if (d.did.trim()) match.did = d.did.trim();
  if (d.callerKind) match.callerKind = d.callerKind;
  if (d.windowOn) {
    match.window = { timeZone: d.timeZone.trim(), ranges: d.ranges };
  }
  return { name: d.name.trim(), match, tag: d.tag };
}

const CALLER_KINDS = [
  { value: "", label: "Any caller" },
  { value: "extension", label: "An extension" },
  { value: "trunk", label: "A trunk" },
  { value: "door", label: "A door station" },
];

function validateRule(d: RuleDraft, i: number): Record<string, string> {
  const out: Record<string, string> = {};
  const p = (k: string) => `rules[${i}].${k}`;
  if (!d.tag) out[p("tag")] = "Choose a tag.";
  const p0 = toPayload(d).match;
  if (Object.keys(p0).length === 0) {
    out[p("match")] = "Set at least one condition.";
  }
  if (d.windowOn) {
    if (!/^[A-Za-z0-9_+/-]{1,64}$/.test(d.timeZone)) {
      out[p("timeZone")] = "Use an IANA time zone such as Asia/Dubai.";
    }
    if (d.ranges.length === 0) out[p("ranges")] = "Add at least one range.";
    d.ranges.forEach((r, j) => {
      if (!HHMM.test(r.start)) out[p(`ranges[${j}].start`)] = "Use HH:MM.";
      if (!HHMM_END.test(r.end)) out[p(`ranges[${j}].end`)] = "Use HH:MM.";
      if (r.start === r.end) {
        out[p(`ranges[${j}].end`)] = "Must differ from the start.";
      }
    });
  }
  return out;
}

function Rules() {
  const [list] = useLoadedList(listRingRules);
  if (list.status === "loading") return <Spinner label="Loading rules…" />;
  if (list.status === "error") {
    return (
      <Alert tone="bad" title="Could not load the rules">
        {list.message}
      </Alert>
    );
  }
  return <RuleEditor initial={list.items.map(toDraft)} />;
}

function RuleEditor({ initial }: { initial: RuleDraft[] }) {
  const [rules, setRules] = useState<RuleDraft[]>(initial);
  const [tags] = useLoadedList(listRingTags);
  const [busy, setBusy] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const toast = useToast();
  const tagOptions =
    tags.status === "ready"
      ? [
          { value: "", label: "Choose a tag" },
          ...tags.items.map((t) => ({ value: t.name, label: t.name })),
        ]
      : [{ value: "", label: "Loading…" }];

  const edit = (next: RuleDraft[]) => {
    setRules(next);
    setDirty(true);
  };
  const patch = (key: number, p: Partial<RuleDraft>) =>
    edit(rules.map((r) => (r.key === key ? { ...r, ...p } : r)));

  async function onSave() {
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = {};
    rules.forEach((r, i) => Object.assign(found, validateRule(r, i)));
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      const saved = await putRingRules(rules.map(toPayload));
      setRules(saved.map(toDraft));
      setDirty(false);
      toast.show("Ring tag rules saved.");
    } catch (err: unknown) {
      const keys = rules.flatMap((_, i) => [
        `rules[${i}]`,
        `rules[${i}].tag`,
        `rules[${i}].name`,
        `rules[${i}].match`,
      ]);
      const mapped = mapFieldErrors(fieldErrors(err), ["rules", ...keys]);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
    }
    setBusy(false);
  }

  return (
    <div className="cf-stack">
      <div className="cf-add">
        <Can>
          <Button
            icon="plus"
            disabled={rules.length >= MAX_RULES}
            onClick={() =>
              edit([...rules, toDraft({ name: "", tag: "", match: {} })])
            }
          >
            Add rule
          </Button>
          <Button disabled={busy || !dirty} onClick={() => void onSave()}>
            Save rules
          </Button>
        </Can>
      </div>
      <FormAlert message={formError} unmatched={unmatched} />
      {errors.rules && <p className="cf-form__error">{errors.rules}</p>}
      {rules.length === 0 && (
        <EmptyState
          icon="bell-electric"
          title="No rules yet"
          description="Without rules a call gets its ring group's tag, external when it came in on a trunk, and internal otherwise."
        />
      )}
      <ol className="cf-rows" aria-label="Ring tag rules">
        {rules.map((r, i) => (
          <li
            className="az-card cf-card"
            key={r.key}
            aria-label={`Rule ${i + 1}`}
          >
            <div className="cf-card__head cf-card__head--top">
              <strong>Rule {i + 1}</strong>
              <Can>
                <div className="cf-add">
                  <IconButton
                    icon="arrow-up"
                    label={`Move rule ${i + 1} up`}
                    disabled={i === 0}
                    onClick={() => edit(moved(rules, i, -1))}
                  />
                  <IconButton
                    icon="arrow-down"
                    label={`Move rule ${i + 1} down`}
                    disabled={i === rules.length - 1}
                    onClick={() => edit(moved(rules, i, 1))}
                  />
                  <IconButton
                    icon="trash-2"
                    label={`Remove rule ${i + 1}`}
                    onClick={() => edit(rules.filter((x) => x.key !== r.key))}
                  />
                </div>
              </Can>
            </div>
            <div className="cf-form">
              <div className="cf-two">
                <Input
                  id={`rule-${r.key}-name`}
                  label={`Rule ${i + 1} name`}
                  value={r.name}
                  error={errors[`rules[${i}].name`]}
                  onChange={(e) => patch(r.key, { name: e.target.value })}
                />
                <Select
                  id={`rule-${r.key}-tag`}
                  label={`Rule ${i + 1} tag`}
                  value={r.tag}
                  error={errors[`rules[${i}].tag`]}
                  options={
                    r.tag !== "" && !tagOptions.some((o) => o.value === r.tag)
                      ? [...tagOptions, { value: r.tag, label: r.tag }]
                      : tagOptions
                  }
                  onChange={(e) => patch(r.key, { tag: e.target.value })}
                />
              </div>
              {errors[`rules[${i}].match`] && (
                <p className="cf-form__error">{errors[`rules[${i}].match`]}</p>
              )}
              <div className="cf-two">
                <Input
                  id={`rule-${r.key}-caller`}
                  label={`Rule ${i + 1} caller number pattern`}
                  mono
                  value={r.callerPattern}
                  hint="An exact number, an X pattern such as 971XXXXXXXXX, or a regex."
                  error={errors[`rules[${i}].match.callerPattern`]}
                  onChange={(e) =>
                    patch(r.key, { callerPattern: e.target.value })
                  }
                />
                <Input
                  id={`rule-${r.key}-did`}
                  label={`Rule ${i + 1} dialled number pattern`}
                  mono
                  value={r.did}
                  error={errors[`rules[${i}].match.did`]}
                  onChange={(e) => patch(r.key, { did: e.target.value })}
                />
              </div>
              <div className="cf-two">
                <Input
                  id={`rule-${r.key}-trunk`}
                  label={`Rule ${i + 1} inbound trunk`}
                  value={r.trunk}
                  error={errors[`rules[${i}].match.trunk`]}
                  onChange={(e) => patch(r.key, { trunk: e.target.value })}
                />
                <Select
                  id={`rule-${r.key}-kind`}
                  label={`Rule ${i + 1} caller kind`}
                  value={r.callerKind}
                  error={errors[`rules[${i}].match.callerKind`]}
                  options={CALLER_KINDS}
                  onChange={(e) =>
                    patch(r.key, {
                      callerKind: e.target.value as "" | CallerKind,
                    })
                  }
                />
              </div>
              <WindowEditor
                draft={r}
                index={i}
                errors={errors}
                onChange={(p) => patch(r.key, p)}
              />
            </div>
          </li>
        ))}
      </ol>
      {toast.node}
    </div>
  );
}

function WindowEditor({
  draft: r,
  index: i,
  errors,
  onChange,
}: {
  draft: RuleDraft;
  index: number;
  errors: ErrorMap;
  onChange: (p: Partial<RuleDraft>) => void;
}) {
  const label = `Rule ${i + 1}`;
  const setRange = (j: number, next: Partial<DayRange>) =>
    onChange({
      ranges: r.ranges.map((x, k) => (k === j ? { ...x, ...next } : x)),
    });
  if (!r.windowOn) {
    return (
      <div>
        <Button
          variant="ghost"
          size="sm"
          icon="clock"
          aria-label={`Limit ${label} to a time window`}
          onClick={() =>
            onChange({
              windowOn: true,
              ranges: [{ day: 1, start: "09:00", end: "17:00" }],
            })
          }
        >
          Limit to a time window
        </Button>
      </div>
    );
  }
  return (
    <fieldset className="cf-form__section">
      <legend className="az-eyebrow">{label} time window</legend>
      <Input
        id={`rule-${r.key}-zone`}
        label={`${label} time zone`}
        mono
        value={r.timeZone}
        error={errors[`rules[${i}].match.window.timeZone`]}
        onChange={(e) => onChange({ timeZone: e.target.value })}
      />
      {errors[`rules[${i}].ranges`] && (
        <p className="cf-form__error">{errors[`rules[${i}].ranges`]}</p>
      )}
      {r.ranges.map((x, j) => (
        <div className="cf-row" key={j}>
          <Select
            id={`rule-${r.key}-range-${j}-day`}
            label={`${label} range ${j + 1} day`}
            value={String(x.day)}
            options={WEEK.map((w) => ({
              value: String(w.day),
              label: w.label,
            }))}
            onChange={(e) => setRange(j, { day: Number(e.target.value) })}
          />
          <Input
            id={`rule-${r.key}-range-${j}-start`}
            label={`${label} range ${j + 1} start`}
            mono
            value={x.start}
            error={errors[`rules[${i}].ranges[${j}].start`]}
            onChange={(e) => setRange(j, { start: e.target.value })}
          />
          <Input
            id={`rule-${r.key}-range-${j}-end`}
            label={`${label} range ${j + 1} end`}
            mono
            value={x.end}
            error={errors[`rules[${i}].ranges[${j}].end`]}
            onChange={(e) => setRange(j, { end: e.target.value })}
          />
          <IconButton
            icon="trash-2"
            label={`Remove ${label} range ${j + 1}`}
            onClick={() =>
              onChange({ ranges: r.ranges.filter((_, k) => k !== j) })
            }
          />
        </div>
      ))}
      <div className="cf-add">
        <Button
          variant="ghost"
          size="sm"
          icon="plus"
          aria-label={`Add a range to ${label}`}
          onClick={() =>
            onChange({
              ranges: [...r.ranges, { day: 1, start: "09:00", end: "17:00" }],
            })
          }
        >
          Add range
        </Button>
        <Button
          variant="ghost"
          size="sm"
          aria-label={`Remove the time window of ${label}`}
          onClick={() => onChange({ windowOn: false, ranges: [] })}
        >
          Remove time window
        </Button>
      </div>
    </fieldset>
  );
}

// --- test a call -----------------------------------------------------------

const KINDS = [
  { value: "", label: "An ordinary call" },
  { value: "page", label: "A page" },
  { value: "intercom", label: "An intercom call" },
  { value: "alarm", label: "An alarm call" },
  { value: "door", label: "A door call" },
  { value: "emergency-callback", label: "An emergency callback" },
];

function TestCall() {
  const [kind, setKind] = useState("");
  const [callerNumber, setCallerNumber] = useState("");
  const [did, setDid] = useState("");
  const [trunk, setTrunk] = useState("");
  const [fromTrunk, setFromTrunk] = useState(false);
  const [groupTag, setGroupTag] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<RingChoice | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    try {
      setResult(
        await evaluateRingRules({
          kind,
          callerNumber,
          did,
          trunk,
          fromTrunk: fromTrunk || trunk !== "" ? true : undefined,
          groupTag,
        }),
      );
    } catch (err: unknown) {
      setResult(null);
      setError(errorMessage(err));
    }
    setBusy(false);
  }

  return (
    <form
      className="cf-form"
      aria-label="Test a call"
      onSubmit={(e) => void onSubmit(e)}
    >
      <Select
        id="rt-test-kind"
        label="Kind of call"
        value={kind}
        options={KINDS}
        onChange={(e) => setKind(e.target.value)}
      />
      <div className="cf-two">
        <Input
          id="rt-test-caller"
          label="Caller number"
          mono
          value={callerNumber}
          onChange={(e) => setCallerNumber(e.target.value)}
        />
        <Input
          id="rt-test-did"
          label="Dialled number"
          mono
          value={did}
          onChange={(e) => setDid(e.target.value)}
        />
      </div>
      <div className="cf-two">
        <Input
          id="rt-test-trunk"
          label="Inbound trunk"
          value={trunk}
          hint="Leave empty for a call from a phone."
          onChange={(e) => setTrunk(e.target.value)}
        />
        <Input
          id="rt-test-group"
          label="Ring group's tag"
          mono
          value={groupTag}
          hint="Empty when the call does not come through a ring group; ringgroup for a group without its own tag."
          onChange={(e) => setGroupTag(e.target.value)}
        />
      </div>
      <Checkbox
        label="The call entered on a trunk"
        checked={fromTrunk}
        onChange={(e) => setFromTrunk(e.target.checked)}
      />
      <div>
        <Button type="submit" disabled={busy}>
          Test
        </Button>
      </div>
      {error && (
        <Alert tone="bad" title="Could not test the call">
          {error}
        </Alert>
      )}
      {result && (
        <Alert tone="info" title={`Rings with ${result.tag}`}>
          {result.why}. Alert-Info:{" "}
          <span className="cf-mono">{result.alertInfo}</span>
        </Alert>
      )}
    </form>
  );
}
