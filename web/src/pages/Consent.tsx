import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { errorMessage } from "../api";
import {
  approveConsent,
  type ConsentRequest,
  denyConsent,
  fetchRole,
  getConsentRequest,
  type Role,
  type Scope,
  SCOPE_TEXT,
  SECRETS_OPERATIONS,
} from "../api/ai";
import { HelloLogo } from "../brand";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  Spinner,
  ThemeToggle,
} from "../design/azrty/components";
import "./ai.css";

/** Leaves the console for the client's redirect URI (a seam for tests). */
export const browser = {
  go(url: string) {
    window.location.assign(url);
  },
};

const ROLE_NAME: Record<Role, string> = {
  viewer: "Viewer",
  operator: "Operator",
  admin: "Administrator",
};

/**
 * The OAuth consent screen at /oauth/consent?request=<id>: who is asking
 * (the client id's host, since the name is self-asserted), for which
 * resources and scopes; the user may narrow the scopes before approving.
 */
export function Consent() {
  const [params] = useSearchParams();
  const requestId = params.get("request") ?? "";
  const [state, setState] = useState<
    | { status: "loading" }
    | { status: "error"; message: string }
    | { status: "ready"; request: ConsentRequest; role: Role }
  >({ status: "loading" });
  const [checked, setChecked] = useState<ReadonlySet<Scope>>(new Set());
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (requestId === "") return;
    const controller = new AbortController();
    Promise.all([
      getConsentRequest(requestId, controller.signal),
      fetchRole(controller.signal),
    ])
      .then(([request, role]) => {
        const grantable = request.grantableScopes;
        // Every requested, grantable scope starts checked except secrets.
        setChecked(
          new Set(
            request.scopes.filter(
              (s) => s !== "secrets" && grantable.includes(s),
            ),
          ),
        );
        setState({ status: "ready", request, role });
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setState({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [requestId]);

  // Only what the role may grant (as the server says) is ever sent, whatever the boxes say.
  const granted =
    state.status === "ready"
      ? state.request.grantableScopes.filter((s) => checked.has(s))
      : [];

  async function decide(approve: boolean) {
    setBusy(true);
    setError(null);
    try {
      const { redirect } = approve
        ? await approveConsent(requestId, granted)
        : await denyConsent(requestId);
      browser.go(redirect);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <div className="signin" data-pillar="operate">
      <main id="main" className="signin__main">
        <div className="signin__head">
          <HelloLogo layout="horizontal" size={40} />
          <ThemeToggle />
        </div>
        <section className="signin__body" aria-labelledby="page-title">
          {requestId === "" ? (
            <Alert tone="bad" title="No authorization request">
              This page is opened by an app asking for access. Start again from
              the app.
            </Alert>
          ) : state.status === "loading" ? (
            <Spinner label="Loading the request…" />
          ) : state.status === "error" ? (
            <Alert tone="bad" title="Could not load the request">
              {state.message} The request may have expired; start again from the
              app.
            </Alert>
          ) : (
            <ConsentForm
              request={state.request}
              role={state.role}
              checked={new Set(granted)}
              onToggle={(s, on) =>
                setChecked((prev) => {
                  const next = new Set(prev);
                  if (on) next.add(s);
                  else next.delete(s);
                  return next;
                })
              }
              busy={busy}
              error={error}
              onDecide={(approve) => void decide(approve)}
            />
          )}
        </section>
      </main>
    </div>
  );
}

function ConsentForm({
  request,
  role,
  checked,
  onToggle,
  busy,
  error,
  onDecide,
}: {
  request: ConsentRequest;
  role: Role;
  checked: ReadonlySet<Scope>;
  onToggle: (s: Scope, on: boolean) => void;
  busy: boolean;
  error: string | null;
  onDecide: (approve: boolean) => void;
}) {
  const grantable = request.grantableScopes;
  const host = request.clientHost;
  const scopes = request.scopes.filter((s) => s !== "session");
  return (
    <>
      <div className="signin__intro">
        <span className="signin__eyebrow">Authorize an app</span>
        <h1 id="page-title" className="signin__title">
          {request.clientName || host} wants access to Hello
        </h1>
        <p className="ai-host">
          <span className="ai-host__label">From</span>
          <span className="ai-host__value" data-testid="client-host">
            {host}
          </span>
          {!request.verified && <Badge tone="warn">Unverified</Badge>}
        </p>
        <p className="signin__subtitle">
          The name is chosen by the app; the address above is not. Approve only
          if you started this from an app you trust.
        </p>
      </div>

      {request.resources.length > 0 && (
        <div>
          <h2 className="ai-h">Resources</h2>
          <ul className="ai-list" aria-label="Resources">
            {request.resources.map((r) => (
              <li key={r} className="ai-mono">
                {r}
              </li>
            ))}
          </ul>
        </div>
      )}

      <fieldset className="ai-scopes">
        <legend className="ai-h">Permissions</legend>
        {scopes.map((s) => {
          const allowed = grantable.includes(s);
          return (
            <Checkbox
              key={s}
              name="scope"
              value={s}
              label={s}
              checked={allowed && checked.has(s)}
              disabled={!allowed || busy}
              onChange={(e) => onToggle(s, e.target.checked)}
              hint={
                allowed
                  ? SCOPE_TEXT[s]
                  : `${SCOPE_TEXT[s]} Your role (${ROLE_NAME[role]}) cannot grant this.`
              }
            />
          );
        })}
      </fieldset>

      {scopes.includes("secrets") && grantable.includes("secrets") && (
        <Alert tone="warn" title="secrets shows plaintext credentials">
          With it the app can {SECRETS_OPERATIONS.join("; ")}. Leave it
          unchecked unless the app needs exactly that.
        </Alert>
      )}

      {error && (
        <Alert tone="bad" title="Could not complete the request">
          {error}
        </Alert>
      )}

      <div className="ai-actions">
        <Button
          variant="secondary"
          disabled={busy}
          onClick={() => onDecide(false)}
        >
          Deny
        </Button>
        <Button
          disabled={busy || checked.size === 0}
          onClick={() => onDecide(true)}
        >
          Approve
        </Button>
      </div>
    </>
  );
}
