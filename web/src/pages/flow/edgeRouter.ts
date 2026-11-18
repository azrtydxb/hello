// Orthogonal edge routing for the canvas (docs/flow-canvas.md, "Connections").
// A connection leaves its source port to the right and enters its target port
// from the left, and runs around the boxes of the nodes instead of through
// them. The route is an A* search over a sparse grid built from the padded
// node boxes near the connection (an orthogonal visibility grid): every box
// edge is a grid line, so a grid segment is either wholly inside a box or
// wholly outside it, and the search never crosses one. Pure and free of DOM:
// the edge component calls it, and the tests call it directly.

export interface Point {
  x: number;
  y: number;
}

/** A box: its top-left corner and size, in canvas units. */
export interface Rect {
  x: number;
  y: number;
  w: number;
  h: number;
}

/** A node's box the routes keep clear of. */
export interface Obstacle extends Rect {
  id: string;
}

/** One connection to route: its two ports and the nodes they belong to. */
export interface RouteRequest {
  /** The source port: the route leaves it rightwards. */
  source: Point;
  /** The target port: the route enters it from the left. */
  target: Point;
  /** The node the source port belongs to (its box is passed in obstacles). */
  sourceNode?: string;
  targetNode?: string;
  /** Spreads the first and last vertical runs of edges of one node. */
  sourceLane?: number;
  targetLane?: number;
}

/** The clearance kept around every box. */
export const PADDING = 16;
/** How far the lanes of one node's ports are spread. */
const LANE_STEP = 6;
const LANES = 4;
/** A turn costs as much as this many pixels of length. */
const BEND_COST = 40;
/** Search boxes tried, by margin around the two ends; the last is everything. */
const MARGINS = [80, 320, Infinity];

const R = 0;
const L = 1;
const D = 2;
const U = 3;
const DX = [1, -1, 0, 0];
const DY = [0, 0, 1, -1];
const OPPOSITE = [L, R, U, D];

interface Box {
  l: number;
  t: number;
  r: number;
  b: number;
}

const inflate = (o: Rect, p: number): Box => ({
  l: o.x - p,
  t: o.y - p,
  r: o.x + o.w + p,
  b: o.y + o.h + p,
});

const strictlyInside = (x: number, y: number, b: Box): boolean =>
  x > b.l && x < b.r && y > b.t && y < b.b;

const overlaps = (a: Box, b: Box): boolean =>
  a.l < b.r && b.l < a.r && a.t < b.b && b.t < a.b;

/** The index of `v` in the sorted `arr` (it is always there). */
function indexOf(arr: number[], v: number): number {
  let lo = 0;
  let hi = arr.length - 1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (arr[mid]! < v) lo = mid + 1;
    else if (arr[mid]! > v) hi = mid - 1;
    else return mid;
  }
  return -1;
}

const uniqueSorted = (vs: number[]): number[] =>
  [...new Set(vs)].sort((a, b) => a - b);

/** A minimal binary heap of values by priority. */
class Heap {
  private p: number[] = [];
  private v: number[] = [];
  get size() {
    return this.p.length;
  }
  private swap(i: number, j: number) {
    const { p, v } = this;
    const pi = p[i]!;
    const vi = v[i]!;
    p[i] = p[j]!;
    v[i] = v[j]!;
    p[j] = pi;
    v[j] = vi;
  }
  push(priority: number, value: number) {
    const { p, v } = this;
    p.push(priority);
    v.push(value);
    let i = p.length - 1;
    while (i > 0) {
      const parent = (i - 1) >> 1;
      if (p[parent]! <= p[i]!) break;
      this.swap(parent, i);
      i = parent;
    }
  }
  pop(): number {
    const { p, v } = this;
    const top = v[0]!;
    const lastP = p.pop()!;
    const lastV = v.pop()!;
    if (p.length > 0) {
      p[0] = lastP;
      v[0] = lastV;
      let i = 0;
      for (;;) {
        const a = 2 * i + 1;
        const b = a + 1;
        let m = i;
        if (a < p.length && p[a]! < p[m]!) m = a;
        if (b < p.length && p[b]! < p[m]!) m = b;
        if (m === i) break;
        this.swap(m, i);
        i = m;
      }
    }
    return top;
  }
}

/**
 * The cheapest orthogonal path from `s0` (entered moving right) to `t0`
 * (left moving right), around `boxes`, inside `area`. Null when there is none.
 */
