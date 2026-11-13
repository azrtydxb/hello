import { useCallback, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import { errorMessage } from "../api";
import {
  connectCRMConnector,
  createCRMConnector,
  crmAppUrl,
  CRM_LOG_STATUSES,
  deleteCRMConnector,
  disconnectCRMConnector,
  getCRMSetup,
  listCRMConnectors,
  listCRMLogs,
  listCRMUsers,
  retryCRMLog,
  setCRMUser,
  testCRMConnector,
  updateCRMConnector,
  type CRMConnector,
  type CRMLog,
  type CRMLogStatus,
  type CRMSetupItem,
  type CRMUserMapping,
} from "../api/crm";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  ConfirmDialog,
  EmptyState,
  Input,
  PageHeader,
  PropertyList,
  Select,
  Spinner,
  Switch,
  Table,
  Tabs,
  useToast,
  type BadgeTone,
} from "../design/azrty/components";
import { formatTime } from "../format";
import {
  buildConfig,
  initialValues,
  KINDS,
  kindSpec,
  type KindField,
} from "./crmKinds";
import { usePolling } from "../usePolling";
import "./platform/platform.css";

const CONNECTION_TONE: Record<string, BadgeTone> = {
  connected: "good",
  needs_reauth: "bad",
  revoked: "warn",
};

const LOG_TONE: Record<CRMLogStatus, BadgeTone> = {
  waiting: "outline",
  pending: "info",
  logged: "good",
  update: "info",
  dead: "bad",
  skipped: "warn",
};

const ERRORS: Record<string, string> = {
  denied: "The sign-in was declined in the CRM.",
  state: "The sign-in expired or was already used. Start it again.",
  failed: "The CRM did not accept the sign-in. Check the client id and secret.",
};

/** The values of the HubSpot calling extension (the iframe URL is Hello's). */
function callingSettings(redirectUri?: string): string {
  let origin = "";
  try {
    if (redirectUri) origin = new URL(redirectUri).origin;
  } catch {
    // Not a URL: the page says the public URL is missing.
  }
  return JSON.stringify(
    {
      name: "Hello",
      url: origin
        ? `${origin}/integrations/hubspot/iframe`
        : "Set HELLO_PUBLIC_URL first",
      width: 320,
      height: 240,
      isReady: true,
      supportsCustomObjects: false,
    },
    null,
    2,
  );
}

function statusLabel(c: CRMConnector): { text: string; tone: BadgeTone } {
  if (!c.connection) return { text: "Not connected", tone: "warn" };
  if (c.connection.status === "needs_reauth")
    return { text: "Sign in again", tone: "bad" };
  return {
    text: c.connection.status === "connected" ? "Connected" : "Revoked",
    tone: CONNECTION_TONE[c.connection.status] ?? "neutral",
  };
}

/**
 * CRM connectors (/integrations/crm): the customer's own CRM app (client id
 * and secret), the sign-in that connects it, the mapping of Hello users to CRM
 * users and the log of calls written to the CRM, with its dead letters.
 */
export function IntegrationsCRM() {
  const connectors = usePolling(listCRMConnectors, 15_000);
  const [params] = useSearchParams();
  const toast = useToast();
  const error = params.get("error");
  const connected = params.get("connected");

  const items =
    connectors.status === "loading" ? null : (connectors.data ?? []);
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Integrations"
        title="CRM"
        description="Write every external call into your CRM, helpdesk or practice system, show the caller's name on incoming calls, and attach recordings if you want to. You create the app or API credentials in your own account; Hello keeps the secrets and tokens sealed."
      />
      <div className="cf-stack">
        {connected && (
          <Alert tone="good" title="Connected">
            The CRM sign-in finished. Calls that were waiting are written now.
          </Alert>
        )}
        {error && (
          <Alert tone="bad" title="Sign-in failed">
            {ERRORS[error] ?? "The sign-in did not finish."}
          </Alert>
        )}
        {connectors.status === "loading" && <Spinner label="Loading…" />}
        {connectors.status === "error" && (
          <Alert tone="bad" title="Could not load the CRM connectors">
            {connectors.message}
          </Alert>
        )}
        {items && items.length === 0 && (
          <EmptyState
            icon="plug"
            title="No CRM connected"
            description="Add HubSpot, Salesforce, Dynamics 365, Zendesk, Freshdesk, ConnectWise, Autotask, HaloPSA or Clio with the credentials you created in your own account."
          />
        )}
        {items?.map((c) => (
          <ConnectorPanel
            key={c.id}
            connector={c}
            reload={connectors.reload}
            say={toast.show}
          />
        ))}
        {items && <NewConnector reload={connectors.reload} say={toast.show} />}
      </div>
      {toast.node}
    </section>
  );
}

