import { useCallback, useEffect, useState } from "react";
import { errorMessage } from "./api";

/** What a polled page shows: first load, last result, or an error (with the last data). */
export type PollState<T> =
  | { status: "loading" }
  | { status: "error"; message: string; data?: T }
  | { status: "ready"; data: T; updatedAt: Date };

/** A poll state plus `reload`, which loads again at once (e.g. after a change). */
export type Polled<T> = PollState<T> & { reload: () => void };

/**
 * Calls `load` now and then every `intervalMs` after each call settles, so
 * slow responses never overlap. Stops (and aborts) on unmount. `load` must be
 * stable (module-level or memoised). `reload()` aborts any call in flight and
 * loads again immediately, keeping the data shown meanwhile.
 */
export function usePolling<T>(
  load: (signal: AbortSignal) => Promise<T>,
  intervalMs: number,
): Polled<T> {
  const [state, setState] = useState<PollState<T>>({ status: "loading" });
  const [generation, setGeneration] = useState(0);
  const reload = useCallback(() => setGeneration((g) => g + 1), []);

  useEffect(() => {
    const controller = new AbortController();
    let timer: ReturnType<typeof setTimeout> | undefined;

    async function tick() {
      try {
        const data = await load(controller.signal);
        if (controller.signal.aborted) return;
        setState({ status: "ready", data, updatedAt: new Date() });
      } catch (err) {
        if (controller.signal.aborted) return;
        // Keep showing the last good data next to the error.
        setState((prev) => ({
          status: "error",
          message: errorMessage(err),
          data: prev.status === "loading" ? undefined : prev.data,
        }));
      }
      if (!controller.signal.aborted)
        timer = setTimeout(() => void tick(), intervalMs);
    }

    void tick();
    return () => {
      controller.abort();
      clearTimeout(timer);
    };
  }, [load, intervalMs, generation]);

  return { ...state, reload };
}

/** Refresh interval of the live pages (Registrations, Active Calls). */
export const LIVE_REFRESH_MS = 5000;
