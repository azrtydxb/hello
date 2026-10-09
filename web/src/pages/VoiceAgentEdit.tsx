import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router";
import { errorMessage, fieldErrors } from "../api";
import {
  agentStateBadge,
  discoverVoiceMCPServer,
  getVoiceAgent,
  getVoiceStatus,
  listVoiceAgentCalls,
  listVoiceAgentVersions,
  listVoiceMCPServers,
  putVoiceAgentTools,
  restoreVoiceAgentVersion,
  updateVoiceAgent,
  CALLER_VERIFICATION_LABEL,
  type CallerVerification,
  type VoiceAgentCall,
  type VoiceAgentInput,
  type VoiceAgentDetail,
  type VoiceAgentVersion,
  type VoiceAttachment,
  type VoiceMCPServer,
  type VoiceStatus,
  type VoiceToolRef,
} from "../api/voice";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Icon,
  Checkbox,
  Input,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  type TabItem,
  type TableColumn,
  Tabs,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { Can } from "../role";
import { usePolling } from "../usePolling";
import { clock, day } from "./calls/common";

/** The editor's tabs (spec S-26, S-29). */
type Tab =
  "persona" | "tools" | "limits" | "numbers" | "history" | "calls" | "test";

const TAB_ORDER: readonly Tab[] = [
  "persona",
  "tools",
  "limits",
  "numbers",
  "history",
  "calls",
  "test",
];

const TAB_LABEL: Record<Tab, string> = {
  persona: "Persona",
  tools: "Tools",
  limits: "Limits",
  numbers: "Numbers",
  history: "History",
  calls: "Calls",
  test: "Test",
};

/** Stub text for a tab the fixed API shape does not fill yet. */
function tabItems(attached: number): readonly TabItem<Tab>[] {
  return TAB_ORDER.map((id) => ({
    id,
    label: TAB_LABEL[id],
    count: id === "tools" ? attached : undefined,
  }));
}

/** Persona field limits of the spec (S-1). */
const PROMPT_MAX = 8000;

/**
 * The voice agent editor (/voice/agents/:id): persona, tools, limits,
 * numbers, history, calls and the test tab (spec S-26, S-29).
 */
export function VoiceAgentEdit() {
  const { id = "" } = useParams();
  const [detail, setDetail] = useState<VoiceAgentDetail | null>(null);
  const [error, setError] = useState<string | null>(null);
  const toast = useToast();
  const status = usePolling(getVoiceStatus, 30_000);

  const load = useCallback(() => {
    const controller = new AbortController();
    getVoiceAgent(id, controller.signal)
      .then(setDetail)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [id]);
  useEffect(() => load(), [load]);

  if (error) {
    return (
      <section aria-labelledby="page-title">
        <PageHeader eyebrow="AI" title="Voice agent" back={<BackLink />} />
        <Alert tone="bad" title="Could not load the voice agent">
          {error}
        </Alert>
      </section>
    );
  }
  if (!detail) {
    return (
      <section aria-labelledby="page-title">
        <PageHeader eyebrow="AI" title="Voice agent" back={<BackLink />} />
        <Spinner label="Loading voice agent…" />
      </section>
    );
  }
  return (
    <VoiceAgentEditLoaded
      key={String(detail.id)}
      detail={detail}
      status={status.status === "loading" ? undefined : status.data}
      reload={() => load()}
      showToast={(m) => toast.show(m)}
    />
  );
}

function BackLink() {
  return (
    <Link className="az-btn az-btn--ghost az-btn--sm" to="/voice/agents">
      <Icon name="arrow-left" size={14} /> Voice agents
    </Link>
  );
}

/** The editor once the agent is loaded; the drafts reset with `key`. */
function VoiceAgentEditLoaded({
  detail,
  status,
  reload,
  showToast,
}: {
  detail: VoiceAgentDetail;
  status: VoiceStatus | undefined;
  reload: () => void;
  showToast: (message: string) => void;
}) {
  const [tab, setTab] = useState<Tab>("persona");
  const attached = detail.tools?.servers.filter((s) => s.enabled) ?? [];
  const toolCount = attached.reduce((n, s) => n + s.tools.length, 0);
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI"
        title={
          <>
            Voice agent {detail.name}
            {!detail.enabled && <Badge tone="outline">Disabled</Badge>}
            {detail.enabled && status && (
              <Badge tone={agentStateBadge(detail, status).tone} dot>
                {agentStateBadge(detail, status).label}
              </Badge>
            )}
          </>
        }
        description={
          detail.description || "An automated assistant that answers calls."
        }
        back={<BackLink />}
      />
      <Tabs
        items={tabItems(toolCount)}
        value={tab}
        onChange={setTab}
        aria-label="Voice agent sections"
        idPrefix="agent"
      />
      <div
        role="tabpanel"
        id={`agent-panel-${tab}`}
        aria-labelledby={`agent-tab-${tab}`}
      >
        {tab === "persona" && (
          <PersonaTab detail={detail} onSaved={reload} showToast={showToast} />
        )}
        {tab === "tools" && (
          <ToolsTab detail={detail} onSaved={reload} showToast={showToast} />
        )}
        {tab === "limits" && (
          <LimitsTab detail={detail} onSaved={reload} showToast={showToast} />
        )}
        {tab === "numbers" && (
          <NumbersTab detail={detail} onSaved={reload} showToast={showToast} />
        )}
        {tab === "history" && (
          <HistoryTab
            agent={detail}
            showToast={showToast}
            onRestored={reload}
          />
        )}
        {tab === "calls" && <CallsTab agent={detail} />}
        {tab === "test" && <TestTab agent={detail} status={status} />}
      </div>
    </section>
  );
}

