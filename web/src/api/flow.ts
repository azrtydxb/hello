// Flow canvas client (spec flow-canvas): the node catalogue, the call-flow
// graph built from the live configuration, and the named canvases that store
// only where each node sits.

import { list, request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

// --- catalogue ------------------------------------------------------------

/** One input an output may connect to. */
export interface FlowTarget {
  kind: string;
  port: string;
  /** Set when the connection cannot be stored yet. */
  requires?: string;
  note?: string;
}

/** One connector dot on a node type. */
export interface FlowPort {
  id: string;
  direction: "in" | "out";
  /** Port type; decides colour and shape. */
  type: string;
  many: boolean;
  label: string;
  field?: string;
  targets?: FlowTarget[];
}

/** One kind of node. */
export interface FlowNodeType {
  kind: string;
  label: string;
  pseudo?: boolean;
  terminal?: boolean;
  requires?: string;
  ports: FlowPort[];
}

/** A port type's presentation. */
export interface FlowPortStyle {
  type: string;
  label: string;
  colour: string;
  shape: string;
}

/** GET /api/v1/flow/catalogue */
export interface FlowCatalogue {
  portTypes: FlowPortStyle[];
  nodes: FlowNodeType[];
}

// --- graph ----------------------------------------------------------------

export interface FlowNode {
  id: string;
  kind: string;
  label: string;
  summary?: Record<string, unknown> | null;
  version?: string;
}

export interface FlowRef {
  node: string;
  port: string;
}

export interface FlowEdge {
  id: string;
  source: FlowRef;
  target: FlowRef;
  owner: { node: string; field: string };
  implicit?: boolean;
  data?: Record<string, unknown>;
}

export interface FlowWarning {
  node: string;
  code: string;
  message: string;
}

/** GET /api/v1/flow/graph */
export interface FlowGraph {
  configRevision: number;
  nodes: FlowNode[];
  edges: FlowEdge[];
  warnings: FlowWarning[];
}

export const getFlowCatalogue = (signal?: AbortSignal) =>
  request<FlowCatalogue>("GET", "/api/v1/flow/catalogue", { signal });

/** The whole configuration, or `closure:<nodeId>`: what a node reaches. */
export const getFlowGraph = (scope?: string, signal?: AbortSignal) =>
  request<FlowGraph>(
    "GET",
    `/api/v1/flow/graph${scope ? `?scope=${encodeURIComponent(scope)}` : ""}`,
    { signal },
  );

export const getFlowRevision = async (signal?: AbortSignal) =>
  (
    await request<{ configRevision: number }>("GET", "/api/v1/flow/revision", {
      signal,
    })
  ).configRevision;

// --- canvases -------------------------------------------------------------

export interface FlowViewport {
  x: number;
  y: number;
  zoom: number;
}

export interface FlowCanvas {
  id: Id;
  name: string;
  description: string;
  viewport?: FlowViewport;
  /** Send back with the next layout save; it moves on every save. */
  layoutVersion: number;
  nodeCount: number;
  createdBy: string;
  createdAt: string;
  updatedAt: string;
}

/** Where one node sits on a canvas. */
export interface FlowCanvasNode {
  node: string;
  x: number;
  y: number;
  collapsed: boolean;
}

export interface FlowCanvasDetail extends FlowCanvas {
  nodes: FlowCanvasNode[];
}

export interface FlowCanvasInput {
  name?: string;
  description?: string;
  viewport?: FlowViewport;
}

export interface FlowLayoutInput {
  layoutVersion: number;
  nodes?: { node: string; x: number; y: number; collapsed?: boolean }[];
  remove?: string[];
}

export const listFlowCanvases = (signal?: AbortSignal) =>
  list<FlowCanvas>("/api/v1/flow/canvases", signal);
export const getFlowCanvas = (canvasId: Id, signal?: AbortSignal) =>
  request<FlowCanvasDetail>("GET", `/api/v1/flow/canvases/${seg(canvasId)}`, {
    signal,
  });
export const createFlowCanvas = (body: FlowCanvasInput) =>
  request<FlowCanvasDetail>("POST", "/api/v1/flow/canvases", { body });
export const updateFlowCanvas = (canvasId: Id, body: FlowCanvasInput) =>
  request<FlowCanvas>("PATCH", `/api/v1/flow/canvases/${seg(canvasId)}`, {
    body,
  });
export const deleteFlowCanvas = (canvasId: Id) =>
  request<void>("DELETE", `/api/v1/flow/canvases/${seg(canvasId)}`);
export const putFlowLayout = (canvasId: Id, body: FlowLayoutInput) =>
  request<FlowCanvasDetail>(
    "PUT",
    `/api/v1/flow/canvases/${seg(canvasId)}/layout`,
    { body },
  );
