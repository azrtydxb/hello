// Trace mode (spec flow-canvas S-18): which nodes and edges a traced call
// passes, and the canvas view with them marked. Pure functions, no React.

import type { Edge } from "@xyflow/react";
import type { FlowGraph } from "../../api/flow";
import type { FlowTrace, FlowTraceStep } from "../../api/flowTrace";
import type { CanvasView } from "./model";

export interface Highlight {
  /** Node id to the order of its first step (1 is where the call starts). */
  nodes: Map<string, number>;
  /** Ids of the graph edges the call takes. */
  edges: Set<string>;
}

/** The steps of a path with the branches expanded, depth first. */
function walk(
  steps: readonly FlowTraceStep[],
  from: FlowTraceStep | undefined,
  visit: (prev: FlowTraceStep | undefined, step: FlowTraceStep) => void,
) {
  let prev = from;
  for (const s of steps) {
    visit(prev, s);
    for (const b of s.branches ?? []) {
      // A branch leaves the step through the branch's port.
      walk(b.steps, { ...s, port: b.port }, visit);
    }
    prev = s;
  }
}

export function highlightOf(trace: FlowTrace, graph: FlowGraph): Highlight {
  const nodes = new Map<string, number>();
  const edges = new Set<string>();
  let n = 0;
  walk(trace.steps, undefined, (prev, step) => {
    n++;
    if (!nodes.has(step.node)) nodes.set(step.node, n);
    if (!prev || prev.node === step.node) return;
    for (const e of graph.edges) {
      if (
        e.source.node === prev.node &&
        e.target.node === step.node &&
        (prev.port === undefined || e.source.port === prev.port)
      ) {
        edges.add(e.id);
      }
    }
  });
  return { nodes, edges };
}

/** Marks the traced nodes and edges; everything else is dimmed by CSS. */
export function applyHighlight(
  view: CanvasView,
  h: Highlight | null,
): CanvasView {
  if (!h) return view;
  return {
    ...view,
    nodes: view.nodes.map((n) =>
      h.nodes.has(n.id)
        ? { ...n, className: [n.className, "flow-node--traced"].join(" ") }
        : n,
    ),
    edges: view.edges.map((e): Edge =>
      h.edges.has(e.id)
        ? {
            ...e,
            className: `${e.className ?? ""} flow-edge--traced`,
            animated: true,
            zIndex: 10,
          }
        : e,
    ),
  };
}

/** How many traced nodes the canvas does not show. */
export function missingFromCanvas(
  h: Highlight,
  onCanvas: (id: string) => boolean,
): number {
  let n = 0;
  for (const id of h.nodes.keys()) if (!onCanvas(id)) n++;
  return n;
}

/** RFC 3339 for a `datetime-local` value (no zone: the browser's). */
export function toRFC3339(local: string): string | undefined {
  if (!local) return undefined;
  const d = new Date(local);
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString();
}

/** A `datetime-local` value for a Date, in local time. */
export function toLocalInput(d: Date): string {
  const pad = (v: number) => String(v).padStart(2, "0");
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}