function search(s0: Point, t0: Point, boxes: Box[], area: Box): Point[] | null {
  const xs = uniqueSorted([
    s0.x,
    t0.x,
    area.l,
    area.r,
    ...boxes.flatMap((b) => [b.l, b.r]),
  ]).filter((x) => x >= area.l && x <= area.r);
  const ys = uniqueSorted([
    s0.y,
    t0.y,
    area.t,
    area.b,
    ...boxes.flatMap((b) => [b.t, b.b]),
  ]).filter((y) => y >= area.t && y <= area.b);
  const nx = xs.length;
  const ny = ys.length;
  const at = (ix: number, iy: number) => iy * nx + ix;
  // Blocked grid points and blocked segments to the next point right/down.
  const point = new Uint8Array(nx * ny);
  const right = new Uint8Array(nx * ny);
  const down = new Uint8Array(nx * ny);
  for (const b of boxes) {
    // Grid lines strictly inside the box, and the cells between box edges.
    let x0 = 0;
    while (x0 < nx && xs[x0]! <= b.l) x0++;
    let x1 = nx - 1;
    while (x1 >= 0 && xs[x1]! >= b.r) x1--;
    let y0 = 0;
    while (y0 < ny && ys[y0]! <= b.t) y0++;
    let y1 = ny - 1;
    while (y1 >= 0 && ys[y1]! >= b.b) y1--;
    for (let iy = y0; iy <= y1; iy++) {
      for (let ix = x0; ix <= x1; ix++) point[at(ix, iy)] = 1;
      // Horizontal segments inside: from the line at or left of b.l.
      for (let ix = Math.max(0, x0 - 1); ix <= x1 && ix < nx - 1; ix++) {
        right[at(ix, iy)] = 1;
      }
    }
    for (let ix = x0; ix <= x1; ix++) {
      for (let iy = Math.max(0, y0 - 1); iy <= y1 && iy < ny - 1; iy++) {
        down[at(ix, iy)] = 1;
      }
    }
  }
  const sx = indexOf(xs, s0.x);
  const sy = indexOf(ys, s0.y);
  const tx = indexOf(xs, t0.x);
  const ty = indexOf(ys, t0.y);
  if (sx < 0 || sy < 0 || tx < 0 || ty < 0) return null;
  if (point[at(sx, sy)] || point[at(tx, ty)]) return null;

  const states = nx * ny * 4;
  const cost = new Float64Array(states).fill(Infinity);
  const prev = new Int32Array(states).fill(-1);
  const heap = new Heap();
  const h = (ix: number, iy: number) =>
    Math.abs(xs[ix]! - t0.x) + Math.abs(ys[iy]! - t0.y);
  const start = at(sx, sy) * 4 + R;
  cost[start] = 0;
  heap.push(h(sx, sy), start);
  const goal = at(tx, ty);
  let found = -1;
  let best = Infinity;
  while (heap.size > 0) {
    const state = heap.pop();
    const c = cost[state]!;
    const cell = state >> 2;
    const dir = state & 3;
    const ix = cell % nx;
    const iy = (cell - ix) / nx;
    if (c + h(ix, iy) >= best) break;
    if (cell === goal) {
      // The last run goes right into the port: a turn there costs too.
      const total = c + (dir === R ? 0 : BEND_COST);
      if (dir !== L && total < best) {
        best = total;
        found = state;
      }
      continue;
    }
    for (let d = 0; d < 4; d++) {
      if (d === OPPOSITE[dir]!) continue;
      const jx = ix + DX[d]!;
      const jy = iy + DY[d]!;
      if (jx < 0 || jy < 0 || jx >= nx || jy >= ny) continue;
      const j = at(jx, jy);
      if (point[j]) continue;
      const blocked =
        d === R
          ? right[cell]
          : d === L
            ? right[j]
            : d === D
              ? down[cell]
              : down[j];
      if (blocked) continue;
      const step =
        Math.abs(xs[jx]! - xs[ix]!) +
        Math.abs(ys[jy]! - ys[iy]!) +
        (d === dir ? 0 : BEND_COST);
      const next = j * 4 + d;
      if (c + step < cost[next]!) {
        cost[next] = c + step;
        prev[next] = state;
        heap.push(c + step + h(jx, jy), next);
      }
    }
  }
  if (found < 0) return null;
  const path: Point[] = [];
  for (let s = found; s >= 0; s = prev[s]!) {
    const cell = s >> 2;
    const ix = cell % nx;
    path.push({ x: xs[ix]!, y: ys[(cell - ix) / nx]! });
  }
  return path.reverse();
}