/** One kind-specific field of the connector form: a select or a text input. */
function KindFieldInput({
  field,
  value,
  onChange,
}: {
  field: KindField;
  value: string;
  onChange: (v: string) => void;
}) {
  if (field.switch)
    return (
      <Switch
        label={field.label}
        hint={field.hint}
        checked={value === "true"}
        onChange={(e) => onChange(e.target.checked ? "true" : "false")}
      />
    );
  if (field.options)
    return (
      <Select
        label={field.label}
        value={value}
        options={field.options}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  return (
    <Input
      label={field.label}
      value={value}
      required={field.required}
      placeholder={field.placeholder}
      hint={field.hint}
      onChange={(e) => onChange(e.target.value)}
    />
  );
}

/** What the customer sets up in the CRM: values to copy, files to import. */
function SetupPanel({ connector: c }: { connector: CRMConnector }) {
  const load = useCallback(
    (sig: AbortSignal) => getCRMSetup(c.id, sig),
    [c.id],
  );
  const setup = usePolling(load, 60_000);
  if (setup.status === "loading") return <Spinner label="Loading setup…" />;
  if (setup.status === "error")
    return (
      <Alert tone="warn" title="Setup values are not available">
        {setup.message}
      </Alert>
    );
  if (setup.data.length === 0) return null;
  return (
    <details open>
      <summary>Set up {kindSpec(c.kind).label}</summary>
      {setup.data.map((it) => (
        <SetupRow key={it.name} item={it} />
      ))}
    </details>
  );
}

function SetupRow({ item }: { item: CRMSetupItem }) {
  if (item.kind === "file") {
    const download = () => {
      const url = URL.createObjectURL(
        new Blob([item.value], { type: "application/xml" }),
      );
      const a = document.createElement("a");
      a.href = url;
      a.download = item.filename ?? "hello.xml";
      a.click();
      URL.revokeObjectURL(url);
    };
    return (
      <div>
        <p>
          <Button icon="download" variant="secondary" onClick={download}>
            Download {item.label}
          </Button>{" "}
          {item.hint}
        </p>
        <CodeBlock title={item.filename ?? item.label} code={item.value} />
      </div>
    );
  }
  return (
    <div>
      <CodeBlock title={item.label} code={item.value} />
      {item.hint && <p>{item.hint}</p>}
    </div>
  );
}

function NewConnector({
  reload,
  say,
}: {
  reload: () => void;
  say: (m: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState(KINDS[0]!.value);
  const spec = kindSpec(kind);
  const [name, setName] = useState(spec.label);
  const [clientId, setClientId] = useState("");
  const [secret, setSecret] = useState("");
  const [values, setValues] = useState(() => initialValues(spec));
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const cfgNow = buildConfig(spec, clientId, values);
  const creds = spec.needsCredentials?.(cfgNow) ?? true;

  function pick(next: string) {
    const sp = kindSpec(next);
    setKind(next);
    setName(sp.label);
    setClientId("");
    setValues(initialValues(sp));
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      const created = await createCRMConnector({
        kind,
        name,
        ...(secret !== "" ? { clientSecret: secret } : {}),
        config: cfgNow,
      });
      setOpen(false);
      setSecret("");
      reload();
      if (spec.auth === "direct") {
        if (!(spec.canConnect?.(cfgNow) ?? true)) {
          say("Connector added.");
          return;
        }
        // No browser step: sign in with the typed credentials now.
        try {
          await connectCRMConnector(created.id);
          reload();
          say("Connector added and connected.");
        } catch (x: unknown) {
          say(`Connector added, but the sign-in failed: ${errorMessage(x)}`);
        }
      } else {
        say("Connector added. Connect it next.");
      }
    } catch (x: unknown) {
      setErr(errorMessage(x));
    } finally {
      setBusy(false);
    }
  }

  if (!open) {
    return (
      <div>
        <Button icon="plus" variant="secondary" onClick={() => setOpen(true)}>
          Add a CRM
        </Button>
      </div>
    );
  }
  return (
    <form onSubmit={(e) => void submit(e)} aria-label="New CRM connector">
      <h2>Add a CRM</h2>
      <Select
        label="CRM"
        value={kind}
        options={KINDS.map((k) => ({ value: k.value, label: k.label }))}
        onChange={(e) => pick(e.target.value)}
      />
      {spec.intro && <p>{spec.intro}</p>}
      <Input
        label="Name"
        value={name}
        maxLength={64}
        required
        onChange={(e) => setName(e.target.value)}
      />
      {spec.idLabel && (
        <Input
          label={spec.idLabel}
          value={clientId}
          required={creds}
          hint={spec.idHint}
          onChange={(e) => setClientId(e.target.value)}
        />
      )}
      <Input
        label={spec.secretLabel}
        type="password"
        autoComplete="off"
        value={secret}
        required={creds}
        onChange={(e) => setSecret(e.target.value)}
        hint={spec.secretHint}
      />
      {spec.fields.map((f) => (
        <KindFieldInput
          key={f.key}
          field={f}
          value={values[f.key] ?? ""}
          onChange={(v) => setValues((m) => ({ ...m, [f.key]: v }))}
        />
      ))}
      {err && (
        <Alert tone="bad" title="Could not add the connector">
          {err}
        </Alert>
      )}
      <Button type="submit" disabled={busy}>
        Add connector
      </Button>{" "}
      <Button variant="ghost" type="button" onClick={() => setOpen(false)}>
        Cancel
      </Button>
    </form>
  );
}

type Tab = "connection" | "users" | "calls";

function ConnectorPanel({
  connector: c,
  reload,
  say,
}: {
  connector: CRMConnector;
  reload: () => void;
  say: (m: string) => void;
}) {
  const [tab, setTab] = useState<Tab>("connection");
  const st = statusLabel(c);
  return (
    <article aria-label={`${c.name} connector`}>
      <h2>
        {c.name} <Badge tone={st.tone}>{st.text}</Badge>
        {!c.enabled && <Badge tone="neutral">Disabled</Badge>}
      </h2>
      {c.connection?.status === "needs_reauth" && (
        <Alert tone="bad" title="This connection needs signing in again">
          {c.connection.lastError || "The CRM refused the stored sign-in."}{" "}
          Calls are kept and written once you connect again.
        </Alert>
      )}
      <Tabs
        aria-label={`${c.name} sections`}
        items={[
          { id: "connection", label: "Connection" },
          { id: "users", label: "Users" },
          { id: "calls", label: "Calls" },
        ]}
        value={tab}
        onChange={setTab}
      />
      {tab === "connection" && (
        <ConnectionTab connector={c} reload={reload} say={say} />
      )}
      {tab === "users" && <UsersTab connector={c} say={say} />}
      {tab === "calls" && <CallsTab connector={c} say={say} />}
    </article>
  );
}

function ConnectionTab({
  connector: c,
  reload,
  say,
}: {
  connector: CRMConnector;
  reload: () => void;
  say: (m: string) => void;
}) {
  const spec = kindSpec(c.kind);
  const [name, setName] = useState(c.name);
  const [clientId, setClientId] = useState(c.config.clientId ?? "");
  const [secret, setSecret] = useState("");
  const [values, setValues] = useState<Record<string, string>>(() =>
    Object.fromEntries(
      spec.fields.map((f) => [f.key, c.config[f.key] ?? f.initial ?? ""]),
    ),
  );
  const [enabled, setEnabled] = useState(c.enabled);
  const [recordings, setRecordings] = useState(c.recordings);
  const [ttlDays, setTtlDays] = useState(
    String(Math.round(c.linkTtlHours / 24)),
  );
  const [logMissed, setLogMissed] = useState(c.logMissed);
  const [fallback, setFallback] = useState(c.fallbackOwner);
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState<"disconnect" | "delete" | null>(null);
  const credsNow =
    spec.needsCredentials?.(buildConfig(spec, clientId, values)) ?? true;
  const canConnect = spec.canConnect?.(c.config) ?? true;
  // Changing the identity or where the secret goes asks for the secret again.
  const idChanged =
    (spec.idLabel !== undefined && clientId !== (c.config.clientId ?? "")) ||
    spec.bound.some((k) => (values[k] ?? "") !== (c.config[k] ?? ""));

  async function save(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await updateCRMConnector(c.id, {
        name,
        enabled,
        recordings,
        logMissed,
        fallbackOwner: fallback,
        linkTtlHours: Math.max(1, Math.round(Number(ttlDays) * 24)),
        config: buildConfig(spec, clientId, values),
        ...(secret !== "" ? { clientSecret: secret } : {}),
      });
      setSecret("");
      reload();
      say("Saved.");
    } catch (x: unknown) {
      setErr(errorMessage(x));
    } finally {
      setBusy(false);
    }
  }

  async function connect() {
    setErr(null);
    try {
      const r = await connectCRMConnector(c.id);
      if (r.authorizeUrl) {
        window.location.assign(r.authorizeUrl);
      } else {
        reload();
        say("Connected.");
      }
    } catch (x: unknown) {
      setErr(errorMessage(x));
    }
  }

  async function test() {
    setErr(null);
    try {
      const r = await testCRMConnector(c.id);
      say(r.ok ? "The CRM answered." : `The CRM refused the test: ${r.error}`);
    } catch (x: unknown) {
      setErr(errorMessage(x));
    }
  }

  const conn = c.connection;
  return (
    <div>
      <PropertyList
        items={[
          { label: "Account", value: conn?.externalAccount || "—", mono: true },
          {
            label: "Granted scopes",
            value: conn?.grantedScopes.length
              ? conn.grantedScopes.join(", ")
              : "—",
          },
          {
            label: "Last change",
            value: conn ? formatTime(conn.updatedAt) : "—",
          },
          ...(c.auth === "oauth"
            ? [
                {
                  label: "Redirect URI",
                  value: c.redirectUri ?? "needs HELLO_PUBLIC_URL",
                  mono: true,
                },
              ]
            : []),
        ]}
      />
      <details>
        <summary>{spec.guide.summary}</summary>
        <p>{spec.guide.text}</p>
        {spec.guide.steps && (
          <ol>
            {spec.guide.steps.map((st) => (
              <li key={st}>{st}</li>
            ))}
          </ol>
        )}
        {c.auth === "oauth" && (
          <CodeBlock
            title="Redirect URL"
            code={c.redirectUri ?? "Set HELLO_PUBLIC_URL first"}
          />
        )}
        {spec.guide.scopes && (
          <CodeBlock title="Scopes" code={spec.guide.scopes.join("\n")} />
        )}
        {c.kind === "hubspot" && (
          <>
            <p>
              <strong>Calling from HubSpot.</strong> For HubSpot&apos;s phone
              icon to place a call with the user&apos;s Hello phone, add a
              calling extension to the same app (Calling, in the app&apos;s
              settings) with these values. Each user opens the phone widget once
              and signs in to Hello in the window that appears.
            </p>
            <CodeBlock
              title="Calling settings"
              code={callingSettings(c.redirectUri)}
            />
          </>
        )}
        {spec.sidebarApp && (
          <p>
            <a href={crmAppUrl(c.id)} download>
              Download the {spec.label} sidebar app
            </a>{" "}
            (shows the requester&apos;s numbers with a Call button and their
            latest tickets).
          </p>
        )}
      </details>
      {spec.setup && (
        <SetupPanel key={JSON.stringify(c.config)} connector={c} />
      )}
      <p>
        {canConnect && (
          <>
            <Button
              icon="link"
              onClick={() => void connect()}
              disabled={
                c.auth === "oauth"
                  ? !c.redirectUri || !c.hasClientSecret || !c.config.clientId
                  : !c.hasClientSecret
              }
            >
              {conn ? "Connect again" : "Connect"}
            </Button>{" "}
          </>
        )}
        <Button
          variant="secondary"
          icon="activity"
          onClick={() => void test()}
          disabled={!conn}
        >
          Test
        </Button>{" "}
        <Button
          variant="secondary"
          disabled={!conn}
          onClick={() => setConfirm("disconnect")}
        >
          Disconnect
        </Button>{" "}
        <Button
          variant="danger"
          icon="trash-2"
          onClick={() => setConfirm("delete")}
        >
          Delete
        </Button>
      </p>
      <form onSubmit={(e) => void save(e)} aria-label={`${c.name} settings`}>
        <Input
          label="Name"
          value={name}
          maxLength={64}
          required
          onChange={(e) => setName(e.target.value)}
        />
        {spec.idLabel && (
          <Input
            label={spec.idLabel}
            value={clientId}
            required={credsNow}
            hint={spec.idHint}
            onChange={(e) => setClientId(e.target.value)}
          />
        )}
        <Input
          label={spec.secretLabel}
          type="password"
          autoComplete="off"
          value={secret}
          placeholder={c.hasClientSecret ? "set; leave empty to keep it" : ""}
          required={idChanged}
          onChange={(e) => setSecret(e.target.value)}
          hint={
            idChanged
              ? "A changed identity or address needs the secret again."
              : "Write-only."
          }
        />
        {spec.fields.map((f) => (
          <KindFieldInput
            key={f.key}
            field={f}
            value={values[f.key] ?? ""}
            onChange={(v) => setValues((m) => ({ ...m, [f.key]: v }))}
          />
        ))}
        <Switch
          label="Enabled"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <Switch
          label="Log missed calls"
          checked={logMissed}
          onChange={(e) => setLogMissed(e.target.checked)}
        />
        <Switch
          label={
            c.capabilities.recordingUpload
              ? "Attach recordings"
              : "Attach recording links"
          }
          hint={
            c.capabilities.recordingUpload
              ? "Off by default. The CRM stores an audio file, not a link: the recording is uploaded to the call log."
              : c.capabilities.recordingCopy
                ? "Off by default. The CRM fetches the recording once from a one-day link and keeps its own copy."
                : "Off by default. A link to the recording is written into the CRM record; anyone with the link can play it until it expires."
          }
          checked={recordings}
          onChange={(e) => setRecordings(e.target.checked)}
        />
        {recordings &&
          !c.capabilities.recordingUpload &&
          !c.capabilities.recordingCopy && (
            <Input
              label="Recording link lifetime (days)"
              type="number"
              min={1}
              max={365}
              value={ttlDays}
              onChange={(e) => setTtlDays(e.target.value)}
              hint="Links of recent calls are renewed before they expire."
            />
          )}
        <Input
          label={spec.ownerLabel}
          value={fallback}
          maxLength={64}
          onChange={(e) => setFallback(e.target.value)}
          hint="Owns the calls of users with no CRM user. Empty skips those calls."
        />
        {err && (
          <Alert tone="bad" title="Not saved">
            {err}
          </Alert>
        )}
        <Button type="submit" disabled={busy}>
          Save settings
        </Button>
      </form>
      {confirm === "disconnect" && (
        <ConfirmDialog
          title={`Disconnect ${c.name}?`}
          description="The stored sign-in is removed and the recording links Hello created are revoked. Calls wait until you connect again."
          confirmLabel="Disconnect"
          errorTitle="Could not disconnect"
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            await disconnectCRMConnector(c.id);
            reload();
            setConfirm(null);
            say("Disconnected.");
          }}
        />
      )}
      {confirm === "delete" && (
        <ConfirmDialog
          title={`Delete ${c.name}?`}
          description="The connector, its sign-in, its log and its user mapping are deleted and its recording links stop working. Calls already in the CRM stay there."
          confirmLabel="Delete connector"
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            await deleteCRMConnector(c.id);
            reload();
            setConfirm(null);
            say("Connector deleted.");
          }}
        />
      )}
    </div>
  );
}

