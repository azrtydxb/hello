import { useState, type FormEvent } from "react";
import {
  createConference,
  errorMessage,
  fieldErrors,
  putConference,
  type Conference,
  type ConferenceFields,
  type ConferenceSound,
  type FieldError,
} from "../api";
import {
  Button,
  Drawer,
  Input,
  Select,
  Switch,
  useRestoreFocus,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { FormAlert } from "../pages/callflow/ui";
import { Can } from "../role";

export const FORM_ID = "conference-form";
export const NUMBER_PATTERN = /^[0-9]{2,10}$/;
export const PIN_PATTERN = /^[0-9]{4,10}$/;
export const ANN_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;

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

export function RoomDrawer({
  room,
  onClose,
  commit,
  onSaved,
}: {
  room: Conference | null;
  onClose: () => void;
  /** The canvas stages the body in its draft instead of saving it here. */
  commit?: (body: object) => Promise<void>;
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
      if (commit) {
        await commit(body);
        onClose();
        return;
      }
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
