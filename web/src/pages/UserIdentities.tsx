import { useEffect, useState } from "react";
import { errorMessage, type Id } from "../api";
import {
  listUserIdentities,
  unlinkUserIdentity,
  type Identity,
} from "../api/sso";
import { Alert, Button } from "../design/azrty/components";

const NAMES = { google: "Google", entra: "Microsoft" } as const;

/**
 * The single sign-on identities linked to a user, for an administrator: an
 * unlink is how a wrong link is undone. Renders nothing when none is linked.
 */
export function UserIdentities({ userId }: { userId: Id }) {
  const [items, setItems] = useState<Identity[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    listUserIdentities(Number(userId))
      .then(setItems)
      .catch((err: unknown) => setError(errorMessage(err)));
  }, [userId]);

  async function unlink(i: Identity) {
    setBusy(true);
    setError(null);
    try {
      await unlinkUserIdentity(Number(userId), i.provider);
      setItems((prev) => prev.filter((x) => x.provider !== i.provider));
    } catch (err: unknown) {
      setError(errorMessage(err));
    }
    setBusy(false);
  }

  if (items.length === 0 && !error) return null;
  return (
    <fieldset className="pf-stack">
      <legend className="az-field__label">Single sign-on</legend>
      {error && <Alert tone="bad">{error}</Alert>}
      {items.map((i) => (
        <div key={i.provider}>
          {NAMES[i.provider]}
          {i.email ? ` (${i.email})` : ""}{" "}
          <Button
            size="sm"
            variant="secondary"
            disabled={busy}
            onClick={() => void unlink(i)}
          >
            Unlink {NAMES[i.provider]}
          </Button>
        </div>
      ))}
    </fieldset>
  );
}
