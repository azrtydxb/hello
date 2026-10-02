import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { json, mockApi, noContent } from "./test/api";
import { useOrderedList } from "./useOrderedList";

interface Row {
  id: number;
  position: number;
  name: string;
}

const row = (id: number, position: number, name: string): Row => ({
  id,
  position,
  name,
});

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((r) => {
    resolve = r;
  });
  return { promise, resolve };
}

const ORDER = "PUT /api/v1/routes/outbound/order";
const REFUSED = () =>
  json({ error: { code: "conflict", message: "order is stale" } }, 409);

describe("useOrderedList", () => {
  it("applies a late reorder to the latest items, keeping edits, creates and deletes", async () => {
    const put = deferred<Response>();
    const calls = mockApi({ [ORDER]: () => put.promise });
    const load = vi.fn(() =>
      Promise.resolve([row(1, 1, "A"), row(2, 2, "B"), row(3, 3, "C")]),
    );
    const { result } = renderHook(() => useOrderedList("outbound", load));
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let moving!: Promise<boolean>;
    act(() => {
      moving = result.current.move(2, -1); // C up: A, C, B
    });
    expect(result.current.busy).toBe(true);

    // These finish while the PUT is still pending.
    act(() => {
      result.current.upsert(row(2, 2, "B edited"));
      result.current.upsert(row(4, 4, "D"));
      result.current.remove(1);
    });

    await act(async () => {
      put.resolve(noContent());
      expect(await moving).toBe(true);
    });

    expect(calls.map((c) => c.body)).toEqual([{ ids: [1, 3, 2] }]);
    expect(result.current.items).toEqual([
      row(3, 1, "C"),
      row(2, 2, "B edited"),
      row(4, 3, "D"),
    ]);
    expect(result.current.busy).toBe(false);
  });

  it("blocks moves until the stored order arrives after a refused reorder", async () => {
    const stored = deferred<Row[]>();
    const load = vi
      .fn<(signal: AbortSignal) => Promise<Row[]>>()
      .mockResolvedValueOnce([row(1, 1, "A"), row(2, 2, "B")])
      .mockReturnValueOnce(stored.promise);
    const calls = mockApi({ [ORDER]: [REFUSED, noContent] });
    const { result } = renderHook(() => useOrderedList("outbound", load));
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let first!: Promise<boolean>;
    act(() => {
      first = result.current.move(1, -1); // B up: refused
    });
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
    expect(result.current.busy).toBe(true);
    expect(result.current.error).toMatch(/order is stale/);

    // A move while the stored order is pending sends nothing.
    let blocked!: boolean;
    await act(async () => {
      blocked = await result.current.move(0, 1);
    });
    expect(blocked).toBe(false);
    expect(calls).toHaveLength(1);

    // Changes made meanwhile survive the (older) list that arrives next.
    act(() => {
      result.current.remove(1);
      result.current.upsert(row(9, 9, "New"));
    });
    await act(async () => {
      stored.resolve([row(3, 1, "Z"), row(2, 2, "B"), row(1, 3, "A")]);
      expect(await first).toBe(false);
    });
    expect(result.current.busy).toBe(false);
    expect(result.current.items.map((r) => r.name)).toEqual(["Z", "B", "New"]);

    // Now a move permutes the stored order.
    await act(async () => {
      expect(await result.current.move(2, -1)).toBe(true);
    });
    expect(calls.map((c) => c.body)).toEqual([
      { ids: [2, 1] },
      { ids: [3, 9, 2] },
    ]);
    expect(result.current.items.map((r) => r.name)).toEqual(["Z", "New", "B"]);
  });

  it("ignores a second move in the same tick", async () => {
    const put = deferred<Response>();
    const calls = mockApi({ [ORDER]: () => put.promise });
    const load = vi.fn(() => Promise.resolve([row(1, 1, "A"), row(2, 2, "B")]));
    const { result } = renderHook(() => useOrderedList("outbound", load));
    await waitFor(() => expect(result.current.state.status).toBe("ready"));

    let a!: Promise<boolean>;
    let b!: Promise<boolean>;
    act(() => {
      a = result.current.move(1, -1);
      b = result.current.move(1, -1);
    });
    await act(async () => {
      put.resolve(noContent());
      expect(await a).toBe(true);
      expect(await b).toBe(false);
    });
    expect(calls).toHaveLength(1);
  });
});
