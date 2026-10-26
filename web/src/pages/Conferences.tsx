import { useState, type FormEvent } from "react";
import {
  controlConference,
  createConference,
  deleteConference,
  errorMessage,
  fieldErrors,
  listConferences,
  putConference,
  type Conference,
  type ConferenceAction,
  type ConferenceFields,
  type ConferenceParticipant,
  type ConferenceSound,
  type FieldError,
} from "../api";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
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
import { usePolling } from "../usePolling";

const FORM_ID = "conference-form";
const NUMBER_PATTERN = /^[0-9]{2,10}$/;
const PIN_PATTERN = /^[0-9]{4,10}$/;
const ANN_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;
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

interface Draft {
  name: string;
  number: string;
  userPin: string;
  clearUserPin: boolean;
  leaderPin: string;
  clearLeaderPin: boolean;
  maxParticipants: string;
  waitForLeader: boolean;
  muteOnEntry: boolean;
  entryExit: ConferenceSound;
  entryAnnouncement: string;
  exitAnnouncement: string;
  record: boolean;
  enabled: boolean;
}

const draftOf = (r: Conference | null): Draft => ({
  name: r?.name ?? "",
  number: r?.number ?? "",
  userPin: "",
  clearUserPin: false,
  leaderPin: "",
  clearLeaderPin: false,
  maxParticipants: String(r?.maxParticipants ?? 10),
  waitForLeader: r?.waitForLeader ?? false,
  muteOnEntry: r?.muteOnEntry ?? false,
  entryExit: r?.entryExit ?? "tone",
  entryAnnouncement: r?.entryAnnouncement ?? "",
  exitAnnouncement: r?.exitAnnouncement ?? "",
  record: r?.record ?? false,
  enabled: r?.enabled ?? true,
});

const KEYS = [
  "name",
  "number",
  "userPin",
  "leaderPin",
  "maxParticipants",
  "waitForLeader",
  "muteOnEntry",
  "entryExit",
  "entryAnnouncement",
  "exitAnnouncement",
  "record",
  "enabled",
];

function validate(d: Draft): Record<string, string> {
  const out: Record<string, string> = {};
  if (d.name.trim() === "" || d.name !== d.name.trim() || d.name.length > 64) {
    out.name = "Use 1 to 64 characters without leading or trailing space.";
  }
  if (!NUMBER_PATTERN.test(d.number)) {
    out.number = "Use 2 to 10 digits, for example 800.";
  }
  const cap = Number(d.maxParticipants);
  if (!Number.isInteger(cap) || cap < 2 || cap > 50) {
    out.maxParticipants = "Use a whole number from 2 to 50.";
  }
  if (d.userPin !== "" && !PIN_PATTERN.test(d.userPin)) {
    out.userPin = "Use 4 to 10 digits.";
  }
  if (d.leaderPin !== "" && !PIN_PATTERN.test(d.leaderPin)) {
    out.leaderPin = "Use 4 to 10 digits.";
  }
  if (d.userPin !== "" && d.userPin === d.leaderPin) {
    out.leaderPin = "The leader PIN must differ from the user PIN.";
  }
  if (d.entryExit === "announcement") {
    for (const k of ["entryAnnouncement", "exitAnnouncement"] as const) {
      if (d[k] !== "" && !ANN_PATTERN.test(d[k])) {
        out[k] = "Use an announcement name: letters, digits, dots, dashes.";
      }
    }
    if (d.entryAnnouncement === "" && d.exitAnnouncement === "") {
      out.entryAnnouncement = "Name an announcement, or use tones.";
    }
  }
  return out;
}

