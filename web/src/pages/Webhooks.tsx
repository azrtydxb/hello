import { useCallback, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type FieldError } from "../api";
import { EVENT_TYPES } from "../api/events";
import {
  createWebhook,
  deleteWebhook,
  getWebhookDelivery,
  listWebhookDeliveries,
  listWebhooks,
  redeliverDeadWebhooks,
  redeliverWebhookDelivery,
  rotateWebhookSecret,
  testWebhook,
  updateWebhook,
  type DeliveryStatus,
  type WebhookDelivery,
  type WebhookEndpoint,
  type WebhookFields,
  type WebhookTestResult,
} from "../api/webhooks";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  CodeBlock,
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
  Tabs,
  useRestoreFocus,
  useToast,
  type BadgeTone,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { useCan } from "../role";
import { usePolling } from "../usePolling";
import "./platform/platform.css";

const FORM_ID = "webhook-form";
const EXTENSION = /^[0-9]{2,10}$/;
/** How often the endpoint list and the delivery log reload. */
const POLL_MS = 5000;
const PAGE = 50;

const REASONS: Record<string, string> = {
  manual: "switched off",
  consecutive_failures: "50 deliveries failed in a row",
  endpoint_gone: "the receiver answered 410 Gone",
};

const STATUS_TONE: Record<DeliveryStatus, BadgeTone> = {
  pending: "info",
  retrying: "warn",
  succeeded: "good",
  dead: "bad",
};

/** What an endpoint receives, in words. */
export function filterText(e: WebhookEndpoint): string {
  const types = e.events.length === 0 ? "all events" : e.events.join(", ");
  return e.extensions.length === 0
    ? types
    : `${types} for ${e.extensions.join(", ")}`;
}

/**
 * Webhooks (/integrations/webhooks): the receivers that get call events as
 * signed HTTP posts, with their delivery log and dead letters. Signing
 * secrets are shown once, when they are made.
 */
export function Webhooks() {
  const admin = useCan("admin");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Integrations"
        title="Webhooks"
        description="Hello posts each matching call event to your receiver, signed with the endpoint's secret. Failed deliveries are retried for about 17 hours, then wait here as dead letters you can redeliver."
      />
      {admin ? (
        <Endpoints />
      ) : (
        <Alert tone="warn" title="Administrators only">
          Your role cannot manage webhooks.
        </Alert>
      )}
    </section>
  );
}

type Dialog =
  | { kind: "form"; endpoint: WebhookEndpoint | null }
  | { kind: "secret"; endpoint: WebhookEndpoint; secret: string; made: string }
  | { kind: "test"; endpoint: WebhookEndpoint; result: WebhookTestResult }
  | { kind: "rotate"; endpoint: WebhookEndpoint }
  | { kind: "delete"; endpoint: WebhookEndpoint };