/** Fields every save sends: the persona, limits, verification and numbers. */
function bodyOf(d: VoiceAgentDetail, draft: VoiceAgentDraft): VoiceAgentInput {
  const num = (v: string) => (v.trim() === "" ? undefined : Number(v));
  return {
    name: d.name,
    description: d.description,
    enabled: d.enabled,
    extension: draft.extension.trim() || undefined,
    prompt: draft.prompt,
    greeting: draft.greeting,
    language: draft.language,
    voice: draft.voice,
    voiceReference: draft.voiceReference,
    style: draft.style,
    temperature: num(draft.temperature),
    maxCallSeconds: num(draft.maxCallSeconds),
    maxConcurrent: num(draft.maxConcurrent),
    maxToolCalls: num(draft.maxToolCalls),
    idleTimeoutSeconds: num(draft.idleTimeoutSeconds),
    recordTranscript: draft.recordTranscript,
    transcriptRetentionDays: num(draft.transcriptRetentionDays),
    callerVerification: draft.callerVerification,
    callerAllowlist: draft.callerAllowlist,
  };
}

/** The editable state of one agent's non-tool fields. */
interface VoiceAgentDraft {
  name: string;
  description: string;
  enabled: boolean;
  extension: string;
  prompt: string;
  greeting: string;
  language: string;
  voice: string;
  voiceReference: string;
  style: string;
  temperature: string;
  maxCallSeconds: string;
  maxConcurrent: string;
  maxToolCalls: string;
  idleTimeoutSeconds: string;
  recordTranscript: boolean;
  transcriptRetentionDays: string;
  callerVerification: CallerVerification;
  callerAllowlist: string[];
}

function draftOf(d: VoiceAgentDetail): VoiceAgentDraft {
  return {
    name: d.name,
    description: d.description,
    enabled: d.enabled,
    extension: d.extension ?? "",
    prompt: d.prompt ?? "",
    greeting: d.greeting ?? "",
    language: d.language ?? "en-US",
    voice: d.voice ?? "",
    voiceReference: d.voiceReference ?? "",
    style: d.style ?? "",
    temperature: d.temperature === undefined ? "" : String(d.temperature),
    maxCallSeconds: String(d.maxCallSeconds ?? 600),
    maxConcurrent: String(d.maxConcurrent ?? 4),
    maxToolCalls: String(d.maxToolCalls ?? 20),
    idleTimeoutSeconds: String(d.idleTimeoutSeconds ?? 20),
    recordTranscript: d.recordTranscript ?? false,
    transcriptRetentionDays: String(d.transcriptRetentionDays ?? 30),
    callerVerification: d.callerVerification ?? "none",
    callerAllowlist: d.callerAllowlist ?? [],
  };
}

