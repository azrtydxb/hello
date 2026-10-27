import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiError, errorMessage, type TwoFactorState } from "../api";
import {
  disableTwoFactor,
  enableTwoFactor,
  getTwoFactor,
  regenerateRecoveryCodes,
  setupTwoFactor,
  type TwoFactorSetup,
} from "../api/twofactor";
import { useAuth } from "../auth";
import { HelloLogo } from "../brand";
import { QRCode } from "../components/QRCode";
import {
  Alert,
  Badge,
  Button,
  CodeBlock,
  Input,
  Spinner,
} from "../design/azrty/components";

/** Fewer recovery codes than this left is worth a warning. */
export const LOW_RECOVERY_CODES = 3;

type Step =
  | { kind: "idle" }
  | { kind: "setup"; setup: TwoFactorSetup }
  | { kind: "codes"; codes: string[] }
  | { kind: "regenerate" }
  | { kind: "disable" };

function codeProblem(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "invalid_code") return "That code is not right.";
    if (err.code === "two_factor_required")
      return "Two-factor authentication is required for your account, so it cannot be turned off.";
  }
  return errorMessage(err);
}

/**
 * The signed-in user's own two-factor authentication: turn it on with a QR
 * code, see and replace the recovery codes, turn it off. `onChange` runs
 * after a change the rest of the console should hear about (the enrolment
 * is done and its recovery codes were acknowledged, or 2FA was turned off).
 */