/** Drops repeated points and the middle of straight runs. */
export function simplify(points: Point[]): Point[] {
  const out: Point[] = [];
  for (const p of points) {
    const last = out[out.length - 1];
    if (last && last.x === p.x && last.y === p.y) continue;
    const before = out[out.length - 2];
    if (
      before &&
      last &&
      ((before.x === last.x && last.x === p.x) ||
        (before.y === last.y && last.y === p.y))
    ) {
      out[out.length - 1] = p;
      continue;
    }
    out.push(p);
  }
  return out;
}

/** A route that ignores the boxes: used only when no clear one exists. */
function fallback(s: Point, s0: Point, t0: Point, t: Point, boxes: Box[]) {
  if (t0.x >= s0.x) {
    const mx = (s0.x + t0.x) / 2;
    return [s, { x: mx, y: s.y }, { x: mx, y: t.y }, t];
  }
  // Back to the left: under everything nearby, then into the port.
  const bottom = Math.max(
    s.y,
    t.y,
    ...boxes.filter((b) => b.r > t0.x && b.l < s0.x).map((b) => b.b),
  );
  const y = bottom + PADDING;
  return [s, s0, { x: s0.x, y }, { x: t0.x, y }, t0, t];
}

/**
 * The route of one connection: orthogonal points from the source port to the
 * target port, leaving rightwards and arriving rightwards (from the left),
 * clear of every box in `obstacles` by `PADDING` where the layout leaves room.
 */
export function routeEdge(
  req: RouteRequest,
  obstacles: readonly Obstacle[],
): Point[] {
  const { source: s, target: t } = req;
  const srcBox = obstacles.find((o) => o.id === req.sourceNode);
  const tgtBox = obstacles.find((o) => o.id === req.targetNode);
  // Tighter clearances are tried when nodes stand too close for the full one.
  for (const pad of [PADDING, PADDING / 2, 2]) {
    const lane = (n: number | undefined) =>
      pad === PADDING ? ((n ?? 0) % LANES) * LANE_STEP : 0;
    const s0 = {
      x:
        Math.max(s.x, srcBox ? srcBox.x + srcBox.w : s.x) +
        pad +
        2 +
        lane(req.sourceLane),
      y: s.y,
    };
    const t0 = {
      x:
        Math.min(t.x, tgtBox ? tgtBox.x : t.x) - pad - 2 - lane(req.targetLane),
      y: t.y,
    };
    const all = obstacles.map((o) => inflate(o, pad));
    const core: Box = {
      l: Math.min(s0.x, t0.x),
      t: Math.min(s0.y, t0.y),
      r: Math.max(s0.x, t0.x),
      b: Math.max(s0.y, t0.y),
    };
    for (const b of [srcBox, tgtBox]) {
      if (!b) continue;
      const i = inflate(b, pad);
      core.l = Math.min(core.l, i.l);
      core.t = Math.min(core.t, i.t);
      core.r = Math.max(core.r, i.r);
      core.b = Math.max(core.b, i.b);
    }
    for (const margin of MARGINS) {
      let area: Box;
      let boxes: Box[];
      if (margin === Infinity) {
        boxes = all;
        area = { ...core };
        for (const b of all) {
          area.l = Math.min(area.l, b.l);
          area.t = Math.min(area.t, b.t);
          area.r = Math.max(area.r, b.r);
          area.b = Math.max(area.b, b.b);
        }
        area = {
          l: area.l - pad,
          t: area.t - pad,
          r: area.r + pad,
          b: area.b + pad,
        };
      } else {
        area = {
          l: core.l - margin,
          t: core.t - margin,
          r: core.r + margin,
          b: core.b + margin,
        };
        boxes = all.filter((b) => overlaps(b, area));
      }
      if (
        boxes.some(
          (b) => strictlyInside(s0.x, s0.y, b) || strictlyInside(t0.x, t0.y, b),
        )
      ) {
        break; // an end is buried: try a tighter clearance
      }
      const path = search(s0, t0, boxes, area);
      if (path) return simplify([s, ...path, t]);
    }
  }
  const boxes = obstacles.map((o) => inflate(o, 0));
  const s0 = { x: s.x + PADDING, y: s.y };
  const t0 = { x: t.x - PADDING, y: t.y };
  return simplify(fallback(s, s0, t0, t, boxes));
}

