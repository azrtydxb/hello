import { useEffect, useState, type FormEvent } from "react";
import {
  errorMessage,
  fieldErrors,
  listExtensions,
  listFeatureCodes,
  type Extension,
  type FeatureCode,
  type FieldError,
  type Id,
} from "../api";
import {
  VM_GROUP_MAX_MEMBERS,
  VM_GROUP_SECONDS,
  createVoicemailGroup,
  deleteVoicemailGroup,
  listVoicemailGroups,
  putVoicemailGroup,
  type VoicemailGroup,
  type VoicemailGroupFields,
} from "../api/vmgroups";
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

const NUMBER_PATTERN = /^[0-9]{2,16}$/;

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: VoicemailGroup[] };

/**
 * Voicemail groups: a dial number and member extensions. A message left for
 * the group, or broadcast by a member with the broadcast code and the number,
 * is copied into every member's box.
 */
export function VoicemailGroups() {
  const toast = useToast();
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [extReady, setExtReady] = useState(false);
  const [code, setCode] = useState<string | undefined>(undefined);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; group: VoicemailGroup } | null
  >(null);
  const [deleting, setDeleting] = useState<VoicemailGroup | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listVoicemailGroups(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    listExtensions(controller.signal)
      .then((items) => {
        setExtensions(items);
        setExtReady(true);
      })
      .catch(() => undefined);
    listFeatureCodes(controller.signal)
      .then((codes: FeatureCode[]) =>
        setCode(codes.find((c) => c.action === "voicemail_broadcast")?.code),
      )
      .catch(() => undefined); // the hint falls back to plain words
    return () => controller.abort();
  }, []);

  const replace = (fn: (items: VoicemailGroup[]) => VoicemailGroup[]) =>
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );

  async function onDelete(g: VoicemailGroup) {
    try {
      await deleteVoicemailGroup(g.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${g.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((items) => items.filter((x) => x.id !== g.id));
    setDeleting(null);
    toast.show(`Voicemail group ${g.name} deleted.`);
  }

  const add = (
    <Can min="admin">
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New voicemail group
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Media"
        title="Voicemail groups"
        description={
          `A message left for a group's number lands in every member's box. ` +
          `A member can also dial ${code ?? "the broadcast code"} and the group's number ` +
          `from their own phone to record a message for all members. Nothing rings.`
        }
        actions={add}
      />
      {list.status === "loading" && (
        <Spinner label="Loading voicemail groups…" />
      )}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the voicemail groups">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <EmptyState
          icon="voicemail"
          title="No voicemail groups"
          description="Create one to share a mailbox between several extensions."
          action={add}
        />
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <ul
          className="cf-grid cf-grid--groups cf-plain"
          aria-label="Voicemail groups"
        >
          {list.items.map((g) => (
            <li
              className="az-card cf-card"
              key={String(g.id)}
              aria-labelledby={`vmgroup-${String(g.id)}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <h3
                  className="cf-card__group-name"
                  id={`vmgroup-${String(g.id)}`}
                >
                  {g.name}
                </h3>
                <span className="cf-mono">{g.number}</span>
                {!g.enabled && <Badge tone="neutral">Disabled</Badge>}
              </div>
              <p className="cf-form__note">
                {g.members.length} member{g.members.length === 1 ? "" : "s"},
                messages up to {g.maxSeconds} s
              </p>
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
              <div className="cf-card__foot">
                <Can min="admin">
                  <Button
                    variant="secondary"
                    size="sm"
                    icon="pencil"
                    aria-label={`Edit voicemail group ${g.name}`}
                    onClick={() => setEditing({ kind: "edit", group: g })}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete voicemail group ${g.name}`}
                    onClick={() => setDeleting(g)}
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
        <GroupDrawer
          key={editing.kind === "edit" ? String(editing.group.id) : "new"}
          group={editing.kind === "edit" ? editing.group : null}
          extensions={extensions}
          extReady={extReady}
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
                ? `Voicemail group ${saved.name} created.`
                : `Voicemail group ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete voicemail group ${deleting.name}?`}
          description="Messages already delivered stay in the member boxes. A group that an IVR menu, queue, time condition or route sends calls to cannot be deleted."
          confirmLabel="Delete group"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

const KEYS = ["number", "name", "maxSeconds", "members"];

function GroupDrawer({
  group,
  extensions,
  extReady,
  onClose,
  onSaved,
}: {
  group: VoicemailGroup | null;
  extensions: Extension[];
  extReady: boolean;
  onClose: () => void;
  onSaved: (g: VoicemailGroup, created: boolean) => void;
}) {
  useRestoreFocus();
  const [number, setNumber] = useState(group?.number ?? "");
  const [name, setName] = useState(group?.name ?? "");
  const [enabled, setEnabled] = useState(group?.enabled ?? true);
  const [seconds, setSeconds] = useState(
    String(group?.maxSeconds ?? VM_GROUP_SECONDS.default),
  );
  const [members, setMembers] = useState<Id[]>(
    group?.members.map((m) => m.extensionId) ?? [],
  );
  const [pick, setPick] = useState("");
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);

  const ext = (extId: Id) =>
    extensions.find((e) => String(e.id) === String(extId));
  const label = (extId: Id) => ext(extId)?.number ?? `#${String(extId)}`;
  // A member needs a voicemail box to receive a copy.
  const candidates = extensions.filter(
    (e) =>
      e.voicemailBoxId != null &&
      !members.some((m) => String(m) === String(e.id)),
  );

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = {};
    if (!NUMBER_PATTERN.test(number)) found.number = "Use 2 to 16 digits.";
    const trimmed = name.trim();
    if (trimmed === "" || trimmed.length > 64) {
      found.name = "Use 1 to 64 characters.";
    }
    const secs = Number(seconds);
    if (
      !/^[0-9]+$/.test(seconds) ||
      secs < VM_GROUP_SECONDS.min ||
      secs > VM_GROUP_SECONDS.max
    ) {
      found.maxSeconds = `Use ${VM_GROUP_SECONDS.min} to ${VM_GROUP_SECONDS.max} seconds.`;
    }
    if (members.length === 0) found.members = "Choose at least one member.";
    if (members.length > VM_GROUP_MAX_MEMBERS) {
      found.members = `At most ${VM_GROUP_MAX_MEMBERS} members.`;
    }
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      const body: VoicemailGroupFields = {
        number,
        name: trimmed,
        enabled,
        maxSeconds: secs,
        members,
      };
      onSaved(
        group
          ? await putVoicemailGroup(group.id, body)
          : await createVoicemailGroup(body),
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

  return (
    <Drawer
      title={group ? `Voicemail group ${group.name}` : "New voicemail group"}
      description="Members need a voicemail box. Each gets its own copy of every message."
      onClose={busy ? undefined : onClose}
      width={520}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="vmgroup-form" disabled={busy}>
            {group ? "Save group" : "Create group"}
          </Button>
        </>
      }
    >
      <form
        id="vmgroup-form"
        className="cf-form"
        aria-label={
          group ? `Edit voicemail group ${group.name}` : "New voicemail group"
        }
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id="vmgroup-name"
          label="Name"
          autoFocus
          value={name}
          error={errors.name}
          hint="Spoken to the caller: leave a message for this name."
          onChange={(e) => setName(e.target.value)}
        />
        <div className="cf-two">
          <Input
            id="vmgroup-number"
            label="Number"
            mono
            inputMode="numeric"
            value={number}
            error={errors.number}
            hint="Dialled like an extension, so it must not be one."
            onChange={(e) => setNumber(e.target.value)}
          />
          <Input
            id="vmgroup-seconds"
            label="Longest message (s)"
            mono
            inputMode="numeric"
            value={seconds}
            error={errors.maxSeconds}
            onChange={(e) => setSeconds(e.target.value)}
          />
        </div>
        <Switch
          label="Enabled"
          hint="A disabled group takes no message"
          labelPosition="end"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">Members</legend>
          {errors.members && <p className="cf-form__error">{errors.members}</p>}
          {members.length === 0 && (
            <p className="cf-form__note">No members chosen.</p>
          )}
          <ol className="cf-rows">
            {members.map((extId) => (
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
                    label={`Remove ${label(extId)} from members`}
                    onClick={() =>
                      setMembers(members.filter((x) => x !== extId))
                    }
                  />
                </div>
              </li>
            ))}
          </ol>
          <div className="cf-add">
            <Select
              id="vmgroup-member-pick"
              label="Add to members"
              size="sm"
              value={pick}
              disabled={!extReady}
              options={[
                { value: "", label: extReady ? "Choose…" : "Loading…" },
                ...candidates.map((e) => ({
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
                setMembers([...members, chosen.id]);
                setPick("");
              }}
            >
              Add
            </Button>
          </div>
        </fieldset>
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