/** The persona tab: prompt with a counter, greeting, language, voice, style (S-26). */
function PersonaTab({
  detail,
  onSaved,
  showToast,
}: {
  detail: VoiceAgentDetail;
  onSaved: () => void;
  showToast: (m: string) => void;
}) {
  const [d, setD] = useState(() => draftOf(detail));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<{ message: string }[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const set = <K extends keyof VoiceAgentDraft>(k: K, v: VoiceAgentDraft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  async function save() {
    setFormError(null);
    setBusy(true);
    try {
      await updateVoiceAgent(detail.id, bodyOf(detail, d));
      showToast(`Persona of ${detail.name} saved.`);
      onSaved();
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), Object.keys(d));
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const greetingEmpty = d.greeting.trim() === "";
  return (
    <div className="cf-form">
      <Alert tone="info" title="Starter text">
        The prompt below is a starting point. Edit it freely; every save keeps
        the last ten versions (see History).
      </Alert>
      <div className="cf-form__section">
        <label className="az-eyebrow" htmlFor="agent-prompt">
          System prompt
        </label>
        <textarea
          id="agent-prompt"
          className="az-input az-textarea az-input--mono"
          rows={10}
          maxLength={PROMPT_MAX}
          value={d.prompt}
          onChange={(e) => set("prompt", e.target.value)}
        />
        <p className="cf-form__note">
          {d.prompt.length} of {PROMPT_MAX} characters.{" "}
          {/sk-[A-Za-z0-9]|BEGIN [A-Z]+ PRIVATE KEY/.test(d.prompt) && (
            <span className="pf-tone--bad">
              This looks like a credential. Never paste secrets into a prompt.
            </span>
          )}
        </p>
      </div>
      <Input
        id="agent-greeting"
        label="Greeting"
        hint="Spoken first."
        value={d.greeting}
        error={errors.greeting}
        onChange={(e) => set("greeting", e.target.value)}
      />
      {greetingEmpty && (
        <Alert tone="warn" title="No greeting">
          The agent answers in silence. Also, the caller is told only if the
          greeting says so.
        </Alert>
      )}
      <div className="cf-two">
        <Select
          id="agent-language"
          label="Language"
          value={d.language}
          options={[
            { value: "en-US", label: "English (US)" },
            { value: "en-GB", label: "English (UK)" },
            { value: "nl-NL", label: "Nederlands" },
            { value: "fr-FR", label: "Français" },
            { value: "de-DE", label: "Deutsch" },
            { value: "ar-AE", label: "العربية" },
          ]}
          onChange={(e) => set("language", e.target.value)}
        />
        <Input
          id="agent-voice"
          label="Voice"
          hint="A talking-agent voice id; empty uses its default."
          value={d.voice}
          onChange={(e) => set("voice", e.target.value)}
        />
      </div>
      <div className="cf-two">
        <Input
          id="agent-voice-reference"
          label="Reference voice"
          hint="Optional, for voice cloning."
          value={d.voiceReference}
          onChange={(e) => set("voiceReference", e.target.value)}
        />
        <Input
          id="agent-style"
          label="Style"
          hint="Up to 500 characters of style instructions."
          value={d.style}
          onChange={(e) => set("style", e.target.value)}
        />
      </div>
      <Can>
        <SaveBar
          busy={busy}
          formError={formError}
          unmatched={unmatched}
          onSave={() => void save()}
          label="Save persona"
        />
      </Can>
    </div>
  );
}

/** Shared save row: the button, a form error and unmatched field errors. */
function SaveBar({
  busy,
  formError,
  unmatched,
  onSave,
  label,
}: {
  busy: boolean;
  formError: string | null;
  unmatched: { message: string }[];
  onSave: () => void;
  label: string;
}) {
  return (
    <div className="cf-stack" style={{ gap: 8 }}>
      {formError && (
        <Alert tone="bad" title="Could not save">
          {formError}
        </Alert>
      )}
      {unmatched.length > 0 && (
        <Alert tone="warn" title="Some problems could not be shown on a field">
          <ul className="cf-plain">
            {unmatched.map((f, i) => (
              <li key={i}>{f.message}</li>
            ))}
          </ul>
        </Alert>
      )}
      <Button onClick={onSave} disabled={busy}>
        {busy ? "Saving…" : label}
      </Button>
    </div>
  );
}

/** The tools tab: attached MCP servers, the discovery checklist, warnings (S-6, S-26). */
function ToolsTab({
  detail,
  onSaved,
  showToast,
}: {
  detail: VoiceAgentDetail;
  onSaved: () => void;
  showToast: (m: string) => void;
}) {
  const [servers, setServers] = useState<VoiceMCPServer[] | null>(null);
  const [offers, setOffers] = useState<Map<string, VoiceToolRef[]>>(new Map());
  const [draft, setDraft] = useState<VoiceAttachment[]>([]);
  const [attachId, setAttachId] = useState("");
  const [busy, setBusy] = useState(false);
  const [formError, setFormError] = useState<string | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listVoiceMCPServers(controller.signal)
      .then((items) => {
        setServers(items);
        setDraft(structuredClone(detail.tools?.servers ?? []));
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setFormError(errorMessage(err));
      });
    return () => controller.abort();
    // Reload the checklist when the attachments change on the server.
  }, [detail.tools]);

  // Discovery fills the checklist: one guarded tools/list per attached server.
  useEffect(() => {
    if (servers === null) return;
    const controller = new AbortController();
    for (const a of draft) {
      const server = servers.find((s) => String(s.id) === String(a.serverId));
      if (!server || offers.has(String(a.serverId))) continue;
      discoverVoiceMCPServer(a.serverId, controller.signal)
        .then((d) =>
          setOffers((prev) => {
            const next = new Map(prev);
            next.set(
              String(a.serverId),
              d.tools.map((t) => ({
                name: t.name,
                // Confirm defaults on unless the server marks it read-only;
                // the discovery view keeps the attachment's stored values.
                confirm: true,
                write: true,
              })),
            );
            return next;
          }),
        )
        .catch(() => {
          // A server that cannot be discovered now shows an empty checklist
          // with its warning; the allowlist itself is untouched.
        });
    }
    return () => controller.abort();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [servers, draft]);

  const serverOf = (serverId: string) =>
    servers?.find((s) => String(s.id) === serverId);

  async function save() {
    setBusy(true);
    setFormError(null);
    try {
      await putVoiceAgentTools(detail.id, { servers: draft });
      showToast(`Tools of ${detail.name} saved.`);
      onSaved();
    } catch (err: unknown) {
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const attachable = (servers ?? []).filter(
    (s) => !draft.some((a) => String(a.serverId) === String(s.id)),
  );

  return (
    <div className="cf-form">
      <Alert tone="info" title="How tools work">
        A tool is available to the agent only after you tick it. Ticking a write
        tool gives it to callers who passed verification; every tool with
        confirmation reads its action aloud first.
      </Alert>
      {formError && (
        <Alert tone="bad" title="Could not load or save the tools">
          {formError}
        </Alert>
      )}
      {draft.length === 0 && (
        <EmptyState
          icon="wrench"
          title="No MCP servers attached"
          description="Attach a server, then tick the tools the agent may call."
        />
      )}
      {draft.map((a, i) => {
        const server = serverOf(String(a.serverId));
        const offered = offers.get(String(a.serverId)) ?? [];
        const setTool = (name: string, patch: Partial<VoiceToolRef>) =>
          setDraft((prev) =>
            prev.map((x, j) =>
              j === i
                ? {
                    ...x,
                    tools: x.tools.some((t) => t.name === name)
                      ? x.tools.map((t) =>
                          t.name === name ? { ...t, ...patch } : t,
                        )
                      : [
                          ...x.tools,
                          { name, confirm: true, write: true, ...patch },
                        ],
                  }
                : x,
            ),
          );
        return (
          <fieldset
            key={String(a.serverId)}
            className="cf-form__section"
            aria-labelledby={`tools-${String(a.serverId)}`}
          >
            <legend className="az-eyebrow" id={`tools-${String(a.serverId)}`}>
              {server?.name ?? `Server #${String(a.serverId)}`}{" "}
              <Switch
                label="Enabled"
                labelPosition="end"
                checked={a.enabled}
                onChange={(e) =>
                  setDraft((prev) =>
                    prev.map((x, j) =>
                      j === i ? { ...x, enabled: e.target.checked } : x,
                    ),
                  )
                }
              />
            </legend>
            {a.tools.length === 0 && (
              <Alert tone="warn" title="No tool ticked">
                No tool of this server is available: nothing is available until
                you tick it.
              </Alert>
            )}
            <ul
              className="cf-plain"
              aria-label={`Tools of ${server?.name ?? String(a.serverId)}`}
            >
              {(offered.length > 0 ? offered : a.tools).map((t) => {
                const ticked = a.tools.some((x) => x.name === t.name);
                return (
                  <li key={t.name}>
                    <Checkbox
                      label={t.name}
                      checked={ticked}
                      onChange={(e) => {
                        if (e.target.checked) setTool(t.name, {});
                        else
                          setDraft((prev) =>
                            prev.map((x, j) =>
                              j === i
                                ? {
                                    ...x,
                                    tools: x.tools.filter(
                                      (x2) => x2.name !== t.name,
                                    ),
                                  }
                                : x,
                            ),
                          );
                      }}
                    />
                    {ticked && (
                      <Checkbox
                        label="Confirm before running"
                        checked={t.confirm}
                        onChange={(e) =>
                          setTool(t.name, { confirm: e.target.checked })
                        }
                      />
                    )}
                  </li>
                );
              })}
            </ul>
            <Can min="admin">
              <Button
                variant="ghost"
                size="sm"
                icon="trash-2"
                aria-label={`Detach ${server?.name ?? String(a.serverId)}`}
                onClick={() =>
                  setDraft((prev) => prev.filter((_, j) => j !== i))
                }
              >
                Detach
              </Button>
            </Can>
          </fieldset>
        );
      })}
      <Can>
        <div className="cf-add">
          <Select
            id="agent-attach-server"
            label="Attach an MCP server"
            value={attachId}
            options={[
              { value: "", label: servers ? "Choose…" : "Loading…" },
              ...attachable.map((s) => ({
                value: String(s.id),
                label: s.name,
              })),
            ]}
            onChange={(e) => {
              const v = e.target.value;
              setAttachId("");
              if (v === "") return;
              setDraft((prev) => [
                ...prev,
                { serverId: v, enabled: true, tools: [] },
              ]);
            }}
          />
        </div>
        <SaveBar
          busy={busy}
          formError={null}
          unmatched={[]}
          onSave={() => void save()}
          label="Save tools"
        />
      </Can>
    </div>
  );
}

/** The limits tab: call limits, transcripts, caller verification (S-2, S-23, S-36). */
function LimitsTab({
  detail,
  onSaved,
  showToast,
}: {
  detail: VoiceAgentDetail;
  onSaved: () => void;
  showToast: (m: string) => void;
}) {
  const [d, setD] = useState(() => draftOf(detail));
  const [allowlistText, setAllowlistText] = useState(() =>
    (detail.callerAllowlist ?? []).join("\n"),
  );
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);
  const set = <K extends keyof VoiceAgentDraft>(k: K, v: VoiceAgentDraft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));
  const NATURAL = /^[0-9]+$/;

  async function save() {
    const allowlist = allowlistText
      .split("\n")
      .map((v) => v.trim())
      .filter((v) => v !== "");
    const found: Record<string, string> = {};
    for (const [k, lo, hi] of [
      ["maxCallSeconds", 30, 1800],
      ["maxConcurrent", 1, 50],
      ["idleTimeoutSeconds", 5, 300],
      ["transcriptRetentionDays", 1, 365],
    ] as const) {
      const v = d[k];
      if (!NATURAL.test(v) || Number(v) < lo || Number(v) > hi) {
        found[k] = `Use a whole number from ${lo} to ${hi}.`;
      }
    }
    if (!NATURAL.test(d.maxToolCalls) || Number(d.maxToolCalls) < 1) {
      found.maxToolCalls = "Use a whole number of 1 or more.";
    }
    if (
      (d.callerVerification === "allowlist" ||
        d.callerVerification === "allowlist_or_pin") &&
      allowlist.length === 0
    ) {
      found.callerAllowlist = "Enter at least one number, one per line.";
    }
    if (allowlist.length > 100) {
      found.callerAllowlist = "At most 100 numbers.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    setFormError(null);
    try {
      await updateVoiceAgent(detail.id, {
        ...bodyOf(detail, d),
        callerAllowlist: allowlist,
      });
      showToast(`Limits of ${detail.name} saved.`);
      onSaved();
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), [
        ...Object.keys(d),
        "callerAllowlist",
      ]);
      setErrors(mapped.byKey);
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const writeTools = (detail.tools?.servers ?? []).some(
    (s) => s.enabled && s.tools.some((t) => t.write),
  );
  return (
    <div className="cf-form">
      <div className="cf-two">
        <Input
          id="agent-max-call-seconds"
          label="Longest call (s)"
          inputMode="numeric"
          value={d.maxCallSeconds}
          error={errors.maxCallSeconds}
          onChange={(e) => set("maxCallSeconds", e.target.value)}
        />
        <Input
          id="agent-max-concurrent"
          label="Max concurrent calls"
          inputMode="numeric"
          value={d.maxConcurrent}
          error={errors.maxConcurrent}
          onChange={(e) => set("maxConcurrent", e.target.value)}
        />
      </div>
      <div className="cf-two">
        <Input
          id="agent-max-tool-calls"
          label="Max tool calls per call"
          inputMode="numeric"
          value={d.maxToolCalls}
          error={errors.maxToolCalls}
          onChange={(e) => set("maxToolCalls", e.target.value)}
        />
        <Input
          id="agent-idle-timeout"
          label="Idle timeout (s)"
          inputMode="numeric"
          hint="The agent hangs up after this much silence."
          value={d.idleTimeoutSeconds}
          error={errors.idleTimeoutSeconds}
          onChange={(e) => set("idleTimeoutSeconds", e.target.value)}
        />
      </div>
      <fieldset className="cf-form__section">
        <legend className="az-eyebrow">Transcript</legend>
        <Switch
          label="Keep a transcript"
          hint="Opt-in. The transcript is kept with the call report only when this is on."
          labelPosition="end"
          checked={d.recordTranscript}
          onChange={(e) => set("recordTranscript", e.target.checked)}
        />
        <Input
          id="agent-transcript-retention"
          label="Transcript retention (days)"
          inputMode="numeric"
          value={d.transcriptRetentionDays}
          error={errors.transcriptRetentionDays}
          onChange={(e) => set("transcriptRetentionDays", e.target.value)}
        />
      </fieldset>
      <fieldset className="cf-form__section">
        <legend className="az-eyebrow">
          Caller verification before write tools
        </legend>
        {writeTools && d.callerVerification === "none" && (
          <Alert tone="warn" title="A write tool is ticked">
            Write tools need caller verification: the API refuses this save
            (voice_verification_required).
          </Alert>
        )}
        <Select
          id="agent-caller-verification"
          label="How a caller proves who they are"
          value={d.callerVerification}
          options={(
            Object.keys(CALLER_VERIFICATION_LABEL) as CallerVerification[]
          ).map((v) => ({ value: v, label: CALLER_VERIFICATION_LABEL[v] }))}
          onChange={(e) =>
            set("callerVerification", e.target.value as CallerVerification)
          }
        />
        {(d.callerVerification === "allowlist" ||
          d.callerVerification === "allowlist_or_pin") && (
          <>
            <label className="az-eyebrow" htmlFor="agent-allowlist">
              Allowlist, one number per line
            </label>
            <textarea
              id="agent-allowlist"
              className="az-input az-textarea az-input--mono"
              rows={4}
              value={allowlistText}
              onChange={(e) => setAllowlistText(e.target.value)}
            />
            {errors.callerAllowlist && (
              <p className="cf-form__error" role="alert">
                {errors.callerAllowlist}
              </p>
            )}
          </>
        )}
      </fieldset>
      <Can>
        <SaveBar
          busy={busy}
          formError={formError}
          unmatched={[]}
          onSave={() => void save()}
          label="Save limits"
        />
      </Can>
    </div>
  );
}

/** The numbers tab: the extension, the generated sip_user, where it is routed. */
function NumbersTab({
  detail,
  onSaved,
  showToast,
}: {
  detail: VoiceAgentDetail;
  onSaved: () => void;
  showToast: (m: string) => void;
}) {
  const [d, setD] = useState(() => draftOf(detail));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);

  async function save() {
    if (
      d.extension.trim() !== "" &&
      !/^[0-9]{2,10}$/.test(d.extension.trim())
    ) {
      setErrors({ extension: "Use 2 to 10 digits." });
      setFormError("Fix the highlighted fields.");
      return;
    }
    setErrors({});
    setBusy(true);
    setFormError(null);
    try {
      await updateVoiceAgent(detail.id, bodyOf(detail, d));
      showToast(`Numbers of ${detail.name} saved.`);
      onSaved();
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), Object.keys(d));
      setErrors(mapped.byKey);
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="cf-form">
      <div className="cf-two">
        <Input
          id="agent-extension"
          label="Extension"
          mono
          inputMode="numeric"
          hint="Dial it from any phone. Empty: reachable only through routes."
          value={d.extension}
          error={errors.extension}
          onChange={(e) =>
            setD((prev) => ({ ...prev, extension: e.target.value }))
          }
        />
        <Input
          id="agent-sip-user"
          label="SIP user"
          mono
          readOnly
          hint="Generated once; this is what Hello sends to talking-agent."
          value={detail.sipUser}
        />
      </div>
      <Can>
        <SaveBar
          busy={busy}
          formError={formError}
          unmatched={[]}
          onSave={() => void save()}
          label="Save numbers"
        />
      </Can>
    </div>
  );
}

