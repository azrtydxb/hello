import { useCallback, useEffect, useState } from "react";
import { errorMessage, fieldErrors, type FieldError } from "../api";
import { mapFieldErrors, type ErrorMap } from "../forms";
import {
  deleteVoiceMCPServer,
  discoverVoiceMCPServer,
  listVoiceMCPServers,
  testVoiceMCPServer,
  createVoiceMCPServer,
  updateVoiceMCPServer,
  VOICE_MCP_AUTHS,
  VOICE_MCP_AUTH_LABEL,
  type VoiceMCPAuth,
  type VoiceMCPServer,
  type VoiceMCPServerInput,
  type VoiceMCPCheck,
  type VoiceMCPTool,
} from "../api/voice";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Icon,
  Input,
  PageHeader,
  Select,
  Spinner,
  Switch,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { ago } from "./platform/health";
import { Can } from "../role";

type ListState<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: T[] };

/** The last check status badge (spec S-9): ok, refused or unreachable. */
function checkBadge(s: VoiceMCPServer) {
  if (!s.lastCheckStatus) return <Badge tone="outline">Never checked</Badge>;
  const tone =
    s.lastCheckStatus === "ok"
      ? "good"
      : s.lastCheckStatus === "unreachable"
        ? "bad"
        : "warn";
  return (
    <Badge tone={tone}>
      {s.lastCheckStatus}
      {s.lastCheckAt ? ` · ${ago(s.lastCheckAt, new Date())}` : ""}
    </Badge>
  );
}

/**
 * Voice MCP servers (/voice/mcp-servers): the registry agents attach to
 * (spec S-5). Credential fields are write-only: an operator sees the form
 * without them, a saved value is never rendered back.
 */
