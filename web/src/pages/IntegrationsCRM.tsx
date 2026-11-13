import { useCallback, useState, type FormEvent } from "react";
import { useSearchParams } from "react-router";
import { errorMessage } from "../api";
import {
  connectCRMConnector,
  createCRMConnector,
  CRM_LOG_STATUSES,
  deleteCRMConnector,
  disconnectCRMConnector,
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
  type CRMUserMapping,
} from "../api/crm";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
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

/** The connector kinds the console can create. */
const KINDS = [
  { value: "hubspot", label: "HubSpot" },
  {
    value: "outlook",
    label: "Outlook / Microsoft 365 (each user's own contacts and calendar)",
  },
  { value: "google", label: "Google (each user's own contacts and calendar)" },
];

/** Kinds every user signs in to with their own account. */
const PERSONAL = new Set(["outlook", "google"]);

const FEATURES = [
  { id: "contacts", label: "Contacts name callers" },
  { id: "calendar", label: "Calendar sets DND" },
] as const;

/** The scopes a personal connector asks for, by feature. */
const PERSONAL_SCOPES: Record<string, Record<string, string>> = {
  outlook: {
    contacts: "Contacts.Read",
    calendar: "Calendars.Read",
  },
  google: {
    contacts: "https://www.googleapis.com/auth/contacts.readonly",
    calendar: "https://www.googleapis.com/auth/calendar.freebusy",
  },
};

