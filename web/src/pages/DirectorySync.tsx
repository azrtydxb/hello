import { useCallback, useState, type FormEvent } from "react";
import { ApiError, errorMessage, fieldErrors, type FieldError } from "../api";
import {
  applyRun,
  confirmRun,
  createConnector,
  deleteConnector,
  getRange,
  getRun,
  listChanges,
  listConnectors,
  listRuns,
  startConsent,
  startRun,
  testConnector,
  updateConnector,
  GOOGLE_SCOPES,
  type AttrMap,
  type AutoProvision,
  type Change,
  type ChangeAction,
  type Connector,
  type ConnectorInput,
  type ConnectorKind,
  type ConnectorMode,
  type GoogleAuthMode,
  type Mappings,
  type Run,
  type RunStatus,
  type TestResult,
} from "../api/dirsync";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  useRestoreFocus,
  useToast,
  type BadgeTone,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { mapFieldErrors, splitList, type ErrorMap } from "../forms";
import { useCan } from "../role";
import { usePolling } from "../usePolling";
import { PBX_EXTENSION, ScimConnectorDrawer, ScimPanel } from "./DirectoryScim";
import "./platform/platform.css";

const FORM_ID = "directory-form";
/** How often the connector list and the run list reload. */
const POLL_MS = 4000;
/** How often the auto-provisioning range usage reloads (it reads every user). */
const RANGE_POLL_MS = 30000;

const MODES: { value: ConnectorMode; label: string }[] = [
  {
    value: "authoritative",
    label: "Authoritative: the directory owns the mapped fields",
  },
  {
    value: "create_only",
    label: "Create only: seed once, Hello owns the fields after",
  },
  {
    value: "link_only",
    label: "Link only: never create users, link existing ones",
  },
];

const RUN_TONE: Record<RunStatus, BadgeTone> = {
  queued: "info",
  running: "info",
  preview: "info",
  succeeded: "good",
  failed: "bad",
  needs_confirmation: "warn",
  stale: "neutral",
};

const ACTION_TONE: Record<ChangeAction, BadgeTone> = {
  create: "good",
  update: "info",
  link: "info",
  adopt: "warn",
  disable: "warn",
  enable: "good",
  conflict: "bad",
  skip: "neutral",
  error: "bad",
};

const ACTIONS: ChangeAction[] = [
  "create",
  "update",
  "adopt",
  "disable",
  "enable",
  "conflict",
  "skip",
  "error",
];

/** A connector's source, for the list: the host without credentials, the
 * Workspace domain or customer, or the Entra tenant. */
export function sourceText(c: Connector): string {
  if (c.kind === "scim") return "SCIM (pushed by an identity provider)";
  if (c.kind === "google")
    return `Google Workspace ${c.config.domain || c.config.customer || "my_customer"}`;
  if (c.kind === "entra") return `Entra tenant ${c.config.tenantId ?? ""}`;
  const url = c.config.url ?? "";
  try {
    return new URL(url).host;
  } catch {
    return url;
  }
}

/** Whether a connector waits for a Google administrator's consent. */
export function needsConsent(c: Connector): boolean {
  return c.kind === "google" && c.config.authMode === "oauth_admin";
}

/** The counts of a run in words. */
export function countsText(r: Run): string {
  const parts = ACTIONS.filter((a) => (r.counts[a] ?? 0) > 0).map(
    (a) => `${String(r.counts[a])} ${a}`,
  );
  return parts.length === 0 ? "no changes" : parts.join(", ");
}

const FULL_REASONS: Record<string, string> = {
  manual: "requested by hand",
  no_mark: "no earlier run to continue from",
  full_interval: "24 hours since the last full run",
  config_changed: "mapping, rules or connection changed",
  usn_regressed: "the server's USN went backwards",
  invocation_changed: "the server's invocation ID changed",
  clock_regressed: "the server's clock went backwards",
  method_changed: "the server's change tracking changed",
  groups_changed: "a group used by the rules changed",
  unsupported: "the server has no change tracking",
};

/** The range of a connector's auto-provisioning in words. */
export function rangeText(a: AutoProvision): string {
  if (!a.enabled) return "off";
  return `${a.rangeStart}–${a.rangeEnd}`;
}

/** Client-side check of a range, mirroring the server's (null when fine). */
export function rangeError(
  start: string,
  end: string,
): { start?: string; end?: string } | null {
  const num = /^[0-9]{2,10}$/;
  const out: { start?: string; end?: string } = {};
  if (!num.test(start)) out.start = "2 to 10 digits.";
  if (!num.test(end)) out.end = "2 to 10 digits.";
  else if (num.test(start)) {
    if (start.length !== end.length)
      out.end = "As many digits as the first number.";
    else if (Number(end) < Number(start))
      out.end = "Not below the first number.";
    else if (Number(end) - Number(start) + 1 > 100000)
      out.end = "At most 100000 numbers.";
  }
  return out.start || out.end ? out : null;
}

/** Whether a run read only the changes or everything, and why. */
export function readText(r: Run): string {
  if (r.incremental) return "Incremental";
  return "Full";
}

/** The longer form: a full run says why it was full. */
export function readDetail(r: Run): string {
  if (r.incremental) {
    return "Incremental: only changes since the last run were read; absent users are not touched.";
  }
  const why = FULL_REASONS[r.fullReason];
  return why ? `Full: ${why}.` : "Full: the whole directory was read.";
}

/**
 * Directory sync (/integrations/directory): AD/LDAP connectors that create,
 * update and disable users from a company directory. A run is previewed
 * first; applying it is a separate, reviewed step.
 */
export function DirectorySync() {
  const admin = useCan("admin");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Integrations"
        title="Directory"
        description="Hello reads users from Active Directory or LDAP, Google Workspace or Microsoft Entra ID, or an identity provider pushes them by SCIM, and Hello keeps its own users in step: it creates and updates them, and disables the ones the directory drops. It never deletes users and never writes to the directory. Synced users have no local password: they sign in through their directory."
      />
      {admin ? (
        <Connectors />
      ) : (
        <Alert tone="warn" title="Administrators only">
          Your role cannot manage directory sync.
        </Alert>
      )}
    </section>
  );
}

type Dialog =
  | { kind: "form"; connector: Connector | null }
  | { kind: "scim-form"; connector: Connector | null }
  | { kind: "test"; connector: Connector; result: TestResult }
  | { kind: "run"; connector: Connector; run: Run }
  | { kind: "delete"; connector: Connector };

