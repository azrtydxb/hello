// The canvas side of AI on the canvas (spec flow-canvas S-19): which
// proposal is drawn, the graph, positions and marks with it, and where the
// nodes it created go once it is applied. Kept here so the canvas page only
// swaps its graph, positions and marks for these.

import { useCallback, useEffect, useMemo, useState } from "react";
import {
  batchActionOf,
  getProposal,
  type AIProposal,
} from "../../../api/aiagent";
import { getFlowGraph, type FlowGraph } from "../../../api/flow";
import type { Pos } from "../layoutState";
import type { CanvasView } from "../model";
import {
  applyOverlay,
  ghostPositions,
  overlayGraph,
  overlayMarks,
  placementsAfterApply,
} from "./overlay";

type Marks = Record<string, "new" | "changed" | "deleted">;

export interface FlowAI {
  open: boolean;
  setOpen: (open: boolean) => void;
  preview: AIProposal | null;
  setPreview: (p: AIProposal | null) => void;
  /** The graph, positions and marks to draw. */
  graph: FlowGraph;
  positions: Record<string, Pos>;
  marks: Marks;
  /** Marks the overlay on a built view. */
  mark: (view: CanvasView) => CanvasView;
  /** After an apply: the new graph and where the created nodes go. */
  applied: (before: FlowGraph) => Promise<{
    graph: FlowGraph;
    nodes: { node: string; x: number; y: number }[];
  }>;
}

export function useFlowAI({
  shown,
  positions,
  marks,
  proposalId,
}: {
  shown: FlowGraph;
  positions: Record<string, Pos>;
  marks: Marks;
  /** `?proposal=<id>`: open the panel with this proposal drawn. */
  proposalId: string | null;
}): FlowAI {
  const [open, setOpen] = useState(false);
  const [preview, setPreview] = useState<AIProposal | null>(null);

  useEffect(() => {
    if (!proposalId) return;
    const c = new AbortController();
    getProposal(proposalId, c.signal).then(
      (p) => {
        setPreview(p);
        setOpen(true);
      },
      () => setOpen(true),
    );
    return () => c.abort();
  }, [proposalId]);

  const diff = batchActionOf(preview)?.preview?.flowDiff ?? null;
  const graph = useMemo(
    () => (diff ? overlayGraph(shown, diff) : shown),
    [shown, diff],
  );
  const drawnPositions = useMemo(
    () => (diff ? ghostPositions(positions, graph, diff) : positions),
    [positions, graph, diff],
  );
  const drawnMarks = useMemo(
    () => (diff ? { ...marks, ...overlayMarks(diff) } : marks),
    [marks, diff],
  );
  const mark = useCallback(
    (view: CanvasView) => {
      const v = applyOverlay(view, diff);
      if (!diff) return v;
      // A ghost is not on the canvas yet: it cannot be dragged.
      const ghosts = new Set(diff.added.map((n) => n.id));
      return {
        ...v,
        nodes: v.nodes.map((n) =>
          ghosts.has(n.id) ? { ...n, draggable: false } : n,
        ),
      };
    },
    [diff],
  );
  const applied = useCallback(
    async (before: FlowGraph) => {
      const after = await getFlowGraph();
      const nodes = diff
        ? placementsAfterApply(before, after, diff.added, drawnPositions)
        : [];
      setPreview(null);
      return { graph: after, nodes };
    },
    [diff, drawnPositions],
  );

  return {
    open,
    setOpen,
    preview,
    setPreview,
    graph,
    positions: drawnPositions,
    marks: drawnMarks,
    mark,
    applied,
  };
}
