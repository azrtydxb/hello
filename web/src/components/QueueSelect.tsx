import { useEffect, useState } from "react";
import { listQueues, type Queue } from "../api";
import { Select } from "../design/azrty/components";

/**
 * A select of the call queues, for every place that sends a call to one
 * (inbound routes, ring group failover, IVR keys, a queue's own failover).
 * It loads the list itself and fails soft: the list stays empty and a stored
 * value is still shown, so saving other fields keeps working.
 */
export function QueueSelect({
  id,
  label,
  value,
  error,
  exclude,
  onChange,
}: {
  id: string;
  label: string;
  value: string;
  error?: string;
  /** A queue that cannot be chosen (the one being edited). */
  exclude?: string;
  onChange: (name: string) => void;
}) {
  const [queues, setQueues] = useState<Queue[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    listQueues(controller.signal)
      .then(setQueues)
      .catch(() => undefined);
    return () => controller.abort();
  }, []);
  const names = queues.map((q) => q.name).filter((n) => n !== exclude);
  return (
    <Select
      id={id}
      label={label}
      value={value}
      error={error}
      options={[
        { value: "", label: "Choose…" },
        ...(value !== "" && !names.includes(value)
          ? [{ value, label: `${value} (not found)` }]
          : []),
        ...names.map((n) => ({ value: n, label: n })),
      ]}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}
