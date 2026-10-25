// Phone onboarding API client: the queue of phones that asked for a
// configuration before they were added (docs/provisioning.md, "Onboarding").

import { list, request, responseJson, type Id } from "../api";
import type { Phone } from "./prov";

const seg = (value: Id) => encodeURIComponent(String(value));

/** An unadopted phone that asked the provisioning endpoint for a configuration. */
export interface Candidate {
  /** 12 lowercase hex digits. */
  mac: string;
  vendor: string;
  /** As the phone reported it; empty when it did not. */
  model: string;
  firmware: string;
  sourceIp: string;
  requestedPath: string;
  firstSeen: string;
  lastSeen: string;
  requestCount: number;
}

/** A MAC the queue never records until it is taken off the list. */
export interface IgnoredMac {
  mac: string;
  vendor: string;
  model: string;
  createdBy: string;
  createdAt: string;
}

/**
 * The extension a phone is adopted onto: give one of `extensionId`,
 * `extensionNumber` or `userId` (the user's linked extension).
 */
export interface AdoptTarget {
  extensionId?: Id;
  extensionNumber?: string;
  userId?: Id;
}

export interface AdoptInput extends AdoptTarget {
  label?: string;
  /** Overrides the reported vendor / model. */
  vendor?: string;
  model?: string;
}

/** One row of a bulk adoption. */
export interface BulkRowInput extends AdoptInput {
  mac: string;
}

/** The report of a bulk adoption; on a 400 it is the error's `rows`. */
export interface BulkResult {
  rows: { index: number; mac: string; extension?: string; errors: string[] }[];
  ok: boolean;
  adopted: number;
}

export const listCandidates = (signal?: AbortSignal) =>
  list<Candidate>("/api/v1/onboarding/candidates", signal);

/** Creates the phone for a queued candidate; the candidate leaves the queue. */
export const adoptCandidate = (mac: string, input: AdoptInput) =>
  request<Phone>("POST", `/api/v1/onboarding/candidates/${seg(mac)}/adopt`, {
    body: input,
  });

/** Removes a candidate; with `ignore` its MAC is never queued again. */
export const dismissCandidate = (mac: string, ignore: boolean) =>
  request<void>("POST", `/api/v1/onboarding/candidates/${seg(mac)}/dismiss`, {
    body: { ignore },
  });

/**
 * Adopts several candidates in one request: all or none. Rows without an
 * extension take consecutive numbers from `startNumber`, in order. When a row
 * cannot be adopted the server answers 400 with the per-row report; that
 * report (`ok` false, nothing adopted) is returned, not thrown.
 */
export async function bulkAdopt(
  items: BulkRowInput[],
  startNumber?: string,
): Promise<BulkResult> {
  const path = "/api/v1/onboarding/adopt";
  const res = await fetch(path, {
    method: "POST",
    headers: { Accept: "application/json", "Content-Type": "application/json" },
    body: JSON.stringify({
      items,
      ...(startNumber ? { startNumber } : {}),
    }),
    credentials: "same-origin",
  });
  if (res.status === 400) {
    const report = (await res
      .clone()
      .json()
      .catch(() => null)) as BulkResult | null;
    if (report && Array.isArray(report.rows)) return report;
  }
  return responseJson<BulkResult>(res, "POST", path);
}

export const listIgnored = (signal?: AbortSignal) =>
  list<IgnoredMac>("/api/v1/onboarding/ignored", signal);

export const unignoreMac = (mac: string) =>
  request<void>("DELETE", `/api/v1/onboarding/ignored/${seg(mac)}`);
