import {
  useEffect,
  useRef,
  useState,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { Link, useSearchParams } from "react-router";
import {
  createInboundRoute,
  createOutboundRoute,
  deleteInboundRoute,
  deleteOutboundRoute,
  errorMessage,
  fieldErrors,
  listInboundRoutes,
  listOutboundRoutes,
  listTrunks,
  updateInboundRoute,
  updateOutboundRoute,
  type FieldError,
  type Id,
  type InboundRoute,
  type InboundRouteFields,
  type OutboundRoute,
  type OutboundRouteFields,
  type RouteDirection,
  type Schedule,
  type Trunk,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";
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
  Field,
  fieldId,
  FormError,
  mapFieldErrors,
  splitList,
  type ErrorMap,
} from "../forms";
import { useOrderedList } from "../useOrderedList";

const DEFAULT_FAILOVER = "408, 480, 500, 502, 503, 504";

const TABS: readonly { id: RouteDirection; label: string }[] = [
  { id: "outbound", label: "Outbound" },
  { id: "inbound", label: "Inbound" },
];

/** Routes: ordered outbound and inbound routes, on two tabs. */
export function RoutesPage() {
  const [params, setParams] = useSearchParams();
  const tab: RouteDirection =
    params.get("tab") === "inbound" ? "inbound" : "outbound";
  const [trunks, setTrunks] = useState<Trunk[]>([]);
  const tabRefs = useRef<Record<string, HTMLButtonElement | null>>({});

  useEffect(() => {
    const controller = new AbortController();
    listTrunks(controller.signal)
      .then(setTrunks)
      .catch(() => {
        // The route lists still work; the trunk picker is just empty.
      });
    return () => controller.abort();
  }, []);

  function select(id: RouteDirection, focus = false) {
    setParams(id === "outbound" ? {} : { tab: id }, { replace: true });
    if (focus) tabRefs.current[id]?.focus();
  }

  function onTabKey(e: KeyboardEvent<HTMLButtonElement>) {
    const i = TABS.findIndex((t) => t.id === tab);
    let next: number | null = null;
    if (e.key === "ArrowRight") next = (i + 1) % TABS.length;
    if (e.key === "ArrowLeft") next = (i - 1 + TABS.length) % TABS.length;
    if (e.key === "Home") next = 0;
    if (e.key === "End") next = TABS.length - 1;
    const t = next === null ? undefined : TABS[next];
    if (t) {
      e.preventDefault();
      select(t.id, true);
    }
  }

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Routes</h1>
      <p>
        <Link to="/routes/test">Test a number against these routes</Link>
      </p>
      <div role="tablist" aria-label="Route direction" className="tabs">
        {TABS.map((t) => (
          <button
            key={t.id}
            ref={(el) => {
              tabRefs.current[t.id] = el;
            }}
            type="button"
            role="tab"
            id={`tab-${t.id}`}
            aria-selected={tab === t.id}
            aria-controls={`panel-${t.id}`}
            tabIndex={tab === t.id ? 0 : -1}
            onClick={() => select(t.id)}
            onKeyDown={onTabKey}
          >
            {t.label}
          </button>
        ))}
      </div>
      <div
        role="tabpanel"
        id={`panel-${tab}`}
        aria-labelledby={`tab-${tab}`}
        tabIndex={0}
        className="tabpanel"
      >
        {tab === "outbound" ? (
          <OutboundPanel trunks={trunks} />
        ) : (
          <InboundPanel trunks={trunks} />
        )}
      </div>
    </section>
  );
}

// --- the ordered table --------------------------------------------------------

interface Column<T> {
  header: string;
  cell: (item: T) => ReactNode;
}