function featuresOf(c: Record<string, string>): string[] {
  const f = (c.features ?? "")
    .split(",")
    .map((x) => x.trim())
    .filter(Boolean);
  return f.length ? f : ["contacts"];
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
            description="Add the HubSpot app you created in your own HubSpot developer account."
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
  const [region, setRegion] = useState("na");
  const [tenant, setTenant] = useState("organizations");
  const [features, setFeatures] = useState<string[]>(["contacts"]);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setErr(null);
    try {
      await createCRMConnector({
        kind,
        name,
        clientSecret: secret,
        config:
          kind === "hubspot"
            ? { clientId, region }
            : kind === "outlook"
              ? { clientId, tenant, features: features.join(",") }
              : { clientId, features: features.join(",") },
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
          setName(KINDS.find((x) => x.value === k)?.label.split(" ")[0] ?? k);
        }}
      />
      <Input
        label="Name"
        value={name}
        maxLength={64}
        required
        onChange={(e) => setName(e.target.value)}
      />
      <Input
        label="Client ID"
        value={clientId}
        required
        onChange={(e) => setClientId(e.target.value)}
      />
      <Input
        label="Client secret"
        type="password"
        autoComplete="off"
        value={secret}
        required
        onChange={(e) => setSecret(e.target.value)}
        hint="Kept sealed; it is never shown again."
      />
      {kind === "hubspot" && (
        <Select
          label="HubSpot data centre"
          value={region}
          options={[
            { value: "na", label: "United States / default" },
            { value: "eu", label: "Europe (app-eu1)" },
          ]}
          onChange={(e) => setRegion(e.target.value)}
        />
      )}
      {kind === "outlook" && (
        <Input
          label="Tenant"
          value={tenant}
          onChange={(e) => setTenant(e.target.value)}
          hint="organizations (any work or school account), a tenant id or your domain."
        />
      )}
      {PERSONAL.has(kind) && (
        <FeaturePicker value={features} onChange={setFeatures} />
      )}
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

/**
 * What the personal connectors may do. Changing it asks users to connect
 * again, because the sign-in asks only for what is on.
 */
function FeaturePicker({
  value,
  onChange,
}: {
  value: string[];
  onChange: (v: string[]) => void;
}) {
  return (
    <fieldset>
      <legend>What users may connect</legend>
      {FEATURES.map((f) => (
        <Checkbox
          key={f.id}
          label={f.label}
          checked={value.includes(f.id)}
          onChange={(e) =>
            onChange(
              e.target.checked
                ? [...value, f.id]
                : value.filter((x) => x !== f.id),
            )
          }
        />
      ))}
      {value.length === 0 && (
        <p role="alert">Pick at least one; contacts is used when none is.</p>
      )}
    </fieldset>
  );
}

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
  const personal = c.capabilities.userScope;
  const st = personal
    ? { text: "Per user", tone: "info" as BadgeTone }
    : statusLabel(c);
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
        items={
          personal
            ? [{ id: "connection", label: "Connection" }]
            : [
                { id: "connection", label: "Connection" },
                { id: "users", label: "Users" },
                { id: "calls", label: "Calls" },
              ]
        }
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
  const [region, setRegion] = useState(c.config.region ?? "na");
  const [tenant, setTenant] = useState(c.config.tenant ?? "organizations");
  const [features, setFeatures] = useState<string[]>(featuresOf(c.config));
  const personal = c.capabilities.userScope;
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
        config:
          c.kind === "hubspot"
            ? { clientId, region }
            : c.kind === "outlook"
              ? { clientId, tenant, features: features.join(",") }
              : { clientId, features: features.join(",") },
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
      window.location.assign(await connectCRMConnector(c.id));
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
      {personal ? (
        <PropertyList
          items={[
            {
              label: "Redirect URI",
              value: c.redirectUri ?? "needs HELLO_PUBLIC_URL",
              mono: true,
            },
            {
              label: "Connected users",
              value: "Each user connects under My connections.",
            },
          ]}
        />
      ) : (
        <PropertyList
          items={[
            {
              label: "Account",
              value: conn?.externalAccount || "—",
              mono: true,
            },
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
            {
              label: "Redirect URI",
              value: c.redirectUri ?? "needs HELLO_PUBLIC_URL",
              mono: true,
            },
          ]}
        />
      )}
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
      {personal && (
        <details>
          <summary>
            {c.kind === "outlook" ? "Microsoft Entra app" : "Google Cloud app"}{" "}
            settings
          </summary>
          <p>
            {c.kind === "outlook"
              ? "In the Microsoft Entra admin center register an application (a web platform), add this redirect URI, create a client secret and add these delegated Microsoft Graph permissions. They are read-only and need no administrator consent."
              : "In the Google Cloud console create an OAuth client (web application), add this redirect URI, enable the People API and the Calendar API, and add these scopes to the consent screen. Keep the app in Testing for your own domain, or publish it internal."}
          </p>
          <CodeBlock
            title="Redirect URI"
            code={c.redirectUri ?? "Set HELLO_PUBLIC_URL first"}
          />
          <CodeBlock
            title="Permissions"
            code={features
              .map((f) => PERSONAL_SCOPES[c.kind]?.[f])
              .filter(Boolean)
              .join("\n")}
          />
        </details>
      )}
      <p>
        {!personal && (
          <>
            <Button
              icon="link"
              onClick={() => void connect()}
              disabled={
                !c.redirectUri || !c.hasClientSecret || !c.config.clientId
              }
            >
              {conn ? "Connect again" : "Connect"}
            </Button>{" "}
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
          </>
        )}
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
        <Input
          label="Client ID"
          value={clientId}
          required
          onChange={(e) => setClientId(e.target.value)}
        />
        <Input
          label="Client secret"
          type="password"
          autoComplete="off"
          value={secret}
          placeholder={c.hasClientSecret ? "set; leave empty to keep it" : ""}
          required={idChanged}
          onChange={(e) => setSecret(e.target.value)}
          hint={
            idChanged
              ? "A new client ID needs its secret again."
              : "Write-only."
          }
        />
        {c.kind === "hubspot" && (
          <Select
            label="HubSpot data centre"
            value={region}
            options={[
              { value: "na", label: "United States / default" },
              { value: "eu", label: "Europe (app-eu1)" },
            ]}
            onChange={(e) => setRegion(e.target.value)}
          />
        )}
        {c.kind === "outlook" && (
          <Input
            label="Tenant"
            value={tenant}
            onChange={(e) => setTenant(e.target.value)}
            hint="organizations, a tenant id or your domain."
          />
        )}
        {personal && <FeaturePicker value={features} onChange={setFeatures} />}
        <Switch
          label="Enabled"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        {!personal && (
          <>
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
          </>
        )}
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
