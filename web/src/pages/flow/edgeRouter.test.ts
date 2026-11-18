import { describe, expect, it } from "vitest";
import {
  midpointOf,
  pathOf,
  RouteCache,
  routeEdge,
  simplify,
  type Obstacle,
  type Point,
  type RouteRequest,
} from "./edgeRouter";

const W = 248;
const H = 120;
const HANDLE = 7;

const node = (id: string, x: number, y: number, h = H): Obstacle => ({
  id,
  x,
  y,
  w: W,
  h,
});

/** The port points React Flow reports: just outside the node's sides. */
const out = (n: Obstacle, row = 0): Point => ({
  x: n.x + n.w + HANDLE,
  y: n.y + 71 + row * 26,
});
const inp = (n: Obstacle, row = 0): Point => ({
  x: n.x - HANDLE,
  y: n.y + 71 + row * 26,
});

const req = (
  a: Obstacle,
  b: Obstacle,
  rows: [number, number] = [0, 0],
): RouteRequest => ({
  source: out(a, rows[0]),
  target: inp(b, rows[1]),
  sourceNode: a.id,
  targetNode: b.id,
});

/** Whether a segment runs through the inside of a box (touching is fine). */
function crosses(a: Point, b: Point, o: Obstacle): boolean {
  const l = Math.min(a.x, b.x);
  const r = Math.max(a.x, b.x);
  const t = Math.min(a.y, b.y);
  const bt = Math.max(a.y, b.y);
  return l < o.x + o.w && r > o.x && t < o.y + o.h && bt > o.y;
}

function expectGoodRoute(points: Point[], r: RouteRequest, obs: Obstacle[]) {
  expect(points[0]).toEqual(r.source);
  expect(points[points.length - 1]).toEqual(r.target);
  // Every run is horizontal or vertical.
  for (let i = 1; i < points.length; i++) {
    const a = points[i - 1]!;
    const b = points[i]!;
    expect(a.x === b.x || a.y === b.y).toBe(true);
  }
  // Out to the right of the source, in from the left of the target.
  expect(points[1]!.y).toBe(r.source.y);
  expect(points[1]!.x).toBeGreaterThan(r.source.x);
  const last = points[points.length - 1]!;
  const beforeLast = points[points.length - 2]!;
  expect(beforeLast.y).toBe(last.y);
  expect(beforeLast.x).toBeLessThan(last.x);
  // Never through a box.
  for (let i = 1; i < points.length; i++) {
    for (const o of obs) {
      if (crosses(points[i - 1]!, points[i]!, o)) {
        throw new Error(
          `segment ${JSON.stringify(points[i - 1])} -> ${JSON.stringify(points[i])} crosses ${o.id}`,
        );
      }
    }
  }
}

