// The messaging inbox's reports and privacy tools (docs/inbox.md, "Reports"
// and "Privacy tools"): the report and its targets, the export of one
// customer, redaction, the provider-side deletions and the supervisor-view
// audit report. None of it carries customer text except the export.
import { request } from "../api";
import type { InboxMessage } from "./inbox";

export type ReportGroup = "" | "channel" | "queue" | "agent" | "day";

/** A set of durations in seconds. */
export interface Durations {
  count: number;
  avgSeconds: number;
  p50Seconds: number;
  p90Seconds: number;
}

/** One group of the report, or the totals. */
export interface ReportRow {
  key: string;
  label: string;
  conversations: number;
  inboundConversations: number;
  outboundConversations: number;
  resolved: number;
  messagesIn: number;
  messagesOut: number;
  firstResponse: Durations;
  resolution: Durations;
  slaFirstResponseBreaches: number;
  slaResolutionBreaches: number;
}

/** The service-level targets in seconds; 0 is off. */
export interface InboxSLA {
  firstResponseSeconds: number;
  resolutionSeconds: number;
}

export interface InboxReport {
  from: string;
  to: string;
  groupBy: ReportGroup;
  timeZone: string;
  sla: InboxSLA;
  totals: ReportRow;
  rows: ReportRow[];
}

export interface ReportQuery {
  from: string;
  to: string;
  groupBy: ReportGroup;
}

function reportQuery(q: ReportQuery): string {
  const p = new URLSearchParams({ from: q.from, to: q.to });
  if (q.groupBy) p.set("groupBy", q.groupBy);
  return p.toString();
}

export const getInboxReport = (q: ReportQuery, signal?: AbortSignal) =>
  request<InboxReport>("GET", `/api/v1/inbox/reports?${reportQuery(q)}`, {
    signal,
  });

/** The URL of the report as CSV (a download). */
export const inboxReportCsvUrl = (q: ReportQuery) =>
  `/api/v1/inbox/reports/export?${reportQuery(q)}`;

export const getInboxSLA = (signal?: AbortSignal) =>
  request<InboxSLA>("GET", "/api/v1/messaging/sla", { signal });

export const putInboxSLA = (t: InboxSLA) =>
  request<InboxSLA>("PUT", "/api/v1/messaging/sla", { body: t });

/** The URL of the export of one customer (a zip download). */
export function peerExportUrl(peer: string, notes: boolean): string {
  const p = new URLSearchParams({ peer });
  if (notes) p.set("notes", "true");
  return `/api/v1/inbox/export?${p.toString()}`;
}

/** What a provider-side deletion did, per provider. */
export interface ProviderRedaction {
  provider: string;
  redacted: boolean;
  messages: number;
  done: number;
  pending: number;
  failed: number;
  detail: string;
}

export interface RedactReport {
  message: InboxMessage;
  providers: ProviderRedaction[];
}

export const redactMessage = (conversationId: number, messageId: number) =>
  request<RedactReport>(
    "POST",
    `/api/v1/inbox/conversations/${String(conversationId)}/messages/${String(messageId)}/redact`,
  );

export interface RedactionCount {
  provider: string;
  pending: number;
  done: number;
  failed: number;
  unsupported: number;
}

export const listRedactions = async (signal?: AbortSignal) =>
  (
    await request<{ items: RedactionCount[] }>(
      "GET",
      "/api/v1/messaging/redactions",
      { signal },
    )
  ).items;

export interface SupervisorView {
  id: number;
  at: string;
  actor: string;
  userId: number | null;
  userName: string;
  conversationId: number;
  via: string;
}

export interface SupervisorViewTotal {
  actor: string;
  userName: string;
  views: number;
  conversations: number;
}

export interface SupervisorViewReport {
  items: SupervisorView[];
  nextBefore: number;
  totals: SupervisorViewTotal[];
}

export interface SupervisorViewQuery {
  from: string;
  to: string;
  actor?: string;
  before?: number;
}

export function listSupervisorViews(
  q: SupervisorViewQuery,
  signal?: AbortSignal,
) {
  const p = new URLSearchParams({ from: q.from, to: q.to });
  if (q.actor) p.set("actor", q.actor);
  if (q.before) p.set("before", String(q.before));
  return request<SupervisorViewReport>(
    "GET",
    `/api/v1/inbox/audit/supervisor-views?${p.toString()}`,
    { signal },
  );
}
