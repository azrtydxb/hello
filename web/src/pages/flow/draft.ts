// The draft of a flow canvas (spec flow-canvas S-12, S-13): the changes the
// user has staged and not applied yet, as a list of batch operations with
// undo and redo. Pure: the reducer, the merge of edits to the same object,
// the rebase onto a newer configuration, the batch request it compiles to,
// and the graph with the draft applied (so the canvas can draw it).

import type { BatchOp, BatchRequest } from "../../api/configBatch";
import type { FlowEdge, FlowGraph, FlowNode } from "../../api/flow";

export type Body = Record<string, unknown>;

/** The edges an operation adds and removes in the drawn graph. */
export interface EdgeChange {
  remove: string[];
  add: FlowEdge[];
  /** Terminal nodes the new edges lead to that the graph does not have yet. */
  nodes?: FlowNode[];
}

/** One staged operation. */
export interface DraftOp {
  /** Edits with the same key merge into one operation. */
  key: string;
  kind: "create" | "update" | "delete";
  /** The graph node it is about; `<kind>:~<ref>` while the node is new. */
  node: string;
  /** Names the result of a create (`${ref.id}` in later operations). */
  ref?: string;
  operationId: string;
  pathParams?: Record<string, string>;
  body?: Body;
  /** The node version the edit was prepared against. */
  expect?: string;
  /** Versions of every node the edit read, to find conflicts on rebase. */
  touches: Record<string, string>;
  edges: EdgeChange[];
  /** New label of the node (a rename). */
  label?: string;
  /** A created node as the canvas draws it. */
  pending?: FlowNode;
  /** One line for the review list. */
  summary: string;
  /** The gestures that staged it: they stand or fall together on a rebase. */
  groups?: string[];
}

export interface DraftState {
  /** The configuration revision the draft was prepared against. */
  baseRevision: number;
  /** Counter for the refs of new nodes. */
  seq: number;
  ops: DraftOp[];
  past: DraftOp[][];
  future: DraftOp[][];
}

export type DraftAction =
  /** Stage operations as one undoable step. */
  | { type: "stage"; ops: readonly DraftOp[] }
  | { type: "undo" }
  | { type: "redo" }
  | { type: "clear"; baseRevision: number }
  | { type: "rebase"; ops: readonly DraftOp[]; baseRevision: number }
  | { type: "load"; state: DraftState };

const MAX_HISTORY = 60;

export const emptyDraft = (baseRevision: number): DraftState => ({
  baseRevision,
  seq: 0,
  ops: [],
  past: [],
  future: [],
});

/** The ref of a node created in the draft, or null for a stored node. */
export function pendingRef(nodeId: string): string | null {
  const i = nodeId.indexOf(":~");
  return i < 0 ? null : nodeId.slice(i + 2);
}

export const isPending = (nodeId: string): boolean =>
  pendingRef(nodeId) !== null;

/** The next ref (`n1`, `n2`, ...) and the draft counter after it. */
export function nextRef(state: DraftState): { ref: string; seq: number } {
  const seq = state.seq + 1;
  return { ref: `n${seq}`, seq };
}

function mergeInto(old: DraftOp, next: DraftOp): DraftOp {
  return {
    ...old,
    body: { ...old.body, ...next.body },
    expect: old.expect ?? next.expect,
    touches: { ...next.touches, ...old.touches },
    edges: [...old.edges, ...next.edges],
    groups: [...new Set([...(old.groups ?? []), ...(next.groups ?? [])])],
    label: next.label ?? old.label,
    pending: old.pending
      ? {
          ...old.pending,
          label: next.label ?? old.pending.label,
          summary: { ...old.pending.summary, ...next.pending?.summary },
        }
      : old.pending,
    summary: old.kind === "create" ? old.summary : next.summary,
  };
}

/** One operation staged onto the list: merged, or replacing, or removing. */
export function stageOp(ops: readonly DraftOp[], op: DraftOp): DraftOp[] {
  if (op.kind === "delete") {
    const ref = pendingRef(op.node);
    // A node that only exists in the draft is simply forgotten.
    if (ref !== null) return ops.filter((o) => o.node !== op.node);
    return [...ops.filter((o) => o.node !== op.node), op];
  }
  const ref = pendingRef(op.node);
  // An edit of a new node goes into its create operation.
  const key = op.kind === "update" && ref !== null ? `create|${ref}` : op.key;
  const at = ops.findIndex((o) => o.key === key);
  if (at < 0) return [...ops, { ...op, key }];
  const merged = [...ops];
  const old = ops[at];
  if (old) merged[at] = op.kind === "create" ? op : mergeInto(old, op);
  return merged;
}

