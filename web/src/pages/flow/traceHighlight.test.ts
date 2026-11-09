import { describe, expect, it } from "vitest";
import type { FlowTrace } from "../../api/flowTrace";
import { GRAPH } from "./fixtures";
import { applyHighlight, highlightOf, toRFC3339 } from "./traceHighlight";
import type { CanvasView } from "./model";

const trace = (steps: FlowTrace["steps"]): FlowTrace => ({
  configRevision: 12,
  at: "2026-10-26T10:00:00Z",
  from: "trunk:1",
  number: "5551",
  outcome: "inbound",
  steps,
  routingTrace: [],
});

describe("highlightOf", () => {
  it("marks the nodes of the path in order and the edges between them", () => {
    const h = highlightOf(
      trace([
        { node: "inboundRoute:1", port: "destination", text: "matches" },
        { node: "ringGroup:1", port: "noAnswer", text: "rings" },
        { node: "extension:1", port: "voicemail", text: "voicemail" },
      ]),
      GRAPH,
    );
    expect([...h.nodes]).toEqual([
      ["inboundRoute:1", 1],
      ["ringGroup:1", 2],
      ["extension:1", 3],
    ]);
    // Only the edge that leaves through the step's port is marked.
    expect([...h.edges]).toEqual([
      "inboundRoute:1.destination>ringGroup:1.ring",
    ]);
  });

  it("follows the branches of a menu through the port of each branch", () => {
    const h = highlightOf(
      trace([
        {
          node: "ivr:5",
          text: "menu",
          branches: [
            {
              port: "1",
              label: "Key 1",
              steps: [{ node: "extension:2", port: "ring", text: "rings" }],
            },
            {
              port: "9",
              label: "Key 9",
              steps: [{ node: "extension:1", text: "rings" }],
            },
          ],
        },
      ]),
      GRAPH,
    );
    expect(h.nodes.has("extension:2")).toBe(true);
    expect([...h.edges]).toEqual(["ivr:5.1>extension:2.ring"]);
  });

  it("does not mark an edge to a step of the same node", () => {
    const h = highlightOf(
      trace([
        { node: "extension:1", port: "ring", text: "rings" },
        { node: "extension:1", port: "voicemail", text: "voicemail" },
      ]),
      GRAPH,
    );
    expect(h.edges.size).toBe(0);
  });
});

describe("applyHighlight", () => {
  const view = {
    nodes: [
      { id: "ringGroup:1", position: { x: 0, y: 0 }, data: {}, className: "a" },
      { id: "extension:1", position: { x: 0, y: 0 }, data: {} },
    ],
    edges: [
      { id: "e1", source: "a", target: "b", className: "flow-edge" },
      { id: "e2", source: "a", target: "c", className: "flow-edge" },
    ],
    offCanvas: [],
    hiddenStubs: 0,
  } as unknown as CanvasView;

  it("changes nothing without a trace", () => {
    expect(applyHighlight(view, null)).toBe(view);
  });

  it("marks only the traced nodes and edges", () => {
    const out = applyHighlight(view, {
      nodes: new Map([["ringGroup:1", 1]]),
      edges: new Set(["e2"]),
    });
    expect(out.nodes[0]?.className).toContain("flow-node--traced");
    expect(out.nodes[1]?.className).toBeUndefined();
    expect(out.edges[0]?.className).toBe("flow-edge");
    expect(out.edges[1]?.className).toContain("flow-edge--traced");
  });
});

describe("toRFC3339", () => {
  it("is empty for no time and for nonsense", () => {
    expect(toRFC3339("")).toBeUndefined();
    expect(toRFC3339("noon")).toBeUndefined();
    expect(toRFC3339("2026-10-25T20:00")).toMatch(
      /^2026-10-2[56]T\d\d:00:00\.000Z$/,
    );
  });
});
