import { useEffect, useState, type FormEvent } from "react";
import { ApiError, errorMessage, fieldErrors } from "../api";
import {
  getSmtp,
  putSmtp,
  sendTestEmail,
  type SmtpSecurity,
  type SmtpSettings,
} from "../api/email";
import {
  Alert,
  Badge,
  Button,
  Input,
  PageHeader,
  Select,
  Spinner,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors } from "../forms";
import { useCan } from "../role";
import "./platform/platform.css";

const SECURITY_OPTIONS = [
  { value: "starttls", label: "STARTTLS (usually port 587)" },
  { value: "tls", label: "TLS (usually port 465)" },
  { value: "none", label: "None (no encryption)" },
];

const DEFAULT_PORT: Record<SmtpSecurity, number> = {
  starttls: 587,
  tls: 465,
  none: 25,
};

const SOURCE_TEXT: Record<SmtpSettings["source"], string> = {
  database: "Saved here",
  environment: "From the deployment's SMTP_* settings",
  none: "Not configured",
};

/** Email: the system SMTP settings voicemail notifications are sent with (admin only). */
export function EmailSettings() {
  const isAdmin = useCan("admin");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Platform"
        title="Email"
        description="The mail server Hello sends voicemail notifications through."
      />
      {isAdmin ? (
        <Loader />
      ) : (
        <Alert tone="warn" title="Administrators only">
          Your role cannot change the email settings.
        </Alert>
      )}
    </section>
  );
}

function Loader() {
  const [settings, setSettings] = useState<SmtpSettings | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    getSmtp(ctl.signal)
      .then(setSettings)
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setError(errorMessage(err));
      });
    return () => ctl.abort();
  }, []);
  if (error)
    return (
      <Alert tone="bad" title="Could not load the email settings">
        {error}
      </Alert>
    );
  if (!settings) return <Spinner label="Loading email settings…" />;
  return <Form initial={settings} />;
}

const KEYS = ["host", "port", "security", "username", "from", "password"];

function Form({ initial }: { initial: SmtpSettings }) {
  const [saved, setSaved] = useState(initial);
  const [host, setHost] = useState(initial.host);
  const [port, setPort] = useState(String(initial.port));
  const [security, setSecurity] = useState<SmtpSecurity>(
    initial.security === "opportunistic" ? "starttls" : initial.security,
  );
  const [username, setUsername] = useState(initial.username);
  const [from, setFrom] = useState(initial.from);
  // The password is never shown; empty keeps the stored one.
  const [password, setPassword] = useState("");
  const [clearPassword, setClearPassword] = useState(false);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setErrors({});
    setFormError(null);
    try {
      const next = await putSmtp({
        host: host.trim(),
        port: Number(port),
        security,
        username: username.trim(),
        from: from.trim(),
        ...(clearPassword
          ? { password: "" }
          : password !== ""
            ? { password }
            : {}),
      });
      setSaved(next);
      setPassword("");
      setClearPassword(false);
      toast.show(
        host.trim() === ""
          ? "Email settings cleared."
          : "Email settings saved.",
      );
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(byKey);
      if (unmatched.length > 0 || Object.keys(byKey).length === 0)
        setFormError(err instanceof ApiError ? err.message : errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="pf-stack">
      <p className="az-field__hint">
        Status:{" "}
        <Badge tone={saved.source === "none" ? "warn" : "good"}>
          {SOURCE_TEXT[saved.source]}
        </Badge>
        {saved.source === "environment" &&
          " Saving settings here replaces them; clearing the host brings them back."}
      </p>
      <form
        onSubmit={(e) => void onSubmit(e)}
        noValidate
        className="az-card pf-stack"
        aria-label="SMTP settings"
      >
        {formError && (
          <Alert tone="bad" title="Not saved">
            {formError}
          </Alert>
        )}
        <Input
          label="SMTP host"
          hint="Leave empty and save to remove the settings."
          value={host}
          error={errors.host}
          autoComplete="off"
          onChange={(e) => setHost(e.target.value)}
        />
        <Select
          label="Security"
          value={security}
          error={errors.security}
          options={SECURITY_OPTIONS}
          onChange={(e) => {
            const s = e.target.value as SmtpSecurity;
            // Follow the usual port while it still is the usual one.
            if (port === String(DEFAULT_PORT[security]))
              setPort(String(DEFAULT_PORT[s]));
            setSecurity(s);
          }}
        />
        <Input
          label="Port"
          inputMode="numeric"
          value={port}
          error={errors.port}
          onChange={(e) => setPort(e.target.value)}
        />
        <Input
          label="Username"
          hint="Empty for a server that needs no sign-in."
          value={username}
          error={errors.username}
          autoComplete="off"
          onChange={(e) => setUsername(e.target.value)}
        />
        <Input
          label="Password"
          type="password"
          autoComplete="new-password"
          placeholder={saved.passwordSet ? "•••••••• (stored)" : ""}
          hint={
            saved.passwordSet
              ? "A password is stored and never shown. Leave empty to keep it."
              : "No password is stored."
          }
          value={password}
          error={errors.password}
          disabled={clearPassword}
          onChange={(e) => setPassword(e.target.value)}
        />
        {saved.passwordSet && (
          <label className="az-check">
            <input
              type="checkbox"
              className="az-check__input"
              checked={clearPassword}
              onChange={(e) => setClearPassword(e.target.checked)}
            />
            <span className="az-check__box" />
            <span className="az-check__text">
              <span className="az-check__label">
                Remove the stored password
              </span>
            </span>
          </label>
        )}
        <Input
          label="From address"
          type="email"
          hint="The sender of every notification, such as pbx@example.com."
          value={from}
          error={errors.from}
          onChange={(e) => setFrom(e.target.value)}
        />
        <div>
          <Button type="submit" icon="save" disabled={busy}>
            Save
          </Button>
        </div>
      </form>
      <TestEmail disabled={saved.source === "none"} />
      {toast.node}
    </div>
  );
}

function TestEmail({ disabled }: { disabled: boolean }) {
  const [to, setTo] = useState("");
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string } | null>(
    null,
  );

  async function send(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setResult(null);
    try {
      await sendTestEmail(to.trim());
      setResult({ ok: true, text: `A test email was sent to ${to.trim()}.` });
    } catch (err) {
      setResult({ ok: false, text: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={(e) => void send(e)}
      noValidate
      className="az-card pf-stack"
      aria-label="Send a test email"
    >
      <h2 className="vm-box__title">Test</h2>
      <p className="az-field__hint">
        Sends one email now with the settings in force, so you see at once
        whether the server accepts it.
      </p>
      <Input
        label="Send the test to"
        type="email"
        value={to}
        onChange={(e) => setTo(e.target.value)}
      />
      {result && (
        <Alert
          tone={result.ok ? "good" : "bad"}
          title={result.ok ? "Sent" : "Sending failed"}
        >
          {result.text}
        </Alert>
      )}
      <div>
        <Button
          type="submit"
          variant="secondary"
          icon="send"
          disabled={busy || disabled || to.trim() === ""}
        >
          Send test email
        </Button>
      </div>
    </form>
  );
}
