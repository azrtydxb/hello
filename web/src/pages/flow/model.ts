// What the canvas draws, derived from the catalogue, the graph and the saved
// positions (spec flow-canvas S-1, S-2, S-8). Nothing here knows a node kind:
// ports, colours and shapes come from the catalogue, so a new kind renders
// without a UI change.

import type { Edge, Node } from "@xyflow/react";
import type {
  FlowCatalogue,
  FlowEdge,
  FlowGraph,
  FlowNode,
  FlowNodeType,
  FlowPort,
  FlowWarning,
} from "../../api/flow";
import type { Pos } from "./layoutState";
import { portIsEditable } from "./objects";

export const NODE_WIDTH = 248;
const HEADER_HEIGHT = 58;
const PORT_ROW = 26;
const BODY_PAD = 10;
export const STUB_WIDTH = 190;
export const STUB_HEIGHT = 40;
const STUB_GAP = 90;
/** More stubs than this are not drawn: the sidebar lists the nodes. */
export const MAX_STUBS = 150;

/** The shape names the catalogue may use; anything else draws as a circle. */
export const KNOWN_SHAPES = new Set([
  "circle",
  "circle-notch",
  "square",
  "diamond",
  "triangle",
  "hexagon",
  "small-square",
]);

export interface PortView {
  id: string;
  label: string;
  type: string;
  direction: "in" | "out";
  /** The port can be wired on the canvas (editing, and the connection is stored). */
  editable?: boolean;
}

/** The data of a drawn node. */
export interface FlowNodeData extends Record<string, unknown> {
  node: FlowNode;
  typeLabel: string;
  inputs: PortView[];
  outputs: PortView[];
  warnings: FlowWarning[];
  /** One line of the node's summary fields. */
  summary: string;
  pseudo: boolean;
  /** What the draft does to the node. */
  draft?: "new" | "changed";
  /** Problems the last check of the draft found on this node. */
  errors: string[];
}

/** The data of a stub: a node that is not on this canvas. */
export interface StubData extends Record<string, unknown> {
  target: string;
  label: string;
  typeLabel: string;
  /** "out": the on-canvas node points at it; "in": it points at the node. */
  dir: "in" | "out";
  warnings: number;
}

export type FlowRFNode = Node<FlowNodeData, "flow"> | Node<StubData, "stub">;

export function nodeHeight(inputs: number, outputs: number): number {
  return HEADER_HEIGHT + Math.max(inputs, outputs, 1) * PORT_ROW + BODY_PAD;
}

export function nodeTypeOf(
  catalogue: FlowCatalogue | undefined,
  kind: string,
): FlowNodeType | undefined {
  return catalogue?.nodes.find((n) => n.kind === kind);
}

export function kindLabel(
  catalogue: FlowCatalogue | undefined,
  kind: string,
): string {
  return nodeTypeOf(catalogue, kind)?.label ?? kind;
}

function toView(p: FlowPort): PortView {
  return { id: p.id, label: p.label, type: p.type, direction: p.direction };
}

/**
 * The ports a node shows: the catalogue's, plus any port an edge uses that
 * the catalogue does not list (an IVR key is one port per configured key),
 * typed like the first catalogue port of that direction.
 */
export function portsFor(
  type: FlowNodeType | undefined,
  nodeId: string,
  edges: readonly FlowEdge[],
): { inputs: PortView[]; outputs: PortView[] } {
  const inputs = (type?.ports ?? [])
    .filter((p) => p.direction === "in")
    .map(toView);
  const outputs = (type?.ports ?? [])
    .filter((p) => p.direction === "out")
    .map(toView);
  const fallback = (list: PortView[]): string => list[0]?.type ?? "call";
  for (const e of edges) {
    if (
      e.source.node === nodeId &&
      !outputs.some((p) => p.id === e.source.port)
    ) {
      outputs.push({
        id: e.source.port,
        label: e.source.port,
        type: fallback(outputs),
        direction: "out",
      });
    }
    if (
      e.target.node === nodeId &&
      !inputs.some((p) => p.id === e.target.port)
    ) {
      inputs.push({
        id: e.target.port,
        label: e.target.port,
        type: fallback(inputs),
        direction: "in",
      });
    }
  }
  return { inputs, outputs };
}

/** "noAnswerEnd" -> "No answer end". */
export function humanize(key: string): string {
  const words = key.replace(/([a-z0-9])([A-Z])/g, "$1 $2").toLowerCase();
  return words.charAt(0).toUpperCase() + words.slice(1);
}

/** A summary value as text; objects and null are not shown. */
export function formatValue(v: unknown): string | null {
  if (typeof v === "boolean") return v ? "Yes" : "No";
  if (typeof v === "number") return String(v);
  if (typeof v === "string") return v === "" ? null : v;
  return null;
}