function Endpoints() {
  const list = usePolling(listWebhooks, POLL_MS);
  const [dialog, setDialog] = useState<Dialog | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const toast = useToast();

  const items = list.status === "loading" ? null : (list.data ?? []);
  const current = items?.find((e) => e.id === selected) ?? null;

  async function onTest(e: WebhookEndpoint) {
    setNotice(null);
    try {
      setDialog({ kind: "test", endpoint: e, result: await testWebhook(e.id) });
    } catch (err: unknown) {
      setNotice(`Could not test ${e.name}: ${errorMessage(err)}`);
    }
  }

  async function onToggle(e: WebhookEndpoint) {
    setNotice(null);
    try {
      await updateWebhook(e.id, { enabled: !e.enabled });
      list.reload();
      toast.show(
        e.enabled ? `${e.name} switched off.` : `${e.name} switched on.`,
      );
    } catch (err: unknown) {
      setNotice(`Could not change ${e.name}: ${errorMessage(err)}`);
    }
  }

  const add = (
    <Button
      icon="plus"
      disabled={items === null}
      onClick={() => setDialog({ kind: "form", endpoint: null })}
    >
      New endpoint
    </Button>
  );

  return (
    <div className="pf-stack">
      {notice && (
        <Alert tone="bad" title="Webhooks">
          {notice}
        </Alert>
      )}
      {list.status === "loading" && <Spinner label="Loading endpoints…" />}
      {list.status === "error" && (
        <Alert tone="bad" title="Could not load the webhook endpoints">
          {list.message}
        </Alert>
      )}
      {items && (
        <div className="az-card pf-card pf-card--tight">
          <div className="pf-cardhead">
            <div>
              <h2 className="pf-h3">Endpoints</h2>
              <p className="pf-head__desc">
                Select an endpoint to see its deliveries and dead letters.
              </p>
            </div>
            {add}
          </div>
          {items.length === 0 ? (
            <EmptyState
              icon="webhook"
              title="No webhook endpoints"
              description="Add the HTTPS URL of a receiver and choose the events it should get."
              action={add}
            />
          ) : (
            <Table
              caption="Webhook endpoints"
              rows={items}
              rowKey={(e) => e.id}
              onRowClick={(e) => setSelected(e.id)}
              columns={[
                {
                  key: "name",
                  label: "Name",
                  render: (e) => (
                    <>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-pressed={selected === e.id}
                        aria-label={`Show deliveries of ${e.name}`}
                        onClick={(ev) => {
                          ev.stopPropagation();
                          setSelected(e.id);
                        }}
                      >
                        {e.name}
                      </Button>
                      <div className="pf-mono">{e.url}</div>
                    </>
                  ),
                },
                { key: "filter", label: "Receives", render: filterText },
                {
                  key: "state",
                  label: "State",
                  render: (e) =>
                    e.enabled ? (
                      <Badge tone="good">on</Badge>
                    ) : (
                      <Badge tone="warn">
                        off: {REASONS[e.disabledReason] ?? "switched off"}
                      </Badge>
                    ),
                },
                {
                  key: "health",
                  label: "Last result",
                  render: (e) =>
                    e.lastStatus === null
                      ? "no delivery yet"
                      : `HTTP ${String(e.lastStatus)}, ${formatTime(e.lastSuccessAt ?? e.lastFailureAt)}`,
                },
                {
                  key: "queue",
                  label: "Queue",
                  render: (e) => (
                    <>
                      {e.pending} waiting
                      {e.dead > 0 && (
                        <>
                          {" "}
                          <Badge tone="bad">{e.dead} dead</Badge>
                        </>
                      )}
                    </>
                  ),
                },
                {
                  key: "actions",
                  label: "",
                  align: "right",
                  render: (e) => (
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
                        aria-label={`Send a test event to ${e.name}`}
                        onClick={() => void onTest(e)}
                      >
                        Test
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        aria-label={`Edit ${e.name}`}
                        onClick={() => setDialog({ kind: "form", endpoint: e })}
                      >
                        Edit
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        aria-label={`${e.enabled ? "Switch off" : "Switch on"} ${e.name}`}
                        onClick={() => void onToggle(e)}
                      >
                        {e.enabled ? "Switch off" : "Switch on"}
                      </Button>
                      <Button
                        variant="secondary"
                        size="sm"
                        aria-label={`Rotate the secret of ${e.name}`}
                        onClick={() =>
                          setDialog({ kind: "rotate", endpoint: e })
                        }
                      >
                        Rotate secret
                      </Button>
                      <Button
                        variant="ghost"
                        size="sm"
                        aria-label={`Delete ${e.name}`}
                        onClick={() =>
                          setDialog({ kind: "delete", endpoint: e })
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
      {current && (
        <EndpointDetail
          key={current.id}
          endpoint={current}
          onChanged={list.reload}
        />
      )}

      {dialog?.kind === "form" && (
        <EndpointDrawer
          key={dialog.endpoint ? String(dialog.endpoint.id) : "new"}
          endpoint={dialog.endpoint}
          onClose={() => setDialog(null)}
          onSaved={(saved, secret) => {
            list.reload();
            if (secret) {
              setSelected(saved.id);
              setDialog({
                kind: "secret",
                endpoint: saved,
                secret,
                made: "created",
              });
            } else {
              setDialog(null);
              toast.show(
                dialog.endpoint
                  ? `Endpoint ${saved.name} saved.`
                  : `Endpoint ${saved.name} already existed; its secret is not shown again.`,
              );
            }
          }}
        />
      )}
      {dialog?.kind === "secret" && (
        <SecretDialog
          endpoint={dialog.endpoint}
          secret={dialog.secret}
          made={dialog.made}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "test" && (
        <TestDialog
          endpoint={dialog.endpoint}
          result={dialog.result}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "rotate" && (
        <ConfirmDialog
          title={`Rotate the secret of ${dialog.endpoint.name}?`}
          description="A new signing secret is made and shown once. For 24 hours both the new and the old secret sign each delivery, so the receiver can switch without missing events."
          confirmLabel="Rotate secret"
          confirmVariant="primary"
          onConfirm={async () => {
            const e = dialog.endpoint;
            try {
              const res = await rotateWebhookSecret(e.id);
              list.reload();
              setDialog({
                kind: "secret",
                endpoint: res,
                secret: res.secret ?? "",
                made: "rotated",
              });
            } catch (err: unknown) {
              throw new Error(
                `Could not rotate the secret: ${errorMessage(err)}`,
                { cause: err },
              );
            }
          }}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog?.kind === "delete" && (
        <ConfirmDialog
          title={`Delete ${dialog.endpoint.name}?`}
          description="The endpoint stops receiving events, and its delivery log and dead letters are deleted."
          confirmLabel="Delete endpoint"
          onConfirm={async () => {
            const e = dialog.endpoint;
            try {
              await deleteWebhook(e.id);
            } catch (err: unknown) {
              throw new Error(
                `Could not delete ${e.name}: ${errorMessage(err)}`,
                {
                  cause: err,
                },
              );
            }
            if (selected === e.id) setSelected(null);
            list.reload();
            setDialog(null);
            toast.show(`Endpoint ${e.name} deleted.`);
          }}
          onClose={() => setDialog(null)}
        />
      )}
      {toast.node}
    </div>
  );
}

function SecretDialog({
  endpoint,
  secret,
  made,
  onClose,
}: {
  endpoint: WebhookEndpoint;
  secret: string;
  made: string;
  onClose: () => void;
}) {
  return (
    <Modal
      title={`Signing secret of ${endpoint.name}`}
      description={`The secret was ${made}. Copy it now: it is shown once and cannot be read again.`}
      onClose={onClose}
      actions={<Button onClick={onClose}>I have copied it</Button>}
    >
      <div className="pf-stack">
        <CodeBlock title="Signing secret" code={secret} />
        <Alert tone="warn" title="Keep it safe">
          Store it where only your receiver can read it. Verify every request:
          HMAC-SHA256 of <span className="pf-mono">t.body</span> with this
          secret must equal a <span className="pf-mono">v1</span> value in the{" "}
          <span className="pf-mono">Hello-Signature</span> header, and{" "}
          <span className="pf-mono">t</span> must be less than 5 minutes old.
        </Alert>
        {made === "rotated" && (
          <p className="pf-head__desc">
            The previous secret keeps signing until{" "}
            {formatTime(endpoint.previousSecretExpiresAt)}.
          </p>
        )}
      </div>
    </Modal>
  );
}

function TestDialog({
  endpoint,
  result,
  onClose,
}: {
  endpoint: WebhookEndpoint;
  result: WebhookTestResult;
  onClose: () => void;
}) {
  return (
    <Modal
      title={`Test event to ${endpoint.name}`}
      description="A signed webhook.test event was posted to the endpoint."
      onClose={onClose}
      actions={<Button onClick={onClose}>Close</Button>}
    >
      <div className="pf-stack">
        <Alert
          tone={result.delivered ? "good" : "bad"}
          title={result.delivered ? "Delivered" : "Not delivered"}
        >
          {result.status > 0
            ? `The receiver answered HTTP ${String(result.status)} in ${String(result.latencyMs)} ms.`
            : `No answer after ${String(result.latencyMs)} ms: ${result.error || "connection failed"}.`}
        </Alert>
        {result.body && (
          <CodeBlock title="Start of the answer" code={result.body} />
        )}
      </div>
    </Modal>
  );
}

interface Draft {
  name: string;
  url: string;
  allTypes: boolean;
  types: string[];
  extensions: string;
  allowPrivate: boolean;
  enabled: boolean;
}

const draftOf = (e: WebhookEndpoint | null): Draft => ({
  name: e?.name ?? "",
  url: e?.url ?? "https://",
  allTypes: e ? e.events.length === 0 : true,
  types: e?.events ?? [],
  extensions: e?.extensions.join(", ") ?? "",
  allowPrivate: e?.allowPrivate ?? false,
  enabled: e?.enabled ?? true,
});

const KEYS = ["name", "url", "events", "extensions", "allowPrivate", "enabled"];

function parseExtensions(text: string): string[] {
  return text
    .split(/[\s,]+/)
    .map((s) => s.trim())
    .filter((s) => s !== "");
}

function validate(d: Draft): Record<string, string> {
  const out: Record<string, string> = {};
  if (d.name.trim() === "" || d.name !== d.name.trim() || d.name.length > 64) {
    out.name = "Use 1 to 64 characters without leading or trailing space.";
  }
  if (!/^https?:\/\/\S+$/.test(d.url)) {
    out.url = "Enter the receiver's URL, for example https://example.com/hook.";
  } else if (!d.allowPrivate && !d.url.startsWith("https://")) {
    out.url = "Use https, or allow a private address for an internal receiver.";
  }
  if (!d.allTypes && d.types.length === 0) {
    out.events = "Choose at least one event type, or all events.";
  }
  if (parseExtensions(d.extensions).some((x) => !EXTENSION.test(x))) {
    out.extensions =
      "Use extension numbers of 2 to 10 digits, separated by commas.";
  }
  return out;
}

function EndpointDrawer({
  endpoint,
  onClose,
  onSaved,
}: {
  endpoint: WebhookEndpoint | null;
  onClose: () => void;
  /** secret is set when a new endpoint was made. */
  onSaved: (e: WebhookEndpoint, secret: string | null) => void;
}) {
  useRestoreFocus();
  const [d, setD] = useState(() => draftOf(endpoint));
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  async function onSubmit(ev: FormEvent<HTMLFormElement>) {
    ev.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found = validate(d);
    setErrors(found);
    if (Object.keys(found).length > 0) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    const body: WebhookFields = {
      name: d.name,
      url: d.url,
      events: d.allTypes ? [] : d.types,
      extensions: parseExtensions(d.extensions),
      enabled: d.enabled,
      allowPrivate: d.allowPrivate,
    };
    try {
      if (endpoint) {
        onSaved(await updateWebhook(endpoint.id, body), null);
      } else {
        const made = await createWebhook(body);
        onSaved(made, made.secret);
      }
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
      title={endpoint ? `Edit ${endpoint.name}` : "New webhook endpoint"}
      description="Hello posts each matching event as JSON, signed with the endpoint's secret."
      onClose={busy ? undefined : onClose}
      width={560}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form={FORM_ID} disabled={busy}>
            {endpoint ? "Save endpoint" : "Create endpoint"}
          </Button>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="pf-stack"
        aria-label={endpoint ? `Edit ${endpoint.name}` : "New webhook endpoint"}
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
        <Input
          id="webhook-name"
          label="Name"
          autoFocus
          value={d.name}
          error={errors.name}
          onChange={(e) => set("name", e.target.value)}
        />
        <Input
          id="webhook-url"
          label="URL"
          mono
          value={d.url}
          error={errors.url}
          hint="Where events are posted. Must be https unless a private address is allowed below."
          onChange={(e) => set("url", e.target.value.trim())}
        />
        <fieldset className="pf-stack" aria-describedby="webhook-events-hint">
          <legend>Events</legend>
          <Switch
            label="Every event type"
            labelPosition="end"
            checked={d.allTypes}
            onChange={(e) => set("allTypes", e.target.checked)}
          />
          {!d.allTypes &&
            EVENT_TYPES.map((t) => (
              <Checkbox
                key={t}
                label={t}
                checked={d.types.includes(t)}
                onChange={(e) =>
                  set(
                    "types",
                    e.target.checked
                      ? [...d.types, t]
                      : d.types.filter((x) => x !== t),
                  )
                }
              />
            ))}
          <p
            id="webhook-events-hint"
            className="pf-head__desc"
            role={errors.events ? "alert" : undefined}
          >
            {errors.events ?? "Pick the call events this receiver needs."}
          </p>
        </fieldset>
        <Input
          id="webhook-extensions"
          label="Extensions"
          mono
          value={d.extensions}
          error={errors.extensions}
          hint="Optional. Only events about these extensions, for example 101, 102. Empty means every extension."
          onChange={(e) => set("extensions", e.target.value)}
        />
        <Switch
          label="Allow a private address"
          labelPosition="end"
          hint="For a receiver inside your network (http allowed). Only addresses in HELLO_INTEGRATIONS_ALLOWED_CIDRS are reachable; loopback and metadata addresses never are."
          checked={d.allowPrivate}
          onChange={(e) => set("allowPrivate", e.target.checked)}
        />
        <Switch
          label="Enabled"
          labelPosition="end"
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />
      </form>
    </Drawer>
  );
}

type Tab = "all" | "dead";

function EndpointDetail({
  endpoint,
  onChanged,
}: {
  endpoint: WebhookEndpoint;
  onChanged: () => void;
}) {
  const [tab, setTab] = useState<Tab>("all");
  return (
    <div className="az-card pf-card pf-card--tight">
      <div className="pf-cardhead">
        <div>
          <h2 className="pf-h3">{endpoint.name}</h2>
          <p className="pf-head__desc">
            {filterText(endpoint)}
            {endpoint.consecutiveFailures > 0 &&
              ` · ${String(endpoint.consecutiveFailures)} failed in a row`}
          </p>
        </div>
      </div>
      <Tabs<Tab>
        aria-label={`${endpoint.name} deliveries`}
        idPrefix="webhook-tabs"
        value={tab}
        onChange={setTab}
        items={[
          { id: "all", label: "Deliveries" },
          { id: "dead", label: "Dead letters", count: endpoint.dead },
        ]}
      />
      <div
        role="tabpanel"
        id={`webhook-tabs-panel-${tab}`}
        aria-labelledby={`webhook-tabs-tab-${tab}`}
      >
        <DeliveryLog
          key={tab}
          endpoint={endpoint}
          dead={tab === "dead"}
          onChanged={onChanged}
        />
      </div>
    </div>
  );
}

const STATUS_OPTIONS = [
  { value: "", label: "All states" },
  { value: "pending", label: "Pending" },
  { value: "retrying", label: "Retrying" },
  { value: "succeeded", label: "Succeeded" },
  { value: "dead", label: "Dead" },
];

function DeliveryLog({
  endpoint,
  dead,
  onChanged,
}: {
  endpoint: WebhookEndpoint;
  dead: boolean;
  onChanged: () => void;
}) {
  const [filter, setFilter] = useState<DeliveryStatus | "">("");
  const status: DeliveryStatus | undefined = dead
    ? "dead"
    : filter === ""
      ? undefined
      : filter;
  return (
    <div className="pf-stack">
      {!dead && (
        <div>
          <Select
            label="State"
            value={filter}
            onChange={(e) => setFilter(e.target.value as DeliveryStatus | "")}
            options={STATUS_OPTIONS}
          />
        </div>
      )}
      {/* A new filter starts over: the rows are keyed by it. */}
      <DeliveryRows
        key={status ?? "all"}
        endpoint={endpoint}
        status={status}
        dead={dead}
        onChanged={onChanged}
      />
    </div>
  );
}

function DeliveryRows({
  endpoint,
  status,
  dead,
  onChanged,
}: {
  endpoint: WebhookEndpoint;
  status: DeliveryStatus | undefined;
  dead: boolean;
  onChanged: () => void;
}) {
  const load = useCallback(
    (signal: AbortSignal) =>
      listWebhookDeliveries(endpoint.id, { status, limit: PAGE }, signal),
    [endpoint.id, status],
  );
  const first = usePolling(load, POLL_MS);
  const [older, setOlder] = useState<WebhookDelivery[]>([]);
  const [cursor, setCursor] = useState<string | null>(null);
  const [open, setOpen] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirmAll, setConfirmAll] = useState(false);
  const toast = useToast();

  const page = first.status === "loading" ? null : first.data;
  const rows = page ? [...page.items, ...older] : null;
  const next = cursor ?? page?.nextCursor ?? "";

  async function more() {
    setError(null);
    try {
      const res = await listWebhookDeliveries(endpoint.id, {
        status,
        before: next,
        limit: PAGE,
      });
      setOlder((prev) => [...prev, ...res.items]);
      setCursor(res.nextCursor);
    } catch (err: unknown) {
      setError(`Could not load older deliveries: ${errorMessage(err)}`);
    }
  }

  async function redeliver(d: WebhookDelivery) {
    setError(null);
    try {
      await redeliverWebhookDelivery(d.id);
      first.reload();
      onChanged();
      toast.show("Queued for delivery again.");
    } catch (err: unknown) {
      setError(`Could not redeliver: ${errorMessage(err)}`);
    }
  }

  return (
    <div className="pf-stack">
      {error && (
        <Alert tone="bad" title="Deliveries">
          {error}
        </Alert>
      )}
      {first.status === "error" && (
        <Alert tone="bad" title="Could not load the deliveries">
          {first.message}
        </Alert>
      )}
      {dead && (
        <div>
          <Button
            variant="secondary"
            disabled={endpoint.dead === 0}
            onClick={() => setConfirmAll(true)}
          >
            Redeliver all dead letters
          </Button>
        </div>
      )}
      {rows === null && <Spinner label="Loading deliveries…" />}
      {rows && rows.length === 0 && (
        <EmptyState
          icon="inbox"
          title={dead ? "No dead letters" : "No deliveries yet"}
          description={
            dead
              ? "Deliveries that fail for good, or exhaust their retries, wait here."
              : "Events that match this endpoint appear here as they are sent."
          }
        />
      )}
      {rows && rows.length > 0 && (
        <Table
          caption={dead ? "Dead letters" : "Deliveries"}
          rows={rows}
          rowKey={(d) => d.id}
          onRowClick={(d) => setOpen((o) => (o === d.id ? null : d.id))}
          columns={[
            {
              key: "time",
              label: "Queued",
              render: (d) => formatTime(d.createdAt),
            },
            {
              key: "type",
              label: "Event",
              mono: true,
              render: (d) => d.eventType,
            },
            {
              key: "status",
              label: "State",
              render: (d) => (
                <Badge tone={STATUS_TONE[d.status]}>{d.status}</Badge>
              ),
            },
            { key: "attempts", label: "Attempts", render: (d) => d.attempts },
            {
              key: "last",
              label: "Last result",
              render: (d) =>
                d.attempts === 0
                  ? "—"
                  : [
                      d.lastStatus ? `HTTP ${String(d.lastStatus)}` : "",
                      d.lastError,
                    ]
                      .filter((x) => x !== "")
                      .join(" · "),
            },
            {
              key: "next",
              label: "Next attempt",
              render: (d) =>
                d.nextAttemptAt ? formatTime(d.nextAttemptAt) : "—",
            },
            {
              key: "actions",
              label: "",
              align: "right",
              render: (d) => (
                <span onClick={(e) => e.stopPropagation()}>
                  <Button
                    variant="secondary"
                    size="sm"
                    aria-label={`Redeliver ${d.eventType} ${d.eventId}`}
                    onClick={() => void redeliver(d)}
                  >
                    Redeliver
                  </Button>
                </span>
              ),
            },
          ]}
        />
      )}
      {open !== null && (
        <DeliveryDetail key={open} endpoint={endpoint} id={open} />
      )}
      {next !== "" && rows && rows.length > 0 && (
        <div>
          <Button variant="secondary" onClick={() => void more()}>
            Load older
          </Button>
        </div>
      )}
      {confirmAll && (
        <ConfirmDialog
          title={`Redeliver all dead letters of ${endpoint.name}?`}
          description="Each dead delivery gets a fresh series of attempts, with the same event id. A receiver that dedupes on Hello-Event-Id ignores any it already handled."
          confirmLabel="Redeliver all"
          confirmVariant="primary"
          onConfirm={async () => {
            try {
              const res = await redeliverDeadWebhooks(endpoint.id);
              first.reload();
              onChanged();
              setConfirmAll(false);
              toast.show(
                `${String(res.redelivered)} dead letters queued again.`,
              );
            } catch (err: unknown) {
              throw new Error(`Could not redeliver: ${errorMessage(err)}`, {
                cause: err,
              });
            }
          }}
          onClose={() => setConfirmAll(false)}
        />
      )}
      {toast.node}
    </div>
  );
}

function DeliveryDetail({
  endpoint,
  id,
}: {
  endpoint: WebhookEndpoint;
  id: number;
}) {
  const load = useCallback(
    (signal: AbortSignal) => getWebhookDelivery(endpoint.id, id, signal),
    [endpoint.id, id],
  );
  const state = usePolling(load, POLL_MS);
  const detail = state.status === "loading" ? null : state.data;
  if (!detail) {
    return state.status === "error" ? (
      <Alert tone="bad" title="Could not load the delivery">
        {state.message}
      </Alert>
    ) : (
      <Spinner label="Loading the delivery…" />
    );
  }
  return (
    <div
      className="pf-stack"
      aria-label={`Delivery ${String(id)}`}
      role="group"
    >
      <Table
        caption="Attempts"
        rows={detail.log}
        rowKey={(a) => a.attempt}
        columns={[
          { key: "n", label: "Attempt", render: (a) => a.attempt },
          { key: "at", label: "Time", render: (a) => formatTime(a.at) },
          {
            key: "status",
            label: "Result",
            render: (a) =>
              [a.status ? `HTTP ${String(a.status)}` : "", a.error]
                .filter((x) => x !== "")
                .join(" · ") || "—",
          },
          {
            key: "ms",
            label: "Latency",
            render: (a) => `${String(a.latencyMs)} ms`,
          },
          {
            key: "ref",
            label: "Hello-Delivery",
            mono: true,
            render: (a) => a.deliveryId,
          },
        ]}
      />
      {detail.event ? (
        <CodeBlock title="Event" code={JSON.stringify(detail.event, null, 2)} />
      ) : (
        <p className="pf-head__desc">
          The event has been purged; this delivery can no longer be sent.
        </p>
      )}
    </div>
  );
}
