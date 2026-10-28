import { useEffect, useState } from "react";
import { listRingTags, type RingTag } from "../../api/ringtag";
import { Select } from "../../design/azrty/components";

/**
 * Loads the ring tags for a picker. A failure leaves the list empty, so a
 * form never depends on it.
 */
export function useRingTags(): { tags: RingTag[]; ready: boolean } {
  const [tags, setTags] = useState<RingTag[]>([]);
  const [ready, setReady] = useState(false);
  useEffect(() => {
    const controller = new AbortController();
    listRingTags(controller.signal)
      .then((items) => {
        setTags(items);
        setReady(true);
      })
      .catch(() => {
        if (!controller.signal.aborted) setReady(true);
      });
    return () => controller.abort();
  }, []);
  return { tags, ready };
}

/**
 * A ring tag picker. With `emptyLabel` the empty value is a choice of its own
 * (a ring group's "ringgroup" default); a stored tag the list does not show
 * stays selectable, so saving other fields does not drop it.
 */
export function RingTagSelect({
  id,
  label,
  hint,
  value,
  error,
  emptyLabel,
  onChange,
}: {
  id: string;
  label: string;
  hint?: string;
  value: string;
  error?: string;
  emptyLabel?: string;
  onChange: (next: string) => void;
}) {
  const { tags, ready } = useRingTags();
  const known = tags.some((t) => t.name === value);
  return (
    <Select
      id={id}
      label={label}
      hint={hint}
      value={value}
      error={error}
      options={[
        ...(emptyLabel !== undefined
          ? [{ value: "", label: ready ? emptyLabel : "Loading…" }]
          : []),
        ...(value !== "" && !known ? [{ value, label: value }] : []),
        ...tags.map((t) => ({ value: t.name, label: t.name })),
      ]}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}