/** The history tab: the last ten persona versions, with restore (S-4). */
function HistoryTab({
  agent,
  onRestored,
  showToast,
}: {
  agent: VoiceAgentDetail;
  onRestored: () => void;
  showToast: (m: string) => void;
}) {
  const [versions, setVersions] = useState<VoiceAgentVersion[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [restoring, setRestoring] = useState<VoiceAgentVersion | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listVoiceAgentVersions(agent.id, controller.signal)
      .then(setVersions)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [agent.id, agent.revision]);

  async function restore(v: VoiceAgentVersion) {
    try {
      await restoreVoiceAgentVersion(agent.id, v.revision);
    } catch (err: unknown) {
      throw new Error(errorMessage(err), { cause: err });
    }
    setRestoring(null);
    showToast(`Persona of ${agent.name} restored to revision ${v.revision}.`);
    onRestored();
  }

  return (
    <div>
      {error && (
        <Alert tone="bad" title="Could not load the versions">
          {error}
        </Alert>
      )}
      {versions === null && !error && <Spinner label="Loading versions…" />}
      {versions !== null && versions.length === 0 && (
        <EmptyState
          icon="history"
          title="No saved versions yet"
          description="Every persona save keeps a version here, ten deep."
        />
      )}
      {versions !== null && versions.length > 0 && (
        <ul className="pf-tokens" aria-label="Persona versions">
          {versions.map((v) => (
            <li key={v.revision}>
              <Icon name="history" size={15} />
              <div className="pf-tokens__meta">
                <div className="pf-tokens__name">Revision {v.revision}</div>
                <div className="pf-tokens__when">
                  {v.actor} · {day(v.createdAt)} {clock(v.createdAt)}
                </div>
              </div>
              <Can>
                <Button
                  variant="secondary"
                  size="sm"
                  aria-label={`Restore revision ${v.revision}`}
                  onClick={() => setRestoring(v)}
                >
                  Restore
                </Button>
              </Can>
            </li>
          ))}
        </ul>
      )}
      {restoring && (
        <ConfirmDialog
          title={`Restore revision ${restoring.revision}?`}
          description="The persona becomes this version again, as a new revision; history is not rewritten."
          confirmLabel="Restore"
          confirmIcon="history"
          errorTitle="Could not restore the version"
          onConfirm={() => restore(restoring)}
          onClose={() => setRestoring(null)}
        />
      )}
    </div>
  );
}

