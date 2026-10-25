import { useEffect, useState, type FormEvent } from "react";
import {
  createPagingGroup,
  deletePagingGroup,
  errorMessage,
  fieldErrors,
  listExtensions,
  listPagingGroups,
  putPagingGroup,
  testPagingGroup,
  type Extension,
  type FieldError,
  type Id,
  type PagingGroup,
  type PagingGroupFields,
  type PagingPriority,
  type PageTestResult,
} from "../api";
import {
  Alert,
  Avatar,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
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
import { FormAlert } from "./callflow/ui";
import { Can } from "../role";
import { listGroups, type Group } from "../api/phonebook";

const FORM_ID = "paging-form";
const CODE_PATTERN = /^\*[0-9]{2,4}$/;
const NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: PagingGroup[] };

export function Paging() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [extReady, setExtReady] = useState(false);
  const [extError, setExtError] = useState<string | null>(null);
  const [phoneGroups, setPhoneGroups] = useState<Group[]>([]);
  const [deleting, setDeleting] = useState<PagingGroup | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; group: PagingGroup } | null
  >(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listPagingGroups(controller.signal)
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
        if (!controller.signal.aborted) setExtError(errorMessage(err));
      });
    return () => controller.abort();
  }, []);

  // The multicast channels a paging group may also page. A failure leaves
  // the picker empty: a group without a channel still works.
  useEffect(() => {
    const controller = new AbortController();
    listGroups(controller.signal)
      .then(setPhoneGroups)
      .catch(() => undefined);
    return () => controller.abort();
  }, []);

  function replaceItems(fn: (items: PagingGroup[]) => PagingGroup[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );
  }

  async function onDelete(group: PagingGroup) {
    try {
      await deletePagingGroup(group.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${group.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replaceItems((items) => items.filter((g) => g.id !== group.id));
    setDeleting(null);
    toast.show(`Paging group ${group.name} deleted.`);
  }

  const newGroup = (
    <Can min="admin">
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New paging group
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Paging"
        description="Dial a page code and every phone in the group answers by itself, on the speaker. Audio is one way. 1-to-1 intercom: dial *80 and the extension number."
        actions={newGroup}
      />
      <div className="cf-stack">
        {extError && (
          <Alert tone="warn" title="Could not load the extension list">
            {extError} The member picker needs it.
          </Alert>
        )}
        {list.status === "loading" && (
          <Spinner label="Loading paging groups…" />
        )}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load the paging groups">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.items.length === 0 && (
          <EmptyState
            icon="megaphone"
            title="No paging groups yet"
            description="A paging group calls all its members at once with auto-answer, from one code such as *60."
            action={newGroup}
          />
        )}
        {list.status === "ready" && list.items.length > 0 && (
          <ul
            className="cf-grid cf-grid--groups cf-plain"
            aria-label="Paging groups"
          >
            {list.items.map((g) => (
              <GroupCard
                key={String(g.id)}
                group={g}
                onEdit={() => setEditing({ kind: "edit", group: g })}
                onDelete={() => setDeleting(g)}
              />
            ))}
          </ul>
        )}
      </div>

      {editing && (
        <PagingDrawer
          key={editing.kind === "edit" ? String(editing.group.id) : "new"}
          group={editing.kind === "edit" ? editing.group : null}
          extensions={extensions}
          extReady={extReady}
          phoneGroups={phoneGroups}
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
                ? `Paging group ${saved.name} created.`
                : `Paging group ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete paging group ${deleting.name}?`}
          description={`The code ${deleting.code} stops paging.`}
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
  onEdit,
  onDelete,
}: {
  group: PagingGroup;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const titleId = `paging-${String(g.id)}-name`;
  const [testing, setTesting] = useState(false);
  return (
    <li className="az-card cf-card" aria-labelledby={titleId}>
      <div className="cf-card__head cf-card__head--top">
        <div style={{ minWidth: 0 }}>
          <h2 className="cf-card__group-name" id={titleId}>
            {g.name}
          </h2>
          <div className="cf-card__sub">
            Code <span className="cf-mono">{g.code}</span> ·{" "}
            {g.callers.length === 0
              ? "any internal extension may page"
              : `${g.callers.length} allowed caller${g.callers.length === 1 ? "" : "s"}`}
          </div>
        </div>
        <span style={{ display: "flex", gap: 6 }}>
          {!g.enabled && <Badge tone="warn">Disabled</Badge>}
          <Badge tone={g.priority === "emergency" ? "bad" : "pillar"}>
            {g.priority === "emergency" ? "Emergency" : "Normal"}
          </Badge>
        </span>
      </div>
      {g.multicastChannel && (
        <div className="cf-card__sub">
          Also multicast to {g.multicastChannel.groupName} ·{" "}
          {g.multicastChannel.label} (
          <span className="cf-mono">
            {g.multicastChannel.address}:{g.multicastChannel.port}
          </span>
          , {g.multicastChannel.codec})
        </div>
      )}
      <ul className="cf-members" aria-label={`Members of ${g.name}`}>
        {g.members.map((m) => (
          <li className="cf-member" key={String(m.extensionId)}>
            <span className="cf-member__who">
              <Avatar name={m.name || m.number} size={26} />
              <span>
                <span className="cf-mono">{m.number}</span> {m.name}
              </span>
            </span>
          </li>
        ))}
      </ul>
      {testing && <TestPanel group={g} />}
      <div className="cf-card__foot">
        <Can min="admin">
          <Button
            variant="secondary"
            size="sm"
            icon="megaphone"
            aria-label={`Test paging group ${g.name}`}
            aria-expanded={testing}
            onClick={() => setTesting((t) => !t)}
          >
            Test
          </Button>
        </Can>
        <Button
          variant="secondary"
          size="sm"
          icon="pencil"
          aria-label={`Edit paging group ${g.name}`}
          onClick={onEdit}
        >
          Edit
        </Button>
        <Can min="admin">
          <Button
            variant="ghost"
            size="sm"
            icon="trash-2"
            className="cf-danger"
            aria-label={`Delete paging group ${g.name}`}
            onClick={onDelete}
          >
            Delete
          </Button>
        </Can>
      </div>
    </li>
  );
}

const TEST_RESULT: Record<string, string> = {
  answered: "Answered and played the tone",
  busy: "In a call",
  dnd: "Do not disturb (a normal group does not page it)",
  no_answer: "Did not answer in time",
  no_registration: "No phone registered",
  failed: "Failed",
};

/**
 * Pages one member as a test: the phone should auto-answer on the speaker,
 * play a short tone and hang up. The result lists the headers sent, to check
 * a phone's auto-answer settings.
 */
function TestPanel({ group: g }: { group: PagingGroup }) {
  const [ext, setExt] = useState(g.members[0]?.number ?? "");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<PageTestResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const id = `paging-${String(g.id)}-test`;

  async function run() {
    setBusy(true);
    setError(null);
    setResult(null);
    try {
      setResult(await testPagingGroup(g.id, ext));
    } catch (err: unknown) {
      setError(`Could not run the test: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="cf-add" role="group" aria-label={`Test ${g.name}`}>
      <Select
        id={`${id}-member`}
        label="Member to page"
        size="sm"
        value={ext}
        disabled={busy}
        options={g.members.map((m) => ({
          value: m.number,
          label: `${m.number} ${m.name}`.trim(),
        }))}
        onChange={(e) => setExt(e.target.value)}
      />
      <Button
        size="sm"
        disabled={busy || ext === ""}
        onClick={() => void run()}
      >
        {busy ? "Paging…" : "Page now"}
      </Button>
      {error && <Alert tone="bad">{error}</Alert>}
      {result && (
        <Alert
          tone={result.result === "answered" ? "good" : "warn"}
          title={TEST_RESULT[result.result] ?? result.result}
        >
          {result.detail && <p>{result.detail}</p>}
          {result.contacts.length > 0 && (
            <ul aria-label="Contacts paged">
              {result.contacts.map((c) => (
                <li key={c.device}>
                  <span className="cf-mono">{c.device}</span>
                  {c.userAgent ? ` (${c.userAgent})` : ""}: {c.result}
                  {c.headers.length > 0 && (
                    <>
                      {" · sent "}
                      <span className="cf-mono">{c.headers.join("; ")}</span>
                    </>
                  )}
                </li>
              ))}
            </ul>
          )}
        </Alert>
      )}
    </div>
  );
}

interface Draft {
  name: string;
  code: string;
  priority: PagingPriority;
  enabled: boolean;
  members: Id[];
  callers: Id[];
  multicastChannelId: Id | null;
}

const draftOf = (g: PagingGroup | null): Draft =>
  g
    ? {
        name: g.name,
        code: g.code,
        priority: g.priority,
        enabled: g.enabled,
        members: g.members.map((m) => m.extensionId),
        callers: g.callers.map((m) => m.extensionId),
        multicastChannelId: g.multicastChannelId ?? null,
      }
    : {
        name: "",
        code: "",
        priority: "normal",
        enabled: true,
        members: [],
        callers: [],
        multicastChannelId: null,
      };

const KEYS = [
  "name",
  "code",
  "priority",
  "enabled",
  "members",
  "callers",
  "multicastChannelId",
];

function validate(d: Draft): Record<string, string> {
  const out: Record<string, string> = {};
  if (!NAME_PATTERN.test(d.name)) {
    out.name = "Use 1 to 64 letters, digits, dots, dashes or underscores.";
  }
  if (!CODE_PATTERN.test(d.code)) {
    out.code = "Use * followed by 2 to 4 digits, for example *60.";
  }
  if (d.members.length === 0 && d.multicastChannelId === null) {
    out.members = "Choose at least one member, or a multicast channel.";
  }
  return out;
}

function PagingDrawer({
  group,
  extensions,
  extReady,
  phoneGroups,
  onClose,
  onSaved,
}: {
  group: PagingGroup | null;
  extensions: Extension[];
  extReady: boolean;
  phoneGroups: Group[];
  onClose: () => void;
  onSaved: (g: PagingGroup, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(group));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [pickMember, setPickMember] = useState("");
  const [pickCaller, setPickCaller] = useState("");
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  const ext = (extId: Id) =>
    extensions.find((e) => String(e.id) === String(extId));
  const label = (extId: Id) => ext(extId)?.number ?? `#${String(extId)}`;
  const options = (taken: readonly Id[]) =>
    extensions
      .filter((e) => !taken.some((t) => String(t) === String(e.id)))
      .map((e) => ({
        value: String(e.id),
        label: e.name ? `${e.number} · ${e.name}` : e.number,
      }));

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
      const { multicastChannelId, ...rest } = d;
      const body: PagingGroupFields = {
        ...rest,
        ...(multicastChannelId === null ? {} : { multicastChannelId }),
      };
      onSaved(
        group
          ? await putPagingGroup(group.id, body)
          : await createPagingGroup(body),
        group === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  const list = (
    field: "members" | "callers",
    legend: string,
    pick: string,
    setPick: (v: string) => void,
    note: string,
  ) => (
    <fieldset className="cf-form__section">
      <legend className="az-eyebrow">{legend}</legend>
      {errors[field] && <p className="cf-form__error">{errors[field]}</p>}
      {d[field].length === 0 && <p className="cf-form__note">{note}</p>}
      <ol className="cf-rows">
        {d[field].map((extId) => (
          <li className="cf-row" key={String(extId)}>
            <span className="cf-row__label">
              <Avatar name={ext(extId)?.name || label(extId)} size={26} />
              <span>
                <span className="cf-mono">{label(extId)}</span>{" "}
                {ext(extId)?.name ?? ""}
              </span>
            </span>
            <div className="cf-row__actions">
              <IconButton
                icon="trash-2"
                label={`Remove ${label(extId)} from ${legend.toLowerCase()}`}
                onClick={() =>
                  set(
                    field,
                    d[field].filter((x) => x !== extId),
                  )
                }
              />
            </div>
          </li>
        ))}
      </ol>
      <div className="cf-add">
        <Select
          id={`paging-${field}-pick`}
          label={`Add to ${legend.toLowerCase()}`}
          size="sm"
          value={pick}
          disabled={!extReady}
          options={[
            { value: "", label: extReady ? "Choose…" : "Loading…" },
            ...options(d[field]),
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
            set(field, [...d[field], chosen.id]);
            setPick("");
          }}
        >
          Add
        </Button>
      </div>
    </fieldset>
  );

  return (
    <Drawer
      title={group ? `Paging group ${group.name}` : "New paging group"}
      description="Dialling the code calls every member at once; the phones answer by themselves and hear the caller."
      onClose={busy ? undefined : onClose}
      width={560}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can min="admin">
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
        aria-label={
          group ? `Edit paging group ${group.name}` : "New paging group"
        }
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id="paging-name"
            label="Name"
            autoFocus
            value={d.name}
            error={errors.name}
            onChange={(e) => set("name", e.target.value)}
          />
          <Input
            id="paging-code"
            label="Page code"
            mono
            value={d.code}
            error={errors.code}
            hint="Dial this from any phone, for example *60."
            onChange={(e) => set("code", e.target.value)}
          />
        </div>
        <Select
          id="paging-priority"
          label="Priority"
          value={d.priority}
          error={errors.priority}
          options={[
            { value: "normal", label: "Normal" },
            { value: "emergency", label: "Emergency (ignores DND)" },
          ]}
          onChange={(e) => set("priority", e.target.value as PagingPriority)}
        />
        <Select
          id="paging-multicast"
          label="Also page a multicast channel"
          hint="The page's audio is also sent to the channel's address, which the phone group listens on. LAN only. G.711 channels that listen."
          value={
            d.multicastChannelId === null ? "" : String(d.multicastChannelId)
          }
          error={errors.multicastChannelId}
          options={[
            { value: "", label: "None" },
            ...phoneGroups.flatMap((pg) =>
              (pg.multicast ?? [])
                .filter((c) => c.role !== "send" && c.codec !== "G722")
                .map((c) => ({
                  value: String(c.id),
                  label: `${pg.name} · ${c.label} (${c.address}:${String(c.port)}, ${c.codec})`,
                })),
            ),
          ]}
          onChange={(e) =>
            set(
              "multicastChannelId",
              e.target.value === "" ? null : Number(e.target.value),
            )
          }
        />
        <Switch
          label="Enabled"
          hint="A disabled group answers its code with Forbidden"
          labelPosition="end"
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />
        {list(
          "members",
          "Members",
          pickMember,
          setPickMember,
          "No members chosen.",
        )}
        {list(
          "callers",
          "Allowed callers",
          pickCaller,
          setPickCaller,
          "Empty: any internal extension may page this group.",
        )}
        <p className="cf-form__note">
          Members on do not disturb, in a call or not registered are skipped. A
          page never interrupts a call.
        </p>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
