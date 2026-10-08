// Client for the in-product AI agent (spec ai-agent): status, findings,
// proposals, assistant sessions and tasks. Shapes follow internal/api/openapi.json.
import { list, request } from "../api";

export interface AIAgentRun {
  startedAt: string;
  finishedAt: string | null;
  outcome: string | null;
  tokens: number;
  error: string | null;
}

export interface AIAgent {
  name: string;
  intervalSeconds: number;
  lastRun: AIAgentRun | null;
  nextDueAt: string | null;
  runRequested: boolean;
}

export interface AIStatus {
  enabled: boolean;
  reason: string | null;
  provider: string;
  model: string;
  endpointHost: string;
  endpointPrivate: boolean;
  structuredOutput: string;
  tokensToday: number;
  dailyTokenBudget: number;
  backgroundTokenLimit: number;
  concurrencyInUse: number;
  maxConcurrency: number;
  agents: AIAgent[];
  recentErrors: { code: string; at: string }[];
  openFindings: { info: number; warning: number; critical: number };
  healthScore: number;
  openProposals: number;
}

export type Severity = "info" | "warning" | "critical";
export const SEVERITIES: readonly Severity[] = ["info", "warning", "critical"];
export type FindingStatus = "open" | "acknowledged" | "dismissed" | "resolved";
export const FINDING_STATUSES: readonly FindingStatus[] = [
  "open",
  "acknowledged",
  "dismissed",
  "resolved",
];

export interface FindingExplanation {
  summary: string;
  likelyCause: string;
  nextStep: string;
  relatedIds?: string[];
}

export interface AIFinding {
  id: string;
  candidateId: string;
  type: string;
  subject: string;
  severity: Severity;
  status: FindingStatus;
  title: string;
  evidence: Record<string, unknown>;
  explanation: FindingExplanation | null;
  explained: boolean;
  rank: number | null;
  firstSeen: string;
  lastSeen: string;
  occurrences: number;
  severityHistory: { severity: Severity; at: string }[];
  acknowledgedBy: number | null;
  acknowledgedAt: string | null;
  dismissedBy: number | null;
  dismissedAt: string | null;
  dismissReason: string | null;
  resolvedAt: string | null;
  proposalId: string | null;
}

export type ProposalStatus =
  "open" | "applied" | "failed" | "stale" | "dismissed" | "superseded";
export const PROPOSAL_STATUSES: readonly ProposalStatus[] = [
  "open",
  "applied",
  "failed",
  "stale",
  "dismissed",
  "superseded",
];

/** What a delete takes with it or leaves dangling. */
export interface ProposalReference {
  kind: string;
  id: string;
  name: string;
  detail: string;
}

export interface ProposalAction {
  operationId: string;
  pathParams: Record<string, string>;
  body?: unknown;
  before?: unknown;
  after?: unknown;
  /** The target now; only on the detail. */
  current?: unknown;
  /** The action deletes a resource. */
  destructive?: boolean;
  /** For a delete: what refers to the deleted resource. */
  references?: ProposalReference[];
}

export interface ProposalFailure {
  index: number;
  status: number;
  code: string;
  message: string;
  applied: number[];
}

export interface AIProposal {
  id: string;
  source: string;
  sessionId: string | null;
  findingId: string | null;
  title: string;
  rationale: string;
  status: ProposalStatus;
  configRevision: number;
  actions: ProposalAction[];
  hasDelete: boolean;
  createdBy: number | null;
  createdAt: string;
  updatedAt: string;
  appliedBy: number | null;
  appliedAt: string | null;
  dismissedBy: number | null;
  dismissedAt: string | null;
  dismissReason: string | null;
  dismissText: string | null;
  failure: ProposalFailure | null;
}

export type DismissReason = "not_needed" | "wrong" | "later" | "other";
export const DISMISS_REASONS: readonly {
  value: DismissReason;
  label: string;
}[] = [
  { value: "not_needed", label: "Not needed" },
  { value: "wrong", label: "Wrong" },
  { value: "later", label: "Later" },
  { value: "other", label: "Other" },
];

