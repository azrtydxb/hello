import { describe, expect, it } from "vitest";
import type { FlowGraphDiff } from "../../../api/configBatch";
import type { FlowEdge, FlowGraph, FlowNode } from "../../../api/flow";
import type { CanvasView } from "../model";
import {
  applyOverlay,
  ghostPositions,
  GHOST_DX,
  overlayCounts,
  overlayGraph,
  overlayMarks,
  placementsAfterApply,
} from "./overlay";

const node = (id: string, label = id): FlowNode => ({
  id,
  kind: id.split(":")[0] ?? id,
  label,
  summary: {},
});
const edge = (id: string, from: string, to: string): FlowEdge => ({
  id,
  source: { node: from, port: "out" },
  target: { node: to, port: "in" },
  owner: { node: from, field: "destination" },
});

const graph: FlowGraph = {
  configRevision: 7,
  nodes: [node("inboundRoute:1", "Main"), node("ringGroup:2", "Sales")],
  edges: [edge("e1", "inboundRoute:1", "ringGroup:2")],
  warnings: [],
};

// The route now goes to a new IVR whose key 1 reaches a new group; the old
// group is deleted.
const diff: FlowGraphDiff = {
  added: [node("ivr:90", "Menu"), node("ringGroup:91", "Support")],
  changed: [node("inboundRoute:1", "Main line")],
  removed: ["ringGroup:2"],
  edgesAdded: [
    edge("e2", "inboundRoute:1", "ivr:90"),
    edge("e3", "ivr:90", "ringGroup:91"),
  ],
  edgesRemoved: ["e1"],
};

describe("proposal overlay", () => {
  it("draws the graph as the proposal leaves it, keeping what it deletes", () => {
    const g = overlayGraph(graph, diff);
    expect(g.nodes.map((n) => n.id)).toEqual([
      "inboundRoute:1",
      "ringGroup:2",
      "ivr:90",
      "ringGroup:91",
    ]);
    expect(g.nodes[0]?.label).toBe("Main line");
    expect(g.edges.map((e) => e.id)).toEqual(["e1", "e2", "e3"]);
    expect(overlayMarks(diff)).toEqual({
      "inboundRoute:1": "changed",
      "ivr:90": "new",
      "ringGroup:91": "new",
    });
  });

  it("places created nodes next to what they connect to, never moving placed ones", () => {
    const positions = {
      "inboundRoute:1": { x: 0, y: 0, collapsed: false },
      "ringGroup:2": { x: GHOST_DX, y: 0, collapsed: false },
    };
    const p = ghostPositions(positions, overlayGraph(graph, diff), diff);
    expect(p["inboundRoute:1"]).toEqual(positions["inboundRoute:1"]);
    expect(p["ringGroup:2"]).toEqual(positions["ringGroup:2"]);
    // Right of the route, but not on top of the old group.
    expect(p["ivr:90"]?.x).toBe(GHOST_DX);
    expect(p["ivr:90"]?.y).not.toBe(0);
    // Followed along the chain.
    expect(p["ringGroup:91"]?.x).toBe(2 * GHOST_DX);
  });

  it("marks added, changed and removed nodes and edges", () => {
    const view: CanvasView = {
      nodes: ["inboundRoute:1", "ringGroup:2", "ivr:90"].map((id) => ({
        id,
        type: "flow" as const,
        position: { x: 0, y: 0 },
        data: {} as never,
      })),
      edges: ["e1", "e2", "x"].map((id) => ({ id, source: "a", target: "b" })),
      offCanvas: [],
      hiddenStubs: 0,
    };
    const v = applyOverlay(view, diff);
    expect(v.nodes.map((n) => n.className)).toEqual([
      "flow-node--ai-changed",
      "flow-node--ai-removed",
      "flow-node--ai-added",
    ]);
    expect(v.edges.map((e) => e.className)).toEqual([
      "flow-edge--ai-removed",
      "flow-edge--ai-added",
      undefined,
    ]);
    expect(applyOverlay(view, null)).toBe(view);
    expect(overlayCounts(diff)).toEqual({
      added: 2,
      changed: 1,
      removed: 1,
      connections: 3,
    });
  });

  it("matches the created nodes of the applied batch by kind and label", () => {
    const after: FlowGraph = {
      ...graph,
      nodes: [
        node("inboundRoute:1", "Main line"),
        node("ivr:95", "Menu"),
        node("ringGroup:96", "Support"),
      ],
    };
    const positions = {
      "ivr:90": { x: 320, y: 150, collapsed: false },
      "ringGroup:91": { x: 640, y: 150, collapsed: false },
    };
    expect(placementsAfterApply(graph, after, diff.added, positions)).toEqual([
      { node: "ivr:95", x: 320, y: 150 },
      { node: "ringGroup:96", x: 640, y: 150 },
    ]);
  });
});