export function VoiceMCPServers() {
  const [state, setState] = useState<ListState<VoiceMCPServer>>({
    status: "loading",
  });
  const [editing, setEditing] = useState<
    { kind: "new" } | { kind: "edit"; server: VoiceMCPServer } | null
  >(null);
  const [deleting, setDeleting] = useState<VoiceMCPServer | null>(null);
  const toast = useToast();

  const reload = useCallback(() => {
    const controller = new AbortController();
    listVoiceMCPServers(controller.signal)
      .then((items) => setState({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);
  useEffect(() => reload(), [reload]);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="AI"
        title="Voice MCP servers"
        description="Tool servers the voice agents may call. Credentials are sealed at rest and never shown again; only administrators manage servers, operators attach them to agents."
        actions={
          <Can min="admin">
            <Button icon="plus" onClick={() => setEditing({ kind: "new" })}>
              New MCP server
            </Button>
          </Can>
        }
      />
      <div className="cf-stack">
        {state.status === "loading" && <Spinner label="Loading MCP servers…" />}
        {state.status === "error" && (
          <Alert tone="bad" title="Could not load the MCP servers">
            {state.message}
          </Alert>
        )}
        {state.status === "ready" && state.items.length === 0 && (
          <EmptyState
            icon="wrench"
            title="No MCP servers yet"
            description="Add the tool server the agents should call; private addresses only, unless an administrator allows public ones."
          />
        )}
        {state.status === "ready" && state.items.length > 0 && (
          <ul className="pf-tokens" aria-label="Voice MCP servers">
            {state.items.map((s) => (
              <li key={String(s.id)}>
                <Icon name="wrench" size={15} />
                <div className="pf-tokens__meta">
                  <div className="pf-tokens__name">
                    {s.name}
                    {!s.enabled && <Badge tone="outline"> disabled</Badge>}
                    {s.credentialSet && (
                      <Badge tone="outline"> credential set</Badge>
                    )}
                  </div>
                  <div className="pf-tokens__when">
                    <span className="pf-mono">{s.url}</span> ·{" "}
                    {VOICE_MCP_AUTH_LABEL[s.auth]}
                  </div>
                </div>
                {checkBadge(s)}
                <Can>
                  <CheckTools server={s} showToast={toast.show} />
                </Can>
                <Can min="admin">
                  <Button
                    variant="secondary"
                    size="sm"
                    icon="pencil"
                    aria-label={`Edit MCP server ${s.name}`}
                    onClick={() => setEditing({ kind: "edit", server: s })}
                  >
                    Edit
                  </Button>
                  <Button
                    variant="ghost"
                    size="sm"
                    icon="trash-2"
                    className="cf-danger"
                    aria-label={`Delete MCP server ${s.name}`}
                    onClick={() => setDeleting(s)}
                  >
                    Delete
                  </Button>
                </Can>
              </li>
            ))}
          </ul>
        )}
      </div>
      {editing && (
        <ServerDrawer
          key={editing.kind === "edit" ? String(editing.server.id) : "new"}
          server={editing.kind === "edit" ? editing.server : null}
          onClose={() => setEditing(null)}
          onSaved={() => {
            setEditing(null);
            reload();
          }}
          showToast={toast.show}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete MCP server ${deleting.name}?`}
          description="Every agent attached to it loses the server's tools."
          confirmLabel="Delete server"
          confirmIcon="trash-2"
          errorTitle="Could not delete the MCP server"
          onConfirm={async () => {
            await deleteVoiceMCPServer(deleting.id);
            toast.show(`MCP server ${deleting.name} deleted.`);
            setDeleting(null);
            reload();
          }}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

/** Test connection and Discover tools, with plain-text output (S-7, S-9). */
function CheckTools({
  server,
  showToast,
}: {
  server: VoiceMCPServer;
  showToast: (m: string) => void;
}) {
  const [busy, setBusy] = useState<"test" | "discover" | null>(null);
  const [result, setResult] = useState<VoiceMCPCheck | null>(null);
  const [discovery, setDiscovery] = useState<VoiceMCPTool[] | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function run(kind: "test" | "discover") {
    setBusy(kind);
    setError(null);
    try {
      if (kind === "test") {
        setResult(await testVoiceMCPServer(server.id));
      } else {
        setDiscovery((await discoverVoiceMCPServer(server.id)).tools);
        showToast(`Discovered the tools of ${server.name}.`);
      }
    } catch (err: unknown) {
      setError(errorMessage(err));
    } finally {
      setBusy(null);
    }
  }

  return (
    <div className="cf-stack" style={{ gap: 4, minWidth: 0 }}>
      <div className="cf-row__actions">
        <Button
          variant="secondary"
          size="sm"
          disabled={busy !== null}
          onClick={() => void run("test")}
        >
          {busy === "test" ? "Testing…" : "Test connection"}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={busy !== null}
          onClick={() => void run("discover")}
        >
          {busy === "discover" ? "Discovering…" : "Discover tools"}
        </Button>
      </div>
      {error && (
        <p className="cf-form__error" role="alert">
          {error}
        </p>
      )}
      {result && (
        <p className="cf-form__note">
          {result.ok ? "Ok" : "Failed"} · {result.latencyMs} ms ·{" "}
          <span className="pf-mono">{result.result}</span>
        </p>
      )}
      {discovery !== null && (
        // Plain text, never HTML: tool names and descriptions are data.
        <pre
          className="pf-mono"
          aria-label={`Discovered tools of ${server.name}`}
        >
          {discovery.length === 0
            ? "The server offers no tools."
            : discovery
                .map((t) => `${t.name}: ${t.description ?? ""}`)
                .join("\n")}
        </pre>
      )}
    </div>
  );
}

/** The create/edit drawer; credential fields are write-only (S-5). */
function ServerDrawer({
  server,
  onClose,
  onSaved,
  showToast,
}: {
  server: VoiceMCPServer | null;
  onClose: () => void;
  onSaved: () => void;
  showToast: (m: string) => void;
}) {
  useRestoreFocus();
  const [name, setName] = useState(server?.name ?? "");
  const [url, setUrl] = useState(server?.url ?? "");
  const [auth, setAuth] = useState<VoiceMCPAuth>(server?.auth ?? "none");
  const [headerName, setHeaderName] = useState(server?.headerName ?? "");
  const [credential, setCredential] = useState("");
  const [tokenUrl, setTokenUrl] = useState(server?.tokenUrl ?? "");
  const [clientId, setClientId] = useState(server?.clientId ?? "");
  const [clientSecret, setClientSecret] = useState("");
  const [scope, setScope] = useState(server?.scope ?? "");
  const [timeoutMs, setTimeoutMs] = useState(
    String(server?.timeoutMs ?? 10000),
  );
  const [enabled, setEnabled] = useState(server?.enabled ?? true);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    const found: Record<string, string> = {};
    if (name.trim() === "") found.name = "Enter a name.";
    if (!/^https?:\/\//.test(url.trim())) {
      found.url = "Use the server's http(s) URL.";
    }
    if (auth === "header" && headerName.trim() === "") {
      found.headerName = "Name the header that carries the credential.";
    }
    if (auth === "oauth_client_credentials" && tokenUrl.trim() === "") {
      found.tokenUrl = "Enter the OAuth token endpoint.";
    }
    if (auth === "oauth_client_credentials" && clientId.trim() === "") {
      found.clientId = "Enter the OAuth client id.";
    }
    if (!/^[0-9]+$/.test(timeoutMs) || Number(timeoutMs) < 1000) {
      found.timeoutMs = "Use milliseconds, 1000 or more.";
    }
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    const input: VoiceMCPServerInput = {
      name: name.trim(),
      url: url.trim(),
      auth,
      headerName: auth === "header" ? headerName.trim() : undefined,
      // Write-only: empty keeps what is stored; a value is sealed on save.
      credential: credential === "" ? undefined : credential,
      tokenUrl:
        auth === "oauth_client_credentials" ? tokenUrl.trim() : undefined,
      clientId:
        auth === "oauth_client_credentials" ? clientId.trim() : undefined,
      clientSecret:
        auth === "oauth_client_credentials" && clientSecret !== ""
          ? clientSecret
          : undefined,
      scope: auth === "oauth_client_credentials" ? scope.trim() : undefined,
      timeoutMs: Number(timeoutMs),
      enabled,
    };
    setBusy(true);
    setFormError(null);
    setUnmatched([]);
    try {
      if (server) {
        await updateVoiceMCPServer(server.id, input);
        showToast(`MCP server ${input.name} saved.`);
      } else {
        await createVoiceMCPServer(input);
        showToast(`MCP server ${input.name} created.`);
      }
      onSaved();
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), Object.keys(input));
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={server ? `MCP server ${server.name}` : "New MCP server"}
      description="Streamable HTTP, private addresses only. The credential is sealed on save and never shown again."
      onClose={busy ? undefined : onClose}
      width={560}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button onClick={() => void submit()} disabled={busy}>
            {server ? "Save server" : "Create server"}
          </Button>
        </>
      }
    >
      <div className="cf-form">
        {formError && (
          <Alert tone="bad" title="Could not save the MCP server">
            {formError}
          </Alert>
        )}
        {unmatched.length > 0 && (
          <Alert
            tone="warn"
            title="Some problems could not be shown on a field"
          >
            <ul className="cf-plain">
              {unmatched.map((f, i) => (
                <li key={i}>{f.message}</li>
              ))}
            </ul>
          </Alert>
        )}
        <Input
          id="mcp-name"
          label="Name"
          mono
          autoFocus
          value={name}
          error={errors.name}
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          id="mcp-url"
          label="URL"
          mono
          placeholder="https://tools.internal/mcp"
          value={url}
          error={errors.url}
          hint="Hello dials only private addresses unless public MCP is allowed."
          onChange={(e) => setUrl(e.target.value)}
        />
        <Select
          id="mcp-auth"
          label="Authentication"
          value={auth}
          options={VOICE_MCP_AUTHS.map((a) => ({
            value: a,
            label: VOICE_MCP_AUTH_LABEL[a],
          }))}
          onChange={(e) => setAuth(e.target.value as VoiceMCPAuth)}
        />
        {auth === "header" && (
          <Input
            id="mcp-header-name"
            label="Header name"
            mono
            value={headerName}
            error={errors.headerName}
            onChange={(e) => setHeaderName(e.target.value)}
          />
        )}
        {auth !== "none" && auth !== "oauth_client_credentials" && (
          <Input
            id="mcp-credential"
            label="Credential"
            mono
            type="password"
            autoComplete="new-password"
            value={credential}
            error={errors.credential}
            hint={
              server?.credentialSet
                ? "Stored; leave empty to keep it."
                : "Sealed on save; never shown again."
            }
            onChange={(e) => setCredential(e.target.value)}
          />
        )}
        {auth === "oauth_client_credentials" && (
          <>
            <Input
              id="mcp-token-url"
              label="Token URL"
              mono
              value={tokenUrl}
              error={errors.tokenUrl}
              onChange={(e) => setTokenUrl(e.target.value)}
            />
            <div className="cf-two">
              <Input
                id="mcp-client-id"
                label="Client id"
                mono
                value={clientId}
                error={errors.clientId}
                onChange={(e) => setClientId(e.target.value)}
              />
              <Input
                id="mcp-client-secret"
                label="Client secret"
                mono
                type="password"
                autoComplete="new-password"
                value={clientSecret}
                error={errors.clientSecret}
                hint={
                  server?.credentialSet
                    ? "Stored; leave empty to keep it."
                    : "Sealed on save; never shown again."
                }
                onChange={(e) => setClientSecret(e.target.value)}
              />
            </div>
            <Input
              id="mcp-scope"
              label="Scope"
              mono
              value={scope}
              onChange={(e) => setScope(e.target.value)}
            />
          </>
        )}
        <div className="cf-two">
          <Input
            id="mcp-timeout"
            label="Timeout (ms)"
            inputMode="numeric"
            value={timeoutMs}
            error={errors.timeoutMs}
            onChange={(e) => setTimeoutMs(e.target.value)}
          />
          <Switch
            label="Enabled"
            labelPosition="end"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
        </div>
      </div>
    </Drawer>
  );
}