export function draftReducer(
  state: DraftState,
  action: DraftAction,
): DraftState {
  switch (action.type) {
    case "stage": {
      let ops = state.ops;
      let seq = state.seq;
      for (const op of action.ops) {
        ops = stageOp(ops, op);
        const ref = op.ref ? Number(/^n(\d+)$/.exec(op.ref)?.[1]) : NaN;
        if (Number.isFinite(ref)) seq = Math.max(seq, ref);
      }
      if (ops === state.ops) return state;
      return {
        ...state,
        seq,
        ops,
        past: [...state.past, state.ops].slice(-MAX_HISTORY),
        future: [],
      };
    }
    case "undo": {
      const prev = state.past[state.past.length - 1];
      if (!prev) return state;
      return {
        ...state,
        ops: prev,
        past: state.past.slice(0, -1),
        future: [state.ops, ...state.future],
      };
    }
    case "redo": {
      const next = state.future[0];
      if (!next) return state;
      return {
        ...state,
        ops: next,
        past: [...state.past, state.ops],
        future: state.future.slice(1),
      };
    }
    case "clear":
      return { ...emptyDraft(action.baseRevision), seq: state.seq };
    case "rebase":
      // The old steps would bring back what the rebase dropped.
      return {
        ...state,
        baseRevision: action.baseRevision,
        ops: [...action.ops],
        past: [],
        future: [],
      };
    case "load":
      return action.state;
  }
}

/**
 * The draft on a newer configuration: an operation survives when every node
 * it read still has the version it had, and is dropped (and reported) when
 * someone else changed or deleted one. Nothing conflicting is kept.
 */
export function rebase(
  state: DraftState,
  graph: FlowGraph,
): { ops: DraftOp[]; dropped: DraftOp[] } {
  const versions = new Map(graph.nodes.map((n) => [n.id, n.version ?? ""]));
  const stale = new Set<DraftOp>();
  for (const op of state.ops) {
    if (
      Object.entries(op.touches).some(
        ([id, v]) => !isPending(id) && versions.get(id) !== v,
      )
    ) {
      stale.add(op);
    }
  }
  // A gesture changes several objects (a rename and every referrer): when
  // one of them cannot be kept, none of them is.
  for (let grew = true; grew;) {
    grew = false;
    const hit = new Set([...stale].flatMap((o) => o.groups ?? []));
    for (const op of state.ops) {
      if (!stale.has(op) && (op.groups ?? []).some((g) => hit.has(g))) {
        stale.add(op);
        grew = true;
      }
    }
  }
  const dropped = state.ops.filter((o) => stale.has(o));
  const alive = state.ops.filter((o) => !stale.has(o));
  return { ops: alive, dropped };
}

/** The request a draft makes: creates first, then edits, then deletes. */
export function compile(
  state: DraftState,
  opts: {
    dryRun: boolean;
    canvasId?: number | string;
    positions?: Record<string, { x: number; y: number }>;
  },
): BatchRequest {
  const sorted = compiledOrder(state);
  const ops: BatchOp[] = sorted.map((o) => ({
    ...(o.ref ? { ref: o.ref } : {}),
    operationId: o.operationId,
    ...(o.pathParams ? { pathParams: o.pathParams } : {}),
    ...(o.body ? { body: o.body } : {}),
    ...(o.expect ? { expect: o.expect } : {}),
  }));
  const layout: { node: string; x: number; y: number }[] = [];
  for (const o of sorted) {
    if (o.kind !== "create" || !o.ref || !o.pending) continue;
    const pos = opts.positions?.[o.node];
    if (!pos) continue;
    const kind = o.node.slice(0, o.node.indexOf(":"));
    layout.push({ node: `${kind}:\${${o.ref}.id}`, x: pos.x, y: pos.y });
  }
  return {
    baseRevision: state.baseRevision,
    dryRun: opts.dryRun,
    ...(opts.canvasId !== undefined ? { canvasId: opts.canvasId } : {}),
    ops,
    ...(layout.length > 0 ? { layout } : {}),
  };
}