export function TwoFactorCard({ onChange }: { onChange?: () => void }) {
  const [state, setState] = useState<TwoFactorState | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [step, setStep] = useState<Step>({ kind: "idle" });
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [code, setCode] = useState("");
  const [password, setPassword] = useState("");

  const load = useCallback(async () => {
    try {
      setState(await getTwoFactor());
      setLoadError(null);
    } catch (err) {
      setLoadError(errorMessage(err));
    }
  }, []);

  useEffect(() => {
    // The first read of the state is the effect's own subscription.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    void load();
  }, [load]);

  function reset(next: Step = { kind: "idle" }) {
    setStep(next);
    setCode("");
    setPassword("");
    setError(null);
  }

  async function run(action: () => Promise<void>) {
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      await action();
    } catch (err) {
      setError(codeProblem(err));
    } finally {
      setBusy(false);
    }
  }

  const begin = () =>
    run(async () => {
      reset({ kind: "setup", setup: await setupTwoFactor() });
    });

  const confirm = (e: FormEvent) => {
    e.preventDefault();
    if (step.kind !== "setup") return;
    return run(async () => {
      const codes = await enableTwoFactor(code.trim());
      reset({ kind: "codes", codes });
    });
  };

  const replaceCodes = (e: FormEvent) => {
    e.preventDefault();
    return run(async () => {
      const codes = await regenerateRecoveryCodes(code.trim());
      reset({ kind: "codes", codes });
    });
  };

  const turnOff = (e: FormEvent) => {
    e.preventDefault();
    return run(async () => {
      await disableTwoFactor(password, code.trim());
      reset();
      await load();
      onChange?.();
    });
  };

  async function acknowledged() {
    reset();
    await load();
    onChange?.();
  }

  if (loadError)
    return (
      <div className="az-card pf-stack">
        <h2 className="vm-box__title">Two-factor authentication</h2>
        <Alert tone="bad" title="Could not read the state">
          {loadError}
        </Alert>
      </div>
    );
  if (!state)
    return (
      <div className="az-card pf-stack">
        <Spinner label="Loading…" />
      </div>
    );

  return (
    <section
      className="az-card pf-stack"
      aria-label="Two-factor authentication"
    >
      <h2 className="vm-box__title">
        Two-factor authentication{" "}
        <Badge tone={state.enabled ? "good" : "neutral"}>
          {state.enabled ? "On" : "Off"}
        </Badge>
      </h2>
      {error && (
        <Alert tone="bad" title="Not done">
          {error}
        </Alert>
      )}

      {step.kind === "codes" && (
        <>
          <Alert tone="warn" title="Save your recovery codes">
            Each code works once if you lose your authenticator. They are shown
            only now.
          </Alert>
          <CodeBlock title="Recovery codes" code={step.codes.join("\n")} />
          <div>
            <Button icon="check" onClick={() => void acknowledged()}>
              I have saved them
            </Button>
          </div>
        </>
      )}

      {step.kind === "setup" && (
        <form
          className="pf-stack"
          onSubmit={(e) => void confirm(e)}
          noValidate
          aria-label="Confirm two-factor authentication"
        >
          <p className="az-field__hint">
            Scan the code with an authenticator app, or type the secret into it.
            Then enter the 6-digit code it shows.
          </p>
          <QRCode
            value={step.setup.otpauthUri}
            label="QR code for your authenticator app"
          />
          <Input
            label="Secret"
            mono
            readOnly
            value={step.setup.secret}
            onFocus={(e) => e.target.select()}
          />
          <Input
            label="Code from the app"
            inputMode="numeric"
            autoComplete="one-time-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
          />
          <div className="az-row-actions">
            <Button type="submit" icon="shield-check" disabled={busy}>
              Turn on
            </Button>
            <Button variant="ghost" onClick={() => reset()}>
              Cancel
            </Button>
          </div>
        </form>
      )}

      {step.kind === "regenerate" && (
        <form
          className="pf-stack"
          onSubmit={(e) => void replaceCodes(e)}
          noValidate
          aria-label="Replace recovery codes"
        >
          <p className="az-field__hint">
            The old recovery codes stop working. Enter a code from your app to
            continue.
          </p>
          <Input
            label="Code from the app"
            inputMode="numeric"
            autoComplete="one-time-code"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
          />
          <div className="az-row-actions">
            <Button type="submit" disabled={busy}>
              Replace recovery codes
            </Button>
            <Button variant="ghost" onClick={() => reset()}>
              Cancel
            </Button>
          </div>
        </form>
      )}

      {step.kind === "disable" && (
        <form
          className="pf-stack"
          onSubmit={(e) => void turnOff(e)}
          noValidate
          aria-label="Turn off two-factor authentication"
        >
          <Input
            label="Password"
            type="password"
            autoComplete="current-password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            required
          />
          <Input
            label="Code from the app"
            inputMode="numeric"
            autoComplete="one-time-code"
            hint="Or a recovery code."
            value={code}
            onChange={(e) => setCode(e.target.value)}
            required
          />
          <div className="az-row-actions">
            <Button type="submit" variant="danger" disabled={busy}>
              Turn off
            </Button>
            <Button variant="ghost" onClick={() => reset()}>
              Cancel
            </Button>
          </div>
        </form>
      )}

      {step.kind === "idle" && !state.enabled && (
        <>
          <p className="az-field__hint">
            {state.required
              ? "Your account requires a second sign-in step. Set it up to continue."
              : "Ask for a code from an authenticator app at sign-in, on top of your password."}
          </p>
          <div>
            <Button icon="shield-check" onClick={() => void begin()}>
              Set up
            </Button>
          </div>
        </>
      )}

      {step.kind === "idle" && state.enabled && (
        <>
          <p className="az-field__hint">
            {state.recoveryCodesLeft} recovery{" "}
            {state.recoveryCodesLeft === 1 ? "code" : "codes"} left.
          </p>
          {state.recoveryCodesLeft < LOW_RECOVERY_CODES && (
            <Alert tone="warn" title="Few recovery codes left">
              Replace them before you run out.
            </Alert>
          )}
          <div className="az-row-actions">
            <Button
              variant="secondary"
              onClick={() => reset({ kind: "regenerate" })}
            >
              Replace recovery codes
            </Button>
            {!state.required && (
              <Button
                variant="danger"
                onClick={() => reset({ kind: "disable" })}
              >
                Turn off
              </Button>
            )}
          </div>
        </>
      )}
    </section>
  );
}

/** Shown instead of the app while 2FA is required of the user and not set up. */
export function ForcedTwoFactor() {
  const { refresh, signOut } = useAuth();
  return (
    <div className="signin" data-pillar="operate">
      <main id="main" className="signin__main">
        <div className="signin__head">
          <HelloLogo layout="horizontal" size={40} />
        </div>
        <section className="signin__body" aria-labelledby="page-title">
          <div className="signin__intro">
            <h1 id="page-title" className="signin__title">
              Set up two-factor authentication
            </h1>
            <p className="signin__subtitle">
              An administrator requires a second sign-in step for your account.
              Set it up to continue.
            </p>
          </div>
          <TwoFactorCard onChange={() => void refresh()} />
          <p>
            <Button variant="ghost" onClick={() => void signOut()}>
              Sign out
            </Button>
          </p>
        </section>
      </main>
    </div>
  );
}
