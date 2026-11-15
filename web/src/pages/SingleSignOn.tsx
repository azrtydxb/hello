import { useEffect, useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../api";
import {
  getSSOSettings,
  putSSOSettings,
  testSSOProvider,
  type SSOProvider,
  type SSOProviderInput,
  type SSOProviderName,
  type SSOSettings,
  type SSOTestResult,
} from "../api/sso";
import {
  Alert,
  Badge,
  Button,
  Input,
  PageHeader,
  Spinner,
  Switch,
} from "../design/azrty/components";
import { splitList } from "../forms";
import { useCan } from "../role";
import "./platform/platform.css";

const TITLES: Record<SSOProviderName, string> = {
  google: "Google Workspace",
  entra: "Microsoft Entra ID",
};

/**
 * Single sign-on (/integrations/sso, spec integrations P11): the OIDC
 * providers, sso_required and the break-glass admins. Administrators only.
 */
export function SingleSignOn() {
  const admin = useCan("admin");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Integrations"
        title="Single sign-on"
        description="People sign in with their Google Workspace or Microsoft account through your own OAuth client. Directory-synced users have no local password: single sign-on (or an LDAP bind for Active Directory) is their only way in. Local users and administrators link their account from their profile; nobody is ever matched by email alone."
      />
      {admin ? (
        <Settings />
      ) : (
        <Alert tone="warn" title="Administrators only">
          Your role cannot manage single sign-on.
        </Alert>
      )}
    </section>
  );
}

function Settings() {
  const [settings, setSettings] = useState<SSOSettings | null>(null);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    getSSOSettings()
      .then(setSettings)
      .catch((err: unknown) => setError(errorMessage(err)));
  }, []);

  if (error)
    return (
      <Alert tone="bad" title="Could not load the single sign-on settings">
        {error}
      </Alert>
    );
  if (!settings) return <Spinner label="Loading single sign-on…" />;
  return (
    <div className="pf-stack">
      {!settings.available && (
        <Alert tone="warn" title="Single sign-on is off">
          It needs HELLO_PUBLIC_URL, the address people reach Hello at: the
          providers send the browser back there.
        </Alert>
      )}
      <Policy settings={settings} onSaved={setSettings} />
      {settings.providers.map((p) => (
        <ProviderCard
          key={p.provider}
          provider={p}
          redirectUri={settings.redirectUris[p.provider] ?? ""}
          onSaved={setSettings}
        />
      ))}
      {settings.redirectUris.directoryConsent && (
        <div className="az-card pf-card">
          <h2 className="pf-h3">Google directory consent</h2>
          <p className="pf-head__desc">
            A Google Workspace directory connector that uses your OAuth client
            needs this redirect URI registered on that client too:
          </p>
          <code>{settings.redirectUris.directoryConsent}</code>
        </div>
      )}
    </div>
  );
}