export interface AISession {
  id: string;
  owner: number;
  title: string;
  createdAt: string;
  lastActiveAt: string;
}

export interface AIToolCall {
  operationId: string;
  arguments?: Record<string, unknown>;
  status: number;
  truncated: boolean;
}

export interface AIMessage {
  id: number;
  role: "user" | "assistant";
  content: string;
  toolCalls: AIToolCall[];
  citations: number[];
  proposalId: string | null;
  taskId: string | null;
  createdAt: string;
}

export type TaskState = "queued" | "running" | "succeeded" | "failed";

export interface AITask {
  id: string;
  kind: string;
  status: TaskState;
  sessionId: string | null;
  errorCode: string | null;
  errorMessage: string | null;
  result: Record<string, unknown> | null;
  createdAt: string;
  startedAt: string | null;
  finishedAt: string | null;
}

export interface AISessionDetail {
  session: AISession;
  messages: AIMessage[];
  task: AITask | null;
}

/** The longest message the assistant accepts (S-8). */
export const MESSAGE_LIMIT = 4000;
/** How often a running assistant task is polled. */
export const TASK_POLL_MS = 2000;

const B = "/api/v1/ai";

export const getAIStatus = (signal?: AbortSignal) =>
  request<AIStatus>("GET", `${B}/status`, { signal });

export const listAgents = (signal?: AbortSignal) =>
  list<AIAgent>(`${B}/agents`, signal);
export const runAgent = (name: string) =>
  request<AIAgent>("POST", `${B}/agents/${encodeURIComponent(name)}/run`);

export interface FindingFilter {
  status?: string;
  severity?: string;
}
export async function listFindings(
  f: FindingFilter,
  signal?: AbortSignal,
): Promise<{ items: AIFinding[]; healthScore: number }> {
  const q = new URLSearchParams();
  if (f.status) q.set("status", f.status);
  if (f.severity) q.set("severity", f.severity);
  const qs = q.size ? `?${q}` : "";
  return request("GET", `${B}/findings${qs}`, { signal });
}
export const acknowledgeFinding = (id: string) =>
  request<AIFinding>("POST", `${B}/findings/${id}/acknowledge`);
export const dismissFinding = (id: string, reason: string) =>
  request<AIFinding>("POST", `${B}/findings/${id}/dismiss`, {
    body: { reason },
  });

export async function listProposals(
  status: string,
  signal?: AbortSignal,
): Promise<AIProposal[]> {
  const qs = status ? `?status=${encodeURIComponent(status)}` : "";
  return list<AIProposal>(`${B}/proposals${qs}`, signal);
}
export const getProposal = (id: string, signal?: AbortSignal) =>
  request<AIProposal>("GET", `${B}/proposals/${id}`, { signal });
export const applyProposal = (id: string) =>
  request<AIProposal>("POST", `${B}/proposals/${id}/apply`);
export const dismissProposal = (
  id: string,
  reason: DismissReason,
  text?: string,
) =>
  request<AIProposal>("POST", `${B}/proposals/${id}/dismiss`, {
    body: text ? { reason, text } : { reason },
  });

export const listSessions = (signal?: AbortSignal) =>
  list<AISession>(`${B}/sessions`, signal);
export const createSession = () =>
  request<AISession>("POST", `${B}/sessions`, { body: {} });
export const getSession = (id: string, signal?: AbortSignal) =>
  request<AISessionDetail>("GET", `${B}/sessions/${id}`, { signal });
export const deleteSession = (id: string) =>
  request<void>("DELETE", `${B}/sessions/${id}`);
export const postMessage = (id: string, content: string) =>
  request<{ taskId: string; messageId: number }>(
    "POST",
    `${B}/sessions/${id}/messages`,
    { body: { content } },
  );
export const getTask = (id: string, signal?: AbortSignal) =>
  request<AITask>("GET", `${B}/tasks/${id}`, { signal });
