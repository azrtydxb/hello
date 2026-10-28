// The positions on one canvas and what is not saved yet, as a pure reducer
// (spec flow-canvas S-7, S-9): a move or an added node is dirty until a
// layout save succeeds; a save that finds the layout moved (409) reloads the
// canvas and merges per node, so the local moves win over the server's and
// everyone else's moves are kept.

import type { FlowCanvasNode } from "../../api/flow";
import { isPending } from "./draft";

export interface Pos {
  x: number;
  y: number;
  collapsed: boolean;
}

export interface LayoutState {
  /** The canvas's layoutVersion as last read. */
  version: number;
  positions: Record<string, Pos>;
  /** Nodes moved or added here and not saved yet. */
  dirty: Record<string, true>;
  /** Nodes taken off the canvas here and not saved yet. */
  removed: Record<string, true>;
}

export type LayoutAction =
  | { type: "load"; version: number; nodes: readonly FlowCanvasNode[] }
  | { type: "move"; node: string; x: number; y: number }
  | { type: "place"; nodes: readonly { node: string; x: number; y: number }[] }
  | { type: "remove"; node: string }
  /** Drops nodes without saving anything (a draft node that was discarded). */
  | { type: "forget"; nodes: readonly string[] }
  | {
      /** A save succeeded (or, with empty `sent`, the canvas was reloaded). */
      type: "synced";
      version: number;
      nodes: readonly FlowCanvasNode[];
      sent?: { nodes: readonly FlowCanvasNode[]; remove: readonly string[] };
    };

export const emptyLayout: LayoutState = {
  version: 0,
  positions: {},
  dirty: {},
  removed: {},
};

function fromNodes(nodes: readonly FlowCanvasNode[]): Record<string, Pos> {
  const out: Record<string, Pos> = {};
  for (const n of nodes) {
    out[n.node] = { x: n.x, y: n.y, collapsed: n.collapsed };
  }
  return out;
}

export function layoutReducer(
  state: LayoutState,
  action: LayoutAction,
): LayoutState {
  switch (action.type) {
    case "load":
      return {
        version: action.version,
        positions: fromNodes(action.nodes),
        dirty: {},
        removed: {},
      };
    case "move": {
      const cur = state.positions[action.node];
      if (!cur || state.removed[action.node]) return state;
      if (cur.x === action.x && cur.y === action.y) return state;
      return {
        ...state,
        positions: {
          ...state.positions,
          [action.node]: { ...cur, x: action.x, y: action.y },
        },
        dirty: { ...state.dirty, [action.node]: true },
      };
    }
    case "place": {
      const positions = { ...state.positions };
      const dirty = { ...state.dirty };
      const removed = { ...state.removed };
      for (const n of action.nodes) {
        positions[n.node] = {
          x: n.x,
          y: n.y,
          collapsed: positions[n.node]?.collapsed ?? false,
        };
        dirty[n.node] = true;
        delete removed[n.node];
      }
      return { ...state, positions, dirty, removed };
    }
    case "remove": {
      if (!state.positions[action.node]) return state;
      const positions = { ...state.positions };
      const dirty = { ...state.dirty };
      delete positions[action.node];
      delete dirty[action.node];
      return {
        ...state,
        positions,
        dirty,
        removed: { ...state.removed, [action.node]: true },
      };
    }
    case "forget": {
      const gone = new Set(action.nodes);
      const keep = <T>(m: Record<string, T>): Record<string, T> =>
        Object.fromEntries(Object.entries(m).filter(([k]) => !gone.has(k)));
      return {
        ...state,
        positions: keep(state.positions),
        dirty: keep(state.dirty),
        removed: keep(state.removed),
      };
    }
    case "synced": {
      const server = fromNodes(action.nodes);
      const sentPos = fromNodes(action.sent?.nodes ?? []);
      const sentRemove = new Set(action.sent?.remove ?? []);
      const dirty: Record<string, true> = {};
      const removed: Record<string, true> = {};
      const positions: Record<string, Pos> = {};
      // What was sent is saved unless it changed again while the save was
      // in flight; what was not sent stays pending.
      for (const id of Object.keys(state.dirty)) {
        const cur = state.positions[id];
        const sent = sentPos[id];
        if (!cur) continue;
        if (!sent || sent.x !== cur.x || sent.y !== cur.y) dirty[id] = true;
      }
      for (const id of Object.keys(state.removed)) {
        if (!sentRemove.has(id)) removed[id] = true;
      }
      for (const [id, p] of Object.entries(server)) {
        if (removed[id]) continue;
        positions[id] =
          dirty[id] && state.positions[id] ? state.positions[id] : p;
      }
      // A pending node the server does not list: pruned there (its object
      // is gone) when it was part of this save, otherwise still to be sent.
      for (const id of Object.keys(dirty)) {
        if (id in server) continue;
        const cur = state.positions[id];
        if (cur && !sentPos[id]) positions[id] = cur;
        else delete dirty[id];
      }
      return { version: action.version, positions, dirty, removed };
    }
  }
}

/** What the next layout save carries, or null when nothing is pending. */
export function pendingWrite(state: LayoutState): {
  layoutVersion: number;
  nodes: FlowCanvasNode[];
  remove: string[];
} | null {
  const nodes: FlowCanvasNode[] = [];
  for (const id of Object.keys(state.dirty).sort()) {
    // A node that only exists in the draft has no place on the server yet.
    if (isPending(id)) continue;
    const p = state.positions[id];
    if (p) nodes.push({ node: id, ...p });
  }
  const remove = Object.keys(state.removed)
    .filter((id) => !isPending(id))
    .sort();
  if (nodes.length === 0 && remove.length === 0) return null;
  return { layoutVersion: state.version, nodes, remove };
}
