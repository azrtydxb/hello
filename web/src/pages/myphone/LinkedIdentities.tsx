import { useEffect, useState } from "react";
import { errorMessage } from "../../api";
import {
  linkMyIdentity,
  listMyIdentities,
  listSSOProviders,
  unlinkMyIdentity,
  type Identity,
  type PublicProvider,
  type SSOProviderName,
} from "../../api/sso";
import { Alert, Button } from "../../design/azrty/components";

const NAMES: Record<SSOProviderName, string> = {
  google: "Google",
  entra: "Microsoft",
};

/**
 * The single sign-on identities linked to the signed-in user, with Link
 * and Unlink. Linking is how a local user or an administrator starts
 * signing in with Google or Microsoft: Hello never matches them by email.
 * Renders nothing when no provider is enabled and nothing is linked.
 */
export function LinkedIdentities() {
  const [providers, setProviders] = useState<PublicProvider[] | null>(null);
  const [linked, setLinked] = useState<Identity[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [justLinked] = useState(() =>
    new URLSearchParams(window.location.search).get("sso_linked"),
  );

  // Bumped after an unlink to read the list again.
  const [generation, setGeneration] = useState(0);

  useEffect(() => {
    Promise.all([listSSOProviders(), listMyIdentities()])
      .then(([p, l]) => {
        setProviders(p);
        setLinked(l);
      })
      .catch((err: unknown) => {
        setProviders([]);
        setError(errorMessage(err));
      });
  }, [generation]);

  async function link(p: SSOProviderName) {
    setBusy(true);
    setError(null);
    try {
      const { url } = await linkMyIdentity(p);
      window.location.assign(url);
    } catch (err: unknown) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  async function unlink(p: SSOProviderName) {
    setBusy(true);
    setError(null);
    try {
      await unlinkMyIdentity(p);
      setGeneration((g) => g + 1);
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
    setBusy(false);
  }

  if (providers === null) return null;
  if (providers.length === 0 && linked.length === 0 && !error) return null;
  const linkedNames = new Set(linked.map((i) => i.provider));
  return (
    <div className="az-card pf-stack" aria-label="Single sign-on">
      <h2 className="vm-box__title">Single sign-on</h2>
      {justLinked && justLinked in NAMES && (
        <Alert tone="good" title="Linked">
          You can now sign in with {NAMES[justLinked as SSOProviderName]}.
        </Alert>
      )}
      {error && <Alert tone="bad">{error}</Alert>}
      <ul className="pf-stack" aria-label="Linked accounts">
        {linked.map((i) => (
          <li key={i.provider}>
            <strong>{NAMES[i.provider]}</strong>
            {i.email ? ` (${i.email})` : ""}{" "}
            <Button
              size="sm"
              variant="secondary"
              disabled={busy}
              onClick={() => void unlink(i.provider)}
            >
              Unlink {NAMES[i.provider]}
            </Button>
          </li>
        ))}
        {providers
          .filter((p) => !linkedNames.has(p.provider))
          .map((p) => (
            <li key={p.provider}>
              <Button
                size="sm"
                disabled={busy}
                onClick={() => void link(p.provider)}
              >
                Link {NAMES[p.provider]}
              </Button>
            </li>
          ))}
      </ul>
      <p className="az-field__hint">
        Linking opens the provider's sign-in page. After that you can use the
        Single sign-on button on the sign-in page.
      </p>
    </div>
  );
}
