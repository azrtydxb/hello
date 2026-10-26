import { describe, expect, it } from "vitest";
import { CATALOGUE, GRAPH } from "./fixtures";
import {
  buildView,
  edgeLabel,
  formPageFor,
  groupByKind,
  humanize,
  MAX_STUBS,
  neighbour,
  portsFor,
  summaryEntries,
} from "./model";

const positions = {
  "inboundRoute:1": { x: 0, y: 0, collapsed: false },
  "ringGroup:1": { x: 400, y: 0, collapsed: false },
  "extension:1": { x: 0, y: 200, collapsed: false },
};

describe("buildView", () => {
  const view = buildView({ graph: GRAPH, catalogue: CATALOGUE, positions });

  it("draws the nodes that have a position and lists the rest", () => {
    expect(
      view.nodes
        .filter((n) => n.type === "flow")
        .map((n) => n.id)
        .sort(),
    ).toEqual(["extension:1", "inboundRoute:1", "ringGroup:1"]);
    expect(view.offCanvas.map((n) => n.id).sort()).toEqual([
      "extension:2",
      "ivr:5",
    ]);
  });

  it("joins two on-canvas nodes with an edge on the catalogue's ports and type", () => {
    const e = view.edges.find(
      (x) => x.id === "inboundRoute:1.destination>ringGroup:1.ring",
    );
    expect(e).toMatchObject({
      source: "inboundRoute:1",
      sourceHandle: "out:destination",
      target: "ringGroup:1",
      targetHandle: "in:ring",
    });
    expect(e?.style?.stroke).toBe("var(--flow-port-call, var(--az-muted))");
    const m = view.edges.find(
      (x) => x.id === "extension:1.member>ringGroup:1.members",
    );
    expect(m?.style?.stroke).toContain("--flow-port-member");
    expect(m?.label).toBe("#1");
  });

  it("draws an edge to a node that is not on the canvas as a stub", () => {
    const stub = view.nodes.find(
      (n) => n.type === "stub" && n.data.target === "extension:2",
    );
    expect(stub).toBeDefined();
    expect(stub?.data).toMatchObject({
      dir: "in",
      label: "101 Bob",
      typeLabel: "Extension",
    });
    const e = view.edges.find(
      (x) => x.id === "extension:2.member>ringGroup:1.members",
    );
    expect(e).toMatchObject({
      source: stub?.id,
      sourceHandle: "stub",
      targetHandle: "in:members",
    });
    expect(e?.className).toContain("flow-edge--stub");
    // An edge between two off-canvas nodes is not drawn at all.
    expect(view.edges.some((x) => x.id === "ivr:5.1>extension:2.ring")).toBe(
      false,
    );
  });

  it("puts a stub to the left of what it feeds and to the right of what it is reached from", () => {
    const stub = view.nodes.find((n) => n.type === "stub");
    expect(stub?.position.x).toBeLessThan(400);
    const out = buildView({
      graph: GRAPH,
      catalogue: CATALOGUE,
      positions: { "ivr:5": { x: 100, y: 0, collapsed: false } },
    });
    const right = out.nodes.find((n) => n.type === "stub");
    expect(right?.data).toMatchObject({ dir: "out", target: "extension:2" });
    expect(right?.position.x).toBeGreaterThan(100 + 248);
  });

  it("adds a port for an edge the catalogue does not list", () => {
    const all = buildView({
      graph: GRAPH,
      catalogue: CATALOGUE,
      positions: {
        ...positions,
        "ivr:5": { x: 0, y: 500, collapsed: false },
        "extension:2": { x: 400, y: 500, collapsed: false },
      },
    });
    const ivr = all.nodes.find((n) => n.id === "ivr:5");
    expect(ivr?.type === "flow" && ivr.data.outputs.map((p) => p.id)).toEqual([
      "timeout",
      "1",
    ]);
    const e = all.edges.find((x) => x.id === "ivr:5.1>extension:2.ring");
    expect(e?.sourceHandle).toBe("out:1");
  });

  it("carries warnings on the node and the selection flag", () => {
    const v = buildView({
      graph: GRAPH,
      catalogue: CATALOGUE,
      positions,
      selected: "ringGroup:1",
    });
    const rg = v.nodes.find((n) => n.id === "ringGroup:1");
    expect(rg?.selected).toBe(true);
    expect(rg?.type === "flow" && rg.data.warnings).toHaveLength(1);
    expect(rg?.ariaLabel).toContain("1 warnings");
  });

  it("renders a kind the catalogue does not know", () => {
    const g = {
      ...GRAPH,
      nodes: [
        ...GRAPH.nodes,
        { id: "queue:1", kind: "queue", label: "Support" },
      ],
      edges: [],
    };
    const v = buildView({
      graph: g,
      catalogue: CATALOGUE,
      positions: { "queue:1": { x: 0, y: 0, collapsed: false } },
    });
    const q = v.nodes[0];
    expect(q?.type === "flow" && q.data.typeLabel).toBe("queue");
  });

  it("caps the stubs and says how many are left out", () => {
    const many = Array.from({ length: MAX_STUBS + 5 }, (_, i) => ({
      id: `extension:${i + 100}`,
      kind: "extension",
      label: `e${i}`,
    }));
    const g = {
      ...GRAPH,
      nodes: [GRAPH.nodes[1]!, ...many],
      edges: many.map((n) => ({
        id: `${n.id}.member>ringGroup:1.members`,
        source: { node: n.id, port: "member" },
        target: { node: "ringGroup:1", port: "members" },
        owner: { node: "ringGroup:1", field: "members" },
      })),
    };
    const v = buildView({
      graph: g,
      catalogue: CATALOGUE,
      positions: { "ringGroup:1": { x: 0, y: 0, collapsed: false } },
    });
    expect(v.nodes.filter((n) => n.type === "stub")).toHaveLength(MAX_STUBS);
    expect(v.hiddenStubs).toBe(5);
  });
});

