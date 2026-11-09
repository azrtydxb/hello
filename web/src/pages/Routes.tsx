import {
  useEffect,
  useRef,
  useState,
  type DragEvent,
  type KeyboardEvent,
  type ReactNode,
} from "react";
import { useSearchParams } from "react-router";
import {
  deleteInboundRoute,
  deleteOutboundRoute,
  errorMessage,
  listExtensions,
  listInboundRoutes,
  listOutboundRoutes,
  listTrunks,
  updateInboundRoute,
  updateOutboundRoute,
  type Extension,
  type Id,
  type InboundRoute,
  type InboundRouteFields,
  type OutboundRoute,
  type RouteDirection,
  type Trunk,
} from "../api";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Icon,
  IconButton,
  LinkButton,
  PageHeader,
  Spinner,
  Switch,
  Tabs,
  useToast,
} from "../design/azrty/components";
import { useOrderedList } from "../useOrderedList";
import { formatSchedule, formatTransform } from "./callflow/format";
import { Can } from "../role";
import {
  InboundDrawer,
  OutboundDrawer,
  type TrunkOptions,
} from "../forms/RouteForm";

const TAB_PREFIX = "routes";

type OrderedList<T extends { id: Id; position: number }> = ReturnType<
  typeof useOrderedList<T>
>;

type TrunkLoad =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready" };

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
  internal: { label: "Internal", icon: "arrow-left-right" },
  external: { label: "External", icon: "phone-forwarded" },
  sip_uri: { label: "SIP URI", icon: "at-sign" },
  ivr: { label: "IVR menu", icon: "list-tree" },
  queue: { label: "Call queue", icon: "headset" },
  time_condition: { label: "Time condition", icon: "clock" },
  ring_group: { label: "Ring group", icon: "users-round" },
  voicemail: { label: "Voicemail", icon: "voicemail" },
  announcement: { label: "Announcement", icon: "megaphone" },
  paging: { label: "Paging group", icon: "megaphone" },
  conference: { label: "Conference room", icon: "users" },
  directory: { label: "Dial-by-name directory", icon: "book-user" },
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