/** The summary fields as "key value" pairs, in a stable order. */
export function summaryEntries(
  summary: FlowNode["summary"],
): { key: string; label: string; value: string }[] {
  const out: { key: string; label: string; value: string }[] = [];
  for (const key of Object.keys(summary ?? {}).sort()) {
    const value = formatValue(summary?.[key]);
    if (value !== null) out.push({ key, label: humanize(key), value });
  }
  return out;
}

function summaryLine(node: FlowNode): string {
  return summaryEntries(node.summary)
    .slice(0, 3)
    .map((e) => `${e.label.toLowerCase()} ${e.value}`)
    .join(" · ");
}

/** Where the existing form page of an object is, when it has one. */
export function formPageFor(node: FlowNode): string | null {
  const id = node.id.slice(node.id.indexOf(":") + 1);
  switch (node.kind) {
    case "trunk":
      return "/trunks";
    case "inboundRoute":
    case "outboundRoute":
      return "/routes";
    case "extension":
      return "/extensions";
    case "device":
      return "/devices";
    case "phone":
      return /^\d+$/.test(id) ? `/phones/${id}` : "/phones";
    case "ringGroup":
      return "/ring-groups";
    case "ivr":
      return "/ivr";
    case "announcement":
      return "/announcements";
    case "pagingGroup":
      return "/paging";
    case "conference":
      return "/conferences";
    case "timeCondition":
    case "schedule":
    case "holidayCalendar":
      return "/time-conditions";
    case "mohClass":
      return "/moh";
    case "queue":
      return "/queues";
    case "parkLot":
    case "pickupGroup":
      return "/park";
    default:
      return null;
  }
}

export interface CanvasView {
  nodes: FlowRFNode[];
  edges: Edge[];
  /** Graph nodes that have no position on this canvas. */
  offCanvas: FlowNode[];
  /** Stubs left out because of MAX_STUBS. */
  hiddenStubs: number;
}

export interface ViewInput {
  graph: FlowGraph;
  catalogue: FlowCatalogue | undefined;
  positions: Record<string, Pos>;
  selected?: string | null;
  /** The user may edit: ports that can be wired are marked. */
  edit?: boolean;
  marks?: Record<string, "new" | "changed" | "deleted">;
  errors?: Record<string, string[]>;
}

/** The label of an edge: its try order or position, when it has one. */
export function edgeLabel(e: FlowEdge): string | undefined {
  const v = e.data?.order ?? e.data?.position;
  return typeof v === "number" ? `#${v}` : undefined;
}

function draftMark(
  m: "new" | "changed" | "deleted" | undefined,
): "new" | "changed" | undefined {
  return m === "new" || m === "changed" ? m : undefined;
}

