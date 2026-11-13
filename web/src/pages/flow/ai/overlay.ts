// An AI proposal on the canvas (spec flow-canvas S-19): the change its
// configuration batch would make, drawn over the graph as a ghost before
// anybody applies it. Pure functions, no React: the graph with the change
// in it, places for the nodes it creates, the marks on the view, and where
// the created nodes go once the batch is applied (their ids are only known
// then).

import type { Edge } from "@xyflow/react";
import type { FlowGraphDiff } from "../../../api/configBatch";
import type { FlowGraph, FlowNode } from "../../../api/flow";
import type { Pos } from "../layoutState";
import type { CanvasView } from "../model";

/** Horizontal and vertical step between a ghost node and its neighbour. */
export const GHOST_DX = 320;
export const GHOST_DY = 150;

/**
 * The graph as the proposal leaves it: created nodes and connections added,
 * changed nodes as they would be. Deleted nodes and dropped connections stay
 * so they can be drawn struck out.
 */
export function overlayGraph(graph: FlowGraph, diff: FlowGraphDiff): FlowGraph {
  const changed = new Map(diff.changed.map((n) => [n.id, n]));
  const have = new Set(graph.nodes.map((n) => n.id));
  const replaced = new Set(diff.edgesAdded.map((e) => e.id));
  return {
    ...graph,
    nodes: [
      ...graph.nodes.map((n) => changed.get(n.id) ?? n),
      ...diff.added.filter((n) => !have.has(n.id)),
    ],
    edges: [
      ...graph.edges.filter((e) => !replaced.has(e.id)),
      ...diff.edgesAdded,
    ],
  };
}

/** The draft marks of the overlay: created and changed nodes. */
export function overlayMarks(
  diff: FlowGraphDiff,
): Record<string, "new" | "changed"> {
  const out: Record<string, "new" | "changed"> = {};
  for (const n of diff.changed) out[n.id] = "changed";
  for (const n of diff.added) out[n.id] = "new";
  return out;
}

/**
 * Places for the created nodes that have none: next to a connected node that
 * has one (to the right of what leads to it, to the left of what it leads
 * to), else in a column right of everything. Chains of created nodes are
 * followed. The canvas's own positions are never moved.
 */
export function ghostPositions(
  positions: Record<string, Pos>,
  graph: FlowGraph,
  diff: FlowGraphDiff,
): Record<string, Pos> {
  const out: Record<string, Pos> = { ...positions };
  const todo = diff.added.map((n) => n.id).filter((id) => !out[id]);
  const used = new Set(Object.values(out).map((p) => `${p.x}|${p.y}`));
  const free = (x: number, y: number): Pos => {
    let yy = y;
    while (used.has(`${x}|${yy}`)) yy += GHOST_DY;
    used.add(`${x}|${yy}`);
    return { x, y: yy, collapsed: false };
  };
  let progress = true;
  while (todo.length > 0 && progress) {
    progress = false;
    for (let i = 0; i < todo.length; i++) {
      const id = todo[i] ?? "";
      let pos: Pos | undefined;
      for (const e of graph.edges) {
        const from = e.target.node === id ? out[e.source.node] : undefined;
        if (from) {
          pos = free(from.x + GHOST_DX, from.y);
          break;
        }
        const to = e.source.node === id ? out[e.target.node] : undefined;
        if (to) {
          pos = free(to.x - GHOST_DX, to.y);
          break;
        }
      }
      if (pos) {
        out[id] = pos;
        todo.splice(i, 1);
        i--;
        progress = true;
      }
    }
  }
  if (todo.length > 0) {
    const all = Object.values(out);
    const x = all.length ? Math.max(...all.map((p) => p.x)) + GHOST_DX : 0;
    const y = all.length ? Math.min(...all.map((p) => p.y)) : 0;
    for (const id of todo) out[id] = free(x, y);
  }
  return out;
}

/** Marks the overlay on the view: what the proposal adds, changes and drops. */
export function applyOverlay(
  view: CanvasView,
  diff: FlowGraphDiff | null,
): CanvasView {
  if (!diff) return view;
  const added = new Set(diff.added.map((n) => n.id));
  const changed = new Set(diff.changed.map((n) => n.id));
  const removed = new Set(diff.removed);
  const edgesAdded = new Set(diff.edgesAdded.map((e) => e.id));
  const edgesRemoved = new Set(diff.edgesRemoved);
  const cls = (base: string | undefined, extra: string) =>
    [base, extra].filter(Boolean).join(" ");
  return {
    ...view,
    nodes: view.nodes.map((n) => {
      const c = added.has(n.id)
        ? "flow-node--ai-added"
        : removed.has(n.id)
          ? "flow-node--ai-removed"
          : changed.has(n.id)
            ? "flow-node--ai-changed"
            : "";
      return c ? { ...n, className: cls(n.className, c) } : n;
    }),
    edges: view.edges.map((e): Edge => {
      if (edgesAdded.has(e.id)) {
        return {
          ...e,
          className: cls(e.className, "flow-edge--ai-added"),
          animated: true,
          zIndex: 10,
        };
      }
      if (edgesRemoved.has(e.id)) {
        return { ...e, className: cls(e.className, "flow-edge--ai-removed") };
      }
      return e;
    }),
  };
}

/** Counts for the summary line of a proposal. */
export function overlayCounts(diff: FlowGraphDiff): {
  added: number;
  changed: number;
  removed: number;
  connections: number;
} {
  const re = new Set(diff.edgesAdded.map((e) => e.id));
  const dropped = diff.edgesRemoved.filter((id) => !re.has(id)).length;
  return {
    added: diff.added.length,
    changed: diff.changed.length,
    removed: diff.removed.length,
    connections: diff.edgesAdded.length + dropped,
  };
}

/**
 * Where to put the nodes an applied batch created. The dry run gave them
 * ids that the real run does not reuse, so each ghost is matched to a node
 * of the new graph that the old one did not have, by kind and label.
 */
export function placementsAfterApply(
  before: FlowGraph,
  after: FlowGraph,
  ghosts: readonly FlowNode[],
  positions: Record<string, Pos>,
): { node: string; x: number; y: number }[] {
  const old = new Set(before.nodes.map((n) => n.id));
  const fresh = after.nodes.filter((n) => !old.has(n.id));
  const taken = new Set<string>();
  const out: { node: string; x: number; y: number }[] = [];
  for (const g of ghosts) {
    const p = positions[g.id];
    if (!p) continue;
    const match = old.has(g.id)
      ? undefined
      : (fresh.find((n) => n.id === g.id && !taken.has(n.id)) ??
        fresh.find(
          (n) => !taken.has(n.id) && n.kind === g.kind && n.label === g.label,
        ));
    if (!match) continue;
    taken.add(match.id);
    out.push({ node: match.id, x: Math.round(p.x), y: Math.round(p.y) });
  }
  return out;
}
