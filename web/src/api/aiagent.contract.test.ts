import { describe, expect, it } from "vitest";
import type {
  AIAgent,
  AIAgentRun,
  AIFinding,
  AIMessage,
  AIProposal,
  AISession,
  AIStatus,
  AIToolCall,
  AITask,
  ProposalAction,
  ProposalFailure,
  ProposalReference,
} from "./aiagent";
import type { Cdr } from "../api";
import openapi from "../../../internal/api/openapi.json";

interface Schema {
  properties?: Record<string, Schema>;
  items?: Schema;
}

const doc = openapi as unknown as {
  components: { schemas: Record<string, Schema> };
};
const schema = (name: string) => doc.components.schemas[name]!;
const keys = (s: Schema) => Object.keys(s.properties ?? {}).sort();

// A Record over keyof T names every property of the web type; adding or
// dropping one without updating this table fails the typecheck, and a
// difference from openapi.json fails the test.
const WEB = {
  AIAgentRun: {
    startedAt: 0,
    finishedAt: 0,
    outcome: 0,
    tokens: 0,
    error: 0,
  } satisfies Record<keyof AIAgentRun, 0>,
  AIAgent: {
    name: 0,
    intervalSeconds: 0,
    lastRun: 0,
    nextDueAt: 0,
    runRequested: 0,
  } satisfies Record<keyof AIAgent, 0>,
  AIStatus: {
    enabled: 0,
    reason: 0,
    provider: 0,
    model: 0,
    endpointHost: 0,
    endpointPrivate: 0,
    structuredOutput: 0,
    tokensToday: 0,
    dailyTokenBudget: 0,
    backgroundTokenLimit: 0,
    concurrencyInUse: 0,
    maxConcurrency: 0,
    agents: 0,
    recentErrors: 0,
    openFindings: 0,
    healthScore: 0,
    openProposals: 0,
  } satisfies Record<keyof AIStatus, 0>,
  AIFinding: {
    id: 0,
    candidateId: 0,
    type: 0,
    subject: 0,
    severity: 0,
    status: 0,
    title: 0,
    evidence: 0,
    explanation: 0,
    explained: 0,
    rank: 0,
    firstSeen: 0,
    lastSeen: 0,
    occurrences: 0,
    severityHistory: 0,
    acknowledgedBy: 0,
    acknowledgedAt: 0,
    dismissedBy: 0,
    dismissedAt: 0,
    dismissReason: 0,
    resolvedAt: 0,
    proposalId: 0,
  } satisfies Record<keyof AIFinding, 0>,
  AIProposalAction: {
    operationId: 0,
    pathParams: 0,
    body: 0,
    before: 0,
    after: 0,
    current: 0,
    destructive: 0,
    references: 0,
  } satisfies Record<keyof ProposalAction, 0>,
  AIProposalFailure: {
    index: 0,
    status: 0,
    code: 0,
    message: 0,
    applied: 0,
  } satisfies Record<keyof ProposalFailure, 0>,
  AIProposal: {
    id: 0,
    source: 0,
    sessionId: 0,
    findingId: 0,
    title: 0,
    rationale: 0,
    status: 0,
    configRevision: 0,
    actions: 0,
    hasDelete: 0,
    createdBy: 0,
    createdAt: 0,
    updatedAt: 0,
    appliedBy: 0,
    appliedAt: 0,
    dismissedBy: 0,
    dismissedAt: 0,
    dismissReason: 0,
    dismissText: 0,
    failure: 0,
  } satisfies Record<keyof AIProposal, 0>,
  AISession: {
    id: 0,
    owner: 0,
    title: 0,
    createdAt: 0,
    lastActiveAt: 0,
  } satisfies Record<keyof AISession, 0>,
  AIToolCall: {
    operationId: 0,
    arguments: 0,
    status: 0,
    truncated: 0,
  } satisfies Record<keyof AIToolCall, 0>,
  AIMessage: {
    id: 0,
    role: 0,
    content: 0,
    toolCalls: 0,
    citations: 0,
    proposalId: 0,
    taskId: 0,
    createdAt: 0,
  } satisfies Record<keyof AIMessage, 0>,
  AITask: {
    id: 0,
    kind: 0,
    status: 0,
    sessionId: 0,
    errorCode: 0,
    errorMessage: 0,
    result: 0,
    createdAt: 0,
    startedAt: 0,
    finishedAt: 0,
  } satisfies Record<keyof AITask, 0>,
};

describe("TestWebTypesMatchOpenAPI", () => {
  for (const [name, table] of Object.entries(WEB)) {
    it(`${name} has the document's properties`, () => {
      expect(Object.keys(table).sort()).toEqual(keys(schema(name)));
    });
  }

  it("the proposal reference has the document's properties", () => {
    const ref = schema("AIProposalAction").properties!.references!.items!;
    const web = {
      kind: 0,
      id: 0,
      name: 0,
      detail: 0,
    } satisfies Record<keyof ProposalReference, 0>;
    expect(Object.keys(web).sort()).toEqual(keys(ref));
  });

  it("the CDR carries the call quality fields", () => {
    const web = { rtpPackets: 0, rtpLost: 0, rtpJitterMs: 0 } satisfies Partial<
      Record<keyof Cdr, 0>
    >;
    const cdr = keys(schema("CDR"));
    for (const k of Object.keys(web)) expect(cdr).toContain(k);
  });
});
