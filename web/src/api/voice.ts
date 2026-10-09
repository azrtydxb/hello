/**
 * API client for the voice-agent pages (Voice agents, their editor, MCP
 * servers, runtime status): the operations of the spec's Interfaces table.
 * Shapes follow internal/api/openapi.json; fields the later backend streams
 * add arrive as optional, so the console renders what a given build sends.
 */
import { request, type Id } from "../api";

const pathId = (value: Id) => encodeURIComponent(String(value));

/**
 * A list response: the spec's *List schemas wrap the rows in `{items}`.
 * The bare array is still accepted so an older build keeps rendering.
 */
async function listOf<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const body = await request<unknown>("GET", path, { signal });
  if (Array.isArray(body)) return body as T[];
  const items = (body as Record<string, unknown> | null)?.items;
  if (Array.isArray(items)) return items as T[];
  throw new Error(`GET ${path} returned an unexpected payload`);
}

// --- agents ---------------------------------------------------------------------

/** How a caller proves their identity before write tools run (spec S-36). */
export type CallerVerification =
  "none" | "allowlist" | "pin" | "allowlist_or_pin";

export const CALLER_VERIFICATION_LABEL: Record<CallerVerification, string> = {
  none: "No verification (read-only tools only)",
  allowlist: "Allowlist of caller numbers",
  pin: "Spoken PIN",
  allowlist_or_pin: "Allowlist or spoken PIN",
};

/** A voice agent as listed (spec S-1). */
export interface VoiceAgent {
  id: Id;
  /** Unique; shown in routes and CDRs. */
  name: string;
  description: string;
  /** False refuses new calls to the agent. */
  enabled: boolean;
  /** Generated on create, never edited; hides the name from talking-agent. */
  sipUser: string;
  /** The internal extension that reaches the agent, when it has one. */
  extension?: string;
  /** Bumped on every persona or attachment save. */
  revision: number;
}

/** The persona fields of an agent, as the detail response carries them. */
export interface VoicePersona {
  prompt?: string;
  greeting?: string;
  /** BCP 47. */
  language?: string;
  voice?: string;
  voiceReference?: string;
  style?: string;
  temperature?: number;
}

/** GET /api/v1/voice/agents/{id}: the agent, its persona and its attachments. */
export interface VoiceAgentDetail
  extends VoiceAgent, VoicePersona, VoiceLimits, VoiceAgentVerification {
  /** The MCP attachments, when the response carries them. */
  tools?: VoiceAgentTools;
}

/** Caller verification fields of an agent (spec S-36). */
export interface VoiceAgentVerification {
  callerVerification?: CallerVerification;
  callerAllowlist?: string[];
}

/** The per-agent limits (spec S-2). */
export interface VoiceLimits {
  /** 30-1800, default 600. */
  maxCallSeconds?: number;
  /** 1-50, default 4. */
  maxConcurrent?: number;
  maxToolCalls?: number;
  /** Silence before the agent ends the call; default 20. */
  idleTimeoutSeconds?: number;
  /** Transcripts are opt-in per agent (spec S-23). */
  recordTranscript?: boolean;
  /** 1-365, default 30. */
  transcriptRetentionDays?: number;
}

/** The editable fields of a voice agent (VoiceAgentInput). */
export interface VoiceAgentInput extends VoicePersona, VoiceLimits {
  name?: string;
  description?: string;
  enabled?: boolean;
  extension?: string;
  callerVerification?: CallerVerification;
  callerAllowlist?: string[];
}

export const listVoiceAgents = (signal?: AbortSignal) =>
  listOf<VoiceAgent>("/api/v1/voice/agents", signal);

export const getVoiceAgent = (agentId: Id, signal?: AbortSignal) =>
  request<VoiceAgentDetail>("GET", `/api/v1/voice/agents/${pathId(agentId)}`, {
    signal,
  });

export const createVoiceAgent = (input: VoiceAgentInput) =>
  request<VoiceAgent>("POST", "/api/v1/voice/agents", { body: input });

export const updateVoiceAgent = (agentId: Id, patch: VoiceAgentInput) =>
  request<VoiceAgent>("PUT", `/api/v1/voice/agents/${pathId(agentId)}`, {
    body: patch,
  });

export const deleteVoiceAgent = (agentId: Id) =>
  request<void>("DELETE", `/api/v1/voice/agents/${pathId(agentId)}`);

