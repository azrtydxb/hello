import { useEffect, useState, type FormEvent } from "react";
import {
  createParkLot,
  createPickupGroup,
  deleteParkLot,
  deletePickupGroup,
  errorMessage,
  fieldErrors,
  listExtensions,
  listFeatureCodes,
  listParkedCalls,
  listParkLots,
  listPickupGroups,
  putParkLot,
  putPickupGroup,
  type Extension,
  type FeatureCode,
  type FieldError,
  type Id,
  type ParkLot,
  type ParkLotFields,
  type PickupGroup,
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
  LiveTag,
  PageHeader,
  Select,
  Spinner,
  Switch,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { generateParkAnnouncement } from "../api/tts";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { FormAlert } from "./callflow/ui";
import { Can } from "../role";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";

const NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;

type ListState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: T[] };

/** Loads a list once; `set` updates it after a change. */
function useList<T>(load: (signal: AbortSignal) => Promise<T[]>) {
  const [state, setState] = useState<ListState<T>>({ status: "loading" });
  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal)
      .then((items) => setState({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [load]);
  const replace = (fn: (items: T[]) => T[]) =>
    setState((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );
  return [state, replace] as const;
}

const codeOf = (codes: readonly FeatureCode[], action: string) =>
  codes.find((c) => c.action === action)?.code;

/** Call park lots, parked calls now, and pickup groups. */
export function ParkPickup() {
  const [codes, setCodes] = useState<FeatureCode[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    listFeatureCodes(controller.signal)
      .then(setCodes)
      .catch(() => undefined); // the hints fall back to plain words
    return () => controller.abort();
  }, []);
  const toast = useToast();
  const park = codeOf(codes, "park");
  const retrieve = codeOf(codes, "park_retrieve");
  const group = codeOf(codes, "pickup_group");
  const directed = codeOf(codes, "pickup_directed");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Park and pickup"
        description={
          `Park a call during a call with ${park ?? "the park code"} (or the phone's Park key), ` +
          `then pick it up from any phone with ${retrieve ?? "the retrieve code"} and the slot, ` +
          `or by dialling the slot. Answer a ringing colleague with ${group ?? "the group pickup code"} ` +
          `or ${directed ?? "the directed pickup code"} and their extension.`
        }
      />
      <div className="cf-stack">
        <ParkedNow />
        <ParkLots toast={toast.show} />
        <PickupGroups toast={toast.show} />
      </div>
      {toast.node}
    </section>
  );
}

// --- parked now -----------------------------------------------------------------

function seconds(iso: string, now: number): number {
  return Math.round((new Date(iso).getTime() - now) / 1000);
}

