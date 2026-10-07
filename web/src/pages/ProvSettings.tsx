import { useCallback, useEffect, useState, type FormEvent } from "react";
import { errorMessage } from "../api";
import {
  checkRedirectAccount,
  deleteRedirectAccount,
  getProvSettings,
  listRedirectAccounts,
  REDIRECT_CREDENTIAL_KEYS,
  saveRedirectAccount,
  vendorLabel,
  type ProvSettings as Settings,
  type RedirectAccount,
  type Vendor,
} from "../api/prov";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  ConfirmDialog,
  IconButton,
  Input,
  PageHeader,
  Spinner,
  Switch,
  Table,
  useToast,
} from "../design/azrty/components";
import {
  copyText,
  CopyField,
  manualRedirectStep,
  PhonesTabs,
} from "./phones/ui";
import "./phones/settings.css";

type Load<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; data: T };

const formatTime = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
};

/** The router snippets that hand out the boot URL as DHCP option 66. */
export function dhcpSnippets(bootUrl: string) {
  return {
    mikrotik: [
      `/ip dhcp-server option add name=prov-66 code=66 value="s'${bootUrl}'"`,
      "/ip dhcp-server network set [find] dhcp-option=prov-66",
    ].join("\n"),
    isc: `option tftp-server-name "${bootUrl}";`,
  };
}

