import { describe, expect, it } from "vitest";
import { computeLayout, layoutInput, runDagre } from "./autoLayout";
import { CATALOGUE, GRAPH } from "./fixtures";

describe("auto-layout", () => {
  it("ranks left to right along the edges", () => {
    const input = layoutInput(GRAPH.nodes, GRAPH.edges, CATALOGUE, {});
    const at = Object.fromEntries(runDagre(input).map((p) => [p.node, p]));
    expect(at["inboundRoute:1"]!.x).toBeLessThan(at["ringGroup:1"]!.x);
    expect(at["extension:1"]!.x).toBeLessThan(at["ringGroup:1"]!.x);
    expect(at["ivr:5"]!.x).toBeLessThan(at["extension:2"]!.x);
    expect(Math.min(...Object.values(at).map((p) => p.x))).toBe(0);
  });

  it("places only nodes without a position, to the right of the rest", async () => {
    const existing = {
      "inboundRoute:1": { x: 0, y: 40, collapsed: false },
      "ringGroup:1": { x: 400, y: 100, collapsed: false },
    };
    const input = layoutInput(GRAPH.nodes, GRAPH.edges, CATALOGUE, existing);
    expect(input.nodes.map((n) => n.id).sort()).toEqual([
      "extension:1",
      "extension:2",
      "ivr:5",
    ]);
    const placed = await computeLayout(input);
    expect(placed.map((p) => p.node).sort()).toEqual([
      "extension:1",
      "extension:2",
      "ivr:5",
    ]);
    for (const p of placed) expect(p.x).toBeGreaterThan(400 + 248);
    expect(Math.min(...placed.map((p) => p.y))).toBe(40);
  });

  it("gives a node with more ports more room", () => {
    const input = layoutInput(GRAPH.nodes, GRAPH.edges, CATALOGUE, {});
    const h = (id: string) => input.nodes.find((n) => n.id === id)?.height ?? 0;
    expect(h("ringGroup:1")).toBeGreaterThan(h("inboundRoute:1"));
  });

  it("does nothing for an empty import and honours a cancelled signal", async () => {
    expect(runDagre({ nodes: [], edges: [], originX: 0, originY: 0 })).toEqual(
      [],
    );
    const ctl = new AbortController();
    ctl.abort();
    await expect(
      computeLayout(
        layoutInput(GRAPH.nodes, GRAPH.edges, CATALOGUE, {}),
        ctl.signal,
      ),
    ).rejects.toMatchObject({ name: "AbortError" });
  });

  it("ignores edges to nodes that are not being placed and self loops", () => {
    const out = runDagre({
      nodes: [{ id: "a", width: 100, height: 40 }],
      edges: [
        { source: "a", target: "a" },
        { source: "a", target: "zzz" },
      ],
      originX: 10,
      originY: 20,
    });
    expect(out).toEqual([{ node: "a", x: 10, y: 20 }]);
  });
});