function UsersTab({
  connector: c,
  say,
}: {
  connector: CRMConnector;
  say: (m: string) => void;
}) {
  const load = useCallback((s: AbortSignal) => listCRMUsers(c.id, s), [c.id]);
  const users = usePolling(load, 30_000);
  const [edits, setEdits] = useState<Record<number, string>>({});
  const [err, setErr] = useState<string | null>(null);

  async function save(u: CRMUserMapping) {
    setErr(null);
    try {
      await setCRMUser(c.id, u.userId, edits[u.userId] ?? "");
      setEdits((m) => {
        const next = { ...m };
        delete next[u.userId];
        return next;
      });
      users.reload();
      say("Mapping saved.");
    } catch (x: unknown) {
      setErr(errorMessage(x));
    }
  }

  if (users.status === "loading") return <Spinner label="Loading users…" />;
  if (users.status === "error")
    return (
      <Alert tone="bad" title="Could not load the users">
        {users.message}
      </Alert>
    );
  return (
    <div>
      <p>
        A user is matched to the CRM user with the same email address, never by
        name. Set an id by hand when the emails differ; a manual mapping is
        never replaced.
      </p>
      {err && (
        <Alert tone="bad" title="Not saved">
          {err}
        </Alert>
      )}
      <Table
        caption={`${c.name} user mapping`}
        rows={users.data}
        rowKey={(u) => u.userId}
        columns={[
          { key: "user", label: "Hello user", render: (u) => u.username },
          { key: "email", label: "Email", render: (u) => u.email || "—" },
          {
            key: "ext",
            label: "Extension",
            mono: true,
            render: (u) => u.extension || "—",
          },
          {
            key: "crm",
            label: "CRM user id",
            render: (u) => (
              <Input
                aria-label={`CRM user id of ${u.username}`}
                size="sm"
                mono
                value={edits[u.userId] ?? u.externalUserId ?? ""}
                onChange={(e) =>
                  setEdits((m) => ({ ...m, [u.userId]: e.target.value }))
                }
              />
            ),
          },
          {
            key: "source",
            label: "Found by",
            render: (u) =>
              u.externalUserId ? (
                <Badge tone={u.source === "manual" ? "info" : "good"}>
                  {u.source}
                </Badge>
              ) : (
                <Badge tone="warn">unmapped</Badge>
              ),
          },
          {
            key: "act",
            label: "",
            render: (u) => (
              <Button
                size="sm"
                variant="secondary"
                disabled={edits[u.userId] === undefined}
                onClick={() => void save(u)}
              >
                Save
              </Button>
            ),
          },
        ]}
      />
    </div>
  );
}

