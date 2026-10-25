import { useEffect, useState } from "react";
import { listMOHClasses, type Id, type MOHClass } from "../../api";
import { Select } from "../../design/azrty/components";

/**
 * Loads the music on hold classes for a picker. A failure leaves the list
 * empty: every picker still offers "Default", so a form never depends on it.
 */
export function useMOHClasses(): { classes: MOHClass[]; ready: boolean } {
  const [classes, setClasses] = useState<MOHClass[]>([]);
  const [ready, setReady] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    listMOHClasses(controller.signal)
      .then((items) => {
        setClasses(items);
        setReady(true);
      })
      .catch(() => {
        if (!controller.signal.aborted) setReady(true);
      });
    return () => controller.abort();
  }, []);
  return { classes, ready };
}

/** The picker value for a stored class id ("" = the global default). */
export const mohValue = (v: Id | null | undefined): string =>
  v === null || v === undefined ? "" : String(v);

/** The class id a picker value stands for (null = the global default). */
export const mohId = (v: string): Id | null => (v === "" ? null : Number(v));

/**
 * A music on hold class picker. "Default" stores null, so the global default
 * class (or the built-in tone when none is set) applies.
 */
export function MOHSelect({
  id,
  label,
  hint,
  value,
  error,
  disabled,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  value: Id | null | undefined;
  error?: string;
  disabled?: boolean;
  onChange: (next: Id | null) => void;
}) {
  const { classes, ready } = useMOHClasses();
  const current = mohValue(value);
  // A stored id the list does not (yet) show stays selectable, so saving
  // other fields does not silently drop it.
  const known = classes.some((c) => String(c.id) === current);
  return (
    <Select
      id={id}
      label={label}
      hint={hint}
      value={current}
      error={error}
      disabled={disabled}
      options={[
        { value: "", label: ready ? "Default" : "Loading…" },
        ...(current !== "" && !known
          ? [{ value: current, label: `Class #${current}` }]
          : []),
        ...classes.map((c) => ({
          value: String(c.id),
          label: c.isDefault ? `${c.name} (default)` : c.name,
        })),
      ]}
      onChange={(e) => onChange(mohId(e.target.value))}
    />
  );
}