/** One saved persona (spec S-4); the last ten are kept. */
export interface VoiceAgentVersion {
  revision: number;
  actor: string;
  createdAt: string;
  persona: VoicePersona & VoiceLimits;
}

export const listVoiceAgentVersions = (agentId: Id, signal?: AbortSignal) =>
  listOf<VoiceAgentVersion>(
    `/api/v1/voice/agents/${pathId(agentId)}/versions`,
    signal,
  );

export const restoreVoiceAgentVersion = (agentId: Id, revision: number) =>
  request<VoiceAgent>(
    "POST",
    `/api/v1/voice/agents/${pathId(agentId)}/versions/${revision}/restore`,
  );

// --- tools ----------------------------------------------------------------------

/** One allowlisted tool of an attachment (spec S-6). */
export interface VoiceToolRef {
  /** The tool's name without the server prefix. */
  name: string;
  /** True reads the action aloud and waits for a spoken yes. */
  confirm: boolean;
  /** True when the tool changes data; it needs caller verification. */
  write: boolean;
}

/** One attached MCP server of an agent. */
export interface VoiceAttachment {
  serverId: Id;
  /** False detaches the server without deleting it. */
  enabled: boolean;
  tools: VoiceToolRef[];
}

/** PUT /api/v1/voice/agents/{id}/tools body and response. */
export interface VoiceAgentTools {
  servers: VoiceAttachment[];
}

export const putVoiceAgentTools = (agentId: Id, tools: VoiceAgentTools) =>
  request<VoiceAgentTools>(
    "PUT",
    `/api/v1/voice/agents/${pathId(agentId)}/tools`,
    {
      body: tools,
    },
  );

// --- agent calls ----------------------------------------------------------------

/** One call to a voice agent, joined with its report (spec S-23). */
export interface VoiceAgentCall {
  correlationId: string;
  agentName: string;
  startTime: string;
  finalStatus: number;
  /** The report's outcome; "unreported" when none arrived. */
  outcome: string;
  summary: string;
  toolCalls: number;
  tokensIn: number;
  tokensOut: number;
}

export const listVoiceAgentCalls = (agentId: Id, signal?: AbortSignal) =>
  listOf<VoiceAgentCall>(
    `/api/v1/voice/agents/${pathId(agentId)}/calls`,
    signal,
  );

// --- MCP servers ----------------------------------------------------------------

/** How Hello authenticates to an MCP server (spec S-5). */
export type VoiceMCPAuth =
  "none" | "bearer" | "header" | "oauth_client_credentials";

export const VOICE_MCP_AUTHS: readonly VoiceMCPAuth[] = [
  "none",
  "bearer",
  "header",
  "oauth_client_credentials",
];

export const VOICE_MCP_AUTH_LABEL: Record<VoiceMCPAuth, string> = {
  none: "No authentication",
  bearer: "Bearer token",
  header: "Named header",
  oauth_client_credentials: "OAuth 2 client credentials",
};

/** An MCP server as listed; never its credential material (spec S-5). */
export interface VoiceMCPServer {
  id: Id;
  name: string;
  url: string;
  auth: VoiceMCPAuth;
  /** The header carrying the credential, for auth header. */
  headerName?: string;
  timeoutMs: number;
  enabled: boolean;
  lastCheckAt?: string;
  /** ok, refused or unreachable, from the last check. */
  lastCheckStatus?: string;
  /** True when a credential is stored; the value never comes back. */
  credentialSet?: boolean;
  tokenUrl?: string;
  clientId?: string;
  scope?: string;
  audience?: string;
}

/** The fields of an MCP server; credential fields are write-only. */
export interface VoiceMCPServerInput {
  name: string;
  url: string;
  auth: VoiceMCPAuth;
  headerName?: string;
  /** The bearer token or header value; sealed on save, never returned. */
  credential?: string;
  tokenUrl?: string;
  clientId?: string;
  /** The OAuth client secret; sealed on save, never returned. */
  clientSecret?: string;
  scope?: string;
  audience?: string;
  timeoutMs?: number;
  enabled?: boolean;
}

export const listVoiceMCPServers = (signal?: AbortSignal) =>
  listOf<VoiceMCPServer>("/api/v1/voice/mcp-servers", signal);

export const getVoiceMCPServer = (serverId: Id, signal?: AbortSignal) =>
  request<VoiceMCPServer>(
    "GET",
    `/api/v1/voice/mcp-servers/${pathId(serverId)}`,
    {
      signal,
    },
  );