describe("helpers", () => {
  it("portsFor falls back to the first port's type for an unlisted port", () => {
    const ports = portsFor(CATALOGUE.nodes[3], "ivr:5", GRAPH.edges);
    expect(ports.outputs.map((p) => [p.id, p.type])).toEqual([
      ["timeout", "call"],
      ["1", "call"],
    ]);
  });

  it("humanizes keys and formats summaries", () => {
    expect(humanize("noAnswerEnd")).toBe("No answer end");
    expect(
      summaryEntries({ b: true, a: "x", c: 3, d: "", e: null, f: { x: 1 } }),
    ).toEqual([
      { key: "a", label: "A", value: "x" },
      { key: "b", label: "B", value: "Yes" },
      { key: "c", label: "C", value: "3" },
    ]);
  });

  it("labels an edge by its try order or position", () => {
    const base = GRAPH.edges[0]!;
    expect(edgeLabel({ ...base, data: { order: 2 } })).toBe("#2");
    expect(edgeLabel({ ...base, data: { position: 4 } })).toBe("#4");
    expect(edgeLabel(base)).toBeUndefined();
  });

  it("links an object to the page that edits it", () => {
    const n = (id: string, kind: string) => ({ id, kind, label: "x" });
    expect(formPageFor(n("ringGroup:1", "ringGroup"))).toBe("/ring-groups");
    expect(formPageFor(n("phone:7", "phone"))).toBe("/phones/7");
    expect(formPageFor(n("external:+32", "external"))).toBeNull();
  });

  it("groups by kind in catalogue order and filters by name or type", () => {
    const g = groupByKind(GRAPH.nodes, CATALOGUE, "");
    expect(g.map((x) => x.label)).toEqual([
      "Inbound route",
      "Ring group",
      "Extension",
      "IVR menu",
    ]);
    expect(
      groupByKind(GRAPH.nodes, CATALOGUE, "bob").map((x) =>
        x.nodes.map((n) => n.id),
      ),
    ).toEqual([["extension:2"]]);
    expect(groupByKind(GRAPH.nodes, CATALOGUE, "ring group")).toHaveLength(1);
  });

  it("finds the nearest node in a direction", () => {
    const nodes = [
      { id: "a", x: 0, y: 0 },
      { id: "b", x: 300, y: 10 },
      { id: "c", x: 300, y: 300 },
      { id: "d", x: 0, y: 200 },
    ];
    expect(neighbour(nodes, "a", "right")).toBe("b");
    expect(neighbour(nodes, "a", "down")).toBe("d");
    expect(neighbour(nodes, "a", "left")).toBeNull();
    expect(neighbour(nodes, "d", "up")).toBe("a");
    expect(neighbour(nodes, "zzz", "up")).toBe("a");
  });
});