function Connectors() {
  const list = usePolling(listConnectors, POLL_MS);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const toast = useToast();

  const items = list.status === "loading" ? null : (list.data ?? []);
  const current = items?.find((c) => c.id === selected) ?? null;

  async function onTest(c: Connector) {
    setNotice(null);
    try {
      setDialog({
        kind: "test",
        connector: c,
        result: await testConnector(c.id),
      });
    } catch (err: unknown) {
      setNotice(`Could not test ${c.name}: ${errorMessage(err)}`);
    }
  }

  async function onRun(c: Connector, dryRun: boolean) {
    setNotice(null);
    try {
      await startRun(c.id, dryRun);
      setSelected(c.id);
      toast.show(dryRun ? "Preview queued." : "Run queued.");
    } catch (err: unknown) {
      setNotice(`Could not start a run of ${c.name}: ${errorMessage(err)}`);
    }
  }

  async function onConsent(c: Connector) {
    setNotice(null);
    try {
      const { url } = await startConsent(c.id);
      window.location.assign(url);
    } catch (err: unknown) {
      setNotice(
        `Could not start the consent of ${c.name}: ${errorMessage(err)}`,
      );
    }
  }

  async function onToggle(c: Connector) {
    setNotice(null);
    try {
      await updateConnector(c.id, { enabled: !c.enabled });
      list.reload();
    } catch (err: unknown) {
      setNotice(`Could not change ${c.name}: ${errorMessage(err)}`);
    }
  }

  const add = (
    <span style={{ display: "inline-flex", gap: 6 }}>
      <Button
        icon="plus"
        disabled={items === null}
        onClick={() => setDialog({ kind: "form", connector: null })}
      >
        New connector
      </Button>
      <Button
        icon="plus"
        variant="secondary"
        disabled={items === null}
        onClick={() => setDialog({ kind: "scim-form", connector: null })}
      >
        New SCIM connector
      </Button>
    </span>
  );

  return (
    <div className="pf-stack">
      <ConsentResult />
      {notice && (
        <Alert tone="bad" title="Directory">
          {notice}
        </Alert>
      )}
      {list.status === "loading" && <Spinner label="Loading connectors…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the directory connectors">
          {list.message}
        </Alert>
      )}
      {items && (
        <div className="az-card pf-card pf-card--tight">
          <div className="pf-cardhead">
            <div>
              <h2 className="pf-h3">Connectors</h2>
              <p className="pf-head__desc">
                Select a connector to see its runs. A schedule starts only after
                you have applied a run by hand.
              </p>
            </div>
            {add}
          </div>
          {items.length === 0 ? (
            <EmptyState
              icon="plug"
              title="No directory connectors"
              description="Add an Active Directory or LDAP server, test the connection, preview what a sync would do, then apply it. Or add a SCIM connector and let Entra ID or Okta push users."
              action={add}
            />
          ) : (
            <Table
              caption="Directory connectors"
              rows={items}
              rowKey={(c) => c.id}
              onRowClick={(c) => setSelected(c.id)}
              columns={[
                {
                  key: "name",
                  label: "Name",
                  render: (c) => (
                    <>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-pressed={selected === c.id}
                        aria-label={`Show runs of ${c.name}`}
                        onClick={(ev) => {
                          ev.stopPropagation();
                          setSelected(c.id);
                        }}
                      >
                        {c.name}
                      </Button>
                      <div className="pf-mono">{sourceText(c)}</div>
                    </>
                  ),
                },
                {
                  key: "state",
                  label: "State",
                  render: (c) => (
                    <>
                      {c.enabled ? (
                        <Badge tone="good">on</Badge>
                      ) : (
                        <Badge tone="warn">off</Badge>
                      )}{" "}
                      {c.status === "failing" && (
                        <Badge tone="bad">failing: {c.lastErrorCode}</Badge>
                      )}
                      {c.pausedAt && <Badge tone="warn">paused</Badge>}
                    </>
                  ),
                },
                { key: "users", label: "Users", render: (c) => c.linkedUsers },
                {
                  key: "last",
                  label: "Last success",
                  render: (c) =>
                    c.lastSuccessAt ? formatTime(c.lastSuccessAt) : "never",
                },
                {
                  key: "schedule",
                  label: "Schedule",
                  render: (c) =>
                    c.kind === "scim"
                      ? "pushed"
                      : c.scheduleMinutes === 0
                        ? "manual"
                        : `every ${String(c.scheduleMinutes)} min`,
                },
                {
                  key: "actions",
                  label: "",
                  align: "right",
                  render: (c) => (
                    <span
                      style={{
                        display: "inline-flex",
                        gap: 6,
                        flexWrap: "wrap",
                      }}
                      onClick={(ev) => ev.stopPropagation()}
                    >
                      <Button
                        variant="secondary"
                        size="sm"
                        aria-label={`Test the connection of ${c.name}`}
                        disabled={c.kind === "ldap" && !c.hasCredential}
                        onClick={() => void onTest(c)}
                      >
                        Test
                      </Button>
                      {needsConsent(c) && (
                        <Button
                          variant="secondary"
                          size="sm"
                          aria-label={`Grant Google consent for ${c.name}`}
                          disabled={!c.hasCredential}
                          onClick={() => void onConsent(c)}
                        >
                          Grant consent
                        </Button>
                      )}
                      <Button
                        variant="secondary"
                        size="sm"
                        aria-label={`Preview a sync of ${c.name}`}
                        disabled={!c.enabled}
                        onClick={() => void onRun(c, true)}
                      >
                        Preview now
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        aria-label={`Edit ${c.name}`}
                        onClick={() =>
                          setDialog({
                            kind: c.kind === "scim" ? "scim-form" : "form",
                            connector: c,
                          })
                        }
                      >
                        Edit
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        aria-label={`${c.enabled ? "Switch off" : "Switch on"} ${c.name}`}
                        onClick={() => void onToggle(c)}
                      >
                        {c.enabled ? "Switch off" : "Switch on"}
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`Delete ${c.name}`}
                        onClick={() =>
                          setDialog({ kind: "delete", connector: c })
                        }
                      >
                        Delete
                      </Button>
                    </span>
                  ),
                },
              ]}
            />
          )}
        </div>
      )}
      {current?.kind === "scim" && (
        <ScimPanel
          key={`scim-${String(current.id)}`}
          connector={current}
          onChanged={list.reload}
        />
      )}
      {current?.autoProvision.enabled && (
        <RangeCard key={`range-${String(current.id)}`} connector={current} />
      )}
      {current && (
        <Runs
          key={current.id}
          connector={current}
          onOpen={(run) => setDialog({ kind: "run", connector: current, run })}
          onRun={(dry) => void onRun(current, dry)}
        />
      )}

      {dialog?.kind === "form" && (
        <ConnectorDrawer
          key={dialog.connector ? String(dialog.connector.id) : "new"}
          connector={dialog.connector}
          onClose={() => setDialog(null)}
          onSaved={(saved) => {
            list.reload();
            setDialog(null);
            toast.show(`Connector ${saved.name} saved.`);
          }}
        />
      )}
      {dialog?.kind === "scim-form" && (
        <ScimConnectorDrawer
          key={dialog.connector ? String(dialog.connector.id) : "new-scim"}
          connector={dialog.connector}
          onClose={() => setDialog(null)}
          onSaved={(saved) => {
            list.reload();
            setDialog(null);
            setSelected(saved.id);
            toast.show(`Connector ${saved.name} saved.`);
          }}
        />
      )}
      {dialog?.kind === "test" && (
        <TestDialog
          connector={dialog.connector}
          result={dialog.result}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "run" && (
        <RunDrawer
          key={dialog.run.id}
          connector={dialog.connector}
          initial={dialog.run}
          onClose={() => {
            setDialog(null);
            list.reload();
          }}
        />
      )}
      {dialog?.kind === "delete" && (
        <DeleteDialog
          connector={dialog.connector}
          onDone={() => {
            if (selected === dialog.connector.id) setSelected(null);
            list.reload();
            setDialog(null);
            toast.show(`Connector ${dialog.connector.name} deleted.`);
          }}
          onClose={() => setDialog(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

/** The result of a Google consent, from the callback's redirect. */
function ConsentResult() {
  const [params] = useState(() => new URLSearchParams(window.location.search));
  const consent = params.get("consent");
  if (consent === "ok")
    return (
      <Alert tone="good" title="Consent granted">
        Google granted Hello read-only access to the directory. Test the
        connector, then preview a sync.
      </Alert>
    );
  if (consent === "failed")
    return (
      <Alert tone="bad" title="Consent not completed">
        Google did not grant the read-only access
        {params.get("reason") ? ` (${params.get("reason") ?? ""})` : ""}. Check
        the OAuth client id and secret, then try Grant consent again.
      </Alert>
    );
  return null;
}

function DeleteDialog({
  connector,
  onDone,
  onClose,
}: {
  connector: Connector;
  onDone: () => void;
  onClose: () => void;
}) {
  const [release, setRelease] = useState(false);
  return (
    <ConfirmDialog
      title={`Delete ${connector.name}?`}
      description={
        connector.linkedUsers > 0
          ? `${String(connector.linkedUsers)} users are managed by this connector. Release them to turn them into local users (they keep their rows and lose the field locks; set a password to let them sign in), or cancel.`
          : "The connector and its run history are deleted."
      }
      confirmLabel="Delete connector"
      onConfirm={async () => {
        try {
          await deleteConnector(connector.id, release);
        } catch (err: unknown) {
          throw new Error(
            `Could not delete ${connector.name}: ${errorMessage(err)}`,
            { cause: err },
          );
        }
        onDone();
      }}
      onClose={onClose}
    >
      {connector.linkedUsers > 0 && (
        <Checkbox
          label="Release the users: turn them into local users"
          checked={release}
          onChange={(e) => setRelease(e.target.checked)}
        />
      )}
    </ConfirmDialog>
  );
}

/** How full the connector's auto-provisioning range is (I-11). */
function RangeCard({ connector }: { connector: Connector }) {
  const load = useCallback(
    (signal: AbortSignal) => getRange(connector.id, signal),
    [connector.id],
  );
  const range = usePolling(load, RANGE_POLL_MS);
  const u = range.status === "loading" ? undefined : range.data;
  return (
    <div className="az-card pf-card pf-card--tight">
      <div className="pf-cardhead">
        <div>
          <h2 className="pf-h3">
            Auto-provisioning range {rangeText(connector.autoProvision)}
          </h2>
          <p className="pf-head__desc">
            Users without an extension get the next free number of this range. A
            number stays with its user; a disabled user keeps it.
          </p>
        </div>
      </div>
      {range.status === "loading" && <Spinner label="Loading the range…" />}
      {range.status === "error" && (
        <Alert tone="bad" title="Could not load the range">
          {range.message}
        </Alert>
      )}
      {u && (
        <>
          <p>
            {String(u.used)} of {String(u.size)} numbers in use,{" "}
            {String(u.free)} free.{" "}
            {u.nextFree === "" ? (
              <Badge tone="bad">full: widen the range</Badge>
            ) : (
              <>
                Next: <span className="pf-mono">{u.nextFree}</span>
              </>
            )}
          </p>
          {u.provisioned.length > 0 && (
            <Table
              caption={`Numbers given by ${connector.name}`}
              rows={u.provisioned}
              rowKey={(p) => p.userId}
              columns={[
                {
                  key: "number",
                  label: "Number",
                  render: (p) => <span className="pf-mono">{p.number}</span>,
                },
                { key: "user", label: "User", render: (p) => p.username },
                {
                  key: "state",
                  label: "State",
                  render: (p) => (
                    <>
                      {p.extensionId === 0 ? (
                        <Badge tone="warn">extension deleted</Badge>
                      ) : (
                        <Badge tone="good">extension</Badge>
                      )}{" "}
                      {!p.enabled && (
                        <Badge tone="neutral">user disabled</Badge>
                      )}
                    </>
                  ),
                },
              ]}
            />
          )}
        </>
      )}
    </div>
  );
}

function Runs({
  connector,
  onOpen,
  onRun,
}: {
  connector: Connector;
  onOpen: (r: Run) => void;
  onRun: (dryRun: boolean) => void;
}) {
  const load = useCallback(
    (signal: AbortSignal) => listRuns(connector.id, signal),
    [connector.id],
  );
  const runs = usePolling(load, POLL_MS);
  const rows = (runs.status === "loading" ? undefined : runs.data) ?? [];
  const halted = rows.find((r) => r.status === "needs_confirmation");
  return (
    <div className="az-card pf-card pf-card--tight">
      <div className="pf-cardhead">
        <div>
          <h2 className="pf-h3">Runs of {connector.name}</h2>
          <p className="pf-head__desc">
            A preview changes nothing. Open a preview to review it and apply it.
          </p>
        </div>
        <span style={{ display: "inline-flex", gap: 6 }}>
          <Button
            variant="secondary"
            disabled={!connector.enabled}
            onClick={() => onRun(true)}
          >
            Preview now
          </Button>
          <Button
            variant="secondary"
            disabled={!connector.enabled}
            onClick={() => onRun(false)}
          >
            Run now
          </Button>
        </span>
      </div>
      {connector.status === "failing" && (
        <Alert
          tone="bad"
          title={`The last runs failed: ${connector.lastErrorCode}`}
        >
          {connector.lastErrorDetail}
        </Alert>
      )}
      {halted && (
        <Alert tone="warn" title="A run is waiting for your confirmation">
          Run {String(halted.id)} would change more than it normally should (
          {halted.errorCode === "empty_result"
            ? "the directory returned no users"
            : "it would disable more than 20 percent of the linked users"}
          ). Open it, review the changes, then confirm or leave it.
        </Alert>
      )}
      {runs.status === "loading" && <Spinner label="Loading runs…" />}
      {runs.status === "ready" && rows.length === 0 ? (
        <EmptyState
          icon="refresh-cw"
          title="No runs yet"
          description="Preview a sync to see what it would do."
        />
      ) : (
        rows.length > 0 && (
          <Table
            caption={`Runs of ${connector.name}`}
            rows={rows}
            rowKey={(r) => r.id}
            onRowClick={onOpen}
            columns={[
              { key: "id", label: "Run", render: (r) => `#${String(r.id)}` },
              { key: "kind", label: "Kind", render: (r) => r.kind },
              {
                key: "read",
                label: "Read",
                render: (r) => (
                  <Badge tone={r.incremental ? "info" : "neutral"}>
                    <span title={readDetail(r)}>{readText(r)}</span>
                  </Badge>
                ),
              },
              {
                key: "status",
                label: "Status",
                render: (r) => (
                  <Badge tone={RUN_TONE[r.status]}>{r.status}</Badge>
                ),
              },
              { key: "counts", label: "Changes", render: countsText },
              {
                key: "when",
                label: "Started",
                render: (r) => formatTime(r.startedAt ?? r.createdAt),
              },
              {
                key: "open",
                label: "",
                align: "right",
                render: (r) => (
                  <Button
                    variant="secondary"
                    size="sm"
                    aria-label={`Open run ${String(r.id)}`}
                    onClick={(ev) => {
                      ev.stopPropagation();
                      onOpen(r);
                    }}
                  >
                    Open
                  </Button>
                ),
              },
            ]}
          />
        )
      )}
    </div>
  );
}

/** What a changed field reads like in the table. */
export function diffText(c: Change): string {
  const keys = Object.keys(c.fieldDiffs);
  return keys
    .map((k) => {
      const d = c.fieldDiffs[k];
      return d
        ? `${k}: ${d.before === "" ? "(none)" : d.before} → ${d.after === "" ? "(none)" : d.after}`
        : k;
    })
    .join("; ");
}

function RunDrawer({
  connector,
  initial,
  onClose,
}: {
  connector: Connector;
  initial: Run;
  onClose: () => void;
}) {
  useRestoreFocus();
  const [action, setAction] = useState<ChangeAction | "">("");
  const [adopt, setAdopt] = useState<Set<string>>(new Set());
  const [override, setOverride] = useState<Set<string>>(new Set());
  const [adoptAll, setAdoptAll] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [run, setRun] = useState<Run>(initial);

  const loadRun = useCallback(
    (signal: AbortSignal) => getRun(initial.id, signal),
    [initial.id],
  );
  const polled = usePolling(loadRun, POLL_MS);
  const current =
    (polled.status === "loading" ? undefined : polled.data) ?? run;

  const loadChanges = useCallback(
    (signal: AbortSignal) =>
      listChanges(initial.id, { action, limit: 500 }, signal),
    [initial.id, action],
  );
  const changes = usePolling(loadChanges, POLL_MS * 4);
  const rows =
    (changes.status === "loading" ? undefined : changes.data)?.items ?? [];

  const toggle = (set: Set<string>, id: string, on: boolean) => {
    const next = new Set(set);
    if (on) next.add(id);
    else next.delete(id);
    return next;
  };

  async function onApply() {
    setBusy(true);
    setError(null);
    try {
      const done = await applyRun(current.id, {
        adoptAll,
        adopt: [...adopt],
        override: [...override],
      });
      setRun(done);
      polled.reload();
      changes.reload();
    } catch (err: unknown) {
      if (err instanceof ApiError && err.code === "stale_preview") {
        setError(
          "The directory changed since this preview (or it is older than 15 minutes). A fresh preview was made: close this and open the newest run.",
        );
        polled.reload();
      } else {
        setError(errorMessage(err));
      }
    } finally {
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={`Run #${String(current.id)} of ${connector.name}`}
      description={`${current.kind}, ${current.status}. ${readDetail(current)} ${countsText(current)}.`}
      onClose={busy ? undefined : onClose}
      width={760}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Close
          </Button>
          {current.status === "needs_confirmation" && (
            <Button disabled={busy} onClick={() => setConfirming(true)}>
              Confirm and apply
            </Button>
          )}
          {current.status === "preview" && (
            <Button disabled={busy} onClick={() => void onApply()}>
              Apply this preview
            </Button>
          )}
        </>
      }
    >
      <div className="pf-stack">
        {error && (
          <Alert tone="bad" title="Not applied">
            {error}
          </Alert>
        )}
        {current.status === "failed" && (
          <Alert tone="bad" title={`The run failed: ${current.errorCode}`}>
            {current.errorDetail}
          </Alert>
        )}
        {current.status === "needs_confirmation" && (
          <Alert tone="warn" title="Halted: nothing was applied">
            {current.errorDetail} Review the changes below. If they are right,
            confirm.
          </Alert>
        )}
        {current.status === "preview" && (
          <p className="pf-head__desc">
            Nothing has been changed. Applying re-reads the directory and
            answers with a fresh preview if it changed. Tick the adoptions and
            overrides you accept; unticked ones stay as they are.
          </p>
        )}
        {current.status === "preview" && (
          <Checkbox
            label="Adopt every local user listed as adopt"
            checked={adoptAll}
            onChange={(e) => setAdoptAll(e.target.checked)}
          />
        )}
        <Select
          label="Show"
          value={action}
          options={[
            { value: "", label: "All changes" },
            ...ACTIONS.map((a) => ({ value: a, label: a })),
          ]}
          onChange={(e) => setAction(e.target.value as ChangeAction | "")}
        />
        {changes.status === "loading" && <Spinner label="Loading changes…" />}
        {changes.status === "ready" && rows.length === 0 ? (
          <EmptyState
            icon="check"
            title="No changes"
            description="Nothing to show."
          />
        ) : (
          rows.length > 0 && (
            <Table
              caption="Changes of the run"
              rows={rows}
              rowKey={(c) => c.id}
              columns={[
                {
                  key: "action",
                  label: "Action",
                  render: (c) => (
                    <Badge tone={ACTION_TONE[c.action]}>{c.action}</Badge>
                  ),
                },
                {
                  key: "user",
                  label: "User",
                  render: (c) => (
                    <>
                      {c.username || c.externalId}
                      <div className="pf-mono">{c.externalId}</div>
                    </>
                  ),
                },
                { key: "diff", label: "Fields", render: diffText },
                { key: "reason", label: "Reason", render: (c) => c.reason },
                {
                  key: "decide",
                  label: "",
                  render: (c) =>
                    current.status !== "preview" ? null : c.action ===
                        "adopt" && c.reason === "adopt_not_confirmed" ? (
                      <Checkbox
                        label="Adopt"
                        aria-label={`Adopt ${c.username}`}
                        checked={adoptAll || adopt.has(c.externalId)}
                        disabled={adoptAll}
                        onChange={(e) =>
                          setAdopt(
                            toggle(adopt, c.externalId, e.target.checked),
                          )
                        }
                      />
                    ) : c.action === "conflict" && c.overridable ? (
                      <Checkbox
                        label="Move extension"
                        aria-label={`Override ${c.username}`}
                        checked={override.has(c.externalId)}
                        onChange={(e) =>
                          setOverride(
                            toggle(override, c.externalId, e.target.checked),
                          )
                        }
                      />
                    ) : null,
                },
              ]}
            />
          )
        )}
      </div>
      {confirming && (
        <ConfirmDialog
          title="Apply the halted run?"
          description="The run is applied as listed, including the users it disables. This is audited."
          confirmLabel="Confirm and apply"
          confirmVariant="primary"
          onConfirm={async () => {
            try {
              setRun(await confirmRun(current.id));
              polled.reload();
              changes.reload();
              setConfirming(false);
            } catch (err: unknown) {
              throw new Error(`Could not confirm: ${errorMessage(err)}`, {
                cause: err,
              });
            }
          }}
          onClose={() => setConfirming(false)}
        />
      )}
    </Drawer>
  );
}

function TestDialog({
  connector,
  result,
  onClose,
}: {
  connector: Connector;
  result: TestResult;
  onClose: () => void;
}) {
  return (
    <Modal
      title={`Test of ${connector.name}`}
      onClose={onClose}
      actions={<Button onClick={onClose}>Close</Button>}
    >
      <div className="pf-stack">
        {result.ok ? (
          <Alert tone="good" title="Connected and bound">
            The first page matched {String(result.matchedUsers)} users.
            {result.serverCertSubject &&
              ` Server certificate ${result.serverCertSubject}, expires ${formatTime(result.serverCertExpires)}.`}
          </Alert>
        ) : (
          <Alert tone="bad" title={`The test failed: ${result.errorCode}`}>
            {result.errorDetail}
          </Alert>
        )}
        {result.sample.length > 0 && (
          <Table
            caption="Sample users with their mapped values"
            rows={result.sample}
            rowKey={(r) => r.externalId}
            columns={[
              {
                key: "u",
                label: "Username",
                render: (r) => r.mapped.username || "—",
              },
              {
                key: "e",
                label: "Email",
                render: (r) => r.mapped.email || "—",
              },
              {
                key: "n",
                label: "Name",
                render: (r) => r.mapped.displayName || "—",
              },
              {
                key: "x",
                label: "Extension",
                render: (r) =>
                  r.mapped.extensionError ? (
                    <Badge tone="bad">{r.mapped.extensionError}</Badge>
                  ) : (
                    r.mapped.extension || "—"
                  ),
              },
              {
                key: "a",
                label: "Active",
                render: (r) => (r.mapped.active ? "yes" : "no"),
              },
            ]}
          />
        )}
        {result.groups.length > 0 && (
          <>
            <h3 className="pf-h3">Groups (for the group rules)</h3>
            <ul>
              {result.groups.map((g) => (
                <li key={g.id}>
                  {g.name} <span className="pf-mono">{g.id}</span>
                </li>
              ))}
            </ul>
          </>
        )}
      </div>
    </Modal>
  );
}

interface Draft {
  kind: ConnectorKind;
  name: string;
  enabled: boolean;
  gAuthMode: GoogleAuthMode;
  gCustomer: string;
  gDomain: string;
  gSubject: string;
  gClientId: string;
  gQuery: string;
  eTenant: string;
  eClient: string;
  eAuth: "secret" | "certificate";
  eCert: string;
  eGuests: boolean;
  /** ldap_bind, oidc:google or oidc:entra. */
  login: string;
  url: string;
  bindDn: string;
  bindPassword: string;
  baseDn: string;
  userFilter: string;
  caCertPem: string;
  serverName: string;
  groupBaseDn: string;
  nestedGroups: boolean;
  mode: ConnectorMode;
  scheduleMinutes: string;
  username: string;
  email: string;
  displayName: string;
  extension: string;
  extDigits: boolean;
  extLast: string;
  activeMode: string;
  activeAttr: string;
  roleRules: string;
  ringRules: string;
  absenceRuns: string;
  allowAdmin: boolean;
  disableDevices: boolean;
  apEnabled: boolean;
  apStart: string;
  apEnd: string;
  apDevice: boolean;
  apVoicemail: boolean;
  apGroups: string;
}

const DEFAULT_URL = "ldaps://";
const DEFAULT_FILTER = "(&(objectCategory=person)(objectClass=user))";

/** The suggested mappings of each kind (the server's defaults). */
const KIND_DEFAULTS: Record<
  ConnectorKind,
  {
    username: string;
    email: string;
    displayName: string;
    extension: string;
    activeMode: string;
    activeAttr: string;
    login: string;
  }
> = {
  ldap: {
    username: "userPrincipalName, sAMAccountName",
    email: "mail",
    displayName: "displayName, cn",
    extension: "ipPhone, telephoneNumber",
    activeMode: "uac",
    activeAttr: "userAccountControl",
    login: "ldap_bind",
  },
  google: {
    username: "primaryEmail",
    email: "primaryEmail",
    displayName: "name.fullName",
    extension: "phones[work].value",
    activeMode: "bool",
    activeAttr: "active",
    login: "oidc:google",
  },
  entra: {
    username: "userPrincipalName",
    email: "mail, userPrincipalName",
    displayName: "displayName",
    extension: "businessPhones",
    activeMode: "bool",
    activeAttr: "accountEnabled",
    login: "oidc:entra",
  },
  // Edited in the SCIM form (DirectoryScim.tsx); listed for completeness.
  scim: {
    username: "userName",
    email: "emails[primary], emails[work], emails",
    displayName: "displayName, name.formatted, name.full",
    extension: PBX_EXTENSION,
    activeMode: "bool",
    activeAttr: "active",
    login: "",
  },
};

const KINDS: { value: ConnectorKind; label: string }[] = [
  { value: "ldap", label: "Active Directory or LDAP" },
  { value: "google", label: "Google Workspace" },
  { value: "entra", label: "Microsoft Entra ID" },
];

const LOGINS: { value: string; label: string; kinds: ConnectorKind[] }[] = [
  {
    value: "ldap_bind",
    label: "Their directory password (LDAP bind)",
    kinds: ["ldap"],
  },
  {
    value: "oidc:google",
    label: "Single sign-on with Google",
    kinds: ["ldap", "google", "entra"],
  },
  {
    value: "oidc:entra",
    label: "Single sign-on with Microsoft",
    kinds: ["ldap", "google", "entra"],
  },
];

function loginOf(c: Connector | null): string {
  if (!c) return "";
  if (c.options.loginMethod === "ldap_bind") return "ldap_bind";
  if (c.options.loginMethod === "oidc" && c.options.oidcProvider)
    return `oidc:${c.options.oidcProvider}`;
  return "";
}

function draftOf(c: Connector | null, kind: ConnectorKind = "ldap"): Draft {
  const ext = c?.mappings.extension;
  const last = ext?.transforms.find((t) => t.kind === "last");
  const k = c?.kind ?? kind;
  const def = KIND_DEFAULTS[k];
  return {
    kind: k,
    name: c?.name ?? "",
    enabled: c?.enabled ?? false,
    gAuthMode: c?.config.authMode ?? "service_account",
    gCustomer: c?.config.customer ?? "my_customer",
    gDomain: c?.config.domain ?? "",
    gSubject: c?.config.subject ?? "",
    gClientId: k === "google" ? (c?.config.clientId ?? "") : "",
    gQuery: c?.config.query ?? "",
    eTenant: c?.config.tenantId ?? "",
    eClient: k === "entra" ? (c?.config.clientId ?? "") : "",
    eAuth: c?.config.authMethod ?? "secret",
    eCert: c?.config.certificatePem ?? "",
    eGuests: c?.config.includeGuests ?? false,
    login: c ? loginOf(c) : def.login,
    url: c?.config.url ?? DEFAULT_URL,
    bindDn: c?.config.bindDn ?? "",
    bindPassword: "",
    baseDn: c?.config.baseDn ?? "",
    userFilter: c?.config.userFilter ?? DEFAULT_FILTER,
    caCertPem: c?.config.caCertPem ?? "",
    serverName: c?.config.serverName ?? "",
    groupBaseDn: c?.config.groupBaseDn ?? "",
    nestedGroups: c?.config.nestedGroups ?? false,
    mode: c?.mode ?? "authoritative",
    scheduleMinutes: String(c?.scheduleMinutes ?? 60),
    username: c ? c.mappings.username.sources.join(", ") : def.username,
    email: c ? c.mappings.email.sources.join(", ") : def.email,
    displayName: c
      ? c.mappings.displayName.sources.join(", ")
      : def.displayName,
    extension: ext ? ext.sources.join(", ") : def.extension,
    extDigits: ext ? ext.transforms.some((t) => t.kind === "digits") : true,
    extLast: last?.n ? String(last.n) : "",
    activeMode: c?.mappings.active.mode ?? def.activeMode,
    activeAttr: c?.mappings.active.attribute ?? def.activeAttr,
    roleRules: (c?.groupRules.roles ?? [])
      .map((r) => `${r.groupId} ${r.role}`)
      .join("\n"),
    ringRules: (c?.groupRules.ringGroups ?? [])
      .map((r) => `${r.groupId} ${String(r.ringGroupId)}`)
      .join("\n"),
    absenceRuns: String(c?.options.absenceRuns || 2),
    allowAdmin: c?.options.allowAdminFromDirectory ?? false,
    disableDevices: c?.options.disableDevices ?? true,
    apEnabled: c?.autoProvision.enabled ?? false,
    apStart: c?.autoProvision.rangeStart ?? "",
    apEnd: c?.autoProvision.rangeEnd ?? "",
    apDevice: c?.autoProvision.device ?? true,
    apVoicemail: c?.autoProvision.voicemail ?? true,
    apGroups: (c?.autoProvision.groups ?? []).join("\n"),
  };
}

function attrMap(sources: string, keep: AttrMap | undefined): AttrMap {
  return { sources: splitList(sources), transforms: keep?.transforms ?? [] };
}

/** Parse "<group id> <value>" lines; null when a line has no value. */
function ruleLines(text: string): [string, string][] | null {
  const out: [string, string][] = [];
  for (const line of text.split("\n")) {
    const t = line.trim();
    if (t === "") continue;
    const parts = t.split(/\s+/);
    if (parts.length !== 2) return null;
    out.push([parts[0] ?? "", parts[1] ?? ""]);
  }
  return out;
}

const ROLES = ["user", "viewer", "operator", "admin"] as const;

function mappingsOf(d: Draft, c: Connector | null): Mappings {
  const extTransforms = [];
  if (d.extDigits) extTransforms.push({ kind: "digits" as const });
  const n = Number.parseInt(d.extLast, 10);
  if (Number.isFinite(n) && n > 0)
    extTransforms.push({ kind: "last" as const, n });
  return {
    username: attrMap(d.username, c?.mappings.username),
    email: attrMap(d.email, c?.mappings.email),
    displayName: attrMap(d.displayName, c?.mappings.displayName),
    extension: { sources: splitList(d.extension), transforms: extTransforms },
    active: {
      mode: d.activeMode as Mappings["active"]["mode"],
      attribute: d.activeAttr.trim(),
    },
  };
}

/** The config of the draft's kind. */
function configOf(d: Draft, c: Connector | null): ConnectorInput["config"] {
  if (d.kind === "google")
    return {
      authMode: d.gAuthMode,
      customer: d.gCustomer.trim() || "my_customer",
      domain: d.gDomain.trim(),
      subject: d.gAuthMode === "delegation" ? d.gSubject.trim() : "",
      clientId: d.gAuthMode === "oauth_admin" ? d.gClientId.trim() : "",
      query: d.gQuery.trim(),
      timeoutSeconds: c?.config.timeoutSeconds,
    };
  if (d.kind === "entra")
    return {
      tenantId: d.eTenant.trim(),
      clientId: d.eClient.trim(),
      authMethod: d.eAuth,
      certificatePem: d.eAuth === "certificate" ? d.eCert : "",
      includeGuests: d.eGuests,
      timeoutSeconds: c?.config.timeoutSeconds,
    };
  return {
    ...(c?.config ?? {}),
    url: d.url.trim(),
    bindDn: d.bindDn.trim(),
    baseDn: d.baseDn.trim(),
    userFilter: d.userFilter.trim(),
    caCertPem: d.caCertPem,
    serverName: d.serverName.trim(),
    groupBaseDn: d.groupBaseDn.trim(),
    nestedGroups: d.nestedGroups,
  };
}

/** The credential field's label and hint for the draft's kind. */
function credentialText(
  d: Draft,
  stored: boolean,
): { label: string; hint: string; multiline: boolean } {
  const keep = stored
    ? " One is stored and never shown; leave empty to keep it."
    : " Stored sealed and never shown again. Needs the secrets scope.";
  if (d.kind === "google") {
    if (d.gAuthMode === "oauth_admin")
      return {
        label: "OAuth client secret",
        hint:
          "The secret of your OAuth client. Then use Grant consent once." +
          keep,
        multiline: false,
      };
    return {
      label: "Service account key (JSON)",
      hint: "The JSON key file of the service account." + keep,
      multiline: true,
    };
  }
  if (d.kind === "entra") {
    if (d.eAuth === "certificate")
      return {
        label: "Certificate private key (PEM)",
        hint: "The RSA private key of the certificate above." + keep,
        multiline: true,
      };
    return {
      label: "Client secret",
      hint: "The value of a client secret of the app registration." + keep,
      multiline: false,
    };
  }
  return {
    label: "Bind password",
    hint: stored
      ? "A password is stored and never shown. Leave empty to keep it; enter it again when you change the server or the bind DN."
      : "Stored sealed and never shown again. Needs the secrets scope.",
    multiline: false,
  };
}

function ConnectorDrawer({
  connector,
  onClose,
  onSaved,
}: {
  connector: Connector | null;
  onClose: () => void;
  onSaved: (c: Connector) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(connector));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [preview, setPreview] = useState<TestResult | null>(null);
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  async function onPreviewMapping() {
    if (!connector) return;
    setFormError(null);
    try {
      setPreview(await testConnector(connector.id, mappingsOf(d, connector)));
    } catch (err: unknown) {
      setFormError(errorMessage(err));
    }
  }

  async function onSubmit(ev: FormEvent<HTMLFormElement>) {
    ev.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = {};
    if (d.name.trim() === "") found.name = "Required.";
    if (d.kind === "ldap") {
      if (d.url.trim() === "" || d.url === DEFAULT_URL)
        found["config.url"] = "Required.";
      if (d.bindDn.trim() === "") found["config.bindDn"] = "Required.";
      if (d.baseDn.trim() === "") found["config.baseDn"] = "Required.";
    }
    if (d.kind === "google") {
      if (d.gAuthMode === "delegation" && d.gSubject.trim() === "")
        found["config.subject"] = "Required for domain-wide delegation.";
      if (d.gAuthMode === "oauth_admin" && d.gClientId.trim() === "")
        found["config.clientId"] = "Required.";
    }
    if (d.kind === "entra") {
      if (d.eTenant.trim() === "") found["config.tenantId"] = "Required.";
      if (d.eClient.trim() === "") found["config.clientId"] = "Required.";
      if (d.eAuth === "certificate" && d.eCert.trim() === "")
        found["config.certificatePem"] = "Required.";
    }
    if (d.enabled && d.login === "")
      found["options.loginMethod"] =
        "Choose how these users sign in: they have no local password.";
    const sched = Number.parseInt(d.scheduleMinutes, 10);
    if (
      !Number.isFinite(sched) ||
      (sched !== 0 && (sched < 5 || sched > 10080))
    )
      found.scheduleMinutes = "0 (manual only) or 5 to 10080.";
    const roles = ruleLines(d.roleRules);
    const rings = ruleLines(d.ringRules);
    if (
      roles === null ||
      roles.some(([, r]) => !(ROLES as readonly string[]).includes(r))
    )
      found.roleRules =
        "One rule per line: <group id> <user|viewer|operator|admin>.";
    if (rings === null || rings.some(([, r]) => !/^[0-9]+$/.test(r)))
      found.ringRules = "One rule per line: <group id> <ring group id>.";
    if (d.apEnabled || d.apStart !== "" || d.apEnd !== "") {
      const re = rangeError(d.apStart, d.apEnd);
      if (re?.start) found["autoProvision.rangeStart"] = re.start;
      if (re?.end) found["autoProvision.rangeEnd"] = re.end;
    }
    setErrors(found);
    if (Object.keys(found).length > 0 || roles === null || rings === null) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    const [loginMethod, oidcProvider] = d.login.startsWith("oidc:")
      ? ["oidc" as const, d.login.slice(5) as "google" | "entra"]
      : [d.login as "" | "ldap_bind", "" as const];
    const body: ConnectorInput = {
      name: d.name.trim(),
      enabled: d.enabled,
      mode: d.mode,
      scheduleMinutes: sched,
      config: configOf(d, connector),
      mappings: mappingsOf(d, connector),
      groupRules: {
        roles: roles.map(([groupId, role]) => ({
          groupId,
          role: role as (typeof ROLES)[number],
        })),
        ringGroups: rings.map(([groupId, id]) => ({
          groupId,
          ringGroupId: Number.parseInt(id, 10),
        })),
      },
      options: {
        absenceRuns: Number.parseInt(d.absenceRuns, 10) || 2,
        allowAdminFromDirectory: d.allowAdmin,
        disableDevices: d.disableDevices,
        loginMethod,
        oidcProvider,
      },
      autoProvision: {
        enabled: d.apEnabled,
        rangeStart: d.apStart,
        rangeEnd: d.apEnd,
        device: d.apDevice,
        voicemail: d.apVoicemail,
        groups: d.apGroups
          .split("\n")
          .map((g) => g.trim())
          .filter((g) => g !== ""),
      },
    };
    if (d.bindPassword !== "") {
      if (d.kind === "ldap") body.bindPassword = d.bindPassword;
      else body.credential = d.bindPassword;
    }
    try {
      onSaved(
        connector
          ? await updateConnector(connector.id, body)
          : await createConnector({ ...body, kind: d.kind }),
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={connector ? `Edit ${connector.name}` : "New directory connector"}
      description="Active Directory or LDAP, Google Workspace or Microsoft Entra ID. Hello only ever reads: connections are verified, plain LDAP is refused and only read-only access is requested."
      onClose={busy ? undefined : onClose}
      width={640}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form={FORM_ID} disabled={busy}>
            {connector ? "Save connector" : "Create connector"}
          </Button>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="pf-stack"
        aria-label={
          connector ? `Edit ${connector.name}` : "New directory connector"
        }
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        {formError && (
          <Alert tone="bad" title="Not saved">
            {formError}
            {unmatched.length > 0 && (
              <ul>
                {unmatched.map((u) => (
                  <li key={u.path + u.message}>
                    {u.path}: {u.message}
                  </li>
                ))}
              </ul>
            )}
          </Alert>
        )}
        {!connector && (
          <Select
            label="Directory"
            value={d.kind}
            options={KINDS}
            onChange={(e) => {
              const kind = e.target.value as ConnectorKind;
              // A new connector starts from its kind's suggested mapping.
              setD((prev) => ({ ...draftOf(null, kind), name: prev.name }));
            }}
          />
        )}
        <Input
          id="dir-name"
          label="Name"
          autoFocus
          value={d.name}
          error={errors.name}
          onChange={(e) => set("name", e.target.value)}
        />
        {d.kind === "ldap" && (
          <>
            <Input
              id="dir-url"
              label="Server URL"
              mono
              value={d.url}
              error={errors["config.url"]}
              hint="ldaps://dc.example.com:636, or ldap://dc.example.com:389 (StartTLS is used)."
              onChange={(e) => set("url", e.target.value.trim())}
            />
            <Input
              id="dir-binddn"
              label="Bind DN"
              mono
              value={d.bindDn}
              error={errors["config.bindDn"]}
              hint="A read-only service account, for example CN=svc-hello,OU=Service,DC=example,DC=com."
              onChange={(e) => set("bindDn", e.target.value)}
            />
          </>
        )}
        {d.kind === "google" && (
          <GoogleFields d={d} set={set} errors={errors} />
        )}
        {d.kind === "entra" && <EntraFields d={d} set={set} errors={errors} />}
        <CredentialField
          d={d}
          stored={connector?.hasCredential ?? false}
          error={errors.bindPassword ?? errors.credential}
          onChange={(v) => set("bindPassword", v)}
        />
        {d.kind === "ldap" && (
          <>
            <Input
              id="dir-basedn"
              label="Base DN"
              mono
              value={d.baseDn}
              error={errors["config.baseDn"]}
              hint="Where the users are, for example OU=People,DC=example,DC=com."
              onChange={(e) => set("baseDn", e.target.value)}
            />
            <Input
              id="dir-filter"
              label="User filter"
              mono
              value={d.userFilter}
              error={errors["config.userFilter"]}
              hint="Disabled accounts must match: they are read and then disabled in Hello."
              onChange={(e) => set("userFilter", e.target.value)}
            />
            <div className="az-field">
              <label className="az-field__label" htmlFor="dir-ca">
                CA certificate (PEM)
              </label>
              <textarea
                id="dir-ca"
                className="az-input prov-textarea"
                rows={4}
                spellCheck={false}
                value={d.caCertPem}
                placeholder="Only for a private CA. Public certificates."
                onChange={(e) => set("caCertPem", e.target.value)}
              />
              {errors["config.caCertPem"] && (
                <span className="az-field__error">
                  {errors["config.caCertPem"]}
                </span>
              )}
            </div>
            <Input
              id="dir-servername"
              label="Server name to verify"
              mono
              value={d.serverName}
              hint="Only when the certificate names a different host than the URL."
              onChange={(e) => set("serverName", e.target.value.trim())}
            />
          </>
        )}

        <fieldset className="pf-stack">
          <legend className="az-field__label">Mapping</legend>
          <p className="az-field__hint">
            Source attributes, in order; the first with a value wins. The fields
            you map become read-only on the users this connector manages.
          </p>
          <Input
            id="dir-map-username"
            label="Username"
            mono
            value={d.username}
            onChange={(e) => set("username", e.target.value)}
          />
          <Input
            id="dir-map-email"
            label="Email"
            mono
            value={d.email}
            onChange={(e) => set("email", e.target.value)}
          />
          <Input
            id="dir-map-name"
            label="Display name"
            mono
            value={d.displayName}
            onChange={(e) => set("displayName", e.target.value)}
          />
          <Input
            id="dir-map-ext"
            label="Extension"
            mono
            value={d.extension}
            error={errors["mappings.extension"]}
            hint="The result must be 2 to 10 digits or the user is reported as a conflict."
            onChange={(e) => set("extension", e.target.value)}
          />
          <Switch
            label="Extension: keep only digits"
            labelPosition="end"
            checked={d.extDigits}
            onChange={(e) => set("extDigits", e.target.checked)}
          />
          <Input
            id="dir-map-last"
            label="Extension: keep the last N digits"
            inputMode="numeric"
            value={d.extLast}
            hint="Optional, for example 4 turns +41 21 555 2001 into 2001."
            onChange={(e) => set("extLast", e.target.value.trim())}
          />
          <Select
            label="Account state"
            value={d.activeMode}
            options={[
              { value: "uac", label: "userAccountControl (Active Directory)" },
              { value: "bool", label: "A true/false attribute means active" },
              {
                value: "bool_inverted",
                label: "A true/false attribute means disabled",
              },
              { value: "always", label: "Every user is active" },
            ]}
            onChange={(e) => set("activeMode", e.target.value)}
          />
          {(d.activeMode === "uac" || d.activeMode.startsWith("bool")) && (
            <Input
              id="dir-active-attr"
              label="Account state attribute"
              mono
              value={d.activeAttr}
              onChange={(e) => set("activeAttr", e.target.value)}
            />
          )}
          {connector && (
            <div>
              <Button
                variant="secondary"
                size="sm"
                onClick={() => void onPreviewMapping()}
              >
                Preview this mapping on 20 users
              </Button>
            </div>
          )}
        </fieldset>

        <fieldset className="pf-stack">
          <legend className="az-field__label">Groups</legend>
          {d.kind === "ldap" ? (
            <Input
              id="dir-groupbase"
              label="Group base DN"
              mono
              value={d.groupBaseDn}
              error={errors["config.groupBaseDn"]}
              hint="Needed for the rules below. Run Test to list the groups and their ids."
              onChange={(e) => set("groupBaseDn", e.target.value)}
            />
          ) : (
            <p className="az-field__hint">
              Run Test to list the groups and their ids. Nested membership is
              always resolved.
            </p>
          )}
          <div className="az-field">
            <label className="az-field__label" htmlFor="dir-roles">
              Role rules
            </label>
            <textarea
              id="dir-roles"
              className="az-input prov-textarea"
              rows={3}
              spellCheck={false}
              value={d.roleRules}
              placeholder={"<group id> operator"}
              onChange={(e) => set("roleRules", e.target.value)}
            />
            <span
              className={
                errors.roleRules ? "az-field__error" : "az-field__hint"
              }
            >
              {errors.roleRules ??
                "The highest matching role wins; users matching none are plain users. Admin from a group needs the switch below."}
            </span>
          </div>
          <div className="az-field">
            <label className="az-field__label" htmlFor="dir-rings">
              Ring group rules
            </label>
            <textarea
              id="dir-rings"
              className="az-input prov-textarea"
              rows={3}
              spellCheck={false}
              value={d.ringRules}
              placeholder={"<group id> 3"}
              onChange={(e) => set("ringRules", e.target.value)}
            />
            <span
              className={
                errors.ringRules ? "az-field__error" : "az-field__hint"
              }
            >
              {errors.ringRules ??
                "Members get their extension added to the ring group. Sync removes only the rows it added."}
            </span>
          </div>
          {d.kind === "ldap" && (
            <Switch
              label="Resolve nested groups"
              labelPosition="end"
              hint="Slow on a large forest."
              checked={d.nestedGroups}
              onChange={(e) => set("nestedGroups", e.target.checked)}
            />
          )}
          <Switch
            label="Allow a group to grant admin"
            labelPosition="end"
            hint="Off by default: a directory group can then never make someone an administrator."
            checked={d.allowAdmin}
            onChange={(e) => set("allowAdmin", e.target.checked)}
          />
        </fieldset>

        <fieldset className="pf-stack">
          <legend className="az-field__label">Auto-provisioning</legend>
          <p className="az-field__hint">
            A user whose extension attribute is empty, or names no extension,
            gets the next free number of this range: an extension named after
            the user, a voicemail box and a SIP device. A number is never
            renumbered; a disabled user keeps its extension and voicemail.
          </p>
          <Switch
            label="Give users without an extension a number from a range"
            labelPosition="end"
            checked={d.apEnabled}
            onChange={(e) => set("apEnabled", e.target.checked)}
          />
          <Input
            id="dir-ap-start"
            label="First number"
            inputMode="numeric"
            mono
            value={d.apStart}
            error={errors["autoProvision.rangeStart"]}
            hint="For example 2000."
            onChange={(e) => set("apStart", e.target.value.trim())}
          />
          <Input
            id="dir-ap-end"
            label="Last number"
            inputMode="numeric"
            mono
            value={d.apEnd}
            error={errors["autoProvision.rangeEnd"]}
            hint="Inclusive, with as many digits as the first, for example 2999."
            onChange={(e) => set("apEnd", e.target.value.trim())}
          />
          <Switch
            label="Create a SIP device"
            labelPosition="end"
            hint="Its secret is shown nowhere: rotate it from the device page when you set up a phone."
            checked={d.apDevice}
            onChange={(e) => set("apDevice", e.target.checked)}
          />
          <Switch
            label="Turn on voicemail, sent to the user's email"
            labelPosition="end"
            checked={d.apVoicemail}
            onChange={(e) => set("apVoicemail", e.target.checked)}
          />
          <div className="az-field">
            <label className="az-field__label" htmlFor="dir-ap-groups">
              Only members of these groups
            </label>
            <textarea
              id="dir-ap-groups"
              className="az-input prov-textarea"
              rows={2}
              spellCheck={false}
              value={d.apGroups}
              placeholder={"<group id>"}
              onChange={(e) => set("apGroups", e.target.value)}
            />
            <span
              className={
                errors["autoProvision.groups"]
                  ? "az-field__error"
                  : "az-field__hint"
              }
            >
              {errors["autoProvision.groups"] ??
                `One group id per line; empty gives every user a number. ${
                  d.kind === "ldap"
                    ? "Needs the group base DN."
                    : d.kind === "google"
                      ? "Google group ids."
                      : "Entra group object ids."
                }`}
            </span>
          </div>
        </fieldset>

        <Select
          label="How these users sign in"
          value={d.login}
          error={
            errors["options.loginMethod"] ?? errors["options.oidcProvider"]
          }
          hint="Synced users have no local password. Single sign-on needs the provider set up under Single sign-on first."
          options={[
            { value: "", label: "Choose…" },
            ...LOGINS.filter((l) => l.kinds.includes(d.kind)).map((l) => ({
              value: l.value,
              label: l.label,
            })),
          ]}
          onChange={(e) => set("login", e.target.value)}
        />
        <Select
          label="Mode"
          value={d.mode}
          options={MODES}
          onChange={(e) => set("mode", e.target.value as ConnectorMode)}
        />
        <Input
          id="dir-schedule"
          label="Schedule (minutes)"
          inputMode="numeric"
          value={d.scheduleMinutes}
          error={errors.scheduleMinutes}
          hint="0 is manual only, otherwise 5 or more. A schedule starts after you have applied a run by hand."
          onChange={(e) => set("scheduleMinutes", e.target.value.trim())}
        />
        <Input
          id="dir-absence"
          label="Disable after this many runs without the user"
          inputMode="numeric"
          value={d.absenceRuns}
          error={errors["options.absenceRuns"]}
          hint="1 to 10. A user whose account is disabled in the directory is disabled at once."
          onChange={(e) => set("absenceRuns", e.target.value.trim())}
        />
        <Switch
          label="Disable a disabled user's devices"
          labelPosition="end"
          checked={d.disableDevices}
          onChange={(e) => set("disableDevices", e.target.checked)}
        />
        <Switch
          label="Enabled"
          labelPosition="end"
          hint="Off stops every job of this connector."
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />
      </form>
      {preview && (
        <TestDialog
          connector={connector ?? ({ name: "this connector" } as Connector)}
          result={preview}
          onClose={() => setPreview(null)}
        />
      )}
    </Drawer>
  );
}

const KEYS = [
  "name",
  "enabled",
  "mode",
  "scheduleMinutes",
  "bindPassword",
  "credential",
  "config.authMode",
  "config.customer",
  "config.domain",
  "config.subject",
  "config.clientId",
  "config.query",
  "config.tenantId",
  "config.authMethod",
  "config.certificatePem",
  "options.loginMethod",
  "options.oidcProvider",
  "config.url",
  "config.bindDn",
  "config.baseDn",
  "config.userFilter",
  "config.caCertPem",
  "config.groupBaseDn",
  "mappings.extension",
  "options.absenceRuns",
  "autoProvision.rangeStart",
  "autoProvision.rangeEnd",
  "autoProvision.groups",
];

type SetDraft = <K extends keyof Draft>(k: K, v: Draft[K]) => void;

function GoogleFields({
  d,
  set,
  errors,
}: {
  d: Draft;
  set: SetDraft;
  errors: ErrorMap;
}) {
  return (
    <>
      <Select
        label="How Hello reads the directory"
        value={d.gAuthMode}
        options={[
          {
            value: "service_account",
            label: "Service account with a read-only admin role (recommended)",
          },
          {
            value: "oauth_admin",
            label: "Your OAuth client, consented once by an administrator",
          },
          {
            value: "delegation",
            label: "Domain-wide delegation (discouraged)",
          },
        ]}
        error={errors["config.authMode"]}
        onChange={(e) => set("gAuthMode", e.target.value as GoogleAuthMode)}
      />
      {d.gAuthMode === "delegation" && (
        <Alert tone="warn" title="Domain-wide delegation is discouraged">
          The service account can act as the administrator it impersonates for
          every scope you authorise. Authorise exactly these read-only scopes
          for its client id in the Admin console:
          <ul>
            {GOOGLE_SCOPES.map((s) => (
              <li key={s}>
                <code>{s}</code>
              </li>
            ))}
          </ul>
        </Alert>
      )}
      {d.gAuthMode === "delegation" && (
        <Input
          id="dir-g-subject"
          label="Administrator to impersonate"
          value={d.gSubject}
          error={errors["config.subject"]}
          onChange={(e) => set("gSubject", e.target.value.trim())}
        />
      )}
      {d.gAuthMode === "oauth_admin" && (
        <Input
          id="dir-g-client"
          label="OAuth client id"
          mono
          value={d.gClientId}
          error={errors["config.clientId"]}
          hint="Ends in .apps.googleusercontent.com. Register the directory consent redirect URI shown under Single sign-on."
          onChange={(e) => set("gClientId", e.target.value.trim())}
        />
      )}
      <Input
        id="dir-g-customer"
        label="Customer"
        mono
        value={d.gCustomer}
        error={errors["config.customer"]}
        hint="my_customer, or your customer id."
        onChange={(e) => set("gCustomer", e.target.value.trim())}
      />
      <Input
        id="dir-g-domain"
        label="Domain (optional)"
        mono
        value={d.gDomain}
        error={errors["config.domain"]}
        hint="List only this domain's users."
        onChange={(e) => set("gDomain", e.target.value.trim())}
      />
      <Input
        id="dir-g-query"
        label="Query (optional)"
        mono
        value={d.gQuery}
        error={errors["config.query"]}
        hint="For example orgUnitPath='/Sales'. Suspended and archived users are read and disabled in Hello."
        onChange={(e) => set("gQuery", e.target.value)}
      />
    </>
  );
}

function EntraFields({
  d,
  set,
  errors,
}: {
  d: Draft;
  set: SetDraft;
  errors: ErrorMap;
}) {
  return (
    <>
      <Input
        id="dir-e-tenant"
        label="Directory (tenant) id"
        mono
        value={d.eTenant}
        error={errors["config.tenantId"]}
        hint="Your own tenant; common and organizations are refused."
        onChange={(e) => set("eTenant", e.target.value.trim())}
      />
      <Input
        id="dir-e-client"
        label="Application (client) id"
        mono
        value={d.eClient}
        error={errors["config.clientId"]}
        hint="An app registration with the application permissions User.Read.All and, for group rules, GroupMember.Read.All."
        onChange={(e) => set("eClient", e.target.value.trim())}
      />
      <Select
        label="Credential"
        value={d.eAuth}
        options={[
          { value: "secret", label: "Client secret" },
          { value: "certificate", label: "Certificate" },
        ]}
        error={errors["config.authMethod"]}
        onChange={(e) =>
          set("eAuth", e.target.value as "secret" | "certificate")
        }
      />
      {d.eAuth === "certificate" && (
        <div className="az-field">
          <label className="az-field__label" htmlFor="dir-e-cert">
            Certificate (PEM)
          </label>
          <textarea
            id="dir-e-cert"
            className="az-input prov-textarea"
            rows={4}
            spellCheck={false}
            value={d.eCert}
            placeholder="The public certificate uploaded to the app registration."
            onChange={(e) => set("eCert", e.target.value)}
          />
          {errors["config.certificatePem"] && (
            <span className="az-field__error">
              {errors["config.certificatePem"]}
            </span>
          )}
        </div>
      )}
      <Switch
        label="Include guest accounts"
        labelPosition="end"
        hint="Off: B2B guests are left out."
        checked={d.eGuests}
        onChange={(e) => set("eGuests", e.target.checked)}
      />
    </>
  );
}

function CredentialField({
  d,
  stored,
  error,
  onChange,
}: {
  d: Draft;
  stored: boolean;
  error?: string;
  onChange: (v: string) => void;
}) {
  const t = credentialText(d, stored);
  if (!t.multiline)
    return (
      <Input
        id="dir-bindpw"
        label={t.label}
        type="password"
        autoComplete="new-password"
        value={d.bindPassword}
        error={error}
        hint={t.hint}
        onChange={(e) => onChange(e.target.value)}
      />
    );
  return (
    <div className="az-field">
      <label className="az-field__label" htmlFor="dir-credential">
        {t.label}
      </label>
      <textarea
        id="dir-credential"
        className="az-input prov-textarea"
        rows={4}
        spellCheck={false}
        autoComplete="off"
        value={d.bindPassword}
        onChange={(e) => onChange(e.target.value)}
      />
      <input
        type="file"
        aria-label={`Load the ${t.label.toLowerCase()} from a file`}
        accept=".json,.pem,.key,application/json"
        onChange={(e) => {
          const file = e.target.files?.[0];
          if (file) void file.text().then(onChange);
        }}
      />
      <span className={error ? "az-field__error" : "az-field__hint"}>
        {error ?? t.hint}
      </span>
    </div>
  );
}
