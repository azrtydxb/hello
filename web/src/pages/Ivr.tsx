import { useEffect, useState } from "react";
import {
  deleteIvrMenu,
  errorMessage,
  IVR_DESTINATION_LABEL,
  listAnnouncements,
  listExtensions,
  listIvrMenus,
  listPagingGroups,
  listRingGroups,
  type Announcement,
  type Extension,
  type IvrDestination,
  type IvrMenu,
  type PagingGroup,
  type RingGroup,
} from "../api";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  PageHeader,
  Spinner,
  useToast,
} from "../design/azrty/components";
import { type DestinationChoices } from "../components/IvrDestination";
import { listTimeConditions, type TimeCondition } from "../api/timecond";
import { Can } from "../role";
import { IvrDrawer } from "../forms/IvrForm";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: IvrMenu[] };

/** One load that fails soft: the pickers just stay empty. */
function useChoices(): { choices: DestinationChoices; error: string | null } {
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [ringGroups, setRingGroups] = useState<RingGroup[]>([]);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [pagingGroups, setPagingGroups] = useState<PagingGroup[]>([]);
  const [timeConditions, setTimeConditions] = useState<TimeCondition[]>([]);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    const fail = (err: unknown) => {
      if (!controller.signal.aborted) setError(errorMessage(err));
    };
    listExtensions(controller.signal).then(setExtensions).catch(fail);
    listRingGroups(controller.signal).then(setRingGroups).catch(fail);
    listAnnouncements(controller.signal).then(setAnnouncements).catch(fail);
    listPagingGroups(controller.signal).then(setPagingGroups).catch(fail);
    listTimeConditions(controller.signal)
      .then(setTimeConditions)
      .catch(() => undefined); // the picker stays empty; saving still works
    return () => controller.abort();
  }, []);
  return {
    choices: {
      extensions,
      ringGroups,
      announcements,
      pagingGroups,
      menus: [],
      timeConditions,
    },
    error,
  };
}

function destinationText(d: IvrDestination): string {
  return d.kind === "hangup"
    ? IVR_DESTINATION_LABEL.hangup
    : `${IVR_DESTINATION_LABEL[d.kind]} ${d.target}`;
}

/**
 * IVR / auto-attendant menus: a greeting, keys that lead to destinations,
 * timeouts and retries. Prompts are announcements.
 */
export function Ivr() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const { choices, error: choicesError } = useChoices();
  const [deleting, setDeleting] = useState<IvrMenu | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; menu: IvrMenu } | null
  >(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listIvrMenus(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  const menus = list.status === "ready" ? list.items : [];
  const all: DestinationChoices = { ...choices, menus };

  function replaceItems(fn: (items: IvrMenu[]) => IvrMenu[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );
  }

  async function onDelete(menu: IvrMenu) {
    try {
      await deleteIvrMenu(menu.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${menu.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replaceItems((items) => items.filter((m) => m.id !== menu.id));
    setDeleting(null);
    toast.show(`IVR menu ${menu.name} deleted.`);
  }

  const newMenu = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New IVR menu
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="IVR"
        description="An auto-attendant answers, plays a greeting and sends the caller where the pressed key points. Send calls to a menu from an inbound route, a ring group's failover, a feature code or another menu, or dial its number."
        actions={newMenu}
      />
      <div className="cf-stack">
        {choicesError && (
          <Alert tone="warn" title="Could not load every picker">
            {choicesError} Some choices below may be empty.
          </Alert>
        )}
        {list.status === "loading" && <Spinner label="Loading IVR menus…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load the IVR menus">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.items.length === 0 && (
          <EmptyState
            icon="list-tree"
            title="No IVR menus yet"
            description="Upload or generate a greeting under Announcements, then create a menu that uses it."
            action={newMenu}
          />
        )}
        {list.status === "ready" && list.items.length > 0 && (
          <ul
            className="cf-grid cf-grid--groups cf-plain"
            aria-label="IVR menus"
          >
            {list.items.map((m) => (
              <MenuCard
                key={String(m.id)}
                menu={m}
                onEdit={() => setEditing({ kind: "edit", menu: m })}
                onDelete={() => setDeleting(m)}
              />
            ))}
          </ul>
        )}
      </div>

      {editing && (
        <IvrDrawer
          key={editing.kind === "edit" ? String(editing.menu.id) : "new"}
          menu={editing.kind === "edit" ? editing.menu : null}
          choices={all}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            replaceItems((items) =>
              items.some((m) => m.id === saved.id)
                ? items.map((m) => (m.id === saved.id ? saved : m))
                : [...items, saved],
            );
            setEditing(null);
            toast.show(
              created
                ? `IVR menu ${saved.name} created.`
                : `IVR menu ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete IVR menu ${deleting.name}?`}
          description="Callers sent to this menu are no longer answered by it."
          confirmLabel="Delete menu"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

function MenuCard({
  menu: m,
  onEdit,
  onDelete,
}: {
  menu: IvrMenu;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const titleId = `ivr-${String(m.id)}-name`;
  return (
    <li className="az-card cf-card" aria-labelledby={titleId}>
      <div className="cf-card__head cf-card__head--top">
        <div style={{ minWidth: 0 }}>
          <h2 className="cf-card__group-name" id={titleId}>
            {m.name}
          </h2>
          <div className="cf-card__sub">
            {m.number ? (
              <>
                Dial <span className="cf-mono">{m.number}</span>
              </>
            ) : (
              "Reached as a destination"
            )}{" "}
            · greeting <span className="cf-mono">{m.greetingName}</span>
          </div>
        </div>
        {!m.enabled && <Badge tone="warn">Disabled</Badge>}
      </div>
      <ul className="cf-members" aria-label={`Keys of ${m.name}`}>
        {m.options.map((o) => (
          <li className="cf-member" key={o.digit}>
            <span className="cf-member__who">
              <span className="cf-mono">{o.digit}</span>
              <span>{destinationText(o)}</span>
            </span>
          </li>
        ))}
        {m.options.length === 0 && (
          <li className="cf-member">
            <span className="cf-member__who">No keys</span>
          </li>
        )}
      </ul>
      <div className="cf-failure">
        No key: <strong>{destinationText(m.timeout)}</strong> · wrong key:{" "}
        <strong>{destinationText(m.invalid)}</strong>
        {m.allowDirectDial ? " · extensions can be dialled" : ""}
      </div>
      <div className="cf-card__foot">
        <Button
          variant="secondary"
          size="sm"
          icon="pencil"
          aria-label={`Edit IVR menu ${m.name}`}
          onClick={onEdit}
        >
          Edit
        </Button>
        <Can>
          <Button
            variant="ghost"
            size="sm"
            icon="trash-2"
            className="cf-danger"
            aria-label={`Delete IVR menu ${m.name}`}
            onClick={onDelete}
          >
            Delete
          </Button>
        </Can>
      </div>
    </li>
  );
}
