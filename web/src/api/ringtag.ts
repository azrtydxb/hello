// Ring tags and tag rules client (migration 00043): the tags a call rings
// with, and the ordered rules that choose one.

import { list, request } from "../api";
import type { DayRange } from "./timecond";

const seg = (value: string) => encodeURIComponent(value);

export interface RingTag {
  name: string;
  builtin: boolean;
  /** The Bellcore-drN (1 to 5) the canonical Alert-Info carries. */
  bellcoreSlot: number;
  description: string;
  /** How many rules and ring groups name the tag. */
  usedBy: number;
}

export interface RingTagPayload {
  name: string;
  bellcoreSlot: number;
  description: string;
}

export const listRingTags = (signal?: AbortSignal) =>
  list<RingTag>("/api/v1/ring-tags", signal);
export const createRingTag = (body: RingTagPayload) =>
  request<RingTag>("POST", "/api/v1/ring-tags", { body });
export const putRingTag = (
  name: string,
  body: Pick<RingTagPayload, "bellcoreSlot" | "description">,
) => request<RingTag>("PUT", `/api/v1/ring-tags/${seg(name)}`, { body });
export const deleteRingTag = (name: string) =>
  request<void>("DELETE", `/api/v1/ring-tags/${seg(name)}`);

export type CallerKind = "extension" | "door" | "trunk";

export interface RingWindow {
  timeZone: string;
  ranges: DayRange[];
}

/** What a rule requires; every field present must match. */
export interface RingMatch {
  callerPattern?: string;
  trunk?: string;
  did?: string;
  callerKind?: CallerKind;
  window?: RingWindow;
}

export interface RingRulePayload {
  name: string;
  match: RingMatch;
  tag: string;
}

export interface RingRule extends RingRulePayload {
  id: number;
  position: number;
}

export const listRingRules = (signal?: AbortSignal) =>
  list<RingRule>("/api/v1/ring-rules", signal);
/** Replaces the whole ordered rule list. */
export const putRingRules = async (rules: RingRulePayload[]) =>
  (
    await request<{ items: RingRule[] }>("PUT", "/api/v1/ring-rules", {
      body: { rules },
    })
  ).items;

export interface RingChoice {
  tag: string;
  why: string;
  bellcoreSlot: number;
  alertInfo: string;
}

export interface RingQuery {
  kind?: string;
  callerNumber?: string;
  did?: string;
  trunk?: string;
  callerKind?: string;
  fromTrunk?: boolean;
  groupTag?: string;
  at?: string;
}

/** GET /api/v1/ring-rules/evaluate: the tag a described call would get. */
export function evaluateRingRules(q: RingQuery, signal?: AbortSignal) {
  const params = new URLSearchParams();
  for (const [k, v] of Object.entries(q)) {
    if (v !== undefined && v !== "" && v !== false) params.set(k, String(v));
  }
  const query = params.toString();
  return request<RingChoice>(
    "GET",
    `/api/v1/ring-rules/evaluate${query ? `?${query}` : ""}`,
    { signal },
  );
}