function Policy({
  settings,
  onSaved,
}: {
  settings: SSOSettings;
  onSaved: (s: SSOSettings) => void;
}) {
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const enabledBreakGlass = settings.breakGlass.filter((b) => b.enabled);

  async function toggle(on: boolean) {
    setBusy(true);
    setError(null);
    try {
      onSaved(await putSSOSettings({ ssoRequired: on }));
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
    setBusy(false);
  }

  return (
    <div className="az-card pf-card pf-stack">
      <h2 className="pf-h3">Policy</h2>
      {error && (
        <Alert tone="bad" title="Not saved">
          {error}
        </Alert>
      )}
      <Switch
        label="Require single sign-on"
        labelPosition="end"
        hint="Only break-glass administrators may then use the password form. If the identity provider is down, they sign in locally and can switch this off."
        checked={settings.ssoRequired}
        disabled={busy || !settings.available}
        onChange={(e) => void toggle(e.target.checked)}
      />
      <div>
        <h3 className="az-field__label">Break-glass administrators</h3>
        {enabledBreakGlass.length === 0 ? (
          <p className="az-field__hint">
            None yet. Flag at least one local administrator as break-glass on
            the Users page before requiring single sign-on.
          </p>
        ) : (
          <ul aria-label="Break-glass administrators">
            {settings.breakGlass.map((b) => (
              <li key={b.id}>
                {b.username}{" "}
                {!b.enabled && <Badge tone="neutral">disabled</Badge>}
              </li>
            ))}
          </ul>
        )}
        <p className="az-field__hint">
          The last enabled break-glass administrator cannot be disabled,
          demoted, deleted or unflagged.
        </p>
      </div>
    </div>
  );
}

interface Draft {
  enabled: boolean;
  clientId: string;
  clientSecret: string;
  tenant: string;
  domains: string;
  jit: boolean;
}

function ProviderCard({
  provider,
  redirectUri,
  onSaved,
}: {
  provider: SSOProvider;
  redirectUri: string;
  onSaved: (s: SSOSettings) => void;
}) {
  const name = provider.provider;
  const [d, setD] = useState<Draft>({
    enabled: provider.enabled,
    clientId: provider.clientId,
    clientSecret: "",
    tenant: provider.tenant,
    domains: provider.domains.join(", "),
    jit: provider.jit,
  });
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [test, setTest] = useState<SSOTestResult | null>(null);
  const set = <K extends keyof Draft>(k: K, v: Draft[K]) =>
    setD((prev) => ({ ...prev, [k]: v }));

  async function onSubmit(ev: FormEvent<HTMLFormElement>) {
    ev.preventDefault();
    setBusy(true);
    setErrors({});
    setFormError(null);
    setSaved(false);
    const body: SSOProviderInput = {
      provider: name,
      enabled: d.enabled,
      clientId: d.clientId.trim(),
      jit: d.jit,
    };
    if (name === "entra") body.tenant = d.tenant.trim();
    else body.domains = splitList(d.domains);
    if (d.clientSecret !== "") body.clientSecret = d.clientSecret;
    try {
      onSaved(await putSSOSettings({ providers: [body] }));
      set("clientSecret", "");
      setSaved(true);
    } catch (err: unknown) {
      const byKey: Record<string, string> = {};
      for (const f of fieldErrors(err)) {
        byKey[f.path.replace(/^providers\[0\]\./, "")] = f.message;
      }
      setErrors(byKey);
      setFormError(errorMessage(err));
    }
    setBusy(false);
  }

  async function onTest() {
    setTest(null);
    try {
      setTest(await testSSOProvider(name));
    } catch (err: unknown) {
      setTest({
        ok: false,
        issuer: "",
        keys: 0,
        errorDetail: errorMessage(err),
      });
    }
  }

  const id = (f: string) => `sso-${name}-${f}`;
  return (
    <form
      className="az-card pf-card pf-stack"
      aria-label={TITLES[name]}
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <div className="pf-cardhead">
        <h2 className="pf-h3">{TITLES[name]}</h2>
        <Badge tone={provider.enabled ? "good" : "neutral"}>
          {provider.enabled ? "on" : "off"}
        </Badge>
      </div>
      {formError && (
        <Alert tone="bad" title="Not saved">
          {formError}
        </Alert>
      )}
      {saved && (
        <Alert tone="good" title="Saved">
          {TITLES[name]} single sign-on is saved.
        </Alert>
      )}
      {redirectUri && (
        <p className="az-field__hint">
          Register this redirect URI on the OAuth client:{" "}
          <code>{redirectUri}</code>
        </p>
      )}
      <Input
        id={id("client")}
        label={name === "entra" ? "Application (client) id" : "OAuth client id"}
        mono
        value={d.clientId}
        error={errors.clientId}
        onChange={(e) => set("clientId", e.target.value.trim())}
      />
      <Input
        id={id("secret")}
        label="Client secret"
        type="password"
        autoComplete="new-password"
        value={d.clientSecret}
        error={errors.clientSecret}
        hint={
          provider.hasClientSecret
            ? "A secret is stored and never shown. Leave empty to keep it."
            : "Stored sealed and never shown again. Needs the secrets scope."
        }
        onChange={(e) => set("clientSecret", e.target.value)}
      />
      {name === "entra" ? (
        <Input
          id={id("tenant")}
          label="Directory (tenant) id"
          mono
          value={d.tenant}
          error={errors.tenant}
          hint="Your own tenant id. common and organizations are refused: the issuer is pinned to your tenant."
          onChange={(e) => set("tenant", e.target.value.trim())}
        />
      ) : (
        <Input
          id={id("domains")}
          label="Workspace domains"
          mono
          value={d.domains}
          error={errors.domains}
          hint="Comma-separated. Only accounts of these domains (the hd claim), with a verified email, are accepted."
          onChange={(e) => set("domains", e.target.value)}
        />
      )}
      <Switch
        label="Create a user on first sign-in"
        labelPosition="end"
        hint="Off by default: synced users already exist. On creates a user-role account, without a local password, for an unknown member of your domain or tenant."
        checked={d.jit}
        onChange={(e) => set("jit", e.target.checked)}
      />
      <Switch
        label={`Enable ${TITLES[name]} sign-in`}
        labelPosition="end"
        checked={d.enabled}
        onChange={(e) => set("enabled", e.target.checked)}
      />
      {test && (
        <Alert
          tone={test.ok ? "good" : "bad"}
          title={test.ok ? "Provider reachable" : "Provider check failed"}
        >
          {test.ok
            ? `${test.issuer} publishes ${String(test.keys)} signing key(s).`
            : test.errorDetail}
        </Alert>
      )}
      <div style={{ display: "flex", gap: 8 }}>
        <Button type="submit" disabled={busy}>
          Save {TITLES[name]}
        </Button>
        <Button variant="secondary" onClick={() => void onTest()}>
          Test {TITLES[name]}
        </Button>
      </div>
    </form>
  );
}
