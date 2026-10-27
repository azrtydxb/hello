import { useEffect, useState } from "react";
import {
  listRingGroups,
  type DoorTargets,
  type Extension,
  type Id,
  type RingGroup,
} from "../../api";
import { Checkbox } from "../../design/azrty/components";

const has = (ids: Id[], id: Id) => ids.some((x) => String(x) === String(id));
const toggle = (ids: Id[], id: Id, on: boolean) =>
  on ? [...ids, id] : ids.filter((x) => String(x) !== String(id));

/**
 * The destinations a door station may call, besides every Door ring group
 * (which it can always call): extensions and ring groups, picked from lists.
 * Door stations are not offered as targets.
 */
export function DoorTargetsField({
  self,
  extensions,
  value,
  error,
  disabled,
  onChange,
}: {
  self: Id;
  extensions: Extension[];
  value: DoorTargets;
  error?: string;
  disabled?: boolean;
  onChange: (next: DoorTargets) => void;
}) {
  const [groups, setGroups] = useState<RingGroup[] | null>(null);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    listRingGroups(controller.signal)
      .then(setGroups)
      .catch(() => {
        if (!controller.signal.aborted) setFailed(true);
      });
    return () => controller.abort();
  }, []);

  const people = extensions.filter(
    (e) => e.kind !== "door" && String(e.id) !== String(self),
  );
  const plain = (groups ?? []).filter((g) => !g.isDoor);
  const doorGroups = (groups ?? []).filter((g) => g.isDoor);

  return (
    <fieldset
      className="dir-section"
      aria-describedby={error ? "door-targets-error" : undefined}
    >
      <legend className="az-eyebrow">Can call</legend>
      <p className="dir-muted">
        The station can call only what is ticked here
        {doorGroups.length > 0
          ? `, and the Door group${doorGroups.length > 1 ? "s" : ""} ${doorGroups.map((g) => g.name).join(", ")}`
          : ", and any ring group marked Door"}
        . Any other number is refused, so a stolen unit cannot dial out.
      </p>
      {error && (
        <p id="door-targets-error" className="cf-form__error">
          {error}
        </p>
      )}
      <div role="group" aria-label="Extensions the station can call">
        {people.length === 0 && <p className="dir-muted">No extensions.</p>}
        {people.map((e) => (
          <Checkbox
            key={String(e.id)}
            label={`${e.number} ${e.name}`}
            disabled={disabled}
            checked={has(value.extensions, e.id)}
            onChange={(ev) =>
              onChange({
                ...value,
                extensions: toggle(value.extensions, e.id, ev.target.checked),
              })
            }
          />
        ))}
      </div>
      <div role="group" aria-label="Ring groups the station can call">
        {failed && <p className="dir-muted">Could not load the ring groups.</p>}
        {plain.map((g) => (
          <Checkbox
            key={String(g.id)}
            label={`Group ${g.name}`}
            disabled={disabled}
            checked={has(value.ringGroups, g.id)}
            onChange={(ev) =>
              onChange({
                ...value,
                ringGroups: toggle(value.ringGroups, g.id, ev.target.checked),
              })
            }
          />
        ))}
      </div>
    </fieldset>
  );
}