function RoomDrawer({
  room,
  onClose,
  onSaved,
}: {
  room: Conference | null;
  onClose: () => void;
  onSaved: (r: Conference, created: boolean) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(room));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  // A PIN is write-only: typing one sets it, the clear box removes it, and
  // leaving both alone keeps what is stored.
  const pinField = (
    which: "userPin" | "leaderPin",
    label: string,
    isSet: boolean,
  ) => {
    const clearKey = which === "userPin" ? "clearUserPin" : "clearLeaderPin";
    return (
      <div>
        <Input
          id={`conference-${which}`}
          label={label}
          type="password"
          autoComplete="new-password"
          mono
          value={d[which]}
          error={errors[which]}
          disabled={d[clearKey]}
          hint={
            isSet
              ? "A PIN is set. Type a new one to replace it, or leave this empty to keep it."
              : "Optional. 4 to 10 digits, keyed on the phone and ended with #."
          }
          onChange={(e) => set(which, e.target.value)}
        />
        {isSet && (
          <Switch
            label={`Remove the ${label.toLowerCase()}`}
            labelPosition="end"
            checked={d[clearKey]}
            onChange={(e) => {
              set(clearKey, e.target.checked);
              if (e.target.checked) set(which, "");
            }}
          />
        )}
      </div>
    );
  };

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found = validate(d);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    try {
      const body: ConferenceFields = {
        name: d.name,
        number: d.number,
        maxParticipants: Number(d.maxParticipants),
        waitForLeader: d.waitForLeader,
        muteOnEntry: d.muteOnEntry,
        entryExit: d.entryExit,
        entryAnnouncement:
          d.entryExit === "announcement" ? d.entryAnnouncement : "",
        exitAnnouncement:
          d.entryExit === "announcement" ? d.exitAnnouncement : "",
        record: d.record,
        enabled: d.enabled,
        ...(d.clearUserPin
          ? { userPin: "" }
          : d.userPin !== ""
            ? { userPin: d.userPin }
            : {}),
        ...(d.clearLeaderPin
          ? { leaderPin: "" }
          : d.leaderPin !== ""
            ? { leaderPin: d.leaderPin }
            : {}),
      };
      onSaved(
        room
          ? await putConference(room.id, body)
          : await createConference(body),
        room === null,
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
      title={room ? `Conference room ${room.name}` : "New conference room"}
      description="Callers dial the number and talk together. Audio is mixed on one SIP node."
      onClose={busy ? undefined : onClose}
      width={560}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can min="admin">
            <Button type="submit" form={FORM_ID} disabled={busy}>
              {room ? "Save room" : "Create room"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={
          room ? `Edit conference room ${room.name}` : "New conference room"
        }
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="cf-two">
          <Input
            id="conference-name"
            label="Name"
            autoFocus
            value={d.name}
            error={errors.name}
            onChange={(e) => set("name", e.target.value)}
          />
          <Input
            id="conference-number"
            label="Room number"
            mono
            value={d.number}
            error={errors.number}
            hint="Dial this from any phone, for example 800."
            onChange={(e) => set("number", e.target.value)}
          />
        </div>
        <div className="cf-two">
          {pinField("userPin", "User PIN", room?.hasUserPin ?? false)}
          {pinField("leaderPin", "Leader PIN", room?.hasLeaderPin ?? false)}
        </div>
        <p className="cf-form__note">
          Leaders (who enter the leader PIN) can press *7 to lock the room, *8
          to remove the last person who joined and *5 to mute everyone else.
          Anyone can press *6 to mute or unmute themselves.
        </p>
        <Input
          id="conference-max"
          label="Seats"
          inputMode="numeric"
          value={d.maxParticipants}
          error={errors.maxParticipants}
          hint="2 to 50. Callers still entering a PIN or waiting for a leader count."
          onChange={(e) => set("maxParticipants", e.target.value)}
        />
        <Select
          id="conference-sound"
          label="When someone joins or leaves"
          value={d.entryExit}
          error={errors.entryExit}
          options={[
            { value: "tone", label: "Play a tone" },
            { value: "announcement", label: "Play an announcement" },
            { value: "none", label: "Nothing" },
          ]}
          onChange={(e) => set("entryExit", e.target.value as ConferenceSound)}
        />
        {d.entryExit === "announcement" && (
          <div className="cf-two">
            <Input
              id="conference-entry-ann"
              label="Join announcement"
              mono
              value={d.entryAnnouncement}
              error={errors.entryAnnouncement}
              hint="An uploaded announcement. Empty plays the tone."
              onChange={(e) => set("entryAnnouncement", e.target.value)}
            />
            <Input
              id="conference-exit-ann"
              label="Leave announcement"
              mono
              value={d.exitAnnouncement}
              error={errors.exitAnnouncement}
              onChange={(e) => set("exitAnnouncement", e.target.value)}
            />
          </div>
        )}
        <Switch
          label="Wait for a leader"
          hint="Callers hear a soft tone until a leader joins"
          labelPosition="end"
          checked={d.waitForLeader}
          onChange={(e) => set("waitForLeader", e.target.checked)}
        />
        <Switch
          label="Mute on entry"
          hint="Everyone but a leader joins muted and presses *6 to talk"
          labelPosition="end"
          checked={d.muteOnEntry}
          onChange={(e) => set("muteOnEntry", e.target.checked)}
        />
        <Switch
          label="Record the room"
          hint="Saved to the recordings list in segments of ten minutes while anyone is in the room"
          labelPosition="end"
          checked={d.record}
          onChange={(e) => set("record", e.target.checked)}
        />
        <Switch
          label="Enabled"
          hint="A disabled room refuses callers"
          labelPosition="end"
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