/** The calls tab: this agent's CDRs with their summaries (S-26). */
function CallsTab({ agent }: { agent: VoiceAgentDetail }) {
  const [calls, setCalls] = useState<VoiceAgentCall[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const controller = new AbortController();
    listVoiceAgentCalls(agent.id, controller.signal)
      .then(setCalls)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [agent.id]);
  const columns: TableColumn<VoiceAgentCall>[] = [
    {
      key: "startTime",
      label: "Started",
      mono: true,
      render: (c) => (
        <>
          {clock(c.startTime)}
          <small>{day(c.startTime)}</small>
        </>
      ),
    },
    { key: "outcome", label: "Outcome", render: (c) => c.outcome },
    {
      key: "summary",
      label: "Summary",
      render: (c) => c.summary || "—",
    },
    { key: "toolCalls", label: "Tools", align: "right", mono: true },
    {
      key: "status",
      label: "Status",
      render: (c) => (
        <Badge
          tone={c.finalStatus >= 400 ? "bad" : "good"}
          className="calls-mono"
        >
          {c.finalStatus}
        </Badge>
      ),
    },
  ];
  return (
    <div>
      {error && (
        <Alert tone="bad" title="Could not load the calls">
          {error}
        </Alert>
      )}
      {calls === null && !error && <Spinner label="Loading calls…" />}
      {calls !== null && calls.length === 0 && (
        <EmptyState
          icon="phone-off"
          title="No calls to this agent yet"
          description="Calls appear here with their summary once the runtime reports them."
        />
      )}
      {calls !== null && calls.length > 0 && (
        <Table
          caption={`Calls to ${agent.name}`}
          columns={columns}
          rows={calls}
          rowKey={(c) => c.correlationId}
        />
      )}
    </div>
  );
}

