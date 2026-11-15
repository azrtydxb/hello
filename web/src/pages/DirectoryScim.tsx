import { useCallback, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors, type FieldError } from "../api";
import {
  confirmRun,
  createConnector,
  getScimStatus,
  issueScimToken,
  listScimTokens,
  revokeScimToken,
  updateConnector,
  type Connector,
  type ConnectorInput,
  type ConnectorMode,
  type Mappings,
  type ScimToken,
  type ScimTokenCreated,
} from "../api/dirsync";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Drawer,
  EmptyState,
  Input,
  Modal,
  PropertyList,
  Select,
  Spinner,
  Switch,
  Table,
  useRestoreFocus,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { mapFieldErrors, splitList, type ErrorMap } from "../forms";
import { usePolling } from "../usePolling";
import "./directory/directory.css";

/**
 * SCIM 2.0 (docs/scim.md): a scim connector is pushed to by an identity
 * provider (Entra ID, Okta, JumpCloud) at /scim/v2 with a hello_scim_ token.
 * This file holds its settings drawer and its panel: base URL, tokens,
 * the mass-deprovision guard and what was pushed.
 */

const POLL_MS = 4000;
const FORM_ID = "scim-form";

/** The pbx extension attribute the default mapping reads. */
export const PBX_EXTENSION =
  "urn:hello:params:scim:schemas:extension:pbx:2.0:User:extension";

