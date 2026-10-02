import { useCallback, useEffect, useRef, useState } from "react";
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
 * The accepted order applied to the latest items, by id: their current
 * fields win, items deleted meanwhile stay gone, and items created meanwhile
 * (not in `ids`) keep their place after the ordered ones.
 */
export function applyOrder<T extends { id: Id; position: number }>(
  current: readonly T[],
  ids: readonly Id[],
): T[] {
  const rank = new Map(ids.map((id, i) => [String(id), i]));
  const at = (r: T) => rank.get(String(r.id)) ?? Infinity;
  const ordered = current
    .filter((r) => rank.has(String(r.id)))
    .sort((a, b) => at(a) - at(b));
  const added = current.filter((r) => !rank.has(String(r.id)));
  return [...ordered, ...added].map((r, i) => ({ ...r, position: i + 1 }));
}

/** Local edits made while a list request is in flight. */
interface LocalChanges<T> {
  upserts: Map<string, T>;
  removed: Set<string>;
}

const noChanges = <T>(): LocalChanges<T> => ({
  upserts: new Map(),
  removed: new Set(),
});

/** The server's list with the local edits made since it was requested. */
function withLocal<T extends { id: Id }>(
  server: readonly T[],
  local: LocalChanges<T>,
): T[] {
  const list = server.filter((r) => !local.removed.has(String(r.id)));
  for (const [key, item] of local.upserts) {
    const i = list.findIndex((r) => String(r.id) === key);
    if (i >= 0) list[i] = item;
    else list.push(item);
  }
  return list;
}

const byPosition = <T extends { position: number }>(items: readonly T[]) =>
  [...items].sort((a, b) => a.position - b.position);

/**
 * An ordered route list: loads it, and reorders it through
 * PUT /api/v1/routes/{direction}/order with the full permutation of ids.
 *
 * - An accepted order is applied to the latest items by id, so an edit,
 *   create or delete that finished while the PUT was pending is kept.
 * - If the server refuses, the list is re-read and `busy` stays true until
 *   it arrives, so no move can send a permutation of a stale list. Edits
 *   made while that read is pending are applied over its result.
 */
export function useOrderedList<T extends { id: Id; position: number }>(
  direction: RouteDirection,
  load: (signal: AbortSignal) => Promise<T[]>,
) {
  const [state, setState] = useState<OrderedState<T>>({ status: "loading" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const local = useRef<LocalChanges<T>>(noChanges());
  const lifetime = useRef<AbortController | null>(null);
  // Guards against a second move in the same tick, before `busy` re-renders.
  const moving = useRef(false);

  useEffect(() => {
    const controller = new AbortController();
    lifetime.current = controller;
    local.current = noChanges();
    load(controller.signal)
      .then((items) => {
        if (controller.signal.aborted) return;
        setState({
          status: "ready",
          items: byPosition(withLocal(items, local.current)),
        });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [load]);

  const items = state.status === "ready" ? state.items : [];

  /** Re-read the stored order; resolves once it is applied (or failed). */
  const recover = useCallback(async () => {
    const signal = lifetime.current?.signal;
    if (!signal || signal.aborted) return;
    local.current = noChanges();
    try {
      const fresh = await load(signal);
      if (signal.aborted) return;
      setState({
        status: "ready",
        items: byPosition(withLocal(fresh, local.current)),
      });
    } catch (err) {
      if (!signal.aborted) {
        setError(`Could not reload the routes: ${errorMessage(err)}`);
      }
    }
  }, [load]);

  const move = useCallback(
    async (index: number, delta: number): Promise<boolean> => {
      if (state.status !== "ready" || moving.current) return false;
      const ids = moved(state.items, index, delta).map((r) => r.id);
      moving.current = true;
      setBusy(true);
      setError(null);
      try {
        await reorderRoutes(direction, ids);
        setState((prev) =>
          prev.status === "ready"
            ? { status: "ready", items: applyOrder(prev.items, ids) }
            : prev,
        );
        return true;
      } catch (err) {
        // The server's order may differ from ours (another admin, a stale
        // page): re-read it before any further move.
        setError(
          `Could not reorder: ${errorMessage(err)} The list was reloaded from the server.`,
        );
        await recover();
        return false;
      } finally {
        moving.current = false;
        setBusy(false);
      }
    },
    [direction, state, recover],
  );

  const upsert = useCallback((item: T) => {
    local.current.upserts.set(String(item.id), item);
    local.current.removed.delete(String(item.id));
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
    local.current.removed.add(String(routeId));
    local.current.upserts.delete(String(routeId));
    setState((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: prev.items.filter((r) => r.id !== routeId) }
        : prev,
    );
  }, []);

  return { state, items, busy, error, setError, move, upsert, remove };
}
