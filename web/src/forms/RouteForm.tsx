import { QueueSelect } from "../components/QueueSelect";
import { targetOptions } from "../components/IvrDestination";
import { listTimeConditions, type TimeCondition } from "../api/timecond";
import { useEffect, useState, type FormEvent, type ReactNode } from "react";
import {
  createInboundRoute,
  createOutboundRoute,
  errorMessage,
  fieldErrors,
  listAnnouncements,
  listConferences,
  listExtensions,
  listIvrMenus,
  listPagingGroups,
  listRingGroups,
  updateInboundRoute,
  updateOutboundRoute,
  type Announcement,
  type Extension,
  type FieldError,
  type Id,
  type InboundRoute,
  type InboundRouteFields,
  type IvrMenu,
  type OutboundRoute,
  type OutboundRouteFields,
  type Conference,
  type PagingGroup,
  type RingGroup,
  type Schedule,
  type Trunk,
} from "../api";
import {
  ScheduleEditor,
  scheduleKeys,
  toTransform,
  TransformEditor,
  transformDraft,
  transformKeys,
  validateSchedule,
  validateTransform,
  type TransformDraft,
} from "../components/RouteEditors";
import {
  Button,
  Drawer,
  IconButton,
  Input,
  Select,
  Switch,
  useRestoreFocus,
} from "../design/azrty/components";
import { fieldId, mapFieldErrors, splitList, type ErrorMap } from "../forms";
import { FormAlert } from "../pages/callflow/ui";
import { Can } from "../role";
import { MOHSelect } from "../pages/media/MohSelect";

export const DEFAULT_FAILOVER = "408, 480, 500, 502, 503, 504";
export const FORM_ID = "route-form";
/** The trunk list as the forms see it; `ready` is false until it has loaded. */
export interface TrunkOptions {
  items: Trunk[];
  ready: boolean;
}

const TRUNKS_PENDING_ID = "trunks-pending";

/** Why saving waits: the trunk list is still loading or failed to load. */
function TrunksPending({ ready }: { ready: boolean }) {
  if (ready) return null;
  return (
    <p id={TRUNKS_PENDING_ID} className="cf-form__note">
      Saving is available once the trunk list has loaded.
    </p>
  );
}

function useServerErrors() {
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  return {
    errors,
    unmatched,
    formError,
    clientErrors(found: Record<string, string>) {
      setErrors(found);
      setUnmatched([]);
      const bad = Object.keys(found).length > 0;
      setFormError(bad ? "Fix the highlighted fields." : null);
      return bad;
    },
    serverError(err: unknown, known: string[]) {
      const mapped = mapFieldErrors(fieldErrors(err), known);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
    },
  };
}

/** The drawer around a route form, with Cancel and the submit button. */
function RouteDrawer({
  title,
  description,
  busy,
  trunksReady,
  submitLabel,
  onClose,
  children,
}: {
  title: string;
  description: string;
  busy: boolean;
  trunksReady: boolean;
  submitLabel: string;
  onClose: () => void;
  children: ReactNode;
}) {
  useRestoreFocus();
  return (
    <Drawer
      title={title}
      description={description}
      onClose={busy ? undefined : onClose}
      width={600}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button
              type="submit"
              form={FORM_ID}
              disabled={busy || !trunksReady}
              aria-describedby={trunksReady ? undefined : TRUNKS_PENDING_ID}
            >
              {submitLabel}
            </Button>
          </Can>
        </>
      }
    >
      {children}
    </Drawer>
  );
}

// --- outbound -----------------------------------------------------------------

interface OutboundDraft {
  name: string;
  matchKind: OutboundRouteFields["matchKind"];
  match: string;
  sourceExtensions: string;
  schedule: Schedule | null;
  numberTransform: TransformDraft;
  callerIdTransform: TransformDraft;
  trunks: Id[];
  failoverCodes: string;
  emergency: boolean;
  enabled: boolean;
}

