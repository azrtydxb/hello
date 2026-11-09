import { useState, type FormEvent } from "react";
import { useNavigate, useSearchParams } from "react-router";
import {
  ApiError,
  errorMessage,
  login,
  loginSecondFactor,
  safeNext,
} from "../api";
import { useAuth } from "../auth";
import { HelloLogo } from "../brand";
import {
  Alert,
  Button,
  Input,
  Logo,
  ThemeToggle,
} from "../design/azrty/components";
import { useControlPlane, type ControlPlane } from "../useControlPlane";

/** Where the API serves its OpenAPI document (public, no session needed). */
export const API_DOCS_PATH = "/api/v1/openapi.json";

const ERROR_ID = "login-error";
const PASSWORD_HINT_ID = "login-password-hint";

function ControlPlaneStatus({ state }: { state: ControlPlane }) {
  if (state.status === "reachable") {
    return (
      <span className="signin__status">
        <span className="az-dot az-dot--pulse" aria-hidden="true" />
        Control plane reachable
      </span>
    );
  }
  return (
    <span className="signin__status signin__status--down">
      <span className="az-dot" aria-hidden="true" />
      {state.status === "checking"
        ? "Checking control plane…"
        : "Control plane unreachable"}
    </span>
  );
}

/** Sign-in screen; on success it returns to the `next` query parameter. */
export function Login() {
  const { refresh } = useAuth();
  const navigate = useNavigate();
  const [params] = useSearchParams();
  const controlPlane = useControlPlane();
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // Set once the password was right for a user with two-factor authentication.
  const [challenge, setChallenge] = useState<string | null>(null);
  const [code, setCode] = useState("");

  async function onCode(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy || challenge === null) return;
    setBusy(true);
    setError(null);
    try {
      await loginSecondFactor(challenge, code.trim());
    } catch (err) {
      setCode("");
      setError(
        err instanceof ApiError && err.status === 401
          ? "The code is incorrect or has expired. Try again, or sign in again."
          : errorMessage(err),
      );
      setBusy(false);
      return;
    }
    await refresh();
    navigate(safeNext(params.get("next")), { replace: true });
  }

  function backToPassword() {
    setChallenge(null);
    setCode("");
    setError(null);
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const result = await login(username, password);
      if (result.mfa === "required") {
        setPassword("");
        setChallenge(result.mfaToken);
        setBusy(false);
        return;
      }
    } catch (err) {
      setPassword("");
      setError(
        err instanceof ApiError && err.status === 401
          ? "The username or password is incorrect."
          : errorMessage(err),
      );
      setBusy(false);
      return;
    }
    await refresh();
    navigate(safeNext(params.get("next")), { replace: true });
  }

  const describedBy = error ? ERROR_ID : undefined;
  // An MCP client's authorization request returns here after sign-in.
  const consent = safeNext(params.get("next")).startsWith("/oauth/consent");

  return (
    <div className="signin" data-pillar="operate">
      <main id="main" className="signin__main">
        <div className="signin__head">
          <HelloLogo layout="horizontal" size={40} />
          <ThemeToggle />
        </div>

        <section className="signin__body" aria-labelledby="page-title">
          <div className="signin__intro">
            <span className="signin__eyebrow">{window.location.host}</span>
            <h1 id="page-title" className="signin__title">
              Welcome back
            </h1>
            <p className="signin__subtitle">
              {consent
                ? "Sign in to review an app's request for access to Hello."
                : "Sign in to manage extensions, trunks, routes and the cluster."}
            </p>
          </div>

          {error && (
            <Alert id={ERROR_ID} tone="bad" title="Sign-in failed">
              {error}
            </Alert>
          )}

          {challenge !== null ? (
            <form
              className="signin__form"
              onSubmit={(e) => void onCode(e)}
              aria-busy={busy || undefined}
              aria-label="Second step"
              noValidate
            >
              <Input
                id="login-code"
                label="Authentication code"
                name="code"
                inputMode="numeric"
                autoComplete="one-time-code"
                autoCapitalize="none"
                spellCheck={false}
                autoFocus
                required
                hint="The 6-digit code from your authenticator app, or one of your recovery codes."
                value={code}
                onChange={(e) => setCode(e.target.value)}
                aria-invalid={error ? true : undefined}
                aria-describedby={describedBy}
              />
              <Button
                type="submit"
                size="lg"
                block
                disabled={busy}
                iconRight={busy ? "loader" : "arrow-right"}
              >
                {busy ? "Verifying…" : "Verify"}
              </Button>
              <Button variant="ghost" block onClick={backToPassword}>
                Use a different account
              </Button>
            </form>
          ) : (
            <form
              className="signin__form"
              onSubmit={(e) => void onSubmit(e)}
              aria-busy={busy || undefined}
              noValidate
            >
              <Input
                id="login-username"
                label="Username"
                name="username"
                autoComplete="username"
                autoCapitalize="none"
                spellCheck={false}
                required
                value={username}
                onChange={(e) => setUsername(e.target.value)}
                aria-invalid={error ? true : undefined}
                aria-describedby={describedBy}
              />
              <Input
                id="login-password"
                label="Password"
                name="password"
                type="password"
                autoComplete="current-password"
                required
                hint="Forgot it? An administrator can reset it."
                value={password}
                onChange={(e) => setPassword(e.target.value)}
                aria-invalid={error ? true : undefined}
                aria-describedby={
                  error ? `${ERROR_ID} ${PASSWORD_HINT_ID}` : PASSWORD_HINT_ID
                }
              />
              <Button
                type="submit"
                size="lg"
                block
                disabled={busy}
                iconRight={busy ? "loader" : "arrow-right"}
              >
                {busy ? "Signing in…" : "Sign in"}
              </Button>
            </form>
          )}
        </section>

        <footer className="signin__foot">
          <span className="signin__maker">
            An Azrty product
            <Logo variant="mark" size={18} />
          </span>
          <span className="signin__foot-group">
            <a href={API_DOCS_PATH}>API docs</a>
            <ControlPlaneStatus state={controlPlane} />
            {controlPlane.status === "reachable" && (
              <span className="signin__version">{controlPlane.version}</span>
            )}
          </span>
        </footer>
      </main>

      <aside className="signin__panel" aria-label="Kuvryn Hello">
        <HelloLogo layout="stacked" size={220} />
        <p className="signin__line">
          Every call explained, every node disposable.
        </p>
      </aside>
    </div>
  );
}
