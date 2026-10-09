import {
  useEffect,
  useRef,
  useState,
  type DragEvent,
  type FormEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { useSearchParams } from "react-router";
import {
  createInboundRoute,
  createOutboundRoute,
  deleteInboundRoute,
  deleteOutboundRoute,
  errorMessage,
  fieldErrors,
  listExtensions,
  listInboundRoutes,
  listOutboundRoutes,
  listTrunks,
  updateInboundRoute,
  updateOutboundRoute,
  type Extension,
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
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Icon,
  IconButton,
  Input,
  LinkButton,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Tabs,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { fieldId, mapFieldErrors, splitList, type ErrorMap } from "../forms";
import { useOrderedList } from "../useOrderedList";
import { formatSchedule, formatTransform } from "./callflow/format";
import { FormAlert } from "./callflow/ui";
import { Can } from "../role";
import { listVoiceAgents, type VoiceAgent } from "../api/voice";

const DEFAULT_FAILOVER = "408, 480, 500, 502, 503, 504";
const FORM_ID = "route-form";
const TAB_PREFIX = "routes";

type OrderedList<T extends { id: Id; position: number }> = ReturnType<
  typeof useOrderedList<T>
>;

type TrunkLoad =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready" };

/** The trunk list as the forms see it; `ready` is false until it has loaded. */
interface TrunkOptions {
  items: Trunk[];
  ready: boolean;
}

type Editing =
  | { kind: "new"; direction: RouteDirection }
  | { kind: "edit"; route: OutboundRoute; direction: "outbound" }
  | { kind: "edit"; route: InboundRoute; direction: "inbound" }
  | null;

type Deleting =
  | { direction: "outbound"; route: OutboundRoute }
  | { direction: "inbound"; route: InboundRoute }
  | null;

/** Routes: ordered outbound and inbound routes, on two tabs. */
export function RoutesPage() {
  const [params, setParams] = useSearchParams();
  const tab: RouteDirection =
    params.get("tab") === "inbound" ? "inbound" : "outbound";
  const [trunks, setTrunks] = useState<Trunk[]>([]);
  const [trunkLoad, setTrunkLoad] = useState<TrunkLoad>({ status: "loading" });
  const [trunkAttempt, setTrunkAttempt] = useState(0);
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [editing, setEditing] = useState<Editing>(null);
  const [deleting, setDeleting] = useState<Deleting>(null);
  const outbound = useOrderedList("outbound", listOutboundRoutes);
  const inbound = useOrderedList("inbound", listInboundRoutes);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listTrunks(controller.signal)
      .then((items) => {
        setTrunks(items);
        setTrunkLoad({ status: "ready" });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setTrunkLoad({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [trunkAttempt]);

  // Only for the destination's name on the Inbound tab; optional.
  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then(setExtensions)
      .catch(() => {});
    return () => controller.abort();
  }, []);

  const trunkOptions: TrunkOptions = {
    items: trunks,
    ready: trunkLoad.status === "ready",
  };
  const trunkName = (tid: Id) =>
    trunks.find((t) => String(t.id) === String(tid))?.name ?? `#${String(tid)}`;

  function select(id: RouteDirection, focus = false) {
    setParams(id === "outbound" ? {} : { tab: id }, { replace: true });
    if (focus) document.getElementById(`${TAB_PREFIX}-tab-${id}`)?.focus();
  }

  // The design system's Tabs leaves arrow keys to the page.
  function onTabKey(e: KeyboardEvent<HTMLDivElement>) {
    if ((e.target as HTMLElement).getAttribute("role") !== "tab") return;
    const order: RouteDirection[] = ["outbound", "inbound"];
    const i = order.indexOf(tab);
    let next: number | null = null;
    if (e.key === "ArrowRight") next = (i + 1) % order.length;
    if (e.key === "ArrowLeft") next = (i - 1 + order.length) % order.length;
    if (e.key === "Home") next = 0;
    if (e.key === "End") next = order.length - 1;
    const t = next === null ? undefined : order[next];
    if (t) {
      e.preventDefault();
      select(t, true);
    }
  }

  /** Flip a route's Enabled switch at once; put it back if the save fails. */
  async function toggle<R extends OutboundRoute | InboundRoute>(
    list: OrderedList<R>,
    save: (id: Id, patch: { enabled: boolean }) => Promise<R>,
    route: R,
    enabled: boolean,
  ) {
    list.setError(null);
    list.upsert({ ...route, enabled });
    try {
      list.upsert(await save(route.id, { enabled }));
      toast.show(`Route ${route.name} ${enabled ? "enabled" : "disabled"}.`);
    } catch (err) {
      list.upsert(route);
      list.setError(
        `Could not ${enabled ? "enable" : "disable"} ${route.name}: ${errorMessage(err)}`,
      );
    }
  }

  async function onDelete(d: NonNullable<Deleting>) {
    try {
      if (d.direction === "outbound") await deleteOutboundRoute(d.route.id);
      else await deleteInboundRoute(d.route.id);
    } catch (err) {
      throw new Error(
        `Could not delete ${d.route.name}: ${errorMessage(err)}`,
        {
          cause: err,
        },
      );
    }
    (d.direction === "outbound" ? outbound : inbound).remove(d.route.id);
    setDeleting(null);
    toast.show(`Route ${d.route.name} deleted.`);
  }

  const count = (l: { state: { status: string }; items: unknown[] }) =>
    l.state.status === "ready" ? l.items.length : undefined;

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Routes"
        description="Matched top to bottom; the first enabled match wins. Drag to reorder."
        actions={
          <>
            <LinkButton to="/routes/test" icon="flask-conical">
              Route tester
            </LinkButton>
            <Can>
              <Button
                icon="plus"
                onClick={() => setEditing({ kind: "new", direction: tab })}
              >
                New route
              </Button>
            </Can>
          </>
        }
      />
      {trunkLoad.status === "error" && (
        <Alert
          tone="bad"
          title="Could not load the trunk list"
          style={{ marginBottom: 20 }}
          action={
            <Button
              variant="secondary"
              size="sm"
              onClick={() => {
                setTrunkLoad({ status: "loading" });
                setTrunkAttempt((n) => n + 1);
              }}
            >
              Retry loading trunks
            </Button>
          }
        >
          {trunkLoad.message} Routes that pick a trunk cannot be saved until it
          loads.
        </Alert>
      )}
      <div onKeyDown={onTabKey}>
        <Tabs<RouteDirection>
          className="cf-tabs"
          aria-label="Route direction"
          idPrefix={TAB_PREFIX}
          value={tab}
          onChange={(id) => select(id)}
          items={[
            { id: "outbound", label: "Outbound", count: count(outbound) },
            { id: "inbound", label: "Inbound", count: count(inbound) },
          ]}
        />
      </div>
      <div
        role="tabpanel"
        id={`${TAB_PREFIX}-panel-${tab}`}
        aria-labelledby={`${TAB_PREFIX}-tab-${tab}`}
        tabIndex={0}
        className="cf-tabpanel cf-stack"
      >
        {tab === "outbound" ? (
          <RouteList
            what="outbound routes"
            caption="Outbound routes, in match order"
            list={outbound}
            onNew={() => setEditing({ kind: "new", direction: "outbound" })}
            onEdit={(route) =>
              setEditing({ kind: "edit", route, direction: "outbound" })
            }
            onDelete={(route) => setDeleting({ direction: "outbound", route })}
            onToggle={(r, on) =>
              void toggle(outbound, updateOutboundRoute, r, on)
            }
            columns={[
              {
                header: "Match",
                cell: (r) => (
                  <span className="cf-inline">
                    <Badge tone="outline">{r.matchKind}</Badge>
                    <span className="cf-match">{r.match}</span>
                  </span>
                ),
              },
              {
                header: "Number rewrite",
                mono: true,
                cell: (r) => formatTransform(r.numberTransform),
              },
              {
                header: "Trunks, in try order",
                cell: (r) => (
                  <ol className="cf-chips cf-plain">
                    {(r.trunks ?? []).map((tid, i) => (
                      <li key={String(tid)} className="cf-inline">
                        {i > 0 && <Icon name="chevron-right" size={12} />}
                        <span className="cf-chip">{trunkName(tid)}</span>
                      </li>
                    ))}
                  </ol>
                ),
              },
              { header: "Schedule", cell: (r) => formatSchedule(r.schedule) },
            ]}
            badge={(r) =>
              r.emergency ? <Badge tone="bad">Emergency</Badge> : null
            }
          />
        ) : (
          <RouteList
            what="inbound routes"
            caption="Inbound routes, in match order"
            list={inbound}
            onNew={() => setEditing({ kind: "new", direction: "inbound" })}
            onEdit={(route) =>
              setEditing({ kind: "edit", route, direction: "inbound" })
            }
            onDelete={(route) => setDeleting({ direction: "inbound", route })}
            onToggle={(r, on) =>
              void toggle(inbound, updateInboundRoute, r, on)
            }
            columns={[
              {
                header: "DID",
                cell: (r) => (
                  <span className="cf-inline">
                    <Badge tone="outline">{r.didKind}</Badge>
                    <span className="cf-match">
                      {r.didKind === "any" ? "*" : r.did}
                    </span>
                  </span>
                ),
              },
              {
                header: "Trunk",
                mono: true,
                cell: (r) =>
                  r.trunkId === null ? "any" : trunkName(r.trunkId),
              },
              { header: "Schedule", cell: (r) => formatSchedule(r.schedule) },
              {
                header: "Destination",
                cell: (r) => <Destination route={r} extensions={extensions} />,
              },
            ]}
          />
        )}
      </div>

      {editing?.direction === "outbound" && (
        <OutboundDrawer
          key={editing.kind === "edit" ? String(editing.route.id) : "new"}
          route={editing.kind === "edit" ? editing.route : null}
          trunks={trunkOptions}
          onClose={() => setEditing(null)}
          onSaved={(r, created) => {
            outbound.upsert(r);
            setEditing(null);
            toast.show(`Route ${r.name} ${created ? "created" : "saved"}.`);
          }}
        />
      )}
      {editing?.direction === "inbound" && (
        <InboundDrawer
          key={editing.kind === "edit" ? String(editing.route.id) : "new"}
          route={editing.kind === "edit" ? editing.route : null}
          trunks={trunkOptions}
          onClose={() => setEditing(null)}
          onSaved={(r, created) => {
            inbound.upsert(r);
            setEditing(null);
            toast.show(`Route ${r.name} ${created ? "created" : "saved"}.`);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete route ${deleting.route.name}?`}
          description="Calls stop matching it at once; the routes below it move up."
          confirmLabel="Delete route"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

const DESTINATION_KIND: Record<
  InboundRouteFields["destinationKind"],
  { label: string; icon: string }
> = {
  extension: { label: "Extension", icon: "user-round" },
  external: { label: "External", icon: "phone-forwarded" },
  sip_uri: { label: "SIP URI", icon: "at-sign" },
  voice_agent: { label: "Voice agent", icon: "bot" },
};

function Destination({
  route: r,
  extensions,
}: {
  route: InboundRoute;
  extensions: readonly Extension[];
}) {
  const kind = DESTINATION_KIND[r.destinationKind] ?? {
    label: r.destinationKind,
    icon: "arrow-right",
  };
  const name =
    r.destinationKind === "extension"
      ? extensions.find((e) => e.number === r.destination)?.name
      : undefined;
  return (
    <span className="cf-dest">
      <span className="cf-inline">
        <Icon name={kind.icon} size={14} />
        <span className="cf-match">{r.destination}</span>
      </span>
      <small>{name ? `${kind.label} · ${name}` : kind.label}</small>
    </span>
  );
}

// --- the ordered table --------------------------------------------------------

interface Column<T> {
  header: string;
  mono?: boolean;
  cell: (item: T) => ReactNode;
}

function RouteList<
  T extends { id: Id; name: string; enabled: boolean; position: number },
>({
  what,
  caption,
  list,
  columns,
  badge,
  onNew,
  onEdit,
  onDelete,
  onToggle,
}: {
  what: string;
  caption: string;
  list: OrderedList<T>;
  columns: Column<T>[];
  badge?: (item: T) => ReactNode;
  onNew: () => void;
  onEdit: (item: T) => void;
  onDelete: (item: T) => void;
  onToggle: (item: T, enabled: boolean) => void;
}) {
  const tableRef = useRef<HTMLTableElement>(null);
  const [focusAfter, setFocusAfter] = useState<{ id: Id; dir: string } | null>(
    null,
  );
  const [drag, setDrag] = useState<{ from: number; over: number } | null>(null);
  const items = list.items;

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
    if (await list.move(index, delta)) {
      setFocusAfter({ id: item.id, dir: delta < 0 ? "up" : "down" });
    }
  }

  function onDrop(e: DragEvent<HTMLTableRowElement>, to: number) {
    e.preventDefault();
    const from = drag?.from;
    setDrag(null);
    if (from === undefined || from === to) return;
    void list.move(from, to - from);
  }

  return (
    <>
      {list.error && (
        <Alert tone="bad" title="Routes not updated">
          {list.error}
        </Alert>
      )}
      {list.state.status === "loading" && (
        <Spinner label={`Loading ${what}…`} />
      )}
      {list.state.status === "error" && (
        <Alert tone="bad" title={`Could not load ${what}`}>
          {list.state.message}
        </Alert>
      )}
      {list.state.status === "ready" && items.length === 0 && (
        <EmptyState
          icon="route"
          title={`No ${what} yet.`}
          description="Routes are matched top to bottom; the first enabled match wins."
          action={
            <Can>
              <Button icon="plus" onClick={onNew}>
                New route
              </Button>
            </Can>
          }
        />
      )}
      {items.length > 0 && (
        <div className="az-table-wrap">
          <table className="az-table" ref={tableRef}>
            <caption className="visually-hidden">{caption}</caption>
            <thead>
              <tr>
                <th scope="col" style={{ width: 56 }}>
                  #
                </th>
                <th scope="col">Name</th>
                {columns.map((c) => (
                  <th scope="col" key={c.header}>
                    {c.header}
                  </th>
                ))}
                <th scope="col">Enabled</th>
                <th scope="col" className="az-table--right">
                  <span className="visually-hidden">Actions</span>
                </th>
              </tr>
            </thead>
            <tbody>
              {items.map((item, i) => (
                <tr
                  key={String(item.id)}
                  draggable={!list.busy}
                  className={
                    drag?.from === i
                      ? "cf-dragging"
                      : drag && drag.over === i
                        ? "cf-drop-target"
                        : undefined
                  }
                  onDragStart={(e) => {
                    e.dataTransfer.effectAllowed = "move";
                    e.dataTransfer.setData("text/plain", String(item.id));
                    setDrag({ from: i, over: i });
                  }}
                  onDragOver={(e) => {
                    if (!drag) return;
                    e.preventDefault();
                    if (drag.over !== i) setDrag({ ...drag, over: i });
                  }}
                  onDrop={(e) => onDrop(e, i)}
                  onDragEnd={() => setDrag(null)}
                >
                  <td>
                    <span className="cf-pos">
                      <Icon
                        name="grip-vertical"
                        size={14}
                        className="cf-grip"
                      />
                      <span className="az-table__mono">{i + 1}</span>
                    </span>
                  </td>
                  <th scope="row" className="az-table__primary">
                    <span className="cf-inline">
                      <button
                        type="button"
                        className="cf-name-btn"
                        onClick={() => onEdit(item)}
                      >
                        {item.name}
                      </button>
                      {badge?.(item)}
                    </span>
                  </th>
                  {columns.map((c) => (
                    <td
                      key={c.header}
                      className={c.mono ? "az-table__mono" : undefined}
                    >
                      {c.cell(item)}
                    </td>
                  ))}
                  <td>
                    <Can fallback={item.enabled ? "On" : "Off"}>
                      <Switch
                        aria-label={`${item.name} enabled`}
                        checked={item.enabled}
                        onChange={(e) => onToggle(item, e.target.checked)}
                      />
                    </Can>
                  </td>
                  <td className="az-table--right">
                    <span className="cf-row-actions">
                      <Can>
                        <IconButton
                          icon="arrow-up"
                          data-move={`${String(item.id)}:up`}
                          label={`Move ${item.name} up`}
                          disabled={list.busy || i === 0}
                          onClick={() => void move(item, i, -1)}
                        />
                      </Can>
                      <Can>
                        <IconButton
                          icon="arrow-down"
                          data-move={`${String(item.id)}:down`}
                          label={`Move ${item.name} down`}
                          disabled={list.busy || i === items.length - 1}
                          onClick={() => void move(item, i, 1)}
                        />
                      </Can>
                      <IconButton
                        icon="pencil"
                        label={`Edit ${item.name}`}
                        onClick={() => onEdit(item)}
                      />
                      <Can>
                        <IconButton
                          icon="trash-2"
                          label={`Delete ${item.name}`}
                          onClick={() => onDelete(item)}
                        />
                      </Can>
                    </span>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </>
  );
}

// --- shared form plumbing -----------------------------------------------------

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

function OutboundDrawer({
  route,
  trunks,
  onClose,
  onSaved,
}: {
  route: OutboundRoute | null;
  trunks: TrunkOptions;
  onClose: () => void;
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
  voice_agent: "Voice agent",
};

function InboundDrawer({
  route,
  trunks,
  onClose,
  onSaved,
}: {
  route: InboundRoute | null;
  trunks: TrunkOptions;
  onClose: () => void;
  onSaved: (r: InboundRoute, created: boolean) => void;
}) {
  const form = "in";
  const [d, setD] = useState(() => inboundDraft(route));
  // The destination picker needs the agents; a failure to load keeps the
  // stored value selectable and lists no new options.
  const [agents, setAgents] = useState<VoiceAgent[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    listVoiceAgents(controller.signal)
      .then(setAgents)
      .catch(() => {
        // The picker then offers only the stored agent, by name.
      });
    return () => controller.abort();
  }, []);
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
      const body = inboundBody(d);
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
              { value: "voice_agent", label: "Voice agent" },
            ]}
            onChange={(e) =>
              set(
                "destinationKind",
                e.target.value as InboundDraft["destinationKind"],
              )
            }
          />
          {d.destinationKind === "voice_agent" ? (
            <Select
              id={id("destination")}
              label="Voice agent"
              value={d.destination}
              error={v.errors.destination}
              options={[
                { value: "", label: "Choose…" },
                ...agents.map((a) => ({
                  value: a.name,
                  label: a.enabled ? a.name : `${a.name} (disabled)`,
                  disabled: !a.enabled,
                })),
              ]}
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