function outboundDraft(r: OutboundRoute | null): OutboundDraft {
  return {
    name: r?.name ?? "",
    matchKind: r?.matchKind ?? "prefix",
    match: r?.match ?? "",
    sourceExtensions: (r?.sourceExtensions ?? []).join(", "),
    schedule: r?.schedule ?? null,
    numberTransform: transformDraft(r?.numberTransform),
    callerIdTransform: transformDraft(r?.callerIdTransform),
    trunks: r?.trunks ?? [],
    failoverCodes: r ? (r.failoverCodes ?? []).join(", ") : DEFAULT_FAILOVER,
    emergency: r?.emergency ?? false,
    enabled: r?.enabled ?? true,
  };
}

function outboundBody(d: OutboundDraft): OutboundRouteFields {
  return {
    name: d.name.trim(),
    matchKind: d.matchKind,
    match: d.match,
    sourceExtensions: splitList(d.sourceExtensions),
    schedule: d.schedule,
    numberTransform: toTransform(d.numberTransform),
    callerIdTransform: toTransform(d.callerIdTransform),
    trunks: d.trunks,
    failoverCodes: splitList(d.failoverCodes).map(Number),
    emergency: d.emergency,
    enabled: d.enabled,
  };
}

function validateOutbound(d: OutboundDraft): Record<string, string> {
  const e: Record<string, string> = {
    ...validateTransform("numberTransform", d.numberTransform),
    ...validateTransform("callerIdTransform", d.callerIdTransform),
    ...validateSchedule(d.schedule),
  };
  if (d.name.trim() === "") e.name = "Enter a name.";
  if (d.match === "") e.match = "Enter what to match.";
  if (d.match.length > 500) e.match = "At most 500 characters.";
  if (d.trunks.length === 0) e.trunks = "Choose at least one trunk.";
  const codes = splitList(d.failoverCodes);
  if (codes.some((c) => !/^[1-6][0-9]{2}$/.test(c))) {
    e.failoverCodes = "Use SIP response codes, e.g. 503, 408.";
  }
  return e;
}

function outboundKeys(d: OutboundDraft): string[] {
  return [
    "name",
    "matchKind",
    "match",
    "sourceExtensions",
    "trunks",
    ...d.trunks.map((_, i) => `trunks[${i}]`),
    "failoverCodes",
    "emergency",
    "enabled",
    ...transformKeys("numberTransform"),
    ...transformKeys("callerIdTransform"),
    ...scheduleKeys(d.schedule),
  ];
}