/** Provisioning settings: DHCP values, URLs, the CA, and vendor redirect services. */
export function ProvSettings() {
  const [settings, setSettings] = useState<Load<Settings>>({
    status: "loading",
  });
  const [accounts, setAccounts] = useState<Load<RedirectAccount[]>>({
    status: "loading",
  });
  const toast = useToast();
  const { show } = toast;

  useEffect(() => {
    const controller = new AbortController();
    getProvSettings(controller.signal)
      .then((data) => setSettings({ status: "ready", data }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setSettings({ status: "error", message: errorMessage(err) });
        }
      });
    listRedirectAccounts(controller.signal)
      .then((data) => setAccounts({ status: "ready", data }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setAccounts({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  /** Reload the redirect accounts; a failure rejects for the caller to show. */
  const reloadAccounts = useCallback(async () => {
    const data = await listRedirectAccounts();
    setAccounts({ status: "ready", data });
  }, []);

  const onAccountSaved = useCallback((saved: RedirectAccount) => {
    setAccounts((prev) =>
      prev.status === "ready"
        ? {
            status: "ready",
            data: prev.data.map((a) => (a.vendor === saved.vendor ? saved : a)),
          }
        : prev,
    );
  }, []);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Provisioning settings"
        description="What the network and the vendors' redirect services need so phones find Hello on their first boot."
      />
      <PhonesTabs />
      <div className="prov-stack">
        {settings.status === "loading" && (
          <Spinner label="Loading provisioning settings…" />
        )}
        {settings.status === "error" && (
          <Alert tone="bad" title="Could not load provisioning settings">
            {settings.message}
          </Alert>
        )}
        {settings.status === "ready" && (
          <>
            <DhcpSection settings={settings.data} onCopied={show} />
            <UrlsSection settings={settings.data} onCopied={show} />
            <CaSection settings={settings.data} onCopied={show} />
          </>
        )}
        <section className="prov-card" aria-labelledby="prov-redirect-title">
          <h2 id="prov-redirect-title" className="prov-card__title">
            Redirect services
          </h2>
          <p className="prov-muted">
            A vendor&apos;s redirect service sends a factory-new phone to Hello
            without DHCP. Credentials are write-only: once saved they are never
            shown again.
          </p>
          {accounts.status === "loading" && (
            <Spinner label="Loading redirect services…" />
          )}
          {accounts.status === "error" && (
            <Alert tone="bad" title="Could not load redirect services">
              {accounts.message}
            </Alert>
          )}
          {accounts.status === "ready" && (
            <ul
              className="prov-cards prov-plain"
              aria-label="Redirect services"
            >
              {accounts.data.map((a) => (
                <RedirectCard
                  key={a.vendor}
                  account={a}
                  onSaved={(saved) => {
                    onAccountSaved(saved);
                    show(`${vendorLabel(saved.vendor)} redirect saved.`);
                  }}
                  onReload={reloadAccounts}
                  onRemoved={() =>
                    show(`${vendorLabel(a.vendor)} credentials removed.`)
                  }
                />
              ))}
            </ul>
          )}
        </section>
      </div>
      {toast.node}
    </section>
  );
}

function DhcpSection({
  settings,
  onCopied,
}: {
  settings: Settings;
  onCopied: (message: string) => void;
}) {
  const snippets = dhcpSnippets(settings.bootUrl);
  return (
    <section className="prov-card" aria-labelledby="prov-dhcp-title">
      <h2 id="prov-dhcp-title" className="prov-card__title">
        DHCP option 66
      </h2>
      <p>
        Option 66 gives every phone the boot URL. Poly phones also read option
        160 and Yealink phones also read option 43; set those too if you have
        them. Hello trusts on first use: a phone&apos;s first boot request hands
        it its own HTTPS URL once. After a factory reset, re-arm the phone on
        its page so it can be handed its URL again.
      </p>
      <Table
        caption="DHCP option values per vendor"
        columns={[
          {
            key: "vendor",
            label: "Vendor",
            render: (d) => vendorLabel(d.vendor),
          },
          { key: "option", label: "Option", mono: true },
          { key: "value", label: "Value", mono: true },
          {
            key: "copy",
            label: <span className="visually-hidden">Copy</span>,
            align: "right",
            render: (d) => (
              <IconButton
                icon="copy"
                label={`Copy ${vendorLabel(d.vendor)} option ${d.option} value`}
                onClick={() => void copyText(d.value, "Value", onCopied)}
              />
            ),
          },
        ]}
        rows={settings.dhcp}
        rowKey={(d) => `${d.vendor}-${d.option}`}
      />
      <div className="prov-grid-2">
        <CodeBlock title="MikroTik RouterOS" code={snippets.mikrotik} />
        <CodeBlock title="ISC dhcpd" code={snippets.isc} />
      </div>
      <p className="prov-muted">
        The full setup, per router and vendor, is in docs/provisioning.md.
      </p>
    </section>
  );
}

function UrlsSection({
  settings,
  onCopied,
}: {
  settings: Settings;
  onCopied: (message: string) => void;
}) {
  return (
    <section className="prov-card" aria-labelledby="prov-urls-title">
      <h2 id="prov-urls-title" className="prov-card__title">
        Provisioning URLs
      </h2>
      <CopyField
        id="prov-public-url"
        label="Public URL"
        value={settings.publicUrl}
        what="URL"
        onCopied={onCopied}
      />
      <CopyField
        id="prov-boot-url"
        label="Boot URL"
        value={settings.bootUrl}
        what="URL"
        onCopied={onCopied}
      />
      <CopyField
        id="prov-sip-server"
        label="SIP server"
        value={settings.sipServer}
        what="Address"
        onCopied={onCopied}
      />
    </section>
  );
}

function CaSection({
  settings,
  onCopied,
}: {
  settings: Settings;
  onCopied: (message: string) => void;
}) {
  return (
    <section className="prov-card" aria-labelledby="prov-ca-title">
      <h2 id="prov-ca-title" className="prov-card__title">
        CA certificate
      </h2>
      <p>
        Phones must trust Hello&apos;s CA before their first HTTPS fetch. The
        boot hand-off installs it; for phones set up by hand, load it from the
        plain-HTTP link below and check the fingerprint.
      </p>
      <div className="prov-row">
        <a
          className="az-btn az-btn--secondary"
          href={settings.caUrl}
          download="CA.crt"
        >
          Download CA.crt
        </a>
      </div>
      <CopyField
        id="prov-ca-sha256"
        label="SHA-256 fingerprint"
        value={settings.caSha256}
        what="Fingerprint"
        onCopied={onCopied}
      />
    </section>
  );
}

/** One vendor's redirect account. Credentials are never prefilled or shown. */
function RedirectCard({
  account: a,
  onSaved,
  onReload,
  onRemoved,
}: {
  account: RedirectAccount;
  onSaved: (saved: RedirectAccount) => void;
  onReload: () => Promise<void>;
  onRemoved: () => void;
}) {
  const vendor = a.vendor;
  const label = vendorLabel(vendor);
  const titleId = `redirect-${vendor}-title`;
  const keys = REDIRECT_CREDENTIAL_KEYS[vendor as Vendor] ?? [];
  // The switch's unsaved value; null follows the stored account.
  const [enabledDraft, setEnabledDraft] = useState<boolean | null>(null);
  const enabled = enabledDraft ?? a.enabled;
  const [editing, setEditing] = useState(false);
  const [creds, setCreds] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [checkError, setCheckError] = useState<string | null>(null);
  const [removing, setRemoving] = useState(false);

  const manual = manualRedirectStep(vendor);
  if (!a.supported) {
    return (
      <li className="prov-card" aria-labelledby={titleId}>
        <div className="prov-card__head">
          <h3 id={titleId} className="prov-card__title">
            {label}
          </h3>
          <Badge tone="warn">Manual step</Badge>
        </div>
        <p>{manual ?? "Hello cannot drive this vendor's redirect service."}</p>
      </li>
    );
  }

  const readOnly = a.fromDeployment;
  const credStatus = readOnly
    ? "Set by the deployment"
    : a.hasCredentials
      ? "set (write-only)"
      : "not set";

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    const typed = Object.fromEntries(
      Object.entries(creds).filter(([, v]) => v !== ""),
    );
    const sending = editing && Object.keys(typed).length > 0;
    setBusy(true);
    try {
      const saved = await saveRedirectAccount(vendor, {
        enabled,
        settings: a.settings,
        ...(sending ? { credentials: typed } : {}),
      });
      // Drop what was typed: credentials are write-only.
      setCreds({});
      setEditing(false);
      setEnabledDraft(null);
      onSaved(saved);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function onTest() {
    setCheckError(null);
    setBusy(true);
    try {
      await checkRedirectAccount(vendor);
    } catch (err) {
      setCheckError(errorMessage(err));
    }
    try {
      await onReload();
    } catch (err) {
      setCheckError((prev) => prev ?? errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function onRemove() {
    await deleteRedirectAccount(vendor);
    setRemoving(false);
    onRemoved();
    await onReload().catch(() => undefined);
  }

  return (
    <li className="prov-card" aria-labelledby={titleId}>
      <form
        className="prov-stack"
        onSubmit={(e) => void onSubmit(e)}
        noValidate
        autoComplete="off"
      >
        <div className="prov-card__head">
          <h3 id={titleId} className="prov-card__title">
            {label}
          </h3>
          <Switch
            label="Enabled"
            checked={enabled}
            disabled={readOnly || busy}
            onChange={(e) => setEnabledDraft(e.target.checked)}
          />
        </div>
        <p>
          Credentials: {credStatus}
          {readOnly && " (read-only here)"}
        </p>
        {manual && <p className="prov-muted">{manual}</p>}
        <p className="prov-muted">
          {a.lastCheckAt || a.lastCheckResult
            ? `Last check: ${a.lastCheckResult ?? "—"}${a.lastCheckAt ? ` · ${formatTime(a.lastCheckAt)}` : ""}`
            : "Not checked yet"}
        </p>
        {checkError && (
          <Alert tone="bad" title="Check failed">
            {checkError}
          </Alert>
        )}
        {!readOnly && editing && (
          <div className="prov-stack">
            {keys.map((k) => (
              <Input
                key={k.key}
                id={`redirect-${vendor}-${k.key}`}
                label={k.label}
                type={k.secret ? "password" : "text"}
                autoComplete="new-password"
                value={creds[k.key] ?? ""}
                disabled={busy}
                onChange={(e) =>
                  setCreds((c) => ({ ...c, [k.key]: e.target.value }))
                }
              />
            ))}
          </div>
        )}
        {error && (
          <Alert tone="bad" title="Could not save">
            {error}
          </Alert>
        )}
        <div className="prov-row">
          {!readOnly && (
            <Button type="submit" size="sm" disabled={busy}>
              Save
            </Button>
          )}
          {!readOnly && keys.length > 0 && (
            <Button
              variant="secondary"
              size="sm"
              disabled={busy}
              onClick={() => {
                setCreds({});
                setEditing((v) => !v);
              }}
            >
              {editing
                ? "Keep current credentials"
                : a.hasCredentials
                  ? "Change credentials"
                  : "Set credentials"}
            </Button>
          )}
          <Button
            variant="secondary"
            size="sm"
            icon="activity"
            disabled={busy}
            onClick={() => void onTest()}
          >
            Test
          </Button>
          {!readOnly && a.hasCredentials && (
            <Button
              variant="danger"
              size="sm"
              icon="trash-2"
              disabled={busy}
              onClick={() => setRemoving(true)}
            >
              Remove credentials
            </Button>
          )}
        </div>
      </form>
      {removing && (
        <ConfirmDialog
          title={`Remove the ${label} credentials?`}
          description="Hello stops registering phones with this redirect service until new credentials are set."
          confirmLabel="Remove credentials"
          errorTitle="Could not remove"
          onConfirm={onRemove}
          onClose={() => setRemoving(false)}
        />
      )}
    </li>
  );
}