function ParkedNow() {
  const parked = usePolling(listParkedCalls, LIVE_REFRESH_MS);
  const items = parked.status === "loading" ? undefined : parked.data;
  const now = parked.status === "ready" ? parked.updatedAt.getTime() : 0;
  return (
    <section aria-labelledby="parked-now-title">
      <h2 className="az-eyebrow" id="parked-now-title">
        Parked now <LiveTag />
      </h2>
      {parked.status === "loading" && <Spinner label="Loading parked calls…" />}
      {parked.status === "error" && (
        <Alert tone="warn" title="Could not load the parked calls">
          {parked.message}
        </Alert>
      )}
      {items && items.length === 0 && (
        <p className="cf-form__note">No call is parked.</p>
      )}
      {items && items.length > 0 && (
        <ul className="cf-rows" aria-label="Parked calls">
          {items.map((p) => (
            <li className="cf-row" key={p.slot}>
              <span className="cf-row__label">
                <Badge tone="pillar">{p.slot}</Badge>
                <span>
                  <span className="cf-mono">{p.caller}</span> {p.callerName}
                  {" · parked by "}
                  <span className="cf-mono">{p.parkedBy}</span>
                  {" · lot "}
                  {p.lot}
                </span>
              </span>
              <span className="cf-card__sub">
                {p.phase === "returning"
                  ? "Ringing the parker"
                  : `Returns in ${String(Math.max(0, seconds(p.returnAt, now)))} s`}
              </span>
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

// --- park lots ------------------------------------------------------------------

/**
 * SlotVoice generates the spoken slot announcement once through the
 * text-to-speech provider. Without it the parker hears the slot as tones.
 */
function SlotVoice({ toast }: { toast: (message: string) => void }) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  async function onGenerate() {
    setBusy(true);
    setError(null);
    try {
      const res = await generateParkAnnouncement();
      toast(
        res.generated === 0
          ? "The spoken slot announcement is already up to date."
          : `Spoken slot announcement ready: ${res.generated} of ${res.announcements.length} clips generated.`,
      );
    } catch (err: unknown) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Can min="operator">
      <Button
        variant="secondary"
        icon="megaphone"
        disabled={busy}
        onClick={() => void onGenerate()}
        title="Speak the slot number to the parker instead of playing tones. Uses the text-to-speech settings and spends credits once."
      >
        {busy ? "Generating…" : "Generate spoken slot announcement"}
      </Button>
      {error && (
        <Alert tone="bad" title="Could not generate the slot announcement">
          {error}
        </Alert>
      )}
    </Can>
  );
}

function ParkLots({ toast }: { toast: (message: string) => void }) {
  const [list, replace] = useList(listParkLots);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; lot: ParkLot } | null
  >(null);
  const [deleting, setDeleting] = useState<ParkLot | null>(null);

  async function onDelete(lot: ParkLot) {
    try {
      await deleteParkLot(lot.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${lot.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((items) => items.filter((l) => l.id !== lot.id));
    setDeleting(null);
    toast(`Park lot ${lot.name} deleted.`);
  }

  const add = (
    <Can min="operator">
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New park lot
      </Button>
    </Can>
  );
  return (
    <section aria-labelledby="park-lots-title">
      <h2 className="az-eyebrow" id="park-lots-title">
        Park lots
      </h2>
      <div
        style={{ margin: "8px 0", display: "flex", gap: 8, flexWrap: "wrap" }}
      >
        {add}
        <SlotVoice toast={toast} />
      </div>
      {list.status === "loading" && <Spinner label="Loading park lots…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the park lots">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <EmptyState
          icon="square-parking"
          title="No park lots"
          description="A park lot is a range of slot numbers, for example 701 to 720. Without one, calls cannot be parked."
          action={add}
        />
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <ul className="cf-grid cf-grid--groups cf-plain" aria-label="Park lots">
          {list.items.map((l) => (
            <li
              className="az-card cf-card"
              key={String(l.id)}
              aria-labelledby={`lot-${String(l.id)}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <div style={{ minWidth: 0 }}>
                  <h3
                    className="cf-card__group-name"
                    id={`lot-${String(l.id)}`}
                  >
                    {l.name}
                  </h3>
                  <div className="cf-card__sub">
                    Slots <span className="cf-mono">{l.slotStart}</span> to{" "}
                    <span className="cf-mono">{l.slotEnd}</span> · returns to
                    the parker after {l.returnSecs} s, then voicemail after{" "}
                    {l.voicemailSecs} s
                  </div>
                </div>
                {!l.enabled && <Badge tone="warn">Disabled</Badge>}
              </div>
              <div className="cf-card__foot">
                <Can min="operator">
                  <Button
                    variant="secondary"
                    size="sm"
                    icon="pencil"
                    aria-label={`Edit park lot ${l.name}`}
                    onClick={() => setEditing({ kind: "edit", lot: l })}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete park lot ${l.name}`}
                    onClick={() => setDeleting(l)}
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
        <ParkLotDrawer
          key={editing.kind === "edit" ? String(editing.lot.id) : "new"}
          lot={editing.kind === "edit" ? editing.lot : null}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            replace((items) =>
              items.some((l) => l.id === saved.id)
                ? items.map((l) => (l.id === saved.id ? saved : l))
                : [...items, saved],
            );
            setEditing(null);
            toast(
              created
                ? `Park lot ${saved.name} created.`
                : `Park lot ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete park lot ${deleting.name}?`}
          description={`Slots ${String(deleting.slotStart)} to ${String(deleting.slotEnd)} stop parking new calls. Calls already parked keep their slot until they are retrieved.`}
          confirmLabel="Delete lot"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  );
}

interface LotDraft {
  name: string;
  slotStart: string;
  slotEnd: string;
  returnSecs: string;
  voicemailSecs: string;
  enabled: boolean;
}

const lotDraft = (l: ParkLot | null): LotDraft =>
  l
    ? {
        name: l.name,
        slotStart: String(l.slotStart),
        slotEnd: String(l.slotEnd),
        returnSecs: String(l.returnSecs),
        voicemailSecs: String(l.voicemailSecs),
        enabled: l.enabled,
      }
    : {
        name: "",
        slotStart: "",
        slotEnd: "",
        returnSecs: "120",
        voicemailSecs: "60",
        enabled: true,
      };

const LOT_KEYS = [
  "name",
  "slotStart",
  "slotEnd",
  "returnSecs",
  "voicemailSecs",
  "enabled",
];

function validateLot(d: LotDraft): {
  errors: Record<string, string>;
  body?: ParkLotFields;
} {
  const errors: Record<string, string> = {};
  const num = (v: string) => (/^[0-9]{1,5}$/.test(v) ? Number(v) : NaN);
  const start = num(d.slotStart);
  const end = num(d.slotEnd);
  const back = num(d.returnSecs);
  const vm = num(d.voicemailSecs);
  if (!NAME_PATTERN.test(d.name)) {
    errors.name = "Use 1 to 64 letters, digits, dots, dashes or underscores.";
  }
  if (!(start >= 1)) errors.slotStart = "Enter the first slot number.";
  if (!(end >= 1)) errors.slotEnd = "Enter the last slot number.";
  else if (end < start || end - start >= 100) {
    errors.slotEnd =
      "Use a range of at most 100 slots, ending at or after the first.";
  }
  if (!(back >= 10 && back <= 3600)) {
    errors.returnSecs = "Use 10 to 3600 seconds.";
  }
  if (!(vm >= 10 && vm <= 3600)) {
    errors.voicemailSecs = "Use 10 to 3600 seconds.";
  }
  if (Object.keys(errors).length > 0) return { errors };
  return {
    errors,
    body: {
      name: d.name,
      slotStart: start,
      slotEnd: end,
      returnSecs: back,
      voicemailSecs: vm,
      enabled: d.enabled,
    },
  };
}

function ParkLotDrawer({
  lot,
  onClose,
  onSaved,
}: {
  lot: ParkLot | null;
  onClose: () => void;
  onSaved: (l: ParkLot, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => lotDraft(lot));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const set = <K extends keyof LotDraft>(k: K, v: LotDraft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found = validateLot(d);
    setErrors(found.errors);
    if (!found.body) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      onSaved(
        lot
          ? await putParkLot(lot.id, found.body)
          : await createParkLot(found.body),
        lot === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), LOT_KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={lot ? `Park lot ${lot.name}` : "New park lot"}
      description="A range of slot numbers. A slot is dialled like an extension, so it must not be one."
      onClose={busy ? undefined : onClose}
      width={520}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="park-lot-form" disabled={busy}>
            {lot ? "Save lot" : "Create lot"}
          </Button>
        </>
      }
    >
      <form
        id="park-lot-form"
        className="cf-form"
        aria-label={lot ? `Edit park lot ${lot.name}` : "New park lot"}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id="lot-name"
          label="Name"
          autoFocus
          value={d.name}
          error={errors.name}
          onChange={(e) => set("name", e.target.value)}
        />
        <div className="cf-two">
          <Input
            id="lot-start"
            label="First slot"
            mono
            inputMode="numeric"
            value={d.slotStart}
            error={errors.slotStart}
            onChange={(e) => set("slotStart", e.target.value)}
          />
          <Input
            id="lot-end"
            label="Last slot"
            mono
            inputMode="numeric"
            value={d.slotEnd}
            error={errors.slotEnd}
            onChange={(e) => set("slotEnd", e.target.value)}
          />
        </div>
        <div className="cf-two">
          <Input
            id="lot-return"
            label="Return to the parker after (s)"
            mono
            inputMode="numeric"
            value={d.returnSecs}
            error={errors.returnSecs}
            hint="An unretrieved call rings the phone that parked it."
            onChange={(e) => set("returnSecs", e.target.value)}
          />
          <Input
            id="lot-voicemail"
            label="Then voicemail after (s)"
            mono
            inputMode="numeric"
            value={d.voicemailSecs}
            error={errors.voicemailSecs}
            hint="If the parker does not answer; the call ends without voicemail."
            onChange={(e) => set("voicemailSecs", e.target.value)}
          />
        </div>
        <Switch
          label="Enabled"
          hint="A disabled lot parks no new calls"
          labelPosition="end"
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}

// --- pickup groups --------------------------------------------------------------

function PickupGroups({ toast }: { toast: (message: string) => void }) {
  const [list, replace] = useList(listPickupGroups);
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [extReady, setExtReady] = useState(false);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; group: PickupGroup } | null
  >(null);
  const [deleting, setDeleting] = useState<PickupGroup | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then((items) => {
        setExtensions(items);
        setExtReady(true);
      })
      .catch(() => undefined);
    return () => controller.abort();
  }, []);

  async function onDelete(g: PickupGroup) {
    try {
      await deletePickupGroup(g.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${g.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replace((items) => items.filter((x) => x.id !== g.id));
    setDeleting(null);
    toast(`Pickup group ${g.name} deleted.`);
  }

  const add = (
    <Can min="operator">
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New pickup group
      </Button>
    </Can>
  );
  return (
    <section aria-labelledby="pickup-title">
      <h2 className="az-eyebrow" id="pickup-title">
        Pickup groups
      </h2>
      <p className="cf-form__note">
        A member may answer a call ringing any other member. An extension in no
        pickup group may answer calls ringing the other members of its ring
        groups.
      </p>
      <div style={{ margin: "8px 0" }}>{add}</div>
      {list.status === "loading" && <Spinner label="Loading pickup groups…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the pickup groups">
          {list.message}
        </Alert>
      )}
      {list.status === "ready" && list.items.length === 0 && (
        <EmptyState
          icon="phone-incoming"
          title="No pickup groups"
          description="Without one, pickup works within each ring group."
          action={add}
        />
      )}
      {list.status === "ready" && list.items.length > 0 && (
        <ul
          className="cf-grid cf-grid--groups cf-plain"
          aria-label="Pickup groups"
        >
          {list.items.map((g) => (
            <li
              className="az-card cf-card"
              key={String(g.id)}
              aria-labelledby={`pickup-${String(g.id)}`}
            >
              <div className="cf-card__head cf-card__head--top">
                <h3
                  className="cf-card__group-name"
                  id={`pickup-${String(g.id)}`}
                >
                  {g.name}
                </h3>
              </div>
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
                <Can min="operator">
                  <Button
                    variant="secondary"
                    size="sm"
                    icon="pencil"
                    aria-label={`Edit pickup group ${g.name}`}
                    onClick={() => setEditing({ kind: "edit", group: g })}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete pickup group ${g.name}`}
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
        <PickupDrawer
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
            toast(
              created
                ? `Pickup group ${saved.name} created.`
                : `Pickup group ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete pickup group ${deleting.name}?`}
          description="Its members fall back to the pickup scope of their ring groups."
          confirmLabel="Delete group"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
    </section>
  );
}

function PickupDrawer({
  group,
  extensions,
  extReady,
  onClose,
  onSaved,
}: {
  group: PickupGroup | null;
  extensions: Extension[];
  extReady: boolean;
  onClose: () => void;
  onSaved: (g: PickupGroup, created: boolean) => void;
}) {
  useRestoreFocus();
  const [name, setName] = useState(group?.name ?? "");
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

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = {};
    if (!NAME_PATTERN.test(name)) {
      found.name = "Use 1 to 64 letters, digits, dots, dashes or underscores.";
    }
    if (members.length === 0) found.members = "Choose at least one member.";
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      const body = { name, members };
      onSaved(
        group
          ? await putPickupGroup(group.id, body)
          : await createPickupGroup(body),
        group === null,
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), ["name", "members"]);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={group ? `Pickup group ${group.name}` : "New pickup group"}
      description="Members can answer each other's ringing phones."
      onClose={busy ? undefined : onClose}
      width={520}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="pickup-form" disabled={busy}>
            {group ? "Save group" : "Create group"}
          </Button>
        </>
      }
    >
      <form
        id="pickup-form"
        className="cf-form"
        aria-label={
          group ? `Edit pickup group ${group.name}` : "New pickup group"
        }
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id="pickup-name"
          label="Name"
          autoFocus
          value={name}
          error={errors.name}
          onChange={(e) => setName(e.target.value)}
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
              id="pickup-member-pick"
              label="Add to members"
              size="sm"
              value={pick}
              disabled={!extReady}
              options={[
                { value: "", label: extReady ? "Choose…" : "Loading…" },
                ...extensions
                  .filter(
                    (e) => !members.some((m) => String(m) === String(e.id)),
                  )
                  .map((e) => ({
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