export function OutboundDrawer({
  route,
  trunks,
  onClose,
  commit,
  onSaved,
}: {
  route: OutboundRoute | null;
  trunks: TrunkOptions;
  onClose: () => void;
  /** The canvas stages the body in its draft instead of saving it here. */
  commit?: (body: object) => Promise<void>;
  onSaved: (r: OutboundRoute, created: boolean) => void;
}) {
  const form = "out";
  const [d, setD] = useState(() => outboundDraft(route));
  const [busy, setBusy] = useState(false);
  const v = useServerErrors();
  const set = <K extends keyof OutboundDraft>(k: K, value: OutboundDraft[K]) =>
    setD((prev) => ({ ...prev, [k]: value }));
  const id = (k: string) => fieldId(form, k);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (v.clientErrors(validateOutbound(d))) return;
    setBusy(true);
    try {
      const body = outboundBody(d);
      if (commit) {
        await commit(body);
        onClose();
        return;
      }
      onSaved(
        route
          ? await updateOutboundRoute(route.id, body)
          : await createOutboundRoute(body),
        route === null,
      );
    } catch (err) {
      v.serverError(err, outboundKeys(d));
      setBusy(false);
    }
  }

  return (
    <RouteDrawer
      title={route ? `Outbound route ${route.name}` : "New outbound route"}
      description="Calls from extensions to numbers outside Hello."
      busy={busy}
      trunksReady={trunks.ready}
      submitLabel={route ? "Save route" : "Create route"}
      onClose={onClose}
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={
          route ? `Edit outbound route ${route.name}` : "New outbound route"
        }
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id={id("name")}
          label="Name"
          autoFocus
          value={d.name}
          error={v.errors.name}
          onChange={(e) => set("name", e.target.value)}
        />
        <div className="cf-two">
          <Select
            id={id("matchKind")}
            label="Match by"
            value={d.matchKind}
            error={v.errors.matchKind}
            options={[
              { value: "prefix", label: "Prefix" },
              { value: "regex", label: "Regex (RE2)" },
            ]}
            onChange={(e) =>
              set("matchKind", e.target.value as OutboundDraft["matchKind"])
            }
          />
          <Input
            id={id("match")}
            label={d.matchKind === "regex" ? "Match regex" : "Match prefix"}
            mono
            spellCheck={false}
            value={d.match}
            error={v.errors.match}
            hint={d.matchKind === "regex" ? "e.g. ^05[0-9]{8}$" : "e.g. 00"}
            onChange={(e) => set("match", e.target.value)}
          />
        </div>
        <Input
          id={id("sourceExtensions")}
          label="From extensions"
          mono
          value={d.sourceExtensions}
          error={v.errors.sourceExtensions}
          hint="Comma-separated; empty means every extension."
          onChange={(e) => set("sourceExtensions", e.target.value)}
        />
        <TrunkPicker
          form={form}
          trunks={trunks.items}
          value={d.trunks}
          errors={v.errors}
          onChange={(t) => set("trunks", t)}
        />
        <Input
          id={id("failoverCodes")}
          label="Fail over on"
          mono
          value={d.failoverCodes}
          error={v.errors.failoverCodes}
          hint="SIP codes that move on to the next trunk."
          onChange={(e) => set("failoverCodes", e.target.value)}
        />
        <div className="cf-field-group">
          <Switch
            label="Emergency route"
            hint="Tried even when a trunk is full or unhealthy"
            labelPosition="end"
            checked={d.emergency}
            onChange={(e) => set("emergency", e.target.checked)}
          />
          <Switch
            label="Enabled"
            labelPosition="end"
            checked={d.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
          />
        </div>
        <TransformEditor
          form={form}
          base="numberTransform"
          legend="Number rewrite"
          value={d.numberTransform}
          errors={v.errors}
          onChange={(t) => set("numberTransform", t)}
        />
        <TransformEditor
          form={form}
          base="callerIdTransform"
          legend="Caller ID rewrite"
          value={d.callerIdTransform}
          errors={v.errors}
          onChange={(t) => set("callerIdTransform", t)}
        />
        <ScheduleEditor
          form={form}
          value={d.schedule}
          errors={v.errors}
          onChange={(s) => set("schedule", s)}
        />
        <FormAlert message={v.formError} unmatched={v.unmatched} />
        <TrunksPending ready={trunks.ready} />
      </form>
    </RouteDrawer>
  );
}

