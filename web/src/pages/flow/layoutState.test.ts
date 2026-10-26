import { describe, expect, it } from "vitest";
import {
  emptyLayout,
  layoutReducer,
  pendingWrite,
  type LayoutAction,
  type LayoutState,
} from "./layoutState";

const run = (actions: LayoutAction[], from: LayoutState = emptyLayout) =>
  actions.reduce(layoutReducer, from);

const loaded = run([
  {
    type: "load",
    version: 4,
    nodes: [
      { node: "extension:1", x: 10, y: 20, collapsed: false },
      { node: "ringGroup:2", x: 300, y: 20, collapsed: false },
    ],
  },
]);

describe("layoutReducer", () => {
  it("loads the saved positions with nothing pending", () => {
    expect(loaded.version).toBe(4);
    expect(loaded.positions["extension:1"]).toEqual({
      x: 10,
      y: 20,
      collapsed: false,
    });
    expect(pendingWrite(loaded)).toBeNull();
  });

  it("marks a move pending and ignores one that changes nothing", () => {
    const same = run(
      [{ type: "move", node: "extension:1", x: 10, y: 20 }],
      loaded,
    );
    expect(same).toBe(loaded);
    const moved = run(
      [{ type: "move", node: "extension:1", x: 50, y: 60 }],
      loaded,
    );
    expect(pendingWrite(moved)).toEqual({
      layoutVersion: 4,
      nodes: [{ node: "extension:1", x: 50, y: 60, collapsed: false }],
      remove: [],
    });
  });

  it("ignores a move of a node that is not on the canvas", () => {
    expect(run([{ type: "move", node: "trunk:9", x: 1, y: 1 }], loaded)).toBe(
      loaded,
    );
  });

  it("places and removes nodes", () => {
    const placed = run(
      [{ type: "place", nodes: [{ node: "trunk:3", x: 5, y: 6 }] }],
      loaded,
    );
    expect(pendingWrite(placed)?.nodes.map((n) => n.node)).toEqual(["trunk:3"]);
    const removed = run([{ type: "remove", node: "extension:1" }], placed);
    expect(removed.positions["extension:1"]).toBeUndefined();
    expect(pendingWrite(removed)?.remove).toEqual(["extension:1"]);
    // Placing again cancels the removal.
    const back = run(
      [{ type: "place", nodes: [{ node: "extension:1", x: 0, y: 0 }] }],
      removed,
    );
    expect(back.removed["extension:1"]).toBeUndefined();
  });

  it("clears what a save sent, but not what moved again meanwhile", () => {
    const a = run(
      [
        { type: "move", node: "extension:1", x: 50, y: 60 },
        { type: "move", node: "ringGroup:2", x: 400, y: 40 },
      ],
      loaded,
    );
    const sent = pendingWrite(a);
    // ringGroup:2 moves again while the save is in flight.
    const b = run([{ type: "move", node: "ringGroup:2", x: 500, y: 40 }], a);
    const done = run(
      [
        {
          type: "synced",
          version: 5,
          nodes: [
            { node: "extension:1", x: 50, y: 60, collapsed: false },
            { node: "ringGroup:2", x: 400, y: 40, collapsed: false },
          ],
          sent: { nodes: sent?.nodes ?? [], remove: [] },
        },
      ],
      b,
    );
    expect(done.version).toBe(5);
    expect(done.positions["ringGroup:2"]?.x).toBe(500);
    expect(pendingWrite(done)?.nodes.map((n) => n.node)).toEqual([
      "ringGroup:2",
    ]);
    expect(pendingWrite(done)?.layoutVersion).toBe(5);
  });

  it("merges per node after a stale layout: local moves win, the rest follows the server", () => {
    const mine = run(
      [{ type: "move", node: "extension:1", x: 99, y: 99 }],
      loaded,
    );
    const merged = run(
      [
        {
          type: "synced",
          version: 7,
          nodes: [
            { node: "extension:1", x: 11, y: 21, collapsed: false },
            { node: "ringGroup:2", x: 321, y: 22, collapsed: false },
            { node: "trunk:1", x: 0, y: 0, collapsed: false },
          ],
        },
      ],
      mine,
    );
    expect(merged.positions["extension:1"]).toMatchObject({ x: 99, y: 99 });
    expect(merged.positions["ringGroup:2"]).toMatchObject({ x: 321, y: 22 });
    expect(merged.positions["trunk:1"]).toBeDefined();
    expect(pendingWrite(merged)).toEqual({
      layoutVersion: 7,
      nodes: [{ node: "extension:1", x: 99, y: 99, collapsed: false }],
      remove: [],
    });
  });

  it("does not bring back a node removed here when the server still lists it", () => {
    const removed = run([{ type: "remove", node: "extension:1" }], loaded);
    const merged = run(
      [
        {
          type: "synced",
          version: 6,
          nodes: [{ node: "extension:1", x: 10, y: 20, collapsed: false }],
        },
      ],
      removed,
    );
    expect(merged.positions["extension:1"]).toBeUndefined();
    expect(pendingWrite(merged)?.remove).toEqual(["extension:1"]);
  });

  it("drops a saved node the server pruned, keeps one not sent yet", () => {
    const a = run(
      [{ type: "place", nodes: [{ node: "extension:9", x: 1, y: 1 }] }],
      loaded,
    );
    const sent = pendingWrite(a);
    const b = run(
      [{ type: "place", nodes: [{ node: "trunk:5", x: 2, y: 2 }] }],
      a,
    );
    const done = run(
      [
        {
          type: "synced",
          version: 5,
          // The server pruned extension:9: its object is gone.
          nodes: [],
          sent: { nodes: sent?.nodes ?? [], remove: [] },
        },
      ],
      b,
    );
    expect(done.positions["extension:9"]).toBeUndefined();
    expect(done.positions["trunk:5"]).toBeDefined();
    expect(pendingWrite(done)?.nodes.map((n) => n.node)).toEqual(["trunk:5"]);
  });
});