/** The draft's operations in the order the request runs them. */
export function compiledOrder(state: DraftState): DraftOp[] {
  const rank = { create: 0, update: 1, delete: 2 } as const;
  return state.ops
    .map((op, i) => ({ op, i }))
    .sort((a, b) => rank[a.op.kind] - rank[b.op.kind] || a.i - b.i)
    .map((x) => x.op);
}

/** The graph as it will be once the draft is applied. */
export function project(graph: FlowGraph, state: DraftState): FlowGraph {
  if (state.ops.length === 0) return graph;
  const deleted = new Set(
    state.ops.filter((o) => o.kind === "delete").map((o) => o.node),
  );
  const labels = new Map<string, string>();
  for (const o of state.ops) if (o.label) labels.set(o.node, o.label);
  const nodes: FlowNode[] = graph.nodes
    .filter((n) => !deleted.has(n.id))
    .map((n) => (labels.has(n.id) ? { ...n, label: labels.get(n.id)! } : n));
  for (const o of state.ops) {
    if (o.kind === "create" && o.pending) nodes.push(o.pending);
    for (const c of o.edges) {
      for (const n of c.nodes ?? []) {
        if (!nodes.some((x) => x.id === n.id)) nodes.push(n);
      }
    }
  }
  let edges = graph.edges.filter(
    (e) => !deleted.has(e.source.node) && !deleted.has(e.target.node),
  );
  for (const o of state.ops) {
    for (const c of o.edges) {
      const drop = new Set(c.remove);
      edges = edges.filter((e) => !drop.has(e.id));
      for (const e of c.add) {
        if (!edges.some((x) => x.id === e.id)) edges.push(e);
      }
    }
  }
  const ids = new Set(nodes.map((n) => n.id));
  edges = edges.filter((e) => ids.has(e.source.node) && ids.has(e.target.node));
  return {
    ...graph,
    nodes,
    edges,
    warnings: graph.warnings.filter((w) => ids.has(w.node)),
  };
}

/** What the draft does to each node, for the badges on the canvas. */
export function draftMarks(
  state: DraftState,
): Record<string, "new" | "changed" | "deleted"> {
  const out: Record<string, "new" | "changed" | "deleted"> = {};
  for (const o of state.ops) {
    out[o.node] =
      o.kind === "create" ? "new" : o.kind === "delete" ? "deleted" : "changed";
  }
  return out;
}

// --- persistence (browser, per canvas and user) ------------------------------

export const draftKey = (canvasId: number | string, user: string): string =>
  `hello.flow.draft.${canvasId}.${user}`;

/** Where the draft's new nodes sit, kept with the draft. */
export type DraftPositions = Record<string, { x: number; y: number }>;

export function loadDraft(
  key: string,
): (DraftState & { positions: DraftPositions }) | null {
  try {
    const raw = localStorage.getItem(key);
    if (!raw) return null;
    const v = JSON.parse(raw) as Partial<DraftState> & {
      history?: { past?: DraftOp[][]; future?: DraftOp[][] };
    };
    if (typeof v.baseRevision !== "number" || !Array.isArray(v.ops)) {
      return null;
    }
    return {
      baseRevision: v.baseRevision,
      seq: typeof v.seq === "number" ? v.seq : 0,
      ops: v.ops,
      past: v.history?.past ?? [],
      future: v.history?.future ?? [],
      positions:
        typeof (v as { positions?: unknown }).positions === "object" &&
        (v as { positions?: unknown }).positions !== null
          ? (v as { positions: DraftPositions }).positions
          : {},
    };
  } catch {
    return null;
  }
}

export function saveDraft(
  key: string,
  state: DraftState,
  positions: DraftPositions = {},
): void {
  try {
    if (state.ops.length === 0 && state.past.length === 0) {
      localStorage.removeItem(key);
      return;
    }
    localStorage.setItem(
      key,
      JSON.stringify({
        baseRevision: state.baseRevision,
        seq: state.seq,
        ops: state.ops,
        history: { past: state.past, future: state.future },
        positions,
      }),
    );
  } catch {
    // Storage full or unavailable: the draft lives on in memory.
  }
}
