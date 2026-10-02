import { useCallback, useEffect, useState } from "react";
import {
  errorMessage,
  reorderRoutes,
  type Id,
  type RouteDirection,
} from "./api";

/** A route list being loaded, failed, or ready in position order. */
export type OrderedState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: T[] };

/** `items` with the element at `index` moved by `delta` (-1 up, +1 down). */
export function moved<T>(
  items: readonly T[],
  index: number,
  delta: number,
): T[] {
  const to = index + delta;
  if (to < 0 || to >= items.length) return [...items];
  const next = [...items];
  const [item] = next.splice(index, 1);
  if (item !== undefined) next.splice(to, 0, item);
  return next;
}

/**
 * An ordered route list: loads it, and reorders it through
 * PUT /api/v1/routes/{direction}/order with the full permutation of ids,
 * applying the new order once the server accepts it. If the server refuses,
 * the list is reloaded so later moves start from the stored order.
 */
export function useOrderedList<T extends { id: Id; position: number }>(
  direction: RouteDirection,
  load: (signal: AbortSignal) => Promise<T[]>,
) {
  const [state, setState] = useState<OrderedState<T>>({ status: "loading" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // Bumped to re-read the list from the server (after a refused reorder).
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal)
      .then((items) =>
        setState({
          status: "ready",
          items: [...items].sort((a, b) => a.position - b.position),
        }),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [load, generation]);

  const items = state.status === "ready" ? state.items : [];

  const move = useCallback(
    async (index: number, delta: number): Promise<boolean> => {
      if (state.status !== "ready") return false;
      const next = moved(state.items, index, delta);
      setBusy(true);
      setError(null);
      try {
        await reorderRoutes(
          direction,
          next.map((r) => r.id),
        );
        setState({
          status: "ready",
          items: next.map((r, i) => ({ ...r, position: i + 1 })),
        });
        return true;
      } catch (err) {
        // The server's order may differ from ours (another admin, a stale
        // page): re-read it so the next move permutes what is really stored.
        setError(
          `Could not reorder: ${errorMessage(err)} The list was reloaded from the server.`,
        );
        setGeneration((g) => g + 1);
        return false;
      } finally {
        setBusy(false);
      }
    },
    [direction, state],
  );

  const upsert = useCallback((item: T) => {
    setState((prev) => {
      if (prev.status !== "ready") return prev;
      const exists = prev.items.some((r) => r.id === item.id);
      const list = exists
        ? prev.items.map((r) => (r.id === item.id ? item : r))
        : [...prev.items, item];
      return { status: "ready", items: list };
    });
  }, []);

  const remove = useCallback((routeId: Id) => {
    setState((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: prev.items.filter((r) => r.id !== routeId) }
        : prev,
    );
  }, []);

  return { state, items, busy, error, setError, move, upsert, remove };
}
