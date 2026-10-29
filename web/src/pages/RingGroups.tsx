import { useEffect, useState } from "react";
import {
  deleteRingGroup,
  errorMessage,
  listExtensions,
  listRingGroups,
  RING_STRATEGY_LABEL,
  type Extension,
  type RingGroup,
} from "../api";
import {
  Alert,
  Avatar,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Icon,
  PageHeader,
  Spinner,
  useToast,
} from "../design/azrty/components";
import { UNKNOWN } from "./callflow/format";
import { Can } from "../role";
import { RingGroupDrawer, findExt, memberLabel } from "../forms/RingGroupForm";

type ListState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: T[] };

/** "Rings 25 s · respects DND", "Hunt · 5 s between members · rings 60 s". */
function groupMeta(g: RingGroup): string {
  return [
    g.hunt ? "Hunt" : null,
    g.memberDelay > 0 ? `${g.memberDelay} s between members` : null,
    `${g.hunt ? "rings" : "Rings"} ${g.ringTimeout} s`,
    g.ignoreDnd ? "ignores DND" : "respects DND",
  ]
    .filter(Boolean)
    .join(" · ");
}

/** What a member's row says on the right: its weight or its ring time. */
function memberExtra(g: RingGroup, m: RingGroup["members"][number]): string {
  if (g.strategy === "weighted") return `weight ${m.weight}`;
  if (m.delay > 0) return `${m.delay} s`;
  return UNKNOWN;
}

function failureText(g: RingGroup): string {
  switch (g.failureKind) {
    case "voicemail":
      return `Voicemail box ${g.failureTarget || UNKNOWN}`;
    case "external":
      return `External ${g.failureTarget || UNKNOWN}`;
    case "announcement":
      return `Announcement ${g.failureTarget || UNKNOWN}`;
    case "ivr":
      return `IVR menu ${g.failureTarget || UNKNOWN}`;
    case "queue":
      return `Queue ${g.failureTarget || UNKNOWN}`;
    case "time_condition":
      return `Time condition ${g.failureTarget || UNKNOWN}`;
    case "directory":
      return "Dial-by-name directory";
    case "extension":
      return `Extension ${g.failureTarget || UNKNOWN}`;
    case "ring_group":
      return `Ring group ${g.failureTarget || UNKNOWN}`;
    default:
      return "Hang up";
  }
}

/**
 * Ring and hunt groups as cards: strategy, members in ring order and the
 * failure destination; created and edited in a drawer.
 */
export function RingGroups() {
  const [list, setList] = useState<ListState<RingGroup>>({ status: "loading" });
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [extReady, setExtReady] = useState(false);
  const [extError, setExtError] = useState<string | null>(null);
  const [deleting, setDeleting] = useState<RingGroup | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; group: RingGroup } | null
  >(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    listRingGroups(controller.signal)
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
        if (!controller.signal.aborted) {
          setExtError(errorMessage(err));
        }
      });
    return () => controller.abort();
  }, []);

  function replaceItems(fn: (items: RingGroup[]) => RingGroup[]) {
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );
  }

  async function onDelete(group: RingGroup) {
    try {
      await deleteRingGroup(group.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${group.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    replaceItems((items) => items.filter((g) => g.id !== group.id));
    setDeleting(null);
    toast.show(`Ring group ${group.name} deleted.`);
  }

  const newGroup = (
    <Can>
      <Button
        icon="plus"
        disabled={list.status !== "ready"}
        onClick={() => setEditing({ kind: "new" })}
      >
        New ring group
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Ring groups"
        description="Ring several extensions for one call, then fall through to a failure destination."
        actions={newGroup}
      />
      <div className="cf-stack">
        {extError && (
          <Alert tone="warn" title="Could not load the extension list">
            {extError} Members show by id, and the member picker needs it.
          </Alert>
        )}
        {list.status === "loading" && <Spinner label="Loading ring groups…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load the groups">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.items.length === 0 && (
          <EmptyState
            icon="users-round"
            title="No ring groups yet"
            description="A ring group rings several extensions for one number, all at once or in turn."
            action={newGroup}
          />
        )}
        {list.status === "ready" && list.items.length > 0 && (
          <ul
            className="cf-grid cf-grid--groups cf-plain"
            aria-label="Ring groups"
          >
            {list.items.map((g) => (
              <GroupCard
                key={String(g.id)}
                group={g}
                extensions={extensions}
                onEdit={() => setEditing({ kind: "edit", group: g })}
                onDelete={() => setDeleting(g)}
              />
            ))}
          </ul>
        )}
      </div>

      {editing && (
        <RingGroupDrawer
          key={editing.kind === "edit" ? String(editing.group.id) : "new"}
          group={editing.kind === "edit" ? editing.group : null}
          extensions={extensions}
          extReady={extReady}
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
                ? `Ring group ${saved.name} created.`
                : `Ring group ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ring group ${deleting.name}?`}
          description="Routes and forwards that send calls to it stop working until they are changed."
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
  extensions,
  onEdit,
  onDelete,
}: {
  group: RingGroup;
  extensions: readonly Extension[];
  onEdit: () => void;
  onDelete: () => void;
}) {
  const titleId = `group-${String(g.id)}-name`;
  const members = g.members.slice().sort((a, b) => a.position - b.position);
  return (
    <li className="az-card cf-card" aria-labelledby={titleId}>
      <div className="cf-card__head cf-card__head--top">
        <div style={{ minWidth: 0 }}>
          <h2 className="cf-card__group-name" id={titleId}>
            {g.name}
          </h2>
          <div className="cf-card__sub">{groupMeta(g)}</div>
        </div>
        <Badge tone="pillar">
          {RING_STRATEGY_LABEL[g.strategy] ?? g.strategy}
        </Badge>
      </div>
      {members.length === 0 ? (
        <p className="cf-form__note">No members.</p>
      ) : (
        <ol
          className="cf-members"
          aria-label={`Members of ${g.name}, in ring order`}
        >
          {members.map((m, i) => {
            const ext = findExt(m.extensionId, extensions);
            return (
              <li className="cf-member" key={String(m.extensionId)}>
                <span className="cf-member__pos" aria-hidden="true">
                  {i + 1}
                </span>
                <span className="cf-member__who">
                  <Avatar name={ext?.name || ext?.number || "?"} size={26} />
                  <span>
                    <span className="cf-mono">
                      {memberLabel(m, extensions)}
                    </span>{" "}
                    {ext?.name ?? ""}
                  </span>
                </span>
                <span className="cf-member__extra">{memberExtra(g, m)}</span>
              </li>
            );
          })}
        </ol>
      )}
      <div className="cf-failure">
        <Icon name="corner-down-right" size={14} />
        If nobody answers: <strong>{failureText(g)}</strong>
      </div>
      <div className="cf-card__foot">
        <Button
          variant="secondary"
          size="sm"
          icon="pencil"
          aria-label={`Edit ring group ${g.name}`}
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
            aria-label={`Delete ring group ${g.name}`}
            onClick={onDelete}
          >
            Delete
          </Button>
        </Can>
      </div>
    </li>
  );
}
