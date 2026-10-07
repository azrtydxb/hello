import { useCallback, useEffect, useState, type ReactNode } from "react";
import {
  createToken,
  deleteToken,
  errorMessage,
  listTokens,
  type ApiToken,
  type CreatedApiToken,
} from "../api";
import {
  createServiceAccount,
  createServiceSecret,
  deleteServiceAccount,
  fetchRole,
  getAISettings,
  grantableScopes,
  listGrants,
  listServiceAccounts,
  listSkills,
  revokeGrant,
  revokeServiceSecret,
  type AISettings,
  type CreatedServiceSecret,
  type Grant,
  type Role,
  type Scope,
  SCOPE_TEXT,
  type ServiceAccount,
  type Skill,
  skillDownloadPath,
} from "../api/ai";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  CodeBlock,
  ConfirmDialog,
  EmptyState,
  Icon,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  useToast,
} from "../design/azrty/components";
import { ago } from "./platform/health";
import "./platform/platform.css";
import "./ai.css";

type Load<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; data: T };

/** Loads once, and again on reload(); `load` must be stable. */
function useLoad<T>(load: (signal: AbortSignal) => Promise<T>) {
  const [state, setState] = useState<Load<T>>({ status: "loading" });
  const [generation, setGeneration] = useState(0);
  useEffect(() => {
    const controller = new AbortController();
    load(controller.signal)
      .then((data) => setState({ status: "ready", data }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [load, generation]);
  const reload = useCallback(() => setGeneration((g) => g + 1), []);
  return [state, reload] as const;
}

/** Shows a load's spinner or error, or renders its data. */
function Loaded<T>({
  state,
  what,
  children,
}: {
  state: Load<T>;
  what: string;
  children: (data: T) => ReactNode;
}) {
  if (state.status === "loading") return <Spinner label={`Loading ${what}…`} />;
  if (state.status === "error") {
    return (
      <Alert tone="bad" title={`Could not load ${what}`}>
        {state.message}
      </Alert>
    );
  }
  return <>{children(state.data)}</>;
}

/**
 * AI access (/ai): connecting an MCP client, what each scope allows, the
 * apps connected with the user's consent, and, for administrators, service
 * accounts and personal API tokens; plus the skills download.
 */
export function AIAccess() {
  const toast = useToast();
  const showToast = toast.show;
  const [role] = useLoad(fetchRole);
  const admin = role.status === "ready" && role.data === "admin";
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Platform"
        title="AI access"
        description="Connect AI agents over MCP, see what they may do, and revoke them."
      />
      <div className="pf-grid2 pf-grid2--wide">
        <div className="pf-stack" style={{ minWidth: 0 }}>
          <Connect />
          <ConnectedApps showToast={showToast} />
          {admin && <ServiceAccounts showToast={showToast} />}
        </div>
        <div className="pf-stack" style={{ minWidth: 0 }}>
          <Skills />
          {role.status === "ready" && admin && (
            <Tokens role={role.data} showToast={showToast} />
          )}
        </div>
      </div>
      {toast.node}
    </section>
  );
}

// --- connect -------------------------------------------------------------------

function Connect() {
  const [state] = useLoad(getAISettings);
  return (
    <div className="az-card pf-card pf-card--tight">
      <div>
        <h2 className="pf-h3" id="connect">
          Connect an MCP client
        </h2>
        <p className="pf-head__desc">
          Point the client at this URL; it opens a browser for you to approve
          the access it asks for.
        </p>
      </div>
      <Loaded state={state} what="the MCP settings">
        {(s) => <ConnectBody settings={s} />}
      </Loaded>
    </div>
  );
}

function ConnectBody({ settings }: { settings: AISettings }) {
  return (
    <>
      <CodeBlock title="MCP server URL" code={settings.mcpUrl} />
      <CodeBlock
        title="Claude Code"
        code={`claude mcp add --transport http hello ${settings.mcpUrl}`}
      />
      <p className="pf-muted">
        Streamable HTTP, protocol {settings.protocolVersions.join(" or ")}.
        Clients identify themselves with a client ID metadata document
        {settings.dcr ? " or register dynamically" : ""}.
      </p>
      <h3 className="ai-h" id="scopes">
        What each scope allows
      </h3>
      <ul className="pf-tokens" aria-labelledby="scopes">
        {settings.scopes.map((s) => (
          <li key={s.name}>
            <div className="pf-tokens__meta">
              <div className="pf-tokens__name">{s.name}</div>
              <div className="pf-tokens__when">
                {s.description || SCOPE_TEXT[s.name]}
              </div>
            </div>
          </li>
        ))}
      </ul>
    </>
  );
}

function ScopeBadges({ scopes }: { scopes?: readonly string[] | null }) {
  if (!scopes) return <Badge tone="outline">every scope</Badge>;
  return (
    <span className="ai-row">
      {scopes.map((s) => (
        <Badge key={s} tone={s === "secrets" ? "warn" : "outline"}>
          {s}
        </Badge>
      ))}
    </span>
  );
}

// --- connected apps ----------------------------------------------------------------

function ConnectedApps({
  showToast,
}: {
  showToast: (message: string) => void;
}) {
  const [state, reload] = useLoad(listGrants);
  const [revoking, setRevoking] = useState<Grant | null>(null);
  const now = new Date();
  return (
    <div className="az-card pf-card pf-card--tight">
      <div>
        <h2 className="pf-h3" id="apps">
          Connected apps
        </h2>
        <p className="pf-head__desc">
          Apps you approved. Revoking one stops its tokens at once.
        </p>
      </div>
      <Loaded state={state} what="connected apps">
        {(grants) =>
          grants.length === 0 ? (
            <EmptyState
              icon="bot"
              title="No connected apps"
              description="Connect an MCP client to see it here."
            />
          ) : (
            <ul className="pf-tokens" aria-labelledby="apps">
              {grants.map((g) => (
                <li key={String(g.id)}>
                  <Icon name="bot" size={15} />
                  <div className="pf-tokens__meta">
                    <div className="pf-tokens__name">
                      {g.clientName || g.clientId}
                    </div>
                    <div className="pf-tokens__when">
                      {g.username ? `${g.username} · ` : ""}approved{" "}
                      {new Date(g.createdAt).toLocaleDateString()} · last used{" "}
                      {g.lastUsedAt ? ago(g.lastUsedAt, now) : "never"}
                    </div>
                    <ScopeBadges scopes={g.scopes} />
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="pf-revoke"
                    aria-label={`Revoke ${g.clientName || g.clientId}`}
                    onClick={() => setRevoking(g)}
                  >
                    Revoke
                  </Button>
                </li>
              ))}
            </ul>
          )
        }
      </Loaded>
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.clientName || revoking.clientId}?`}
          description="Its access and refresh tokens stop working now; it must ask again."
          confirmLabel="Revoke access"
          confirmIcon="ban"
          errorTitle="Could not revoke the app"
          onConfirm={async () => {
            await revokeGrant(revoking.id);
            showToast(`${revoking.clientName || revoking.clientId} revoked.`);
            setRevoking(null);
            reload();
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </div>
  );
}

// --- scope picker ----------------------------------------------------------------

function ScopePicker({
  id,
  allowed,
  value,
  onChange,
  error,
}: {
  id: string;
  allowed: readonly Scope[];
  value: ReadonlySet<Scope>;
  onChange: (next: Set<Scope>) => void;
  error?: string;
}) {
  return (
    <fieldset
      className="ai-scopes"
      aria-describedby={error ? `${id}-error` : undefined}
    >
      <legend className="ai-h">Scopes</legend>
      {allowed.map((s) => (
        <Checkbox
          key={s}
          label={s}
          hint={SCOPE_TEXT[s]}
          checked={value.has(s)}
          onChange={(e) => {
            const next = new Set(value);
            if (e.target.checked) next.add(s);
            else next.delete(s);
            onChange(next);
          }}
        />
      ))}
      {error && (
        <p className="pf-muted pf-tone--bad" id={`${id}-error`} role="alert">
          {error}
        </p>
      )}
    </fieldset>
  );
}

// --- secrets shown once --------------------------------------------------------------

/** A just-issued secret, shown once; closing the dialog drops it. */
function ShownOnce({
  title,
  label,
  value,
  onClose,
}: {
  title: string;
  label: string;
  value: string;
  onClose: () => void;
}) {
  const [copied, setCopied] = useState<"idle" | "copied" | "unavailable">(
    "idle",
  );
  async function copy() {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("no clipboard");
      await navigator.clipboard.writeText(value);
      setCopied("copied");
    } catch {
      setCopied("unavailable");
    }
  }
  return (
    <Modal
      title={title}
      description={label}
      onClose={onClose}
      actions={<Button onClick={onClose}>Done</Button>}
    >
      <Alert tone="warn" title="Shown once">
        Copy it now. It is stored hashed and cannot be shown again; revoke it
        and create another if it is lost.
      </Alert>
      <div className="pf-secret">
        <Input
          label={label}
          id="shown-once"
          mono
          readOnly
          autoFocus
          value={value}
          onFocus={(e) => e.currentTarget.select()}
          hint={
            copied === "copied"
              ? "Copied."
              : copied === "unavailable"
                ? "Copying is not available here; select it and copy it."
                : undefined
          }
        />
        <Button variant="secondary" icon="copy" onClick={() => void copy()}>
          Copy
        </Button>
      </div>
    </Modal>
  );
}

// --- service accounts ----------------------------------------------------------------

function ServiceAccounts({
  showToast,
}: {
  showToast: (message: string) => void;
}) {
  const [state, reload] = useLoad(listServiceAccounts);
  const [creating, setCreating] = useState(false);
  const [issued, setIssued] = useState<{
    account: ServiceAccount;
    secret: CreatedServiceSecret;
  } | null>(null);
  const [deleting, setDeleting] = useState<ServiceAccount | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function newSecret(account: ServiceAccount) {
    setError(null);
    try {
      setIssued({ account, secret: await createServiceSecret(account.id) });
      reload();
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  async function revokeSecret(account: ServiceAccount, secretId: string) {
    setError(null);
    try {
      await revokeServiceSecret(account.id, secretId);
      showToast(`Secret of ${account.name} revoked.`);
      reload();
    } catch (err) {
      setError(errorMessage(err));
    }
  }

  return (
    <div className="az-card pf-card pf-card--tight">
      <div className="pf-cardhead">
        <div>
          <h2 className="pf-h3" id="service-accounts">
            Service accounts
          </h2>
          <p className="pf-head__desc">
            For unattended agents (client credentials). Secrets are shown once.
          </p>
        </div>
        <Button
          variant="secondary"
          size="sm"
          icon="plus"
          onClick={() => setCreating(true)}
        >
          New service account
        </Button>
      </div>
      {error && (
        <Alert tone="bad" title="Could not change the service account">
          {error}
        </Alert>
      )}
      <Loaded state={state} what="service accounts">
        {(accounts) =>
          accounts.length === 0 ? (
            <EmptyState
              icon="bot"
              title="No service accounts"
              description="Create one for an agent that runs without a person."
            />
          ) : (
            <ul className="pf-tokens" aria-labelledby="service-accounts">
              {accounts.map((a) => {
                const live = (a.secrets ?? []).filter((s) => !s.revokedAt);
                return (
                  <li key={String(a.id)}>
                    <Icon name="bot" size={15} />
                    <div className="pf-tokens__meta">
                      <div className="pf-tokens__name">
                        {a.name}
                        {!a.enabled && (
                          <>
                            {" "}
                            <Badge tone="outline">disabled</Badge>
                          </>
                        )}
                      </div>
                      <div className="pf-tokens__when">
                        {a.role} · client id{" "}
                        <span className="pf-mono">{a.clientId}</span>
                      </div>
                      <ScopeBadges scopes={a.scopes} />
                      {live.map((s) => (
                        <div key={String(s.id)} className="pf-tokens__when">
                          Secret {s.prefix ? `${s.prefix}… ` : ""}created{" "}
                          {new Date(s.createdAt).toLocaleDateString()}
                          {s.expiresAt
                            ? ` · expires ${new Date(s.expiresAt).toLocaleDateString()}`
                            : ""}{" "}
                          <Button
                            variant="ghost"
                            size="sm"
                            className="pf-revoke"
                            aria-label={`Revoke secret ${s.prefix ?? s.id} of ${a.name}`}
                            onClick={() => void revokeSecret(a, String(s.id))}
                          >
                            Revoke
                          </Button>
                        </div>
                      ))}
                    </div>
                    <Button
                      variant="secondary"
                      size="sm"
                      disabled={live.length >= 2}
                      title={
                        live.length >= 2
                          ? "Revoke a secret first: at most two at a time."
                          : undefined
                      }
                      aria-label={`New secret for ${a.name}`}
                      onClick={() => void newSecret(a)}
                    >
                      New secret
                    </Button>
                    <Button
                      variant="ghost"
                      size="sm"
                      className="pf-revoke"
                      aria-label={`Delete ${a.name}`}
                      onClick={() => setDeleting(a)}
                    >
                      Delete
                    </Button>
                  </li>
                );
              })}
            </ul>
          )
        }
      </Loaded>
      {creating && (
        <NewServiceAccountModal
          onClose={() => setCreating(false)}
          onCreated={(a) => {
            setCreating(false);
            showToast(`Service account ${a.name} created.`);
            reload();
          }}
        />
      )}
      {issued && (
        <ShownOnce
          title="Secret created"
          label={`Client secret · ${issued.account.name}`}
          value={issued.secret.secret}
          onClose={() => setIssued(null)}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.name}?`}
          description="Its secrets and tokens stop working now."
          confirmLabel="Delete service account"
          confirmIcon="trash-2"
          errorTitle="Could not delete the service account"
          onConfirm={async () => {
            await deleteServiceAccount(deleting.id);
            showToast(`Service account ${deleting.name} deleted.`);
            setDeleting(null);
            reload();
          }}
          onClose={() => setDeleting(null)}
        />
      )}
    </div>
  );
}

