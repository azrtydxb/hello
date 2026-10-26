import { Link } from "react-router";
import { useAuth } from "../../auth";
import { Alert, EmptyState, PageHeader } from "../../design/azrty/components";

/** The landing page of a self-service user. */
export function MyPhoneHome() {
  const { state } = useAuth();
  if (state.status !== "signedIn") return null;
  const exts = state.me.extensions ?? [];
  const name = state.me.displayName || state.username;
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="My phone"
        title={`Hello, ${name}`}
        description="Manage your own extension: address book, voicemail, forwarding and call history."
      />
      {exts.length === 0 ? (
        <EmptyState
          icon="phone-off"
          title="No extension linked"
          description="Your account is not linked to an extension yet. Ask an administrator to link it."
        />
      ) : (
        <>
          <ul className="az-card" aria-label="Your extensions">
            {exts.map((x) => (
              <li key={String(x.id)}>
                <span className="media-mono">{x.number}</span> {x.name}
              </li>
            ))}
          </ul>
          <ul>
            <li>
              <Link to="/me/contacts">Address book</Link>
            </li>
            <li>
              <Link to="/me/voicemail">Voicemail</Link>
            </li>
            <li>
              <Link to="/me/forwarding">Call forwarding</Link>
            </li>
            <li>
              <Link to="/me/history">Call history</Link>
            </li>
            <li>
              <Link to="/me/profile">Profile and password</Link>
            </li>
          </ul>
        </>
      )}
      {state.mustChangePassword && (
        <Alert tone="warn">Choose a new password.</Alert>
      )}
    </section>
  );
}
