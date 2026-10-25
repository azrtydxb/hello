import { Link } from "react-router";
import { listTokens, type ApiToken } from "../api";
import {
  INTEGRATION_SCOPES,
  listServiceAccounts,
  type ServiceAccount,
} from "../api/ai";
import { usePolling } from "../usePolling";
import {
  Alert,
  Badge,
  EmptyState,
  PageHeader,
  Spinner,
  Table,
  useToast,
} from "../design/azrty/components";
import { useCan } from "../role";
import { ServiceAccounts, Tokens } from "./AIAccess";
import "./platform/platform.css";

const EYEBROW = "Integrations";

function holdsIntegrationScope(scopes: readonly string[] | null | undefined) {
  // A legacy token (no scope list) carries every scope.
  if (!scopes) return true;
  return scopes.some((s) =>
    (INTEGRATION_SCOPES as readonly string[]).includes(s),
  );
}

interface CredentialRow {
  key: string;
  name: string;
  disabled: boolean;
  kind: string;
  scopes: string;
  actsFor: string;
}

function limitText(a: ServiceAccount): string {
  if (!a.extensionScope) return "every extension";
  if (a.extensionScope.length === 0) return "no extension";
  return a.extensionScope.join(", ");
}

async function loadCredentials(signal: AbortSignal) {
  const [accounts, tokens] = await Promise.all([
    listServiceAccounts(signal),
    listTokens(signal),
  ]);
  return { accounts, tokens };
}

/**
 * Integrations overview (/integrations): the credentials that can act for
 * the telephony integrations (calls, events, recordings scopes) and where to
 * manage them. Connectors, webhooks and directory sync are added to this
 * area by their own phases.
 */
export function IntegrationsOverview() {
  const admin = useCan("admin");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow={EYEBROW}
        title="Integrations"
        description="Third parties reach Hello with scoped credentials: calls (click-to-call), events (live call events) and recordings (expiring links)."
      />
      {admin ? (
        <Credentials />
      ) : (
        <Alert tone="warn" title="Administrators only">
          Your role cannot manage integration credentials.
        </Alert>
      )}
    </section>
  );
}

function Credentials() {
  const state = usePolling(loadCredentials, 60_000);
  if (state.status === "loading")
    return <Spinner label="Loading credentials…" />;
  const data = state.status === "ready" ? state.data : state.data;
  if (!data) {
    return (
      <Alert tone="bad" title="Could not load credentials">
        {state.status === "error" ? state.message : ""}
      </Alert>
    );
  }
  const accounts = data.accounts.filter((a) => holdsIntegrationScope(a.scopes));
  const tokens = data.tokens.filter((t: ApiToken) =>
    holdsIntegrationScope(t.scopes),
  );
  const rows: CredentialRow[] = [
    ...accounts.map((a) => ({
      key: `sa-${a.id}`,
      name: a.name,
      disabled: !a.enabled,
      kind: "Service account",
      scopes: a.scopes.join(" "),
      actsFor: limitText(a),
    })),
    ...tokens.map((t) => ({
      key: `pat-${t.id}`,
      name: t.name,
      disabled: false,
      kind: t.kind === "legacy" ? "Legacy token" : "Personal token",
      scopes: t.scopes ? t.scopes.join(" ") : "all",
      actsFor: "the owner's extensions",
    })),
  ];
  return (
    <div className="pf-stack">
      {state.status === "error" && (
        <Alert tone="bad" title="Could not refresh credentials">
          {state.message}
        </Alert>
      )}
      <div className="az-card pf-card pf-card--tight">
        <div className="pf-cardhead">
          <div>
            <h2 className="pf-h3" id="int-credentials">
              Credentials with integration scopes
            </h2>
            <p className="pf-head__desc">
              Manage them on{" "}
              <Link to="/integrations/api-access">API access</Link>. Legacy API
              tokens carry every scope.
            </p>
          </div>
        </div>
        {rows.length === 0 ? (
          <EmptyState
            icon="plug"
            title="No integration credentials"
            description="Create a service account or an API token with the calls, events or recordings scope on API access."
          />
        ) : (
          <Table
            caption="Credentials with integration scopes"
            rows={rows}
            rowKey={(r) => r.key}
            columns={[
              {
                key: "name",
                label: "Name",
                render: (r) => (
                  <>
                    {r.name}
                    {r.disabled && (
                      <>
                        {" "}
                        <Badge tone="outline">disabled</Badge>
                      </>
                    )}
                  </>
                ),
              },
              { key: "kind", label: "Kind", render: (r) => r.kind },
              { key: "scopes", label: "Scopes", render: (r) => r.scopes },
              { key: "acts", label: "Acts for", render: (r) => r.actsFor },
            ]}
          />
        )}
      </div>
    </div>
  );
}

/**
 * API access (/integrations/api-access): service accounts (client
 * credentials, optionally limited to some extensions) and personal tokens
 * for integrations. The same credentials appear on AI access; this page
 * words them for third-party apps.
 */
export function IntegrationsApiAccess() {
  const admin = useCan("admin");
  const toast = useToast();
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow={EYEBROW}
        title="API access"
        description="Scoped credentials for apps that place calls, read call events or fetch recording links. Give each app only the scopes and extensions it needs."
      />
      {admin ? (
        <div className="pf-grid2 pf-grid2--wide">
          <div className="pf-stack" style={{ minWidth: 0 }}>
            <ServiceAccounts
              showToast={toast.show}
              title="Service accounts"
              description="For servers and automations (client credentials). Limit them to the extensions they serve. Secrets are shown once."
              emptyText="Create one for an app that calls Hello without a person."
            />
          </div>
          <div className="pf-stack" style={{ minWidth: 0 }}>
            <Tokens role="admin" showToast={toast.show} />
          </div>
        </div>
      ) : (
        <Alert tone="warn" title="Administrators only">
          Your role cannot manage integration credentials.
        </Alert>
      )}
      {toast.node}
    </section>
  );
}