/**
 * The SVG path of a route, its corners rounded by up to `radius`.
 */
export function pathOf(points: readonly Point[], radius = 8): string {
  const [first, ...rest] = points;
  if (!first) return "";
  let d = `M ${first.x} ${first.y}`;
  let prev = first;
  rest.forEach((p, i) => {
    const next = rest[i + 1];
    if (!next) {
      d += ` L ${p.x} ${p.y}`;
      return;
    }
    const inLen = Math.abs(p.x - prev.x) + Math.abs(p.y - prev.y);
    const outLen = Math.abs(next.x - p.x) + Math.abs(next.y - p.y);
    const r = Math.min(radius, inLen / 2, outLen / 2);
    const ux = Math.sign(p.x - prev.x);
    const uy = Math.sign(p.y - prev.y);
    const vx = Math.sign(next.x - p.x);
    const vy = Math.sign(next.y - p.y);
    d += ` L ${p.x - ux * r} ${p.y - uy * r}`;
    d += ` Q ${p.x} ${p.y} ${p.x + vx * r} ${p.y + vy * r}`;
    prev = p;
  });
  return d;
}

/** The runs of a route as pairs of points. */
function runs(points: readonly Point[]): [Point, Point][] {
  const out: [Point, Point][] = [];
  for (let i = 1; i < points.length; i++) {
    out.push([points[i - 1]!, points[i]!]);
  }
  return out;
}

const runLength = ([a, b]: [Point, Point]) =>
  Math.abs(b.x - a.x) + Math.abs(b.y - a.y);

/** The point halfway along a route, where its label goes. */
export function midpointOf(points: readonly Point[]): Point {
  const all = runs(points);
  let left = all.reduce((sum, r) => sum + runLength(r), 0) / 2;
  for (const r of all) {
    const len = runLength(r);
    if (left <= len && len > 0) {
      const [a, b] = r;
      const f = left / len;
      return { x: a.x + (b.x - a.x) * f, y: a.y + (b.y - a.y) * f };
    }
    left -= len;
  }
  return points[points.length - 1] ?? { x: 0, y: 0 };
}

/**
 * Routes of many connections, memoised: a route is reused while its ends
 * and the boxes inside the area it was searched in stay the same, so moving
 * one node only reroutes the connections near it.
 */
export class RouteCache {
  private entries = new Map<
    string,
    { ends: string; area: Box; boxes: string; points: Point[] }
  >();

  route(
    id: string,
    req: RouteRequest,
    obstacles: readonly Obstacle[],
  ): Point[] {
    const ends = [
      req.source.x,
      req.source.y,
      req.target.x,
      req.target.y,
      req.sourceNode,
      req.targetNode,
      req.sourceLane,
      req.targetLane,
    ].join(",");
    const hit = this.entries.get(id);
    if (
      hit &&
      hit.ends === ends &&
      hit.boxes === signature(hit.area, obstacles)
    )
      return hit.points;
    const points = routeEdge(req, obstacles);
    const area = boundsOf(points, PADDING * 2 + 320);
    this.entries.set(id, {
      ends,
      area,
      boxes: signature(area, obstacles),
      points,
    });
    return points;
  }

  /** Forgets routes of connections no longer drawn. */
  retain(ids: ReadonlySet<string>) {
    for (const id of this.entries.keys()) {
      if (!ids.has(id)) this.entries.delete(id);
    }
  }
}

function boundsOf(points: Point[], margin: number): Box {
  let l = Infinity;
  let t = Infinity;
  let r = -Infinity;
  let b = -Infinity;
  for (const p of points) {
    l = Math.min(l, p.x);
    t = Math.min(t, p.y);
    r = Math.max(r, p.x);
    b = Math.max(b, p.y);
  }
  return { l: l - margin, t: t - margin, r: r + margin, b: b + margin };
}

/** The boxes that touch `area`, as text: a route is stale when it changes. */
function signature(area: Box, obstacles: readonly Obstacle[]): string {
  let s = "";
  for (const o of obstacles) {
    if (overlaps(inflate(o, PADDING), area)) {
      s += `${o.id}:${o.x},${o.y},${o.w},${o.h};`;
    }
  }
  return s;
}