const MODES: { value: ConnectorMode; label: string }[] = [
  {
    value: "authoritative",
    label: "Authoritative: the identity provider owns the mapped fields",
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

const ROLES = ["user", "viewer", "operator", "admin"] as const;

interface Draft {
  name: string;
  enabled: boolean;
  mode: ConnectorMode;
  adoptExisting: boolean;
  username: string;
  email: string;
  displayName: string;
  extension: string;
  extDigits: boolean;
  extLast: string;
  roleRules: string;
  ringRules: string;
  allowAdmin: boolean;
  disableDevices: boolean;
  /** "oidc:google" or "oidc:entra"; "" until chosen. */
  login: string;
}

/** How a SCIM connector's users sign in: single sign-on only (they have no
 * local password, and LDAP bind is for LDAP connectors). */
const LOGINS = [
  { value: "oidc:entra", label: "Single sign-on with Microsoft" },
  { value: "oidc:google", label: "Single sign-on with Google" },
];

function loginOf(c: Connector | null): string {
  if (c?.options.loginMethod === "oidc" && c.options.oidcProvider)
    return `oidc:${c.options.oidcProvider}`;
  return "";
}

function draftOf(c: Connector | null): Draft {
  const ext = c?.mappings.extension;
  const last = ext?.transforms.find((t) => t.kind === "last");
  return {
    name: c?.name ?? "",
    enabled: c?.enabled ?? true,
    mode: c?.mode ?? "authoritative",
    adoptExisting: c?.config.adoptExisting ?? false,
    username: (c?.mappings.username.sources ?? ["userName"]).join(", "),
    email: (
      c?.mappings.email.sources ?? ["emails[primary]", "emails[work]", "emails"]
    ).join(", "),
    displayName: (
      c?.mappings.displayName.sources ?? [
        "displayName",
        "name.formatted",
        "name.full",
      ]
    ).join(", "),
    extension: (ext?.sources ?? [PBX_EXTENSION]).join(", "),
    extDigits: ext ? ext.transforms.some((t) => t.kind === "digits") : false,
    extLast: last?.n ? String(last.n) : "",
    roleRules: (c?.groupRules.roles ?? [])
      .map((r) => `${r.groupId} ${r.role}`)
      .join("\n"),
    ringRules: (c?.groupRules.ringGroups ?? [])
      .map((r) => `${r.groupId} ${String(r.ringGroupId)}`)
      .join("\n"),
    allowAdmin: c?.options.allowAdminFromDirectory ?? false,
    disableDevices: c?.options.disableDevices ?? true,
    login: loginOf(c),
  };
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

function mappingsOf(d: Draft): Mappings {
  const extTransforms = [];
  if (d.extDigits) extTransforms.push({ kind: "digits" as const });
  const n = Number.parseInt(d.extLast, 10);
  if (Number.isFinite(n) && n > 0)
    extTransforms.push({ kind: "last" as const, n });
  return {
    username: { sources: splitList(d.username), transforms: [] },
    email: { sources: splitList(d.email), transforms: [] },
    displayName: { sources: splitList(d.displayName), transforms: [] },
    extension: { sources: splitList(d.extension), transforms: extTransforms },
    active: { mode: "bool", attribute: "active" },
  };
}

const KEYS = [
  "name",
  "enabled",
  "mode",
  "config",
  "mappings.username",
  "mappings.email",
  "mappings.displayName",
  "mappings.extension",
  "options.loginMethod",
  "options.oidcProvider",
];

/** The settings of a scim connector: create or edit. */
export function ScimConnectorDrawer({
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
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  async function onSubmit(ev: FormEvent<HTMLFormElement>) {
    ev.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const found: Record<string, string> = {};
    if (d.name.trim() === "") found.name = "Required.";
    if (d.enabled && d.login === "")
      found["options.loginMethod"] =
        "Choose how these users sign in: they have no local password.";
    if (splitList(d.username).length === 0)
      found["mappings.username"] = "Name at least one attribute.";
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
    setErrors(found);
    if (Object.keys(found).length > 0 || roles === null || rings === null) {
      setFormError("Fix the highlighted fields.");
      return;
    }
    setBusy(true);
    const body: ConnectorInput = {
      name: d.name.trim(),
      enabled: d.enabled,
      mode: d.mode,
      config: { adoptExisting: d.adoptExisting },
      mappings: mappingsOf(d),
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
        ...connector?.options,
        allowAdminFromDirectory: d.allowAdmin,
        disableDevices: d.disableDevices,
        loginMethod: d.login ? "oidc" : "",
        oidcProvider:
          d.login === "oidc:google"
            ? "google"
            : d.login === "oidc:entra"
              ? "entra"
              : "",
      },
    };
    try {
      onSaved(
        connector
          ? await updateConnector(connector.id, body)
          : await createConnector({ ...body, kind: "scim" }),
      );
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  const title = connector ? `Edit ${connector.name}` : "New SCIM connector";
  return (
    <Drawer
      title={title}
      description="An identity provider (Entra ID, Okta, JumpCloud) pushes users and groups to Hello. Hello never deletes a user it pushed: a delete or active=false disables it."
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
        aria-label={title}
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
          id="scim-name"
          label="Name"
          autoFocus
          value={d.name}
          error={errors.name}
          onChange={(e) => set("name", e.target.value)}
        />
        <fieldset className="pf-stack">
          <legend className="az-field__label">Mapping</legend>
          <p className="az-field__hint">
            SCIM attributes, in order; the first with a value wins. Multi-valued
            ones read as emails[primary], emails[work] or emails (any). Run Test
            after the first push to see every attribute a user has.
          </p>
          <Input
            id="scim-map-username"
            label="Username"
            mono
            value={d.username}
            error={errors["mappings.username"]}
            onChange={(e) => set("username", e.target.value)}
          />
          <Input
            id="scim-map-email"
            label="Email"
            mono
            value={d.email}
            onChange={(e) => set("email", e.target.value)}
          />
          <Input
            id="scim-map-name"
            label="Display name"
            mono
            value={d.displayName}
            onChange={(e) => set("displayName", e.target.value)}
          />
          <Input
            id="scim-map-ext"
            label="Extension"
            mono
            value={d.extension}
            error={errors["mappings.extension"]}
            hint="Hello's extension attribute by default. Add phoneNumbers[work] with the transforms below to take it from the work phone. The extension must exist in Hello."
            onChange={(e) => set("extension", e.target.value)}
          />
          <Switch
            label="Extension: keep only digits"
            labelPosition="end"
            checked={d.extDigits}
            onChange={(e) => set("extDigits", e.target.checked)}
          />
          <Input
            id="scim-map-last"
            label="Extension: keep the last N digits"
            inputMode="numeric"
            value={d.extLast}
            onChange={(e) => set("extLast", e.target.value.trim())}
          />
        </fieldset>
        <fieldset className="pf-stack">
          <legend className="az-field__label">Groups</legend>
          <div className="az-field">
            <label className="az-field__label" htmlFor="scim-roles">
              Role rules
            </label>
            <textarea
              id="scim-roles"
              className="az-input prov-textarea"
              rows={3}
              spellCheck={false}
              value={d.roleRules}
              placeholder={"<group id or externalId> operator"}
              onChange={(e) => set("roleRules", e.target.value)}
            />
            <span
              className={
                errors.roleRules ? "az-field__error" : "az-field__hint"
              }
            >
              {errors.roleRules ??
                "A pushed group is named by its Hello id or its externalId (Test lists both). The highest matching role wins."}
            </span>
          </div>
          <div className="az-field">
            <label className="az-field__label" htmlFor="scim-rings">
              Ring group rules
            </label>
            <textarea
              id="scim-rings"
              className="az-input prov-textarea"
              rows={3}
              spellCheck={false}
              value={d.ringRules}
              placeholder={"<group id or externalId> 3"}
              onChange={(e) => set("ringRules", e.target.value)}
            />
            <span
              className={
                errors.ringRules ? "az-field__error" : "az-field__hint"
              }
            >
              {errors.ringRules ??
                "Members get their extension added to the ring group; only the rows it added are removed."}
            </span>
          </div>
          <Switch
            label="Allow a group to grant admin"
            labelPosition="end"
            hint="Off by default: SCIM can then never make someone an administrator."
            checked={d.allowAdmin}
            onChange={(e) => set("allowAdmin", e.target.checked)}
          />
        </fieldset>
        <Select
          label="How these users sign in"
          value={d.login}
          error={
            errors["options.loginMethod"] ?? errors["options.oidcProvider"]
          }
          hint="Pushed users have no local password. Single sign-on needs the provider set up under Single sign-on first."
          options={[{ value: "", label: "Choose…" }, ...LOGINS]}
          onChange={(e) => set("login", e.target.value)}
        />
        <Select
          label="Mode"
          value={d.mode}
          options={MODES}
          onChange={(e) => set("mode", e.target.value as ConnectorMode)}
        />
        <Switch
          label="Take over matching local users"
          labelPosition="end"
          hint="A pushed user with the e-mail or username of a local user takes it over (never an administrator). There is no preview for a push, so this is your confirmation; off, such a push is refused."
          checked={d.adoptExisting}
          onChange={(e) => set("adoptExisting", e.target.checked)}
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
          hint="Off answers every SCIM request with 403."
          checked={d.enabled}
          onChange={(e) => set("enabled", e.target.checked)}
        />
      </form>
    </Drawer>
  );
}

async function copy(value: string): Promise<boolean> {
  try {
    if (!navigator.clipboard?.writeText) return false;
    await navigator.clipboard.writeText(value);
    return true;
  } catch {
    return false;
  }
}

function TokenModal({
  issued,
  onClose,
}: {
  issued: ScimTokenCreated;
  onClose: () => void;
}) {
  const [note, setNote] = useState<string | null>(null);
  return (
    <Modal
      title="SCIM token"
      description="Put the base URL and this token in the identity provider's provisioning settings (Entra: Tenant URL and Secret Token; Okta: SCIM connector base URL and HTTP Header bearer token)."
      onClose={onClose}
      actions={<Button onClick={onClose}>Done</Button>}
    >
      <Alert tone="warn" title="Shown once">
        Copy it now. Hello keeps only its hash and cannot show it again; issue a
        new one if it is lost.
      </Alert>
      <div className="dir-secret">
        <span className="az-field__label" id="scim-base-label">
          Base URL
        </span>
        <div className="dir-secret__box">
          <span
            className="dir-secret__value"
            aria-labelledby="scim-base-label"
            role="textbox"
            aria-readonly="true"
          >
            {issued.baseUrl}
          </span>
        </div>
      </div>
      <div className="dir-secret">
        <span className="az-field__label" id="scim-token-label">
          Token
        </span>
        <div className="dir-secret__box">
          <span
            className="dir-secret__value"
            aria-labelledby="scim-token-label"
            role="textbox"
            aria-readonly="true"
          >
            {issued.token}
          </span>
          <Button
            variant="secondary"
            size="sm"
            icon="copy"
            autoFocus
            onClick={() =>
              void copy(issued.token).then((ok) =>
                setNote(
                  ok
                    ? "Token copied."
                    : "Copying is not available here; select the token and copy it.",
                ),
              )
            }
          >
            Copy
          </Button>
        </div>
        {note && <span className="az-field__hint">{note}</span>}
      </div>
    </Modal>
  );
}

/** The SCIM panel of a scim connector: base URL, guard, tokens. */
export function ScimPanel({
  connector,
  onChanged,
}: {
  connector: Connector;
  onChanged: () => void;
}) {
  const loadStatus = useCallback(
    (signal: AbortSignal) => getScimStatus(connector.id, signal),
    [connector.id],
  );
  const loadTokens = useCallback(
    (signal: AbortSignal) => listScimTokens(connector.id, signal),
    [connector.id],
  );
  const status = usePolling(loadStatus, POLL_MS);
  const tokens = usePolling(loadTokens, POLL_MS);
  const [issued, setIssued] = useState<ScimTokenCreated | null>(null);
  const [revoking, setRevoking] = useState<ScimToken | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const toast = useToast();

  const st = status.status === "ready" ? status.data : null;
  const rows = (tokens.status === "ready" ? tokens.data : null) ?? [];
  const active = rows.filter((t) => t.revokedAt === null).length;

  async function onIssue() {
    setNotice(null);
    try {
      setIssued(await issueScimToken(connector.id));
      tokens.reload();
      status.reload();
    } catch (err: unknown) {
      setNotice(`Could not issue a token: ${errorMessage(err)}`);
    }
  }

  async function onResume(runId: number) {
    setNotice(null);
    try {
      await confirmRun(runId);
      toast.show("SCIM writes resumed.");
      status.reload();
      onChanged();
    } catch (err: unknown) {
      setNotice(`Could not resume: ${errorMessage(err)}`);
    }
  }

  return (
    <div className="az-card pf-card pf-card--tight">
      <div className="pf-cardhead">
        <div>
          <h2 className="pf-h3">SCIM of {connector.name}</h2>
          <p className="pf-head__desc">
            The identity provider pushes users and groups here. Every change is
            recorded on the connector's SCIM run of the hour, below.
          </p>
        </div>
        <Button
          icon="plus"
          disabled={active >= 2 || tokens.status !== "ready"}
          onClick={() => void onIssue()}
        >
          Issue token
        </Button>
      </div>
      {notice && (
        <Alert tone="bad" title="SCIM">
          {notice}
        </Alert>
      )}
      {status.status === "loading" && <Spinner label="Loading SCIM status…" />}
      {status.status === "error" && (
        <Alert tone="bad" title="Could not load the SCIM status">
          {status.message}
        </Alert>
      )}
      {st && st.baseUrl === "" && (
        <Alert tone="warn" title="SCIM is not reachable">
          HELLO_PUBLIC_URL is not set, so /scim/v2 answers 404 and no token can
          be issued.
        </Alert>
      )}
      {st?.pausedAt && (
        <Alert tone="warn" title="SCIM writes are paused">
          The identity provider tried to disable {String(st.guard.disables + 1)}{" "}
          or more users within an hour (the limit is {String(st.guard.limit)}:
          20 percent of the {String(st.guard.linked)} linked users, at least 5).
          Check the provider's scoping and assignments. Writes answer 429 until
          you resume; the provider then sends the refused changes again.
          {st.pausedRunId !== null && (
            <div style={{ marginTop: 8 }}>
              <Button
                size="sm"
                onClick={() => void onResume(st.pausedRunId ?? 0)}
              >
                Resume SCIM
              </Button>
            </div>
          )}
        </Alert>
      )}
      {st && (
        <PropertyList
          items={[
            { label: "Base URL", value: st.baseUrl || "not set", mono: true },
            {
              label: "Pushed",
              value: `${String(st.resources.users)} users (${String(st.resources.activeUsers)} active, ${String(st.resources.deletedUsers)} deleted), ${String(st.resources.groups)} groups`,
            },
            {
              label: "Disables in the last hour",
              value: `${String(st.guard.disables)} of ${String(st.guard.limit)} allowed`,
            },
            {
              label: "Last request",
              value: st.lastRequestAt ? formatTime(st.lastRequestAt) : "never",
            },
            {
              label: "Local users",
              value: st.adoptExisting
                ? "taken over when they match"
                : "a match is refused (409)",
            },
          ]}
        />
      )}
      {tokens.status === "ready" && rows.length === 0 ? (
        <EmptyState
          icon="key"
          title="No SCIM tokens"
          description="Issue a token and put it, with the base URL, in the identity provider."
        />
      ) : (
        rows.length > 0 && (
          <Table
            caption={`SCIM tokens of ${connector.name}`}
            rows={rows}
            rowKey={(t) => t.id}
            columns={[
              {
                key: "hint",
                label: "Token",
                render: (t) => <span className="pf-mono">{t.hint}…</span>,
              },
              {
                key: "state",
                label: "State",
                render: (t) =>
                  t.revokedAt ? (
                    <Badge tone="neutral">revoked</Badge>
                  ) : (
                    <Badge tone="good">active</Badge>
                  ),
              },
              {
                key: "created",
                label: "Issued",
                render: (t) => `${formatTime(t.createdAt)} by ${t.createdBy}`,
              },
              {
                key: "used",
                label: "Last used",
                render: (t) =>
                  t.lastUsedAt ? formatTime(t.lastUsedAt) : "never",
              },
              {
                key: "actions",
                label: "",
                align: "right",
                render: (t) =>
                  t.revokedAt ? null : (
                    <Button
                      variant="ghost"
                      size="sm"
                      aria-label={`Revoke token ${t.hint}`}
                      onClick={() => setRevoking(t)}
                    >
                      Revoke
                    </Button>
                  ),
              },
            ]}
          />
        )
      )}
      {issued && <TokenModal issued={issued} onClose={() => setIssued(null)} />}
      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.hint}…?`}
          description="The identity provider's next request with this token is refused (401). Provisioning stops until it has another token."
          confirmLabel="Revoke token"
          onConfirm={async () => {
            try {
              await revokeScimToken(revoking.id);
            } catch (err: unknown) {
              throw new Error(`Could not revoke: ${errorMessage(err)}`, {
                cause: err,
              });
            }
            setRevoking(null);
            tokens.reload();
            status.reload();
            toast.show("Token revoked.");
          }}
          onClose={() => setRevoking(null)}
        />
      )}
      {toast.node}
    </div>
  );
}
