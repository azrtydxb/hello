import { useCallback, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import { errorMessage } from "../api";
import {
  connectCRMConnector,
  createCRMConnector,
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
import { usePolling } from "../usePolling";
import "./platform/platform.css";

/** One of a connector kind's own settings. */
interface KindField {
  key: string;
  label: string;
  type: "text" | "select" | "switch";
  options?: { value: string; label: string }[];
  hint?: string;
  placeholder?: string;
  required?: boolean;
}

interface KindDef {
  value: string;
  label: string;
  /** What the connector needs from the customer, shown when it is added. */
  intro: string;
  fields: KindField[];
  defaults: Record<string, string>;
  /** Whether the client id and secret are needed for these settings. */
  needsCredentials: (cfg: Record<string, string>) => boolean;
  /** Whether Connect (a sign-in) does anything for these settings. */
  canConnect: (cfg: Record<string, string>) => boolean;
}

/** The connector kinds the console can create. */
const KIND_DEFS: KindDef[] = [
  {
    value: "hubspot",
    label: "HubSpot",
    intro:
      "Add the HubSpot app you created in your own HubSpot developer account.",
    fields: [
      {
        key: "region",
        label: "HubSpot data centre",
        type: "select",
        options: [
          { value: "na", label: "United States / default" },
          { value: "eu", label: "Europe (app-eu1)" },
        ],
      },
    ],
    defaults: { region: "na" },
    needsCredentials: () => true,
    canConnect: () => true,
  },
  {
    value: "salesforce",
    label: "Salesforce",
    intro:
      "The Open CTI softphone needs no sign-in: import the call center from the Connection tab. Add a connected app only to let Hello also log calls nobody had a browser open for.",
    fields: [
      {
        key: "serverLogging",
        label: "Also log calls from the server",
        type: "switch",
        hint: "Needs a connected app with the client credentials flow and an integration user. Logs missed calls and calls made on a desk phone, looks callers up by phone and adds recording links.",
      },
      {
        key: "domain",
        label: "My Domain",
        type: "text",
        placeholder: "acme.my.salesforce.com",
        hint: "The host of your org, without https://. Needed for server-side logging.",
      },
      {
        key: "apiVersion",
        label: "API version",
        type: "text",
        placeholder: "62.0",
      },
    ],
    defaults: { serverLogging: "false", apiVersion: "62.0", domain: "" },
    needsCredentials: (c) => c.serverLogging === "true",
    canConnect: (c) => c.serverLogging === "true",
  },
  {
    value: "dynamics",
    label: "Dynamics 365",
    intro:
      "Register an app in Microsoft Entra, add it to your environment as an application user, and enter its tenant, environment address and credentials.",
    fields: [
      {
        key: "orgUrl",
        label: "Environment URL",
        type: "text",
        placeholder: "https://contoso.crm4.dynamics.com",
        required: true,
      },
      {
        key: "tenantId",
        label: "Directory (tenant) ID",
        type: "text",
        required: true,
        hint: "A GUID, from the app registration.",
      },
    ],
    defaults: { orgUrl: "", tenantId: "" },
    needsCredentials: () => true,
    canConnect: () => true,
  },
];

const KINDS = KIND_DEFS.map((k) => ({ value: k.value, label: k.label }));

function kindDef(kind: string): KindDef {
  return KIND_DEFS.find((k) => k.value === kind) ?? KIND_DEFS[0]!;
}

/** The kind's own settings as the form edits them. */
function KindFields({
  def,
  cfg,
  setCfg,
}: {
  def: KindDef;
  cfg: Record<string, string>;
  setCfg: (c: Record<string, string>) => void;
}) {
  const set = (key: string, v: string) => setCfg({ ...cfg, [key]: v });
  return (
    <>
      {def.fields.map((f) => {
        const v = cfg[f.key] ?? "";
        if (f.type === "select")
          return (
            <Select
              key={f.key}
              label={f.label}
              value={v}
              options={f.options ?? []}
              onChange={(e) => set(f.key, e.target.value)}
            />
          );
        if (f.type === "switch")
          return (
            <Switch
              key={f.key}
              label={f.label}
              hint={f.hint}
              checked={v === "true"}
              onChange={(e) => set(f.key, e.target.checked ? "true" : "false")}
            />
          );
        return (
          <Input
            key={f.key}
            label={f.label}
            value={v}
            placeholder={f.placeholder}
            hint={f.hint}
            required={f.required}
            onChange={(e) => set(f.key, e.target.value)}
          />
        );
      })}
    </>
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
      <summary>Set up {kindDef(c.kind).label}</summary>
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

const HUBSPOT_SCOPES = [
  "crm.objects.contacts.read",
  "crm.objects.contacts.write",
  "crm.objects.owners.read",
];

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
        description="Write every external call into your CRM, show the caller's name from the CRM on incoming calls, and attach recordings if you want to. You create the CRM app in your own account; Hello keeps its client secret and tokens sealed."
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
            description="Add HubSpot, Salesforce or Dynamics 365. You create the app in your own account."
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

function NewConnector({
  reload,
  say,
}: {
  reload: () => void;
  say: (m: string) => void;
}) {
  const [open, setOpen] = useState(false);
  const [kind, setKind] = useState(KINDS[0]!.value);
  const [name, setName] = useState("HubSpot");
  const [clientId, setClientId] = useState("");
  const [secret, setSecret] = useState("");
  const [cfg, setCfg] = useState<Record<string, string>>(
    kindDef(KINDS[0]!.value).defaults,
  );
  const [busy, setBusy] = useState(false);
  const def = kindDef(kind);
  const creds = def.needsCredentials(cfg);
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await createCRMConnector({
        kind,
        name,
        ...(secret !== "" ? { clientSecret: secret } : {}),
        config: { ...cfg, clientId },
      });
      setOpen(false);
      setSecret("");
      reload();
      say("Connector added. Connect it next.");
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
        options={KINDS}
        onChange={(e) => {
          const k = e.target.value;
          setKind(k);
          setName(kindDef(k).label);
          setCfg(kindDef(k).defaults);
        }}
      />
      <p>{def.intro}</p>
      <Input
        label="Name"
        value={name}
        maxLength={64}
        required
        onChange={(e) => setName(e.target.value)}
      />
      <KindFields def={def} cfg={cfg} setCfg={setCfg} />
      <Input
        label="Client ID"
        value={clientId}
        required={creds}
        onChange={(e) => setClientId(e.target.value)}
      />
      <Input
        label="Client secret"
        type="password"
        autoComplete="off"
        value={secret}
        required={creds}
        onChange={(e) => setSecret(e.target.value)}
        hint="Kept sealed; it is never shown again."
      />
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
  const [name, setName] = useState(c.name);
  const [clientId, setClientId] = useState(c.config.clientId ?? "");
  const [secret, setSecret] = useState("");
  const def = kindDef(c.kind);
  const [cfg, setCfg] = useState<Record<string, string>>({
    ...def.defaults,
    ...c.config,
  });
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
  const idChanged = clientId !== (c.config.clientId ?? "");
  const creds = def.needsCredentials(cfg);
  const canConnect = def.canConnect(c.config);

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
        config: { ...cfg, clientId },
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
      if (r.connected) {
        reload();
        say("Connected.");
      } else {
        window.location.assign(r.authorizeUrl);
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
          ...(c.kind === "hubspot"
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
      {c.kind === "hubspot" && (
        <details>
          <summary>HubSpot app settings</summary>
          <p>
            In your HubSpot developer account, create a private (unlisted) app
            and set these under Auth. HubSpot rejects a sign-in whose redirect
            URI differs by a character.
          </p>
          <CodeBlock
            title="Redirect URL"
            code={c.redirectUri ?? "Set HELLO_PUBLIC_URL first"}
          />
          <CodeBlock title="Scopes" code={HUBSPOT_SCOPES.join("\n")} />
        </details>
      )}
      {c.kind !== "hubspot" && (
        <SetupPanel key={JSON.stringify(c.config)} connector={c} />
      )}
      <p>
        {canConnect && (
          <>
            <Button
              icon="link"
              onClick={() => void connect()}
              disabled={
                (c.kind === "hubspot" && !c.redirectUri) ||
                !c.hasClientSecret ||
                !c.config.clientId
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
        <KindFields def={def} cfg={cfg} setCfg={setCfg} />
        <Input
          label="Client ID"
          value={clientId}
          required={creds}
          onChange={(e) => setClientId(e.target.value)}
        />
        <Input
          label="Client secret"
          type="password"
          autoComplete="off"
          value={secret}
          placeholder={c.hasClientSecret ? "set; leave empty to keep it" : ""}
          required={idChanged && clientId !== ""}
          onChange={(e) => setSecret(e.target.value)}
          hint={
            idChanged
              ? "A new client ID needs its secret again."
              : "Write-only."
          }
        />
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
          label="Attach recording links"
          hint="Off by default. A link to the recording is written into the CRM record; anyone with the link can play it until it expires."
          checked={recordings}
          onChange={(e) => setRecordings(e.target.checked)}
        />
        {recordings && (
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
          label="Fallback owner (CRM user id)"
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
