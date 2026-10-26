// Call blocklist client (pbx-gaps S-7): GET|POST /api/v1/blocklist,
// PUT|DELETE /api/v1/blocklist/{id} and POST /api/v1/blocklist/import.

import { request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

export type BlockMatch = "exact" | "prefix";
export type BlockAction = "block" | "allow";

/** A blocklist entry as the API returns it. */
export interface BlocklistEntry {
  id: Id;
  /** The stored pattern: a normalised number, with a trailing * for a prefix. */
  pattern: string;
  match: BlockMatch;
  action: BlockAction;
  /** The trunk the entry is limited to; null is every trunk. */
  trunkId: Id | null;
  trunkName: string;
  /** The administrators' note; empty for any other role. */
  reason: string;
  announcementId: Id | null;
  enabled: boolean;
  createdAt: string;
  updatedAt: string;
}

/** What is written for an entry. */
export interface BlocklistInput {
  pattern: string;
  match: BlockMatch;
  action: BlockAction;
  trunkId: Id | null;
  /** Absent keeps the current note on a replace. */
  reason?: string;
  announcementId: Id | null;
  enabled: boolean;
}

export interface BlocklistPage {
  items: BlocklistEntry[];
  /** Pass as `after` for the next page; empty at the end. */
  next: string;
  total: number;
}

export interface BlocklistQuery {
  q?: string;
  action?: BlockAction | "";
  /** "global" or a trunk id. */
  scope?: string;
  after?: string;
  limit?: number;
}

export function listBlocklist(
  query: BlocklistQuery,
  signal?: AbortSignal,
): Promise<BlocklistPage> {
  const q = new URLSearchParams();
  if (query.q) q.set("q", query.q);
  if (query.action) q.set("action", query.action);
  if (query.scope) q.set("scope", query.scope);
  if (query.after) q.set("after", query.after);
  if (query.limit) q.set("limit", String(query.limit));
  const qs = q.toString();
  return request<BlocklistPage>(
    "GET",
    `/api/v1/blocklist${qs ? `?${qs}` : ""}`,
    { signal },
  );
}

export const createBlocklistEntry = (body: BlocklistInput) =>
  request<BlocklistEntry>("POST", "/api/v1/blocklist", { body });

export const putBlocklistEntry = (id: Id, body: BlocklistInput) =>
  request<BlocklistEntry>("PUT", `/api/v1/blocklist/${seg(id)}`, { body });

export const deleteBlocklistEntry = (id: Id) =>
  request<void>("DELETE", `/api/v1/blocklist/${seg(id)}`);

export interface BlocklistRowError {
  row: number;
  column: string;
  reason: string;
}

/** The answer of an import, a dry run or not. */
export interface BlocklistImportResult {
  ok: boolean;
  dryRun: boolean;
  rows: number;
  created: number;
  updated: number;
  errors: BlocklistRowError[];
  warnings: { row: number; reason: string }[];
}

/**
 * POST /api/v1/blocklist/import: the CSV is the multipart part "file". A dry
 * run writes nothing; an import with row errors answers 400 and writes
 * nothing, so a dry run is the way to read the row errors.
 */
export function importBlocklist(
  csv: string,
  opts: { dryRun: boolean },
): Promise<BlocklistImportResult> {
  const form = new FormData();
  form.set("file", new Blob([csv], { type: "text/csv" }), "blocklist.csv");
  const q = new URLSearchParams({ dry_run: String(opts.dryRun) });
  return request<BlocklistImportResult>(
    "POST",
    `/api/v1/blocklist/import?${q.toString()}`,
    { rawBody: form },
  );
}
