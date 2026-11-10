// Call trace client (spec flow-canvas S-18): where a call from X to Y at a
// given time would go through the call flow. Kept apart from api/flow.ts,
// which holds the graph and the canvases.

import { request } from "../api";

/** One stop on a call's path. `node` is a graph node id. */
export interface FlowTraceStep {
  node: string;
  /** The port the call leaves through (or, on a last step, ends at). */
  port?: string;
  text: string;
  /** On a time condition step: the instant that was evaluated. */
  at?: string;
  /** The choices of an IVR menu traced without a key. */
  branches?: FlowTraceBranch[];
}

export interface FlowTraceBranch {
  port: string;
  label: string;
  steps: FlowTraceStep[];
}

export interface RoutingTraceLine {
  n: number;
  text: string;
}

/** GET /api/v1/flow/trace */
export interface FlowTrace {
  configRevision: number;
  at: string;
  from: string;
  number: string;
  /** internal, inbound, outbound or reject. */
  outcome: string;
  route?: string;
  reason?: string;
  steps: FlowTraceStep[];
  routingTrace: RoutingTraceLine[];
}

export interface FlowTraceQuery {
  /** An extension number or `trunk:<id>`. */
  from: string;
  /** The dialled number, or the DID of a trunk call. */
  number: string;
  callerId?: string;
  /** RFC 3339. */
  at?: string;
  /** Keys pressed in IVR menus, in order. */
  digits?: string;
}

export const traceFlow = (q: FlowTraceQuery, signal?: AbortSignal) => {
  const p = new URLSearchParams({ from: q.from, number: q.number });
  if (q.callerId) p.set("callerId", q.callerId);
  if (q.at) p.set("at", q.at);
  if (q.digits) p.set("digits", q.digits);
  return request<FlowTrace>("GET", `/api/v1/flow/trace?${p.toString()}`, {
    signal,
  });
};