function CallsTab({
  connector: c,
  say,
}: {
  connector: CRMConnector;
  say: (m: string) => void;
}) {
  const [status, setStatus] = useState<CRMLogStatus | "">("");
  const load = useCallback(
    (s: AbortSignal) => listCRMLogs(c.id, status, s),
    [c.id, status],
  );
  const logs = usePolling(load, 10_000);
  const [err, setErr] = useState<string | null>(null);

  async function retry(l: CRMLog) {
    setErr(null);
    try {
      await retryCRMLog(l.id);
      logs.reload();
      say("Queued again.");
    } catch (x: unknown) {
      setErr(errorMessage(x));
    }
  }

  return (
    <div>
      <Select
        label="Show"
        value={status}
        options={[
          { value: "", label: "All calls" },
          ...CRM_LOG_STATUSES.map((s) => ({
            value: s,
            label: s === "dead" ? "dead (failed for good)" : s,
          })),
        ]}
        onChange={(e) => setStatus(e.target.value as CRMLogStatus | "")}
      />
      {err && (
        <Alert tone="bad" title="Could not retry">
          {err}
        </Alert>
      )}
      {logs.status === "loading" && <Spinner label="Loading calls…" />}
      {logs.status === "error" && (
        <Alert tone="bad" title="Could not load the calls">
          {logs.message}
        </Alert>
      )}
      {logs.status === "ready" && logs.data.items.length === 0 && (
        <EmptyState
          icon="phone"
          title="Nothing here yet"
          description="Calls appear once a call ends and the connector is connected."
        />
      )}
      {logs.status === "ready" && logs.data.items.length > 0 && (
        <Table
          caption={`${c.name} call log`}
          rows={logs.data.items}
          rowKey={(l) => l.id}
          columns={[
            { key: "call", label: "Call", mono: true, render: (l) => l.callId },
            {
              key: "dir",
              label: "Direction",
              render: (l) => l.direction || "—",
            },
            {
              key: "status",
              label: "Status",
              render: (l) => (
                <Badge tone={LOG_TONE[l.status]}>{l.status}</Badge>
              ),
            },
            {
              key: "tries",
              label: "Attempts",
              align: "right",
              render: (l) => l.attempts,
            },
            {
              key: "info",
              label: "Detail",
              render: (l) =>
                l.lastError ||
                (l.status === "pending"
                  ? `next ${formatTime(l.nextAttemptAt)}`
                  : l.externalId || "—"),
            },
            {
              key: "act",
              label: "",
              render: (l) =>
                l.status === "dead" || l.status === "skipped" ? (
                  <Button
                    size="sm"
                    variant="secondary"
                    onClick={() => void retry(l)}
                  >
                    Retry
                  </Button>
                ) : null,
            },
          ]}
        />
      )}
    </div>
  );
}
