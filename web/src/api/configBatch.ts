// Configuration batch client (spec flow-canvas S-13, S-14): many changes in
// one transaction, a dry run that shows the diff, staleness checks, and
// reverting an applied batch. See docs/config-batch.md.

import { ApiError, request, type Id } from "../api";

/** One operation of a batch, as the API documents it. */
export interface BatchOp {
  ref?: string;
  operationId: string;
  pathParams?: Record<string, string>;
  body?: unknown;
  /** The node version the change was prepared against. */
  expect?: string;
}

export interface BatchRequest {
  baseRevision: number;
  dryRun?: boolean;
  canvasId?: Id;
  ops: BatchOp[];
  layout?: { node: string; x: number; y: number }[];
}

export interface BatchDiff {
  path: string;
  before?: unknown;
  after?: unknown;
}

export interface BatchResult {
  ref?: string;
  operationId: string;
  status: "created" | "updated" | "deleted" | "unchanged";
  id?: string;
  node?: string;
  before: unknown;
  after: unknown;
  diff: BatchDiff[];
}

export interface BatchWarning {
  node: string;
  code: string;
  message: string;
}

export interface BatchResponse {
  applied: boolean;
  batchId?: string;
  baseRevision: number;
  revision: number;
  results: BatchResult[];
  warnings: BatchWarning[];
  skipped?: string[];
}

export interface BatchSummary {
  id: string;
  actor: string;
  via: string;
  baseRevision: number;
  revision: number;
  opCount: number;
  reverts?: string;
  revertedBy?: string;
  createdAt: string;
}

/** One problem of a rejected batch (422 validation_failed). */
export interface BatchProblem {
  opIndex: number;
  node?: string;
  path?: string;
  message: string;
}

/** One conflict of a stale batch (409 config_stale). */
export interface BatchConflict {
  opIndex: number;
  node?: string;
}

export const applyConfigBatch = (body: BatchRequest, signal?: AbortSignal) =>
  request<BatchResponse>("POST", "/api/v1/config/batch", { body, signal });

export const listConfigBatches = async (signal?: AbortSignal) =>
  (
    await request<{ items: BatchSummary[] }>(
      "GET",
      "/api/v1/config/batches?limit=30",
      { signal },
    )
  ).items;

export const revertConfigBatch = (
  id: string,
  dryRun: boolean,
  signal?: AbortSignal,
) =>
  request<BatchResponse>(
    "POST",
    `/api/v1/config/batches/${encodeURIComponent(id)}/revert`,
    { body: { dryRun }, signal },
  );

/** The per-operation problems of a 422, empty for any other error. */
export function problemsOf(err: unknown): BatchProblem[] {
  if (!(err instanceof ApiError)) return [];
  const list = err.details.errors;
  return Array.isArray(list) ? (list as BatchProblem[]) : [];
}

/** The conflicts of a 409 config_stale, empty for any other error. */
export function conflictsOf(err: unknown): BatchConflict[] {
  if (!(err instanceof ApiError)) return [];
  const list = err.details.conflicts;
  return Array.isArray(list) ? (list as BatchConflict[]) : [];
}

/** A stale batch: the configuration changed under the draft. */
export const isStale = (err: unknown): boolean =>
  err instanceof ApiError && err.status === 409 && err.code === "config_stale";
