/**
 * API client for the calls pages (Dashboard, Active calls, Call history,
 * Call detail): the CDR filters, counts, concurrency series and export
 * that GET /api/v1/cdrs* adds on top of the shared client in ../api.ts.
 */
import {
  request,
  type Cdr,
  type CdrDetail,
  type CdrPage,
  type Id,
} from "../api";

/** Which calls a CDR listing keeps; empty keeps all. */
export interface CdrFilter {
  direction?: Cdr["direction"];
  /** Only calls whose final status is outside 2xx. */
  failed?: boolean;
}

function filterQuery(filter: CdrFilter): URLSearchParams {
  const q = new URLSearchParams();
  if (filter.direction) q.set("direction", filter.direction);
  if (filter.failed) q.set("failed", "true");
  return q;
}

/** GET /api/v1/cdrs with filters, newest first; `before` is the previous page's `next`. */
export async function listCdrPage(
  opts: CdrFilter & { before?: string; limit?: number },
  signal?: AbortSignal,
): Promise<CdrPage> {
  const q = new URLSearchParams();
  if (opts.before) q.set("before", opts.before);
  if (opts.limit !== undefined) q.set("limit", String(opts.limit));
  filterQuery(opts).forEach((v, k) => q.set(k, v));
  const qs = q.toString();
  const path = `/api/v1/cdrs${qs ? `?${qs}` : ""}`;
  const body = await request<unknown>("GET", path, { signal });
  const rec =
    typeof body === "object" && body !== null
      ? (body as Record<string, unknown>)
      : {};
  if (!Array.isArray(rec.items)) {
    throw new Error(`GET ${path} returned an unexpected payload`);
  }
  return {
    items: rec.items as Cdr[],
    next: typeof rec.next === "string" ? rec.next : "",
  };
}

/** GET /api/v1/cdrs/counts */
export interface CdrCounts {
  all: number;
  failed: number;
}

export const getCdrCounts = (signal?: AbortSignal) =>
  request<CdrCounts>("GET", "/api/v1/cdrs/counts", { signal });

/** A concurrency window of GET /api/v1/cdrs/concurrency. */
export type ConcurrencyRange = "1h" | "6h" | "24h";

/** Recorded calls in progress at one instant, by direction. */
export interface ConcurrencyPoint {
  at: string;
  inbound: number;
  outbound: number;
  internal: number;
}

/** GET /api/v1/cdrs/concurrency: samples of recorded calls in progress. */
export interface CdrConcurrency {
  range: ConcurrencyRange;
  stepSeconds: number;
  points: ConcurrencyPoint[];
  /** The busiest sample; null when no call was in progress. */
  peak: { calls: number; at: string } | null;
}

export const getCdrConcurrency = (
  range: ConcurrencyRange,
  signal?: AbortSignal,
) =>
  request<CdrConcurrency>("GET", `/api/v1/cdrs/concurrency?range=${range}`, {
    signal,
  });

/** The URL of GET /api/v1/cdrs/export for a filter (a CSV download). */
export function cdrExportUrl(filter: CdrFilter): string {
  const qs = filterQuery(filter).toString();
  return `/api/v1/cdrs/export${qs ? `?${qs}` : ""}`;
}

/** GET /api/v1/cdrs/{id}, with the failover note. */
export interface CallRecord extends CdrDetail {
  /** For an answered call that failed over: what happened. Empty otherwise. */
  note?: string;
}

export const getCallRecord = (cdrId: Id, signal?: AbortSignal) =>
  request<CallRecord>(
    "GET",
    `/api/v1/cdrs/${encodeURIComponent(String(cdrId))}`,
    { signal },
  );