/** Ordered trunk choice for an outbound route: tried top to bottom. */
function TrunkPicker({
  form,
  trunks,
  value,
  errors,
  onChange,
}: {
  form: string;
  trunks: Trunk[];
  value: Id[];
  errors: ErrorMap;
  onChange: (v: Id[]) => void;
}) {
  const error = errors.trunks;
  const [pick, setPick] = useState("");
  const selectId = fieldId(form, "trunks");
  const errorId = `${selectId}-error`;
  const chosen = value.map(
    (tid) =>
      trunks.find((t) => String(t.id) === String(tid)) ?? {
        id: tid,
        name: `#${String(tid)}`,
      },
  );
  const available = trunks.filter(
    (t) => !value.some((v) => String(v) === String(t.id)),
  );
  const swap = (i: number, j: number) => {
    const next = [...value];
    const a = next[i];
    const b = next[j];
    if (a === undefined || b === undefined) return;
    next[i] = b;
    next[j] = a;
    onChange(next);
  };

  return (
    <fieldset
      className="cf-form__section"
      aria-describedby={error ? errorId : undefined}
    >
      <legend className="az-eyebrow">Trunks, in try order</legend>
      {error && (
        <p id={errorId} className="cf-form__error">
          {error}
        </p>
      )}
      {chosen.length === 0 ? (
        <p className="cf-form__note">No trunks chosen.</p>
      ) : (
        <ol className="cf-rows">
          {chosen.map((t, i) => {
            const itemError = errors[`trunks[${i}]`];
            const itemErrorId = `${selectId}-${i}-error`;
            const describedBy = itemError ? itemErrorId : undefined;
            return (
              <li className="cf-row" key={String(t.id)}>
                <div className="cf-stack" style={{ gap: 4, minWidth: 0 }}>
                  <span className="cf-row__label">
                    <span className="cf-member__pos">{i + 1}</span>
                    <span className="cf-chip">{t.name}</span>
                  </span>
                  {itemError && (
                    <p id={itemErrorId} className="cf-form__error">
                      {itemError}
                    </p>
                  )}
                </div>
                <span className="cf-row__actions">
                  <IconButton
                    icon="arrow-up"
                    label={`Try ${t.name} earlier`}
                    aria-describedby={describedBy}
                    disabled={i === 0}
                    onClick={() => swap(i, i - 1)}
                  />
                  <IconButton
                    icon="arrow-down"
                    label={`Try ${t.name} later`}
                    aria-describedby={describedBy}
                    disabled={i === chosen.length - 1}
                    onClick={() => swap(i, i + 1)}
                  />
                  <IconButton
                    icon="trash-2"
                    label={`Remove ${t.name}`}
                    aria-describedby={describedBy}
                    onClick={() => onChange(value.filter((_, j) => j !== i))}
                  />
                </span>
              </li>
            );
          })}
        </ol>
      )}
      <div className="cf-add">
        <Select
          id={selectId}
          label="Add trunk"
          size="sm"
          value={pick}
          aria-invalid={error ? true : undefined}
          aria-describedby={error ? errorId : undefined}
          options={[
            { value: "", label: "Choose…" },
            ...available.map((t) => ({ value: String(t.id), label: t.name })),
          ]}
          onChange={(e) => setPick(e.target.value)}
        />
        <Button
          variant="secondary"
          size="sm"
          icon="plus"
          disabled={pick === ""}
          onClick={() => {
            const t = trunks.find((x) => String(x.id) === pick);
            if (t) onChange([...value, t.id]);
            setPick("");
          }}
        >
          Add
        </Button>
      </div>
    </fieldset>
  );
}

// --- inbound ------------------------------------------------------------------

interface InboundDraft {
  name: string;
  didKind: InboundRouteFields["didKind"];
  did: string;
  /** The trunk id exactly as the API gave it; null = any trunk. */
  trunkId: Id | null;
  sipDomain: string;
  headerName: string;
  headerRegex: string;
  schedule: Schedule | null;
  callerIdTransform: TransformDraft;
  destinationKind: InboundRouteFields["destinationKind"];
  destination: string;
  enabled: boolean;
  mohClassId: Id | null;
  mohRingback: boolean;
}

function inboundDraft(r: InboundRoute | null): InboundDraft {
  return {
    name: r?.name ?? "",
    didKind: r?.didKind ?? "exact",
    did: r?.did ?? "",
    trunkId: r?.trunkId ?? null,
    sipDomain: r?.sipDomain ?? "",
    headerName: r?.headerName ?? "",
    headerRegex: r?.headerRegex ?? "",
    schedule: r?.schedule ?? null,
    callerIdTransform: transformDraft(r?.callerIdTransform),
    destinationKind: r?.destinationKind ?? "extension",
    destination: r?.destination ?? "",
    enabled: r?.enabled ?? true,
    mohClassId: r?.mohClassId ?? null,
    mohRingback: r?.mohRingback ?? false,
  };
}

function inboundBody(d: InboundDraft): InboundRouteFields {
  return {
    name: d.name.trim(),
    didKind: d.didKind,
    did: d.didKind === "any" ? "" : d.did,
    trunkId: d.trunkId,
    sipDomain: d.sipDomain,
    headerName: d.headerName,
    headerRegex: d.headerRegex,
    schedule: d.schedule,
    callerIdTransform: toTransform(d.callerIdTransform),
    destinationKind: d.destinationKind,
    destination: d.destination.trim(),
    enabled: d.enabled,
    mohClassId: d.mohClassId,
    mohRingback: d.mohRingback,
  };
}