/** The test tab: extension, runtime state, and the next call as it lands (S-29). */
function TestTab({
  agent,
  status,
}: {
  agent: VoiceAgentDetail;
  status: VoiceStatus | undefined;
}) {
  const [calls, setCalls] = useState<VoiceAgentCall[]>([]);
  useEffect(() => {
    const controller = new AbortController();
    const tick = () => {
      listVoiceAgentCalls(agent.id, controller.signal)
        .then(setCalls)
        .catch(() => {
          // The list stays as it was; the next tick tries again.
        });
    };
    tick();
    const timer = setInterval(tick, 5000);
    return () => {
      clearInterval(timer);
      controller.abort();
    };
  }, [agent.id]);
  const latest = calls[0];
  return (
    <div className="cf-form">
      <Alert tone="info" title="How to test">
        Dial the agent's extension from any phone. Hello places no call for you;
        when the call ends, its outcome and summary appear below.
      </Alert>
      <Input
        id="agent-test-extension"
        label="Test extension"
        mono
        readOnly
        value={agent.extension ?? ""}
        hint={
          agent.extension
            ? "Dial this number."
            : "The agent has no extension; give it one on the Numbers tab."
        }
      />
      <div className="cf-field-group">
        <h3 className="az-eyebrow">Runtime</h3>
        {status === undefined ? (
          <p className="cf-form__note">Loading runtime state…</p>
        ) : status.lastSeenAt ? (
          <p className="cf-form__note">
            talking-agent {status.healthy ? "live" : "stale"}, running revision{" "}
            {status.revision}
            {status.version ? ` · version ${status.version}` : ""}.
          </p>
        ) : (
          <p className="cf-form__note">
            talking-agent has never reported; calls still route, but the status
            stays red.
          </p>
        )}
      </div>
      <fieldset className="cf-form__section">
        <legend className="az-eyebrow">Latest call</legend>
        {latest ? (
          <ul className="cf-plain" aria-label="Latest call">
            <li>
              {clock(latest.startTime)} · status {latest.finalStatus} · outcome{" "}
              <strong>{latest.outcome}</strong>
            </li>
            <li>{latest.summary || "No summary yet (unreported)."}</li>
          </ul>
        ) : (
          <p className="cf-form__note">
            Waiting for the next call to {agent.name}…
          </p>
        )}
      </fieldset>
    </div>
  );
}
