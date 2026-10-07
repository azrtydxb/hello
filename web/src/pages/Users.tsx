import { useEffect, useState } from "react";
import { ApiError, errorMessage } from "../api";
import { listUsers, setUserRole, type User } from "../api/users";
import { useAuth } from "../auth";
import {
  Alert,
  Badge,
  type BadgeTone,
  EmptyState,
  PageHeader,
  Select,
  Spinner,
  Table,
  useToast,
} from "../design/azrty/components";
import { ROLES, useCan, type Role } from "../role";

const ROLE_TONE: Record<Role, BadgeTone> = {
  viewer: "neutral",
  operator: "info",
  admin: "warn",
};

const ROLE_HELP =
  "Viewers read everything; operators also change day-to-day configuration; administrators also manage users, credentials, connected apps and secrets.";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; users: User[] };

/** Users: each management user's role, with an editor (admin only). */
export function Users() {
  const isAdmin = useCan("admin");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Platform"
        title="Users"
        description="Who can sign in to Hello and what their role allows. Add or remove users with hello-control user add."
      />
      {isAdmin ? (
        <UserList />
      ) : (
        <Alert tone="warn" title="Administrators only">
          Your role cannot manage users.
        </Alert>
      )}
    </section>
  );
}

function UserList() {
  const { state: auth, refresh } = useAuth();
  const me = auth.status === "signedIn" ? auth.username : "";
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const toast = useToast();

  useEffect(() => {
    const ctl = new AbortController();
    listUsers(ctl.signal)
      .then((users) => setList({ status: "ready", users }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted)
          setList({ status: "error", message: errorMessage(err) });
      });
    return () => ctl.abort();
  }, []);

  async function onChange(u: User, role: Role) {
    if (role === u.role) return;
    setBusy(String(u.id));
    setError(null);
    try {
      const updated = await setUserRole(u.id, role);
      setList((l) =>
        l.status === "ready"
          ? {
              ...l,
              users: l.users.map((x) =>
                String(x.id) === String(u.id) ? updated : x,
              ),
            }
          : l,
      );
      toast.show(`${u.username} is now ${role}.`);
      // Demoting yourself changes what this console may show.
      if (u.username === me) await refresh();
    } catch (err) {
      setError(
        err instanceof ApiError && err.code === "last_admin"
          ? "At least one user must remain administrator."
          : errorMessage(err),
      );
    } finally {
      setBusy(null);
    }
  }

  if (list.status === "loading") return <Spinner label="Loading users…" />;
  if (list.status === "error")
    return (
      <Alert tone="bad" title="Could not load users">
        {list.message}
      </Alert>
    );
  if (list.users.length === 0)
    return <EmptyState icon="user-cog" title="No users" />;

  return (
    <>
      <p className="az-field__hint">{ROLE_HELP}</p>
      {error && (
        <Alert tone="bad" title="Role not changed">
          {error}
        </Alert>
      )}
      <Table
        caption="Users"
        rows={list.users}
        rowKey={(u) => String(u.id)}
        columns={[
          { key: "username", label: "User", render: (u) => u.username },
          {
            key: "current",
            label: "Current role",
            primary: false,
            render: (u) => <Badge tone={ROLE_TONE[u.role]}>{u.role}</Badge>,
          },
          {
            key: "role",
            label: "Change role",
            primary: false,
            render: (u) => (
              <Select
                aria-label={`Role of ${u.username}`}
                size="sm"
                value={u.role}
                disabled={busy !== null}
                options={ROLES.map((r) => ({ value: r, label: r }))}
                onChange={(e) => void onChange(u, e.target.value as Role)}
              />
            ),
          },
        ]}
      />
      {toast.node}
    </>
  );
}