export const createVoiceMCPServer = (input: VoiceMCPServerInput) =>
  request<VoiceMCPServer>("POST", "/api/v1/voice/mcp-servers", { body: input });

export const updateVoiceMCPServer = (
  serverId: Id,
  patch: VoiceMCPServerInput,
) =>
  request<VoiceMCPServer>(
    "PUT",
    `/api/v1/voice/mcp-servers/${pathId(serverId)}`,
    {
      body: patch,
    },
  );

export const deleteVoiceMCPServer = (serverId: Id) =>
  request<void>("DELETE", `/api/v1/voice/mcp-servers/${pathId(serverId)}`);

/** One tool an MCP server offers (POST .../discover). */
export interface VoiceMCPTool {
  name: string;
  description?: string;
}

export const discoverVoiceMCPServer = (serverId: Id, signal?: AbortSignal) =>
  request<{ tools: VoiceMCPTool[] }>(
    "POST",
    `/api/v1/voice/mcp-servers/${pathId(serverId)}/discover`,
    { signal },
  );

/** The result of a test connection (spec S-9). */
export interface VoiceMCPCheck {
  ok: boolean;
  latencyMs: number;
  /** The tool's result, as text. */
  result: string;
}

export const testVoiceMCPServer = (serverId: Id, signal?: AbortSignal) =>
  request<VoiceMCPCheck>(
    "POST",
    `/api/v1/voice/mcp-servers/${pathId(serverId)}/test`,
    {
      signal,
    },
  );

// --- runtime status and administrator operations ----------------------------------

/** Per-agent state as last reported by talking-agent (spec S-20). */
export interface VoiceAgentRuntimeState {
  name: string;
  /** The revision the agent loaded. */
  revision: number;
  /** loaded, loading or failed. */
  state: string;
}

/** GET /api/v1/voice/status (spec S-22, S-30). */
export interface VoiceStatus {
  revision: number;
  /** When talking-agent last acked; null when never. */
  lastSeenAt?: string | null;
  version?: string;
  /** False when the last ack is more than two minutes old. */
  healthy: boolean;
  agents: VoiceAgentRuntimeState[];
}

export const getVoiceStatus = (signal?: AbortSignal) =>
  request<VoiceStatus>("GET", "/api/v1/voice/status", { signal });

/** POST /api/v1/voice/secret/rotate: the new secret, shown once (spec S-16). */
export interface VoiceSecretRotation {
  secret: string;
}

export const rotateVoiceSIPSecret = () =>
  request<VoiceSecretRotation>("POST", "/api/v1/voice/secret/rotate");

/** POST /api/v1/voice/runtime-account: the service account for talking-agent. */
export interface VoiceRuntimeAccount {
  id: Id;
  clientId?: string;
  /** The client secret, shown once. */
  secret: string;
}

export const createVoiceRuntimeAccount = () =>
  request<VoiceRuntimeAccount>("POST", "/api/v1/voice/runtime-account");

// --- shared helpers ----------------------------------------------------------------

/** The runtime entry of one agent, or undefined when the status has none. */
export function runtimeOf(
  agent: VoiceAgent,
  status: VoiceStatus | undefined,
): VoiceAgentRuntimeState | undefined {
  return status?.agents.find((a) => a.name === agent.name);
}

/** The badge for an agent's enabled flag and runtime state (spec S-20, S-25). */
export function agentStateBadge(
  agent: VoiceAgent,
  status: VoiceStatus | undefined,
): { label: string; tone: "good" | "warn" | "bad" | "outline" } {
  if (!agent.enabled) return { label: "Disabled", tone: "outline" };
  const runtime = runtimeOf(agent, status);
  if (!runtime) {
    return { label: "Not loaded", tone: status?.healthy ? "warn" : "bad" };
  }
  if (runtime.state === "loaded") {
    // Stale when the runtime runs an older persona (S-20).
    return runtime.revision === agent.revision
      ? { label: "Live", tone: "good" }
      : { label: `Stale (revision ${runtime.revision})`, tone: "warn" };
  }
  if (runtime.state === "loading") return { label: "Loading", tone: "warn" };
  if (runtime.state === "failed") return { label: "Failed", tone: "bad" };
  return { label: "Not loaded", tone: "warn" };
}
