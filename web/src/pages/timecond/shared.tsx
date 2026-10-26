import { useEffect, useState } from "react";
import { errorMessage } from "../../api";

export type ListState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: T[] };

/** Loads a list once; `replace` edits it after a save or delete. */
export function useLoadedList<T>(
  fetcher: (signal: AbortSignal) => Promise<T[]>,
): [ListState<T>, (fn: (items: T[]) => T[]) => void] {
  const [state, setState] = useState<ListState<T>>({ status: "loading" });
  useEffect(() => {
    const controller = new AbortController();
    fetcher(controller.signal)
      .then((items) => setState({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
    // The fetcher is a module-level function; load once.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const replace = (fn: (items: T[]) => T[]) =>
    setState((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: fn(prev.items) }
        : prev,
    );
  return [state, replace];
}

/** Loads a list for a picker; a failure leaves it empty. */
export function useQuietList<T>(
  fetcher: (signal: AbortSignal) => Promise<T[]>,
): T[] {
  const [items, setItems] = useState<T[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    fetcher(controller.signal)
      .then(setItems)
      .catch(() => undefined);
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  return items;
}

/** Monday first, as a week is read; the API's day 0 is Sunday. */
export const WEEK: ReadonlyArray<{ day: number; label: string }> = [
  { day: 1, label: "Monday" },
  { day: 2, label: "Tuesday" },
  { day: 3, label: "Wednesday" },
  { day: 4, label: "Thursday" },
  { day: 5, label: "Friday" },
  { day: 6, label: "Saturday" },
  { day: 0, label: "Sunday" },
];

export const HHMM = /^([01][0-9]|2[0-3]):[0-5][0-9]$/;
export const HHMM_END = /^(([01][0-9]|2[0-3]):[0-5][0-9]|24:00)$/;
export const NAME_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;