function validateInbound(d: InboundDraft): Record<string, string> {
  const e: Record<string, string> = {
    ...validateTransform("callerIdTransform", d.callerIdTransform),
    ...validateSchedule(d.schedule),
  };
  if (d.name.trim() === "") e.name = "Enter a name.";
  if (d.didKind !== "any" && d.did === "") e.did = "Enter the DID to match.";
  if (d.headerRegex !== "" && d.headerName === "") {
    e.headerName = "Name the header the regex applies to.";
  }
  if (d.destination.trim() === "") {
    e.destination = isListed(d.destinationKind)
      ? `Choose a ${LISTED_NOUN[d.destinationKind]}.`
      : d.destinationKind === "conference"
        ? "Choose a conference room."
        : "Enter where calls go.";
  }
  return e;
}

function inboundKeys(d: InboundDraft): string[] {
  return [
    "name",
    "didKind",
    "did",
    "trunkId",
    "sipDomain",
    "headerName",
    "headerRegex",
    "destinationKind",
    "destination",
    "enabled",
    "mohClassId",
    "mohRingback",
    ...transformKeys("callerIdTransform"),
    ...scheduleKeys(d.schedule),
  ];
}

const DESTINATION_LABEL: Record<InboundDraft["destinationKind"], string> = {
  extension: "Extension number",
  internal: "Internal (the DID names the extension)",
  external: "External number",
  sip_uri: "SIP URI",
  ivr: "IVR menu",
  queue: "Call queue",
  time_condition: "Time condition",
  ring_group: "Ring group",
  voicemail: "Voicemail box of",
  announcement: "Announcement",
  paging: "Paging group",
  conference: "Conference room",
};

type ListedKind = "ring_group" | "voicemail" | "announcement" | "paging";

/** What the empty-target error calls each listed kind's target. */
const LISTED_NOUN: Record<ListedKind, string> = {
  ring_group: "ring group",
  voicemail: "voicemail box",
  announcement: "announcement",
  paging: "paging group",
};

/** Conference rooms are chosen by room number, which the route stores. */
function conferenceOptions(rooms: readonly Conference[], current: string) {
  const head = { value: "", label: "Choose…" };
  const list = rooms.map((c) => ({
    value: c.number,
    label: c.name ? `${c.number} · ${c.name}` : c.number,
  }));
  if (current !== "" && !list.some((o) => o.value === current)) {
    return [head, { value: current, label: `${current} (not found)` }, ...list];
  }
  return [head, ...list];
}

/** Destination kinds whose target is chosen from a list of stored objects. */
function isListed(k: InboundDraft["destinationKind"]): k is ListedKind {
  return k in LISTED_NOUN;
}

