import { useEffect, useState } from "react";
import {
  deleteQueue,
  errorMessage,
  IVR_DESTINATION_LABEL,
  listAnnouncements,
  listExtensions,
  listIvrMenus,
  listMOHClasses,
  listPagingGroups,
  listQueues,
  listRingGroups,
  QUEUE_STRATEGY_LABEL,
  type Announcement,
  type Extension,
  type IvrDestination,
  type IvrMenu,
  type MOHClass,
  type PagingGroup,
  type Queue,
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
import { listTimeConditions, type TimeCondition } from "../api/timecond";
import { Can } from "../role";
import { QueueDrawer, type Choices } from "../forms/QueueForm";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Queue[] };

function useChoices(): { choices: Choices; error: string | null } {
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [ringGroups, setRingGroups] = useState<RingGroup[]>([]);
  const [announcements, setAnnouncements] = useState<Announcement[]>([]);
  const [pagingGroups, setPagingGroups] = useState<PagingGroup[]>([]);
  const [menus, setMenus] = useState<IvrMenu[]>([]);
  const [mohClasses, setMohClasses] = useState<MOHClass[]>([]);
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
    listIvrMenus(controller.signal).then(setMenus).catch(fail);
    listMOHClasses(controller.signal).then(setMohClasses).catch(fail);
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
      menus,
      timeConditions,
      all: extensions,
      mohClasses,
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
 * Call queues: callers wait on music and announcements and are offered to the
 * next free agent, by the queue's strategy.
 */
export function Queues() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const { choices, error: choicesError } = useChoices();
  const [deleting, setDeleting] = useState<Queue | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; queue: Queue } | null
  >(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listQueues(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  function replaceItems(fn: (items: Queue[]) => Queue[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );
  }

  async function onDelete(q: Queue) {
    try {
      await deleteQueue(q.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${q.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replaceItems((items) => items.filter((x) => x.id !== q.id));
    setDeleting(null);
    toast.show(`Queue ${q.name} deleted.`);
  }

  const newQueue = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New queue
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Queues"
        description="Callers wait on music, hear their position and are offered to the next free agent. Send calls to a queue from an inbound route, an IVR key, a ring group's failover or a transfer to its number. Agents log in and pause with feature codes or on the wallboard."
        actions={newQueue}
      />
      <div className="cf-stack">
        {choicesError && (
          <Alert tone="warn" title="Could not load every picker">
            {choicesError} Some choices below may be empty.
          </Alert>
        )}
        {list.status === "loading" && <Spinner label="Loading queues…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load the queues">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.items.length === 0 && (
          <EmptyState
            icon="headset"
            title="No queues yet"
            description="Create a queue, add the extensions that answer it and point an inbound route at it."
            action={newQueue}
          />
        )}
        {list.status === "ready" && list.items.length > 0 && (
          <ul className="cf-grid cf-grid--groups cf-plain" aria-label="Queues">
            {list.items.map((q) => (
              <QueueCard
                key={String(q.id)}
                queue={q}
                onEdit={() => setEditing({ kind: "edit", queue: q })}
                onDelete={() => setDeleting(q)}
              />
            ))}
          </ul>
        )}
      </div>

      {editing && (
        <QueueDrawer
          key={editing.kind === "edit" ? String(editing.queue.id) : "new"}
          queue={editing.kind === "edit" ? editing.queue : null}
          choices={choices}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            replaceItems((items) =>
              items.some((x) => x.id === saved.id)
                ? items.map((x) => (x.id === saved.id ? saved : x))
                : [...items, saved],
            );
            setEditing(null);
            toast.show(
              created
                ? `Queue ${saved.name} created.`
                : `Queue ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete queue ${deleting.name}?`}
          description="Callers waiting in this queue are no longer served by it."
          confirmLabel="Delete queue"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

function QueueCard({
  queue: q,
  onEdit,
  onDelete,
}: {
  queue: Queue;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const titleId = `queue-${String(q.id)}-name`;
  return (
    <li className="az-card cf-card" aria-labelledby={titleId}>
      <div className="cf-card__head cf-card__head--top">
        <div style={{ minWidth: 0 }}>
          <h2 className="cf-card__group-name" id={titleId}>
            {q.name}
          </h2>
          <div className="cf-card__sub">
            {q.number ? (
              <>
                Dial <span className="cf-mono">{q.number}</span>
              </>
            ) : (
              "Reached as a destination"
            )}{" "}
            · {QUEUE_STRATEGY_LABEL[q.strategy]}
          </div>
        </div>
        {!q.enabled && <Badge tone="warn">Disabled</Badge>}
      </div>
      <ul className="cf-members" aria-label={`Agents of ${q.name}`}>
        {q.members.map((m) => (
          <li className="cf-member" key={String(m.extensionId)}>
            <span className="cf-member__who">
              <span className="cf-mono">{m.number}</span>
              <span>{m.name}</span>
            </span>
            <span className="cf-member__meta">
              {m.dynamic ? "logs in" : "always"} · penalty {m.penalty}
            </span>
          </li>
        ))}
        {q.members.length === 0 && (
          <li className="cf-member">
            <span className="cf-member__who">No agents</span>
          </li>
        )}
      </ul>
      <div className="cf-failure">
        Too long: <strong>{destinationText(q.timeout)}</strong> · full:{" "}
        <strong>{destinationText(q.full)}</strong> · no agents:{" "}
        <strong>{destinationText(q.noAgents)}</strong>
      </div>
      <div className="cf-card__foot">
        <Button
          variant="secondary"
          size="sm"
          icon="pencil"
          aria-label={`Edit queue ${q.name}`}
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
            aria-label={`Delete queue ${q.name}`}
            onClick={onDelete}
          >
            Delete
          </Button>
        </Can>
      </div>
    </li>
  );
}