const ROLES: readonly Role[] = ["viewer", "operator", "admin"];

function NewServiceAccountModal({
  onClose,
  onCreated,
}: {
  onClose: () => void;
  onCreated: (a: ServiceAccount) => void;
}) {
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [role, setRole] = useState<Role>("viewer");
  const [scopes, setScopes] = useState<Set<Scope>>(new Set(["read"]));
  const [errors, setErrors] = useState<{ name?: string; scopes?: string }>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const allowed = grantableScopes(role);

  async function submit() {
    const chosen = [...scopes].filter((s) => allowed.includes(s));
    const found = {
      name: name.trim() === "" ? "Enter a name." : undefined,
      scopes: chosen.length === 0 ? "Choose at least one scope." : undefined,
    };
    setErrors(found);
    if (found.name || found.scopes) return;
    setBusy(true);
    setFormError(null);
    try {
      onCreated(
        await createServiceAccount({
          name: name.trim(),
          description: description.trim() || undefined,
          role,
          scopes: chosen,
        }),
      );
    } catch (err) {
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New service account"
      description="An identity for an agent that runs without a person. Create its secret afterwards."
      onClose={onClose}
      actions={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="new-sa" disabled={busy}>
            Create service account
          </Button>
        </>
      }
    >
      <form
        id="new-sa"
        className="pf-stack"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {formError && (
          <Alert tone="bad" title="Could not create the service account">
            {formError}
          </Alert>
        )}
        <Input
          label="Name"
          id="new-sa-name"
          mono
          autoFocus
          placeholder="nightly-report"
          value={name}
          error={errors.name}
          onChange={(e) => setName(e.target.value)}
        />
        <Input
          label="Description"
          id="new-sa-description"
          value={description}
          onChange={(e) => setDescription(e.target.value)}
        />
        <Select
          label="Role"
          id="new-sa-role"
          options={ROLES}
          value={role}
          hint="Bounds the scopes it may hold."
          onChange={(e) => setRole(e.target.value as Role)}
        />
        <ScopePicker
          id="new-sa-scopes"
          allowed={allowed}
          value={scopes}
          onChange={setScopes}
          error={errors.scopes}
        />
      </form>
    </Modal>
  );
}

// --- personal API tokens ---------------------------------------------------------------

const EXPIRY: readonly { value: string; label: string; days: number }[] = [
  { value: "30", label: "30 days", days: 30 },
  { value: "90", label: "90 days", days: 90 },
  { value: "365", label: "1 year", days: 365 },
  { value: "never", label: "Never", days: 0 },
];

function Tokens({
  role,
  showToast,
}: {
  role: Role;
  showToast: (message: string) => void;
}) {
  const [state, reload] = useLoad(listTokens);
  const [creating, setCreating] = useState(false);
  const [created, setCreated] = useState<CreatedApiToken | null>(null);
  const [revoking, setRevoking] = useState<ApiToken | null>(null);
  const now = new Date();
  return (
    <div className="az-card pf-card pf-card--tight">
      <div className="pf-cardhead">
        <div>
          <h2 className="pf-h3" id="tokens">
            API tokens
          </h2>
          <p className="pf-head__desc">
            Personal bearer tokens with scopes and an expiry. Shown once at
            creation.
          </p>
        </div>
        <Button
          variant="secondary"
          size="sm"
          icon="plus"
          onClick={() => setCreating(true)}
        >
          New token
        </Button>
      </div>
      <Loaded state={state} what="API tokens">
        {(tokens) =>
          tokens.length === 0 ? (
            <EmptyState
              icon="key-round"
              title="No API tokens"
              description="Create one for scripts and integrations."
            />
          ) : (
            <ul className="pf-tokens" aria-labelledby="tokens">
              {tokens.map((t) => (
                <li key={String(t.id)}>
                  <Icon name="key-round" size={15} />
                  <div className="pf-tokens__meta">
                    <div className="pf-tokens__name">{t.name}</div>
                    <div className="pf-tokens__when">
                      Created {new Date(t.createdAt).toLocaleDateString()} ·
                      last used{" "}
                      {t.lastUsedAt ? ago(t.lastUsedAt, now) : "never"} ·{" "}
                      {t.expiresAt
                        ? `expires ${new Date(t.expiresAt).toLocaleDateString()}`
                        : "no expiry"}
                    </div>
                    <ScopeBadges scopes={t.scopes} />
                  </div>
                  <Button
                    variant="ghost"
                    size="sm"
                    className="pf-revoke"
                    aria-label={`Revoke ${t.name}`}
                    onClick={() => setRevoking(t)}
                  >
                    Revoke
                  </Button>
                </li>
              ))}
            </ul>
          )
        }
      </Loaded>
      {creating && (
        <NewTokenModal
          role={role}
          onClose={() => setCreating(false)}
          onCreated={(t) => {
            setCreating(false);
            setCreated(t);
            reload();
          }}
        />
      )}
      {created && (
        <ShownOnce
          title="Token created"
          label={`API token · ${created.name}`}
          value={created.token}
          onClose={() => setCreated(null)}
        />
      )}
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.name}?`}
          description="Requests with this token are refused from now on."
          confirmLabel="Revoke token"
          confirmIcon="key-round"
          errorTitle="Could not revoke the token"
          onConfirm={async () => {
            await deleteToken(revoking.id);
            showToast(`Token ${revoking.name} revoked.`);
            setRevoking(null);
            reload();
          }}
          onClose={() => setRevoking(null)}
        />
      )}
    </div>
  );
}

function NewTokenModal({
  role,
  onClose,
  onCreated,
}: {
  role: Role;
  onClose: () => void;
  onCreated: (t: CreatedApiToken) => void;
}) {
  const allowed = grantableScopes(role);
  const [name, setName] = useState("");
  const [scopes, setScopes] = useState<Set<Scope>>(new Set(["read"]));
  const [expiry, setExpiry] = useState("90");
  const [errors, setErrors] = useState<{ name?: string; scopes?: string }>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function submit() {
    const found = {
      name: name.trim() === "" ? "Enter a name." : undefined,
      scopes: scopes.size === 0 ? "Choose at least one scope." : undefined,
    };
    setErrors(found);
    if (found.name || found.scopes) return;
    const days = EXPIRY.find((e) => e.value === expiry)?.days ?? 0;
    setBusy(true);
    setFormError(null);
    try {
      onCreated(
        await createToken({
          name: name.trim(),
          scopes: allowed.filter((s) => scopes.has(s)),
          expiresAt:
            days > 0
              ? new Date(Date.now() + days * 86_400_000).toISOString()
              : undefined,
        }),
      );
    } catch (err) {
      setFormError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New API token"
      description="Send it as a Bearer token. It can do only what its scopes allow."
      onClose={onClose}
      actions={
        <>
          <Button variant="secondary" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="new-token" disabled={busy}>
            Create token
          </Button>
        </>
      }
    >
      <form
        id="new-token"
        className="pf-stack"
        noValidate
        onSubmit={(e) => {
          e.preventDefault();
          void submit();
        }}
      >
        {formError && (
          <Alert tone="bad" title="Could not create the token">
            {formError}
          </Alert>
        )}
        <Input
          label="Name"
          id="new-token-name"
          mono
          autoFocus
          placeholder="grafana-read"
          hint="What uses it, so you know what to revoke."
          value={name}
          error={errors.name}
          onChange={(e) => setName(e.target.value)}
        />
        <Select
          label="Expires"
          id="new-token-expiry"
          options={EXPIRY.map((e) => ({ value: e.value, label: e.label }))}
          value={expiry}
          onChange={(e) => setExpiry(e.target.value)}
        />
        <ScopePicker
          id="new-token-scopes"
          allowed={allowed}
          value={scopes}
          onChange={setScopes}
          error={errors.scopes}
        />
      </form>
    </Modal>
  );
}

// --- skills ------------------------------------------------------------------------------

function Skills() {
  const [state] = useLoad(listSkills);
  return (
    <div className="az-card pf-card pf-card--tight">
      <div>
        <h2 className="pf-h3" id="skills">
          Agent skills
        </h2>
        <p className="pf-head__desc">
          Teach an agent Hello's workflows. Unpack the zip into the client's
          skills folder: <span className="pf-mono">~/.claude/skills/</span> for
          Claude Code, or upload it under Settings, Capabilities in Claude
          Desktop.
        </p>
      </div>
      <Loaded state={state} what="skills">
        {(skills: Skill[]) => (
          <ul className="pf-tokens" aria-labelledby="skills">
            {skills.map((s) => (
              <li key={s.name}>
                <Icon name="book-open" size={15} />
                <div className="pf-tokens__meta">
                  <div className="pf-tokens__name">{s.name}</div>
                  <div className="pf-tokens__when">{s.description}</div>
                </div>
                <a
                  className="az-btn az-btn--secondary az-btn--sm"
                  href={skillDownloadPath(s.name)}
                  download={`${s.name}.zip`}
                  aria-label={`Download ${s.name}`}
                >
                  <Icon name="download" size={14} />
                  Download
                </a>
              </li>
            ))}
          </ul>
        )}
      </Loaded>
    </div>
  );
}