export function InboundDrawer({
  route,
  trunks,
  onClose,
  commit,
  onSaved,
}: {
  route: InboundRoute | null;
  trunks: TrunkOptions;
  onClose: () => void;
  /** The canvas stages the body in its draft instead of saving it here. */
  commit?: (body: object) => Promise<void>;
  onSaved: (r: InboundRoute, created: boolean) => void;
}) {
  const form = "in";
  const [d, setD] = useState(() => inboundDraft(route));
  const [busy, setBusy] = useState(false);
  const [menus, setMenus] = useState<IvrMenu[]>([]);
  const [timeConds, setTimeConds] = useState<TimeCondition[]>([]);
  const [exts, setExts] = useState<Extension[]>([]);
  const [ringGroups, setRingGroups] = useState<RingGroup[]>([]);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [pagingGroups, setPagingGroups] = useState<PagingGroup[]>([]);
  const [rooms, setRooms] = useState<Conference[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    // The pickers stay empty when a list fails; saving still works.
    listExtensions(controller.signal)
      .then(setExts)
      .catch(() => undefined);
    listRingGroups(controller.signal)
      .then(setRingGroups)
      .catch(() => undefined);
    listAnnouncements(controller.signal)
      .then(setAnnouncements)
      .catch(() => undefined);
    listPagingGroups(controller.signal)
      .then(setPagingGroups)
      .catch(() => undefined);
    listConferences(controller.signal)
      .then(setRooms)
      .catch(() => undefined);
    listTimeConditions(controller.signal)
      .then(setTimeConds)
      .catch(() => undefined); // the picker stays empty; saving still works
    listIvrMenus(controller.signal)
      .then(setMenus)
      .catch(() => undefined); // the picker stays empty; saving still works
    return () => controller.abort();
  }, []);
  const v = useServerErrors();
  const set = <K extends keyof InboundDraft>(k: K, value: InboundDraft[K]) =>
    setD((prev) => ({ ...prev, [k]: value }));
  const id = (k: string) => fieldId(form, k);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (v.clientErrors(validateInbound(d))) return;
    setBusy(true);
    try {
      const body = inboundBody(d);
      if (commit) {
        await commit(body);
        onClose();
        return;
      }
      onSaved(
        route
          ? await updateInboundRoute(route.id, body)
          : await createInboundRoute(body),
        route === null,
      );
    } catch (err) {
      v.serverError(err, inboundKeys(d));
      setBusy(false);
    }
  }

  const storedTrunkMissing =
    d.trunkId !== null &&
    !trunks.items.some((t) => String(t.id) === String(d.trunkId));

  return (
    <RouteDrawer
      title={route ? `Inbound route ${route.name}` : "New inbound route"}
      description="Calls arriving from trunks."
      busy={busy}
      trunksReady={trunks.ready}
      submitLabel={route ? "Save route" : "Create route"}
      onClose={onClose}
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={
          route ? `Edit inbound route ${route.name}` : "New inbound route"
        }
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Input
          id={id("name")}
          label="Name"
          autoFocus
          value={d.name}
          error={v.errors.name}
          onChange={(e) => set("name", e.target.value)}
        />
        <div className="cf-two">
          <Select
            id={id("didKind")}
            label="Match DID"
            value={d.didKind}
            error={v.errors.didKind}
            options={[
              { value: "exact", label: "Exactly" },
              { value: "prefix", label: "By prefix" },
              { value: "regex", label: "By regex (RE2)" },
              { value: "any", label: "Any DID" },
            ]}
            onChange={(e) =>
              set("didKind", e.target.value as InboundDraft["didKind"])
            }
          />
          {d.didKind !== "any" && (
            <Input
              id={id("did")}
              label="DID"
              mono
              spellCheck={false}
              value={d.did}
              error={v.errors.did}
              onChange={(e) => set("did", e.target.value)}
            />
          )}
        </div>
        <Select
          id={id("trunkId")}
          label="From trunk"
          value={d.trunkId === null ? "" : String(d.trunkId)}
          disabled={!trunks.ready}
          error={v.errors.trunkId}
          options={[
            { value: "", label: "Any trunk" },
            ...trunks.items.map((t) => ({
              value: String(t.id),
              label: t.name,
            })),
            ...(storedTrunkMissing
              ? [
                  {
                    value: String(d.trunkId),
                    label: `Trunk #${String(d.trunkId)}`,
                  },
                ]
              : []),
          ]}
          onChange={(e) => {
            const value = e.target.value;
            const t = trunks.items.find((x) => String(x.id) === value);
            set("trunkId", value === "" ? null : (t?.id ?? d.trunkId));
          }}
        />
        <div className="cf-two">
          <Select
            id={id("destinationKind")}
            label="Send to"
            value={d.destinationKind}
            error={v.errors.destinationKind}
            options={[
              { value: "extension", label: "Extension" },
              { value: "external", label: "External number" },
              { value: "sip_uri", label: "SIP URI" },
              { value: "ivr", label: "IVR menu" },
              { value: "queue", label: "Call queue" },
              { value: "time_condition", label: "Time condition" },
              { value: "ring_group", label: "Ring group" },
              { value: "voicemail", label: "Voicemail" },
              { value: "announcement", label: "Announcement" },
              { value: "paging", label: "Paging group" },
              { value: "conference", label: "Conference room" },
            ]}
            onChange={(e) =>
              // The target belongs to the kind: a name chosen for one kind
              // is not a valid target of another.
              setD((prev) => ({
                ...prev,
                destinationKind: e.target
                  .value as InboundDraft["destinationKind"],
                destination: "",
              }))
            }
          />
          {d.destinationKind === "queue" ? (
            <QueueSelect
              id={id("destination")}
              label={DESTINATION_LABEL.queue}
              value={d.destination}
              error={v.errors.destination}
              onChange={(target) => set("destination", target)}
            />
          ) : d.destinationKind === "time_condition" ? (
            <Select
              id={id("destination")}
              label={DESTINATION_LABEL.time_condition}
              value={d.destination}
              error={v.errors.destination}
              options={[
                { value: "", label: "Choose…" },
                ...(d.destination !== "" &&
                !timeConds.some((t) => t.name === d.destination)
                  ? [
                      {
                        value: d.destination,
                        label: `${d.destination} (not found)`,
                      },
                    ]
                  : []),
                ...timeConds.map((t) => ({ value: t.name, label: t.name })),
              ]}
              onChange={(e) => set("destination", e.target.value)}
            />
          ) : d.destinationKind === "ivr" ? (
            <Select
              id={id("destination")}
              label={DESTINATION_LABEL.ivr}
              value={d.destination}
              error={v.errors.destination}
              options={[
                { value: "", label: "Choose…" },
                ...(d.destination !== "" &&
                !menus.some((m) => m.name === d.destination)
                  ? [
                      {
                        value: d.destination,
                        label: `${d.destination} (not found)`,
                      },
                    ]
                  : []),
                ...menus.map((m) => ({ value: m.name, label: m.name })),
              ]}
              onChange={(e) => set("destination", e.target.value)}
            />
          ) : d.destinationKind === "conference" ? (
            <Select
              id={id("destination")}
              label={DESTINATION_LABEL.conference}
              value={d.destination}
              error={v.errors.destination}
              options={conferenceOptions(rooms, d.destination)}
              onChange={(e) => set("destination", e.target.value)}
            />
          ) : isListed(d.destinationKind) ? (
            <Select
              id={id("destination")}
              label={DESTINATION_LABEL[d.destinationKind]}
              value={d.destination}
              error={v.errors.destination}
              options={
                targetOptions(
                  d.destinationKind,
                  {
                    extensions: exts,
                    ringGroups,
                    announcements,
                    pagingGroups,
                    menus,
                  },
                  d.destination,
                ) ?? []
              }
              onChange={(e) => set("destination", e.target.value)}
            />
          ) : (
            <Input
              id={id("destination")}
              label={DESTINATION_LABEL[d.destinationKind]}
              mono
              value={d.destination}
              error={v.errors.destination}
              onChange={(e) => set("destination", e.target.value)}
            />
          )}
        </div>
        <fieldset className="cf-form__section">
          <legend className="az-eyebrow">More conditions</legend>
          <Input
            id={id("sipDomain")}
            label="SIP domain"
            mono
            value={d.sipDomain}
            error={v.errors.sipDomain}
            hint="Optional: match the Request-URI host."
            onChange={(e) => set("sipDomain", e.target.value)}
          />
          <div className="cf-two">
            <Input
              id={id("headerName")}
              label="Header name"
              mono
              value={d.headerName}
              error={v.errors.headerName}
              hint="Optional."
              onChange={(e) => set("headerName", e.target.value)}
            />
            <Input
              id={id("headerRegex")}
              label="Header regex"
              mono
              spellCheck={false}
              value={d.headerRegex}
              error={v.errors.headerRegex}
              onChange={(e) => set("headerRegex", e.target.value)}
            />
          </div>
          <Switch
            label="Enabled"
            labelPosition="end"
            checked={d.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
          />
          <MOHSelect
            id={id("mohClassId")}
            label="Music on hold"
            hint="What these callers hear when held. Default uses the global class."
            value={d.mohClassId}
            error={v.errors.mohClassId}
            onChange={(next) => set("mohClassId", next)}
          />
          <Switch
            label="Play music instead of ringback"
            hint="The caller hears the class while the destination rings, not a ringing tone"
            labelPosition="end"
            checked={d.mohRingback}
            onChange={(e) => set("mohRingback", e.target.checked)}
          />
        </fieldset>
        <TransformEditor
          form={form}
          base="callerIdTransform"
          legend="Caller ID rewrite"
          value={d.callerIdTransform}
          errors={v.errors}
          onChange={(t) => set("callerIdTransform", t)}
        />
        <ScheduleEditor
          form={form}
          value={d.schedule}
          errors={v.errors}
          onChange={(s) => set("schedule", s)}
        />
        <FormAlert message={v.formError} unmatched={v.unmatched} />
        <TrunksPending ready={trunks.ready} />
      </form>
    </RouteDrawer>
  );
}
