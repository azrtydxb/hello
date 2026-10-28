import { useState } from "react";
import {
  controlConference,
  deleteConference,
  errorMessage,
  listConferences,
  type Conference,
  type ConferenceAction,
  type ConferenceParticipant,
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
import { Can } from "../role";
import { usePolling } from "../usePolling";
import { RoomDrawer } from "../forms/ConferenceForm";

/** How often the live view reloads. */
const LIVE_MS = 2000;

export function Conferences() {
  const rooms = usePolling(listConferences, LIVE_MS);
  const [deleting, setDeleting] = useState<Conference | null>(null);
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; room: Conference } | null
  >(null);
  const [notice, setNotice] = useState<string | null>(null);
  const toast = useToast();

  async function run(room: Conference, action: ConferenceAction, who?: string) {
    setNotice(null);
    try {
      await controlConference(room.id, action, who);
      rooms.reload();
    } catch (err: unknown) {
      setNotice(`Could not ${action.replace("_", " ")}: ${errorMessage(err)}`);
    }
  }

  async function onDelete(room: Conference) {
    try {
      await deleteConference(room.id);
    } catch (err: unknown) {
      throw new Error(`Could not delete ${room.name}: ${errorMessage(err)}`, {
        cause: err,
      });
    }
    rooms.reload();
    setDeleting(null);
    toast.show(`Conference room ${room.name} deleted.`);
  }

  const items = rooms.status === "loading" ? null : (rooms.data ?? []);
  const newRoom = (
    <Can min="admin">
      <Button
        icon="plus"
        disabled={items === null}
        onClick={() => setEditing({ kind: "new" })}
      >
        New conference room
      </Button>
    </Can>
  );

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Call flow"
        title="Conferences"
        description="Dial a room's number to join it. Rooms can ask for a PIN, hold callers until a leader arrives, and be recorded. Inbound routes, the flow canvas and phone keys can all point at a room."
        actions={newRoom}
      />
      <div className="cf-stack">
        {notice && (
          <Alert tone="bad" title="Conference control">
            {notice}
          </Alert>
        )}
        {rooms.status === "loading" && <Spinner label="Loading rooms…" />}
        {rooms.status === "error" && (
          <Alert tone="bad" title="Could not load the conference rooms">
            {rooms.message}
          </Alert>
        )}
        {items && items.length === 0 && (
          <EmptyState
            icon="users"
            title="No conference rooms yet"
            description="A room is a number such as 800 that callers dial to talk together."
            action={newRoom}
          />
        )}
        {items && items.length > 0 && (
          <ul
            className="cf-grid cf-grid--groups cf-plain"
            aria-label="Conference rooms"
          >
            {items.map((r) => (
              <RoomCard
                key={String(r.id)}
                room={r}
                onControl={(a, who) => void run(r, a, who)}
                onEdit={() => setEditing({ kind: "edit", room: r })}
                onDelete={() => setDeleting(r)}
              />
            ))}
          </ul>
        )}
      </div>

      {editing && (
        <RoomDrawer
          key={editing.kind === "edit" ? String(editing.room.id) : "new"}
          room={editing.kind === "edit" ? editing.room : null}
          onClose={() => setEditing(null)}
          onSaved={(saved, created) => {
            rooms.reload();
            setEditing(null);
            toast.show(
              created
                ? `Conference room ${saved.name} created.`
                : `Conference room ${saved.name} saved.`,
            );
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete conference room ${deleting.name}?`}
          description={`The number ${deleting.number} stops answering. A route that points at the room blocks the deletion.`}
          confirmLabel="Delete room"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

function RoomCard({
  room: r,
  onControl,
  onEdit,
  onDelete,
}: {
  room: Conference;
  onControl: (a: ConferenceAction, who?: string) => void;
  onEdit: () => void;
  onDelete: () => void;
}) {
  const titleId = `conference-${String(r.id)}-name`;
  const live = r.live;
  const people = live?.participants ?? [];
  return (
    <li className="az-card cf-card" aria-labelledby={titleId}>
      <div className="cf-card__head cf-card__head--top">
        <div style={{ minWidth: 0 }}>
          <h2 className="cf-card__group-name" id={titleId}>
            {r.name}
          </h2>
          <div className="cf-card__sub">
            Number <span className="cf-mono">{r.number}</span> ·{" "}
            <span
              aria-label={`${String(people.length)} of ${String(r.maxParticipants)} seats used`}
            >
              {people.length} of {r.maxParticipants}
            </span>
            {live && (
              <>
                {" · on "}
                <span className="cf-mono">{live.node}</span>
              </>
            )}
          </div>
        </div>
        <span style={{ display: "flex", gap: 6, flexWrap: "wrap" }}>
          {!r.enabled && <Badge tone="warn">Disabled</Badge>}
          {live?.locked && <Badge tone="warn">Locked</Badge>}
          {live?.recording && <Badge tone="bad">Recording</Badge>}
          {r.hasUserPin && <Badge tone="pillar">PIN</Badge>}
          {r.hasLeaderPin && <Badge tone="pillar">Leader PIN</Badge>}
          {r.waitForLeader && <Badge tone="pillar">Waits for leader</Badge>}
        </span>
      </div>
      {people.length === 0 ? (
        <p className="cf-card__sub">Nobody is in the room.</p>
      ) : (
        <ul className="cf-members" aria-label={`Participants in ${r.name}`}>
          {people.map((p) => (
            <Participant key={p.id} room={r} p={p} onControl={onControl} />
          ))}
        </ul>
      )}
      <div className="cf-card__foot">
        <Can min="operator">
          {live && (
            <>
              <Button
                variant="secondary"
                size="sm"
                icon={live.locked ? "lock-open" : "lock"}
                aria-label={`${live.locked ? "Unlock" : "Lock"} room ${r.name}`}
                onClick={() => onControl(live.locked ? "unlock" : "lock")}
              >
                {live.locked ? "Unlock" : "Lock"}
              </Button>
              <Button
                variant="secondary"
                size="sm"
                icon="mic-off"
                aria-label={`Mute everyone in ${r.name}`}
                onClick={() => onControl("mute_all")}
              >
                Mute all
              </Button>
              <Button
                variant="secondary"
                size="sm"
                icon="mic"
                aria-label={`Unmute everyone in ${r.name}`}
                onClick={() => onControl("unmute_all")}
              >
                Unmute all
              </Button>
              <Button
                variant="ghost"
                size="sm"
                icon="phone-off"
                className="cf-danger"
                aria-label={`End room ${r.name}`}
                onClick={() => onControl("end")}
              >
                End
              </Button>
            </>
          )}
        </Can>
        <Can min="admin">
          <Button
            variant="secondary"
            size="sm"
            icon="pencil"
            aria-label={`Edit conference room ${r.name}`}
            onClick={onEdit}
          >
            Edit
          </Button>
          <Button
            variant="ghost"
            size="sm"
            icon="trash-2"
            className="cf-danger"
            aria-label={`Delete conference room ${r.name}`}
            onClick={onDelete}
          >
            Delete
          </Button>
        </Can>
      </div>
    </li>
  );
}

function Participant({
  room,
  p,
  onControl,
}: {
  room: Conference;
  p: ConferenceParticipant;
  onControl: (a: ConferenceAction, who?: string) => void;
}) {
  const who = p.name ? `${p.number} ${p.name}` : p.number || "caller";
  return (
    <li className="cf-member" aria-label={who}>
      <span className="cf-member__who">
        <span>
          <span className="cf-mono">{p.number || "—"}</span> {p.name}
        </span>
        {p.leader && <Badge tone="pillar">Leader</Badge>}
        {p.waiting && <Badge tone="warn">Waiting</Badge>}
        {p.muted && (
          <Badge tone="warn">
            {p.mutedByLeader ? "Muted by leader" : "Muted"}
          </Badge>
        )}
      </span>
      <Can min="operator">
        <span style={{ display: "flex", gap: 6 }}>
          {!p.waiting && (
            <Button
              variant="secondary"
              size="sm"
              icon={p.muted ? "mic" : "mic-off"}
              aria-label={`${p.muted ? "Unmute" : "Mute"} ${who} in ${room.name}`}
              onClick={() => onControl(p.muted ? "unmute" : "mute", p.id)}
            >
              {p.muted ? "Unmute" : "Mute"}
            </Button>
          )}
          <Button
            variant="ghost"
            size="sm"
            icon="user-x"
            className="cf-danger"
            aria-label={`Kick ${who} from ${room.name}`}
            onClick={() => onControl("kick", p.id)}
          >
            Kick
          </Button>
        </span>
      </Can>
    </li>
  );
}