export function buildView({
  graph,
  catalogue,
  positions,
  selected,
  edit,
  marks,
  errors,
}: ViewInput): CanvasView {
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const onCanvas = (id: string) => byId.has(id) && positions[id] !== undefined;
  const warnings = new Map<string, FlowWarning[]>();
  for (const w of graph.warnings) {
    warnings.set(w.node, [...(warnings.get(w.node) ?? []), w]);
  }

  const nodes: FlowRFNode[] = [];
  const typeOfPort = new Map<string, string>(); // "node|out|port" -> port type
  const sizes = new Map<string, { w: number; h: number }>();
  for (const n of graph.nodes) {
    const pos = positions[n.id];
    if (!pos) continue;
    const type = nodeTypeOf(catalogue, n.kind);
    const own = graph.edges.filter(
      (e) => e.source.node === n.id || e.target.node === n.id,
    );
    const { inputs, outputs } = portsFor(type, n.id, own);
    if (edit) {
      for (const p of inputs) p.editable = true;
      for (const p of outputs) p.editable = portIsEditable(n.kind, p.id);
    }
    for (const p of outputs) typeOfPort.set(`${n.id}|${p.id}`, p.type);
    const h = nodeHeight(inputs.length, outputs.length);
    sizes.set(n.id, { w: NODE_WIDTH, h });
    nodes.push({
      id: n.id,
      type: "flow",
      position: { x: pos.x, y: pos.y },
      initialWidth: NODE_WIDTH,
      initialHeight: h,
      selected: selected === n.id,
      ariaLabel: `${type?.label ?? n.kind} ${n.label}${
        warnings.get(n.id)?.length
          ? `, ${warnings.get(n.id)?.length} warnings`
          : ""
      }`,
      data: {
        node: n,
        typeLabel: type?.label ?? n.kind,
        inputs,
        outputs,
        warnings: warnings.get(n.id) ?? [],
        summary: summaryLine(n),
        pseudo: Boolean(type?.pseudo),
        draft: draftMark(marks?.[n.id]),
        errors: errors?.[n.id] ?? [],
      },
    });
  }

  const edges: Edge[] = [];
  const stubSlots = new Map<string, number>();
  let stubs = 0;
  let hidden = 0;
  const addStub = (e: FlowEdge, dir: "in" | "out"): string | null => {
    const anchorId = dir === "out" ? e.source.node : e.target.node;
    const farId = dir === "out" ? e.target.node : e.source.node;
    const anchor = positions[anchorId];
    const far = byId.get(farId);
    if (!anchor || !far) return null;
    if (stubs >= MAX_STUBS) {
      hidden++;
      return null;
    }
    stubs++;
    const slotKey = `${anchorId}|${dir}`;
    const slot = stubSlots.get(slotKey) ?? 0;
    stubSlots.set(slotKey, slot + 1);
    const size = sizes.get(anchorId);
    const id = `stub:${dir}:${e.id}`;
    nodes.push({
      id,
      type: "stub",
      position: {
        x:
          dir === "out"
            ? anchor.x + (size?.w ?? NODE_WIDTH) + STUB_GAP
            : anchor.x - STUB_WIDTH - STUB_GAP,
        y: anchor.y + slot * (STUB_HEIGHT + 12),
      },
      initialWidth: STUB_WIDTH,
      initialHeight: STUB_HEIGHT,
      draggable: false,
      ariaLabel: `Not on this canvas: ${kindLabel(catalogue, far.kind)} ${far.label}`,
      data: {
        target: far.id,
        label: far.label,
        typeLabel: kindLabel(catalogue, far.kind),
        dir,
        warnings: warnings.get(far.id)?.length ?? 0,
      },
    });
    return id;
  };

  for (const e of graph.edges) {
    const src = onCanvas(e.source.node);
    const dst = onCanvas(e.target.node);
    if (!src && !dst) continue;
    const type = typeOfPort.get(`${e.source.node}|${e.source.port}`) ?? "call";
    const base = {
      label: edgeLabel(e),
      data: { implicit: Boolean(e.implicit), type },
      className: e.implicit ? "flow-edge flow-edge--implicit" : "flow-edge",
      style: { stroke: `var(--flow-port-${type}, var(--az-muted))` },
    };
    if (src && dst) {
      edges.push({
        id: e.id,
        source: e.source.node,
        sourceHandle: `out:${e.source.port}`,
        target: e.target.node,
        targetHandle: `in:${e.target.port}`,
        ...base,
      });
    } else if (src) {
      const stub = addStub(e, "out");
      if (!stub) continue;
      edges.push({
        id: e.id,
        source: e.source.node,
        sourceHandle: `out:${e.source.port}`,
        target: stub,
        targetHandle: "stub",
        ...base,
        className: "flow-edge flow-edge--stub",
        style: {},
      });
    } else {
      const stub = addStub(e, "in");
      if (!stub) continue;
      edges.push({
        id: e.id,
        source: stub,
        sourceHandle: "stub",
        target: e.target.node,
        targetHandle: `in:${e.target.port}`,
        ...base,
        className: "flow-edge flow-edge--stub",
        style: {},
      });
    }
  }

  return {
    nodes,
    edges,
    offCanvas: graph.nodes.filter((n) => !positions[n.id]),
    hiddenStubs: hidden,
  };
}

/** Nodes grouped by kind label, for the "Not on this canvas" list. */
export function groupByKind(
  nodes: readonly FlowNode[],
  catalogue: FlowCatalogue | undefined,
  query: string,
): { kind: string; label: string; nodes: FlowNode[] }[] {
  const q = query.trim().toLowerCase();
  const groups = new Map<string, FlowNode[]>();
  for (const n of nodes) {
    const label = kindLabel(catalogue, n.kind);
    if (q && !`${n.label} ${label}`.toLowerCase().includes(q)) continue;
    groups.set(n.kind, [...(groups.get(n.kind) ?? []), n]);
  }
  const order = new Map(
    (catalogue?.nodes ?? []).map((t, i) => [t.kind, i] as const),
  );
  return [...groups.entries()]
    .sort(
      ([a], [b]) =>
        (order.get(a) ?? 999) - (order.get(b) ?? 999) || a.localeCompare(b),
    )
    .map(([kind, ns]) => ({
      kind,
      label: kindLabel(catalogue, kind),
      nodes: ns.sort((a, b) =>
        a.label.localeCompare(b.label, undefined, { numeric: true }),
      ),
    }));
}

/**
 * The nearest node in a direction from `from`, for arrow-key movement
 * between nodes. Positions are the nodes' top-left corners.
 */
export function neighbour(
  nodes: readonly { id: string; x: number; y: number }[],
  from: string,
  dir: "left" | "right" | "up" | "down",
): string | null {
  const cur = nodes.find((n) => n.id === from);
  if (!cur) return nodes[0]?.id ?? null;
  let best: { id: string; score: number } | null = null;
  for (const n of nodes) {
    if (n.id === from) continue;
    const dx = n.x - cur.x;
    const dy = n.y - cur.y;
    const along =
      dir === "left" ? -dx : dir === "right" ? dx : dir === "up" ? -dy : dy;
    const across =
      dir === "left" || dir === "right" ? Math.abs(dy) : Math.abs(dx);
    if (along <= 0) continue;
    const score = along + across * 2;
    if (!best || score < best.score) best = { id: n.id, score };
  }
  return best?.id ?? null;
}