describe("routeEdge", () => {
  it("draws a straight line between two ports level with each other", () => {
    const a = node("a", 0, 0);
    const b = node("b", 500, 0);
    const r = req(a, b);
    const points = routeEdge(r, [a, b]);
    expectGoodRoute(points, r, [a, b]);
    expect(points).toHaveLength(2);
  });

  it("steps down between two nodes at different heights", () => {
    const a = node("a", 0, 0);
    const b = node("b", 500, 300);
    const r = req(a, b);
    const points = routeEdge(r, [a, b]);
    expectGoodRoute(points, r, [a, b]);
    expect(points).toHaveLength(4);
  });

  it("goes around a node standing in the way", () => {
    const a = node("a", 0, 0);
    const mid = node("mid", 400, -40, 200);
    const b = node("b", 800, 0);
    const r = req(a, b);
    const points = routeEdge(r, [a, mid, b]);
    expectGoodRoute(points, r, [a, mid, b]);
    expect(points.length).toBeGreaterThan(2);
  });

  it("loops a back-edge around both boxes, out right and in left", () => {
    const a = node("a", 600, 0);
    const b = node("b", 0, 0);
    const r = req(a, b);
    const points = routeEdge(r, [a, b]);
    expectGoodRoute(points, r, [a, b]);
  });

  it("loops a node to itself", () => {
    const a = node("a", 0, 0);
    const r = req(a, a, [0, 1]);
    const points = routeEdge(r, [a]);
    expectGoodRoute(points, r, [a]);
  });

  it("routes to a stub placed right next to its node", () => {
    const a = node("a", 0, 0);
    const stub: Obstacle = { id: "stub", x: W + 90, y: 0, w: 190, h: 40 };
    const r: RouteRequest = {
      source: out(a),
      target: { x: stub.x - 4, y: 20 },
      sourceNode: "a",
      targetNode: "stub",
    };
    const points = routeEdge(r, [a, stub]);
    expectGoodRoute(points, r, [a, stub]);
  });

  it("never crosses a box on random layouts", () => {
    let seed = 42;
    const rand = () => {
      seed = (seed * 1103515245 + 12345) % 2 ** 31;
      return seed / 2 ** 31;
    };
    for (let round = 0; round < 25; round++) {
      // A loose grid with jitter, so there is always room around each node.
      const nodes: Obstacle[] = [];
      for (let i = 0; i < 12; i++) {
        nodes.push(
          node(
            `n${i}`,
            (i % 4) * 420 + Math.round(rand() * 60),
            Math.floor(i / 4) * 260 + Math.round(rand() * 60),
            H + Math.round(rand() * 60),
          ),
        );
      }
      for (let k = 0; k < 6; k++) {
        const a = nodes[Math.floor(rand() * nodes.length)]!;
        const b = nodes[Math.floor(rand() * nodes.length)]!;
        const r = req(a, b, [0, 1]);
        expectGoodRoute(routeEdge(r, nodes), r, nodes);
      }
    }
  });

  it("routes 300 connections among 200 nodes quickly", () => {
    const nodes: Obstacle[] = [];
    for (let i = 0; i < 200; i++) {
      nodes.push(node(`n${i}`, (i % 20) * 400, Math.floor(i / 20) * 240));
    }
    const cache = new RouteCache();
    const started = performance.now();
    for (let k = 0; k < 300; k++) {
      const a = nodes[(k * 7) % 200]!;
      const b = nodes[(k * 13 + 5) % 200]!;
      cache.route(`e${k}`, req(a, b), nodes);
    }
    const first = performance.now() - started;
    // A second pass with nothing moved is served from the cache.
    const again = performance.now();
    for (let k = 0; k < 300; k++) {
      const a = nodes[(k * 7) % 200]!;
      const b = nodes[(k * 13 + 5) % 200]!;
      cache.route(`e${k}`, req(a, b), nodes);
    }
    expect(performance.now() - again).toBeLessThan(first);
    expect(first).toBeLessThan(5000);
  });
});

describe("RouteCache", () => {
  it("reroutes when a node moves into the way, and only then", () => {
    const a = node("a", 0, 0);
    const b = node("b", 800, 0);
    const far = node("far", 0, 3000);
    const cache = new RouteCache();
    const r = req(a, b);
    const first = cache.route("e", r, [a, b, far]);
    expect(first).toHaveLength(2);
    // Something moves far away: the same route object is returned.
    const farMoved = { ...far, x: 100 };
    expect(cache.route("e", r, [a, b, farMoved])).toBe(first);
    // Something moves into the way: the route changes and stays clear.
    const blocker = node("far", 400, -20, 200);
    const rerouted = cache.route("e", r, [a, b, blocker]);
    expect(rerouted).not.toBe(first);
    expectGoodRoute(rerouted, r, [a, b, blocker]);
  });
});

describe("path helpers", () => {
  it("simplifies straight runs and repeated points", () => {
    expect(
      simplify([
        { x: 0, y: 0 },
        { x: 0, y: 0 },
        { x: 10, y: 0 },
        { x: 20, y: 0 },
        { x: 20, y: 10 },
      ]),
    ).toEqual([
      { x: 0, y: 0 },
      { x: 20, y: 0 },
      { x: 20, y: 10 },
    ]);
  });

  it("rounds corners and finds the middle", () => {
    const pts = [
      { x: 0, y: 0 },
      { x: 100, y: 0 },
      { x: 100, y: 100 },
    ];
    expect(pathOf(pts)).toBe("M 0 0 L 92 0 Q 100 0 100 8 L 100 100");
    expect(midpointOf(pts)).toEqual({ x: 100, y: 0 });
  });
});
