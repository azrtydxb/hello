import {
  IVR_DESTINATION_KINDS,
  IVR_DESTINATION_LABEL,
  type Announcement,
  type Extension,
  type IvrDestination,
  type IvrDestinationKind,
  type IvrMenu,
  type PagingGroup,
  type RingGroup,
} from "../api";
import type { TimeCondition } from "../api/timecond";
import { Input, Select } from "../design/azrty/components";

/** What the destination pickers choose from; each list loads on its own. */
export interface DestinationChoices {
  extensions: readonly Extension[];
  ringGroups: readonly RingGroup[];
  announcements: readonly Announcement[];
  pagingGroups: readonly PagingGroup[];
  menus: readonly IvrMenu[];
  /** Time conditions; absent where a page does not load them. */
  timeConditions?: readonly TimeCondition[];
}

interface Option {
  value: string;
  label: string;
}

/** A target select that keeps a stored value the list no longer has. */
function withCurrent(options: Option[], current: string): Option[] {
  const head: Option = { value: "", label: "Choose…" };
  if (current !== "" && !options.some((o) => o.value === current)) {
    return [
      head,
      { value: current, label: `${current} (not found)` },
      ...options,
    ];
  }
  return [head, ...options];
}

function targetOptions(
  kind: IvrDestinationKind,
  c: DestinationChoices,
  value: string,
  selfName?: string,
): Option[] | null {
  switch (kind) {
    case "extension":
    case "voicemail":
      return withCurrent(
        c.extensions.map((e) => ({
          value: e.number,
          label: e.name ? `${e.number} · ${e.name}` : e.number,
        })),
        value,
      );
    case "ring_group":
      return withCurrent(
        c.ringGroups.map((g) => ({ value: g.name, label: g.name })),
        value,
      );
    case "announcement":
      return withCurrent(
        c.announcements.map((a) => ({ value: a.name, label: a.name })),
        value,
      );
    case "paging":
      return withCurrent(
        c.pagingGroups.map((g) => ({
          value: g.code,
          label: `${g.name} (${g.code})`,
        })),
        value,
      );
    case "ivr":
      return withCurrent(
        c.menus
          .filter((m) => m.name !== selfName)
          .map((m) => ({ value: m.name, label: m.name })),
        value,
      );
    case "time_condition":
      return withCurrent(
        (c.timeConditions ?? [])
          .filter((t) => t.name !== selfName)
          .map((t) => ({ value: t.name, label: t.name })),
        value,
      );
    default:
      return null;
  }
}

const TARGET_LABEL: Partial<Record<IvrDestinationKind, string>> = {
  extension: "Extension",
  voicemail: "Voicemail box of",
  ring_group: "Ring group",
  announcement: "Announcement",
  paging: "Paging group",
  ivr: "IVR menu",
  time_condition: "Time condition",
  external: "Number",
};

/**
 * Chooses a destination: its kind and, for every kind but hang-up, the
 * target from the matching list (or a number for an external destination).
 * Reused by every IVR destination: options, timeout and invalid choice.
 */
export function DestinationPicker({
  id,
  label,
  value,
  choices,
  selfName,
  kindError,
  targetError,
  onChange,
}: {
  id: string;
  label: string;
  value: IvrDestination;
  choices: DestinationChoices;
  /** The menu being edited: it cannot be its own automatic exit. */
  selfName?: string;
  kindError?: string;
  targetError?: string;
  onChange: (next: IvrDestination) => void;
}) {
  const options = targetOptions(value.kind, choices, value.target, selfName);
  return (
    <div className="cf-two">
      <Select
        id={`${id}-kind`}
        label={label}
        value={value.kind}
        error={kindError}
        options={IVR_DESTINATION_KINDS.map((k) => ({
          value: k,
          label: IVR_DESTINATION_LABEL[k],
        }))}
        onChange={(e) =>
          onChange({ kind: e.target.value as IvrDestinationKind, target: "" })
        }
      />
      {value.kind === "hangup" ? (
        <span />
      ) : options ? (
        <Select
          id={`${id}-target`}
          label={TARGET_LABEL[value.kind] ?? "Target"}
          value={value.target}
          error={targetError}
          options={options}
          onChange={(e) => onChange({ ...value, target: e.target.value })}
        />
      ) : (
        <Input
          id={`${id}-target`}
          label={TARGET_LABEL[value.kind] ?? "Target"}
          mono
          value={value.target}
          error={targetError}
          hint="2 to 32 digits, optionally starting with +. Routed through the outbound routes."
          onChange={(e) => onChange({ ...value, target: e.target.value })}
        />
      )}
    </div>
  );
}
