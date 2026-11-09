// Auto-layout of nodes placed by "Import flow" (spec flow-canvas S-8): dagre,
// left to right, so ranks run entry -> routes -> branches -> targets ->
// terminals as the edges lead. Only nodes without a position are placed, and
// they go to the right of what is already on the canvas.

import { Graph, layout } from "@dagrejs/dagre";
import type { FlowCatalogue, FlowEdge, FlowNode } from "../../api/flow";
import { NODE_WIDTH, nodeHeight, nodeTypeOf, portsFor } from "./model";
import type { Pos } from "./layoutState";

export interface Placement {
  node: string;
  x: number;
  y: number;
}

export interface LayoutInput {
  nodes: { id: string; width: number; height: number }[];
  edges: { source: string; target: string }[];
  /** Where the placed nodes start (the left edge and top of the result). */
  originX: number;
  originY: number;
}

const GAP = 120;

/**
 * Runs dagre over `input`. Pure and free of DOM, so a worker can call it
 * (the main thread does for small graphs and in tests).
 */
export function runDagre(input: LayoutInput): Placement[] {
  if (input.nodes.length === 0) return [];
  const g = new Graph({ directed: true, multigraph: true });
  g.setGraph({
    rankdir: "LR",
    nodesep: 36,
    ranksep: 110,
    marginx: 0,
    marginy: 0,
  });
  g.setDefaultEdgeLabel(() => ({}));
  const known = new Set(input.nodes.map((n) => n.id));
  for (const n of input.nodes)
    g.setNode(n.id, { width: n.width, height: n.height });
  input.edges.forEach((e, i) => {
    if (known.has(e.source) && known.has(e.target) && e.source !== e.target) {
      g.setEdge(e.source, e.target, {}, `e${i}`);
    }
  });
  layout(g);
  let minX = Infinity;
  let minY = Infinity;
  const centres = input.nodes.map((n) => {
    const p = g.node(n.id);
    const x = p.x - n.width / 2;
    const y = p.y - n.height / 2;
    minX = Math.min(minX, x);
    minY = Math.min(minY, y);
    return { node: n.id, x, y };
  });
  return centres.map((c) => ({
    node: c.node,
    x: Math.round(c.x - minX + input.originX),
    y: Math.round(c.y - minY + input.originY),
  }));
}

/** The corner a new block of nodes starts at: right of what is placed. */
export function originFor(positions: Record<string, Pos>): {
  x: number;
  y: number;
} {
  let right = -Infinity;
  let top = Infinity;
  for (const p of Object.values(positions)) {
    right = Math.max(right, p.x + NODE_WIDTH);
    top = Math.min(top, p.y);
  }
  if (!Number.isFinite(right)) return { x: 0, y: 0 };
  return { x: right + GAP + 120, y: top };
}

/**
 * The dagre input for the nodes of `incoming` that are not in `positions`.
 * Edges among those new nodes decide the ranks.
 */
export function layoutInput(
  incoming: readonly FlowNode[],
  edges: readonly FlowEdge[],
  catalogue: FlowCatalogue | undefined,
  positions: Record<string, Pos>,
): LayoutInput {
  const fresh = incoming.filter((n) => !positions[n.id]);
  const nodes = fresh.map((n) => {
    const own = edges.filter(
      (e) => e.source.node === n.id || e.target.node === n.id,
    );
    const { inputs, outputs } = portsFor(
      nodeTypeOf(catalogue, n.kind),
      n.id,
      own,
    );
    return {
      id: n.id,
      width: NODE_WIDTH,
      height: nodeHeight(inputs.length, outputs.length),
    };
  });
  const origin = originFor(positions);
  return {
    nodes,
    edges: edges.map((e) => ({ source: e.source.node, target: e.target.node })),
    originX: origin.x,
    originY: origin.y,
  };
}

/** Graphs at least this large are laid out in a Web Worker. */
export const WORKER_THRESHOLD = 150;

/**
 * Lays `input` out: in a Web Worker for a large graph (so the page stays
 * responsive and the work can be cancelled through `signal`), otherwise on
 * the calling thread. Rejects with an AbortError when cancelled.
 */
export function computeLayout(
  input: LayoutInput,
  signal?: AbortSignal,
): Promise<Placement[]> {
  if (signal?.aborted) return Promise.reject(abortError());
  if (input.nodes.length < WORKER_THRESHOLD || typeof Worker === "undefined") {
    return Promise.resolve(runDagre(input));
  }
  return new Promise<Placement[]>((resolve, reject) => {
    const worker = new Worker(new URL("./layoutWorker.ts", import.meta.url), {
      type: "module",
    });
    const done = () => {
      worker.terminate();
      signal?.removeEventListener("abort", onAbort);
    };
    const onAbort = () => {
      done();
      reject(abortError());
    };
    signal?.addEventListener("abort", onAbort);
    worker.onmessage = (e: MessageEvent<Placement[]>) => {
      done();
      resolve(e.data);
    };
    worker.onerror = () => {
      // No worker support after all: do the work here.
      done();
      try {
        resolve(runDagre(input));
      } catch (err) {
        reject(err instanceof Error ? err : new Error(String(err)));
      }
    };
    worker.postMessage(input);
  });
}

function abortError(): Error {
  const e = new Error("Layout cancelled");
  e.name = "AbortError";
  return e;
}