function OrderedTable<T extends { id: Id; name: string; enabled: boolean }>({
  caption,
  items,
  columns,
  busy,
  onMove,
  onEdit,
  onDelete,
}: {
  caption: string;
  items: T[];
  columns: Column<T>[];
  busy: boolean;
  onMove: (index: number, delta: number) => Promise<boolean>;
  onEdit: (item: T) => void;
  onDelete: (item: T) => Promise<void>;
}) {
  const tableRef = useRef<HTMLTableElement>(null);
  const [focusAfter, setFocusAfter] = useState<{ id: Id; dir: string } | null>(
    null,
  );

  // Keep focus on the moved route's control (or its other arrow at an edge).
  useEffect(() => {
    if (!focusAfter || !tableRef.current) return;
    const find = (dir: string) =>
      tableRef.current?.querySelector<HTMLButtonElement>(
        `button[data-move="${String(focusAfter.id)}:${dir}"]`,
      );
    const preferred = find(focusAfter.dir);
    const other = find(focusAfter.dir === "up" ? "down" : "up");
    (preferred && !preferred.disabled ? preferred : other)?.focus();
    setFocusAfter(null);
  }, [focusAfter, items]);

  async function move(item: T, index: number, delta: number) {
    if (await onMove(index, delta)) {
      setFocusAfter({ id: item.id, dir: delta < 0 ? "up" : "down" });
    }
  }

  return (
    <table ref={tableRef}>
      <caption className="visually-hidden">{caption}</caption>
      <thead>
        <tr>
          <th scope="col">Order</th>
          <th scope="col">Name</th>
          {columns.map((c) => (
            <th scope="col" key={c.header}>
              {c.header}
            </th>
          ))}
          <th scope="col">Enabled</th>
          <th scope="col">
            <span className="visually-hidden">Actions</span>
          </th>
        </tr>
      </thead>
      <tbody>
        {items.map((item, i) => (
          <tr key={item.id}>
            <td>{i + 1}</td>
            <th scope="row">{item.name}</th>
            {columns.map((c) => (
              <td key={c.header}>{c.cell(item)}</td>
            ))}
            <td>{item.enabled ? "Yes" : "No"}</td>
            <td className="row-actions">
              <button
                type="button"
                data-move={`${String(item.id)}:up`}
                aria-label={`Move ${item.name} up`}
                disabled={busy || i === 0}
                onClick={() => void move(item, i, -1)}
              >
                <span aria-hidden="true">↑</span> Up
              </button>
              <button
                type="button"
                data-move={`${String(item.id)}:down`}
                aria-label={`Move ${item.name} down`}
                disabled={busy || i === items.length - 1}
                onClick={() => void move(item, i, 1)}
              >
                <span aria-hidden="true">↓</span> Down
              </button>
              <button
                type="button"
                aria-label={`Edit ${item.name}`}
                onClick={() => onEdit(item)}
              >
                Edit
              </button>
              <ConfirmButton
                label="Delete"
                accessibleLabel={`Delete ${item.name}`}
                prompt={`Delete route ${item.name}?`}
                confirmLabel="Delete route"
                onConfirm={() => onDelete(item)}
              />
            </td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function ListStatus({
  state,
  what,
}: {
  state: { status: string; message?: string };
  what: string;
}) {
  if (state.status === "loading") {
    return (
      <p role="status" aria-live="polite">
        Loading {what}…
      </p>
    );
  }
  if (state.status === "error") {
    return (
      <div role="alert" className="error">
        <strong>Could not load {what}.</strong>
        <p>{state.message}</p>
      </div>
    );
  }
  return null;
}

function summarizeTransform(t: {
  strip?: number;
  prefix?: string;
  regex?: string;
  template?: string;
}): string {
  const parts: string[] = [];
  if (t.strip) parts.push(`strip ${t.strip}`);
  if (t.prefix) parts.push(`prefix ${t.prefix}`);
  if (t.regex) parts.push(`${t.regex} → ${t.template ?? ""}`);
  return parts.length ? parts.join(", ") : "unchanged";
}

type Editing<T> = { kind: "new" } | { kind: "edit"; route: T } | null;

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

function OutboundPanel({ trunks }: { trunks: Trunk[] }) {
  const list = useOrderedList("outbound", listOutboundRoutes);
  const [editing, setEditing] = useState<Editing<OutboundRoute>>(null);
  const trunkName = (tid: Id) =>
    trunks.find((t) => String(t.id) === String(tid))?.name ?? `#${String(tid)}`;

  return (
    <>
      {editing ? (
        <OutboundForm
          key={editing.kind === "edit" ? String(editing.route.id) : "new"}
          route={editing.kind === "edit" ? editing.route : null}
          trunks={trunks}
          onCancel={() => setEditing(null)}
          onSaved={(r) => {
            list.upsert(r);
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
            New outbound route
          </button>
        </p>
      )}
      <h2>Outbound routes</h2>
      <p className="muted">
        Calls from extensions to external numbers take the first route that
        matches, top to bottom.
      </p>
      {list.error && (
        <p role="alert" className="error">
          {list.error}
        </p>
      )}
      <ListStatus state={list.state} what="outbound routes" />
      {list.state.status === "ready" && list.items.length === 0 && (
        <p className="muted">No outbound routes yet.</p>
      )}
      {list.items.length > 0 && (
        <div className="table-wrap">
          <OrderedTable
            caption="Outbound routes, in match order"
            items={list.items}
            busy={list.busy}
            onMove={list.move}
            onEdit={(route) => setEditing({ kind: "edit", route })}
            onDelete={async (r) => {
              try {
                await deleteOutboundRoute(r.id);
                list.remove(r.id);
              } catch (err) {
                list.setError(
                  `Could not delete ${r.name}: ${errorMessage(err)}`,
                );
              }
            }}
            columns={[
              {
                header: "Match",
                cell: (r) => (
                  <>
                    {r.matchKind} <code>{r.match}</code>
                  </>
                ),
              },
              {
                header: "Number",
                cell: (r) => summarizeTransform(r.numberTransform ?? {}),
              },
              {
                header: "Trunks",
                cell: (r) => (r.trunks ?? []).map(trunkName).join(" → "),
              },
              {
                header: "Schedule",
                cell: (r) => (r.schedule ? r.schedule.timeZone : "always"),
              },
              {
                header: "Emergency",
                cell: (r) => (r.emergency ? "Yes" : ""),
              },
            ]}
          />
        </div>
      )}
    </>
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
      const mapped = mapFieldErrors(fieldErrors(err), known, {
        aliasTransforms: true,
      });
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
    },
  };
}

function OutboundForm({
  route,
  trunks,
  onCancel,
  onSaved,
}: {
  route: OutboundRoute | null;
  trunks: Trunk[];
  onCancel: () => void;
  onSaved: (r: OutboundRoute) => void;
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
      onSaved(
        route
          ? await updateOutboundRoute(route.id, body)
          : await createOutboundRoute(body),
      );
    } catch (err) {
      v.serverError(err, outboundKeys(d));
      setBusy(false);
    }
  }

  return (
    <form
      className="inline-form"
      aria-labelledby="out-form-title"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="out-form-title">
        {route ? `Edit outbound route ${route.name}` : "New outbound route"}
      </h2>
      <div className="fields">
        <Field id={id("name")} label="Name" error={v.errors.name}>
          {(p) => (
            <input
              {...p}
              value={d.name}
              onChange={(e) => set("name", e.target.value)}
            />
          )}
        </Field>
        <Field id={id("matchKind")} label="Match by" error={v.errors.matchKind}>
          {(p) => (
            <select
              {...p}
              value={d.matchKind}
              onChange={(e) =>
                set("matchKind", e.target.value as OutboundDraft["matchKind"])
              }
            >
              <option value="prefix">Prefix</option>
              <option value="regex">Regex (RE2)</option>
            </select>
          )}
        </Field>
        <Field
          id={id("match")}
          label={d.matchKind === "regex" ? "Match regex" : "Match prefix"}
          error={v.errors.match}
          hint={d.matchKind === "regex" ? "e.g. ^05[0-9]{8}$" : "e.g. 00"}
        >
          {(p) => (
            <input
              {...p}
              className="mono"
              spellCheck={false}
              value={d.match}
              onChange={(e) => set("match", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("sourceExtensions")}
          label="From extensions"
          error={v.errors.sourceExtensions}
          hint="Comma-separated; empty means every extension."
        >
          {(p) => (
            <input
              {...p}
              value={d.sourceExtensions}
              onChange={(e) => set("sourceExtensions", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("failoverCodes")}
          label="Fail over on"
          error={v.errors.failoverCodes}
          hint="SIP codes that move on to the next trunk."
        >
          {(p) => (
            <input
              {...p}
              value={d.failoverCodes}
              onChange={(e) => set("failoverCodes", e.target.value)}
            />
          )}
        </Field>
        <div className="field checkbox">
          <input
            id={id("emergency")}
            type="checkbox"
            checked={d.emergency}
            onChange={(e) => set("emergency", e.target.checked)}
          />
          <label htmlFor={id("emergency")}>Emergency route</label>
        </div>
        <div className="field checkbox">
          <input
            id={id("enabled")}
            type="checkbox"
            checked={d.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
          />
          <label htmlFor={id("enabled")}>Enabled</label>
        </div>
      </div>

      <TrunkPicker
        form={form}
        trunks={trunks}
        value={d.trunks}
        error={v.errors.trunks}
        onChange={(t) => set("trunks", t)}
      />
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

      <FormError message={v.formError} unmatched={v.unmatched} />
      <div className="actions start">
        <button type="submit" className="primary" disabled={busy}>
          {route ? "Save route" : "Create route"}
        </button>
        <button type="button" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}

/** Ordered trunk choice for an outbound route: tried top to bottom. */
function TrunkPicker({
  form,
  trunks,
  value,
  error,
  onChange,
}: {
  form: string;
  trunks: Trunk[];
  value: Id[];
  error?: string;
  onChange: (v: Id[]) => void;
}) {
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
    <fieldset className="group" aria-describedby={error ? errorId : undefined}>
      <legend>Trunks, in try order</legend>
      {error && (
        <p id={errorId} className="field-error">
          {error}
        </p>
      )}
      {chosen.length === 0 ? (
        <p className="hint">No trunks chosen.</p>
      ) : (
        <ol className="picked">
          {chosen.map((t, i) => (
            <li key={String(t.id)}>
              <span>{t.name}</span>
              <button
                type="button"
                aria-label={`Try ${t.name} earlier`}
                disabled={i === 0}
                onClick={() => swap(i, i - 1)}
              >
                <span aria-hidden="true">↑</span>
              </button>
              <button
                type="button"
                aria-label={`Try ${t.name} later`}
                disabled={i === chosen.length - 1}
                onClick={() => swap(i, i + 1)}
              >
                <span aria-hidden="true">↓</span>
              </button>
              <button
                type="button"
                aria-label={`Remove ${t.name}`}
                onClick={() => onChange(value.filter((_, j) => j !== i))}
              >
                Remove
              </button>
            </li>
          ))}
        </ol>
      )}
      <div className="fields">
        <div className="field">
          <label htmlFor={selectId}>Add trunk</label>
          <select
            id={selectId}
            value={pick}
            aria-invalid={error ? true : undefined}
            aria-describedby={error ? errorId : undefined}
            onChange={(e) => setPick(e.target.value)}
          >
            <option value="">Choose…</option>
            {available.map((t) => (
              <option key={String(t.id)} value={String(t.id)}>
                {t.name}
              </option>
            ))}
          </select>
        </div>
        <button
          type="button"
          className="align-end"
          disabled={pick === ""}
          onClick={() => {
            const t = trunks.find((x) => String(x.id) === pick);
            if (t) onChange([...value, t.id]);
            setPick("");
          }}
        >
          Add
        </button>
      </div>
    </fieldset>
  );
}

// --- inbound ------------------------------------------------------------------

interface InboundDraft {
  name: string;
  didKind: InboundRouteFields["didKind"];
  did: string;
  trunkId: string;
  sipDomain: string;
  headerName: string;
  headerRegex: string;
  schedule: Schedule | null;
  callerIdTransform: TransformDraft;
  destinationKind: InboundRouteFields["destinationKind"];
  destination: string;
  enabled: boolean;
}

function inboundDraft(r: InboundRoute | null): InboundDraft {
  return {
    name: r?.name ?? "",
    didKind: r?.didKind ?? "exact",
    did: r?.did ?? "",
    trunkId:
      r?.trunkId === null || r?.trunkId === undefined ? "" : String(r.trunkId),
    sipDomain: r?.sipDomain ?? "",
    headerName: r?.headerName ?? "",
    headerRegex: r?.headerRegex ?? "",
    schedule: r?.schedule ?? null,
    callerIdTransform: transformDraft(r?.callerIdTransform),
    destinationKind: r?.destinationKind ?? "extension",
    destination: r?.destination ?? "",
    enabled: r?.enabled ?? true,
  };
}

function inboundBody(d: InboundDraft, trunks: Trunk[]): InboundRouteFields {
  const trunk = trunks.find((t) => String(t.id) === d.trunkId);
  return {
    name: d.name.trim(),
    didKind: d.didKind,
    did: d.didKind === "any" ? "" : d.did,
    trunkId: d.trunkId === "" ? null : (trunk?.id ?? d.trunkId),
    sipDomain: d.sipDomain,
    headerName: d.headerName,
    headerRegex: d.headerRegex,
    schedule: d.schedule,
    callerIdTransform: toTransform(d.callerIdTransform),
    destinationKind: d.destinationKind,
    destination: d.destination.trim(),
    enabled: d.enabled,
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
  if (d.destination.trim() === "") e.destination = "Enter where calls go.";
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
    ...transformKeys("callerIdTransform"),
    ...scheduleKeys(d.schedule),
  ];
}

const DESTINATION_LABEL: Record<InboundDraft["destinationKind"], string> = {
  extension: "Extension number",
  external: "External number",
  sip_uri: "SIP URI",
};

function InboundPanel({ trunks }: { trunks: Trunk[] }) {
  const list = useOrderedList("inbound", listInboundRoutes);
  const [editing, setEditing] = useState<Editing<InboundRoute>>(null);
  const trunkName = (tid: Id | null) =>
    tid === null
      ? "any"
      : (trunks.find((t) => String(t.id) === String(tid))?.name ??
        `#${String(tid)}`);

  return (
    <>
      {editing ? (
        <InboundForm
          key={editing.kind === "edit" ? String(editing.route.id) : "new"}
          route={editing.kind === "edit" ? editing.route : null}
          trunks={trunks}
          onCancel={() => setEditing(null)}
          onSaved={(r) => {
            list.upsert(r);
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
            New inbound route
          </button>
        </p>
      )}
      <h2>Inbound routes</h2>
      <p className="muted">
        Calls arriving from trunks take the first route that matches, top to
        bottom.
      </p>
      {list.error && (
        <p role="alert" className="error">
          {list.error}
        </p>
      )}
      <ListStatus state={list.state} what="inbound routes" />
      {list.state.status === "ready" && list.items.length === 0 && (
        <p className="muted">No inbound routes yet.</p>
      )}
      {list.items.length > 0 && (
        <div className="table-wrap">
          <OrderedTable
            caption="Inbound routes, in match order"
            items={list.items}
            busy={list.busy}
            onMove={list.move}
            onEdit={(route) => setEditing({ kind: "edit", route })}
            onDelete={async (r) => {
              try {
                await deleteInboundRoute(r.id);
                list.remove(r.id);
              } catch (err) {
                list.setError(
                  `Could not delete ${r.name}: ${errorMessage(err)}`,
                );
              }
            }}
            columns={[
              {
                header: "DID",
                cell: (r) =>
                  r.didKind === "any" ? (
                    "any"
                  ) : (
                    <>
                      {r.didKind} <code>{r.did}</code>
                    </>
                  ),
              },
              { header: "Trunk", cell: (r) => trunkName(r.trunkId) },
              {
                header: "Destination",
                cell: (r) => (
                  <>
                    {DESTINATION_LABEL[r.destinationKind] ?? r.destinationKind}{" "}
                    <code>{r.destination}</code>
                  </>
                ),
              },
              {
                header: "Schedule",
                cell: (r) => (r.schedule ? r.schedule.timeZone : "always"),
              },
            ]}
          />
        </div>
      )}
    </>
  );
}

function InboundForm({
  route,
  trunks,
  onCancel,
  onSaved,
}: {
  route: InboundRoute | null;
  trunks: Trunk[];
  onCancel: () => void;
  onSaved: (r: InboundRoute) => void;
}) {
  const form = "in";
  const [d, setD] = useState(() => inboundDraft(route));
  const [busy, setBusy] = useState(false);
  const v = useServerErrors();
  const set = <K extends keyof InboundDraft>(k: K, value: InboundDraft[K]) =>
    setD((prev) => ({ ...prev, [k]: value }));
  const id = (k: string) => fieldId(form, k);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (v.clientErrors(validateInbound(d))) return;
    setBusy(true);
    try {
      const body = inboundBody(d, trunks);
      onSaved(
        route
          ? await updateInboundRoute(route.id, body)
          : await createInboundRoute(body),
      );
    } catch (err) {
      v.serverError(err, inboundKeys(d));
      setBusy(false);
    }
  }

  return (
    <form
      className="inline-form"
      aria-labelledby="in-form-title"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="in-form-title">
        {route ? `Edit inbound route ${route.name}` : "New inbound route"}
      </h2>
      <div className="fields">
        <Field id={id("name")} label="Name" error={v.errors.name}>
          {(p) => (
            <input
              {...p}
              value={d.name}
              onChange={(e) => set("name", e.target.value)}
            />
          )}
        </Field>
        <Field id={id("didKind")} label="Match DID" error={v.errors.didKind}>
          {(p) => (
            <select
              {...p}
              value={d.didKind}
              onChange={(e) =>
                set("didKind", e.target.value as InboundDraft["didKind"])
              }
            >
              <option value="exact">Exactly</option>
              <option value="prefix">By prefix</option>
              <option value="regex">By regex (RE2)</option>
              <option value="any">Any DID</option>
            </select>
          )}
        </Field>
        {d.didKind !== "any" && (
          <Field id={id("did")} label="DID" error={v.errors.did}>
            {(p) => (
              <input
                {...p}
                className="mono"
                spellCheck={false}
                value={d.did}
                onChange={(e) => set("did", e.target.value)}
              />
            )}
          </Field>
        )}
        <Field id={id("trunkId")} label="From trunk" error={v.errors.trunkId}>
          {(p) => (
            <select
              {...p}
              value={d.trunkId}
              onChange={(e) => set("trunkId", e.target.value)}
            >
              <option value="">Any trunk</option>
              {trunks.map((t) => (
                <option key={String(t.id)} value={String(t.id)}>
                  {t.name}
                </option>
              ))}
            </select>
          )}
        </Field>
        <Field
          id={id("sipDomain")}
          label="SIP domain"
          error={v.errors.sipDomain}
          hint="Optional: match the Request-URI host."
        >
          {(p) => (
            <input
              {...p}
              value={d.sipDomain}
              onChange={(e) => set("sipDomain", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("headerName")}
          label="Header name"
          error={v.errors.headerName}
          hint="Optional."
        >
          {(p) => (
            <input
              {...p}
              value={d.headerName}
              onChange={(e) => set("headerName", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("headerRegex")}
          label="Header regex"
          error={v.errors.headerRegex}
        >
          {(p) => (
            <input
              {...p}
              className="mono"
              spellCheck={false}
              value={d.headerRegex}
              onChange={(e) => set("headerRegex", e.target.value)}
            />
          )}
        </Field>
        <Field
          id={id("destinationKind")}
          label="Send to"
          error={v.errors.destinationKind}
        >
          {(p) => (
            <select
              {...p}
              value={d.destinationKind}
              onChange={(e) =>
                set(
                  "destinationKind",
                  e.target.value as InboundDraft["destinationKind"],
                )
              }
            >
              <option value="extension">Extension</option>
              <option value="external">External number</option>
              <option value="sip_uri">SIP URI</option>
            </select>
          )}
        </Field>
        <Field
          id={id("destination")}
          label={DESTINATION_LABEL[d.destinationKind]}
          error={v.errors.destination}
        >
          {(p) => (
            <input
              {...p}
              value={d.destination}
              onChange={(e) => set("destination", e.target.value)}
            />
          )}
        </Field>
        <div className="field checkbox">
          <input
            id={id("enabled")}
            type="checkbox"
            checked={d.enabled}
            onChange={(e) => set("enabled", e.target.checked)}
          />
          <label htmlFor={id("enabled")}>Enabled</label>
        </div>
      </div>
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
      <FormError message={v.formError} unmatched={v.unmatched} />
      <div className="actions start">
        <button type="submit" className="primary" disabled={busy}>
          {route ? "Save route" : "Create route"}
        </button>
        <button type="button" disabled={busy} onClick={onCancel}>
          Cancel
        </button>
      </div>
    </form>
  );
}
