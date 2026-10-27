import { useEffect, useMemo, useState, type FormEvent } from "react";
import {
  ApiError,
  errorMessage,
  fieldErrors,
  listExtensions,
  type Extension,
  type Id,
  type VoicemailEmailAfter,
} from "../api";
import {
  AFTER_LABELS,
  createUser,
  deleteUser,
  generatePassword,
  listUsers,
  updateUser,
  type User,
  type UserChange,
} from "../api/users";
import {
  getTwoFactorPolicy,
  putTwoFactorPolicy,
  resetUserTwoFactor,
} from "../api/twofactor";
import { useAuth } from "../auth";
import {
  Alert,
  Badge,
  type BadgeTone,
  Button,
  Checkbox,
  ConfirmDialog,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  useToast,
} from "../design/azrty/components";
import { mapFieldErrors } from "../forms";
import { ROLES, useCan, type Role } from "../role";

const ROLE_TONE: Record<Role, BadgeTone> = {
  user: "outline",
  viewer: "neutral",
  operator: "info",
  admin: "warn",
};

const ROLE_HELP =
  "Users manage only their own extension (address book, voicemail, forwarding, history). Viewers read everything; operators also change day-to-day configuration; administrators also manage users, credentials, connected apps and secrets.";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; users: User[]; extensions: Extension[] };

/** Users: create, edit, disable and delete accounts and link them to extensions (admin only). */
export function Users() {
  const isAdmin = useCan("admin");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Platform"
        title="Users"
        description="Who can sign in to Hello, what their role allows and which extensions they manage themselves."
      />
      {isAdmin ? (
        <>
          <TwoFactorPolicyCard />
          <UserList />
        </>
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
  const [editing, setEditing] = useState<User | "new" | null>(null);
  const [deleting, setDeleting] = useState<User | null>(null);
  const [resetting, setResetting] = useState<User | null>(null);
  const [error, setError] = useState<string | null>(null);
  const toast = useToast();

  useEffect(() => {
    const ctl = new AbortController();
    Promise.all([listUsers(ctl.signal), listExtensions(ctl.signal)])
      .then(([users, extensions]) =>
        setList({ status: "ready", users, extensions }),
      )
      .catch((err: unknown) => {
        if (!ctl.signal.aborted)
          setList({ status: "error", message: errorMessage(err) });
      });
    return () => ctl.abort();
  }, []);

  const numbers = useMemo(() => {
    const m = new Map<string, string>();
    if (list.status === "ready")
      for (const e of list.extensions) m.set(String(e.id), e.number);
    return m;
  }, [list]);

  function upsert(u: User) {
    setList((l) => {
      if (l.status !== "ready") return l;
      const known = l.users.some((x) => String(x.id) === String(u.id));
      return {
        ...l,
        users: known
          ? l.users.map((x) => (String(x.id) === String(u.id) ? u : x))
          : [...l.users, u],
      };
    });
  }

  async function toggle(u: User) {
    setError(null);
    try {
      const updated = await updateUser(u.id, { enabled: !u.enabled });
      upsert(updated);
      toast.show(
        `${u.username} is now ${updated.enabled ? "enabled" : "disabled"}.`,
      );
    } catch (err) {
      setError(userError(err));
    }
  }

  if (list.status === "loading") return <Spinner label="Loading users…" />;
  if (list.status === "error")
    return (
      <Alert tone="bad" title="Could not load users">
        {list.message}
      </Alert>
    );

  return (
    <>
      <p className="az-field__hint">{ROLE_HELP}</p>
      <p>
        <Button icon="user-plus" onClick={() => setEditing("new")}>
          Add user
        </Button>
      </p>
      {error && (
        <Alert tone="bad" title="Not changed">
          {error}
        </Alert>
      )}
      {list.users.length === 0 ? (
        <EmptyState icon="user-cog" title="No users" />
      ) : (
        <Table
          caption="Users"
          rows={list.users}
          rowKey={(u) => String(u.id)}
          columns={[
            {
              key: "username",
              label: "Login",
              render: (u) => (
                <>
                  {u.username}
                  {u.displayName && (
                    <span className="az-field__hint"> {u.displayName}</span>
                  )}
                </>
              ),
            },
            {
              key: "role",
              label: "Role",
              primary: false,
              render: (u) => <Badge tone={ROLE_TONE[u.role]}>{u.role}</Badge>,
            },
            {
              key: "ext",
              label: "Extensions",
              primary: false,
              render: (u) =>
                u.extensionIds.length === 0
                  ? "—"
                  : u.extensionIds
                      .map((id) => numbers.get(String(id)) ?? id)
                      .join(", "),
            },
            {
              key: "email",
              label: "Email",
              primary: false,
              render: (u) => u.email || "—",
            },
            {
              key: "state",
              label: "State",
              primary: false,
              render: (u) =>
                !u.enabled ? (
                  <Badge tone="bad">Disabled</Badge>
                ) : u.mustChangePassword ? (
                  <Badge tone="warn">Must change password</Badge>
                ) : (
                  <Badge tone="good">Active</Badge>
                ),
            },
            {
              key: "2fa",
              label: "2FA",
              primary: false,
              render: (u) =>
                u.totpEnabled ? (
                  <Badge tone="good">On</Badge>
                ) : u.totpRequired ? (
                  <Badge tone="warn">Required</Badge>
                ) : (
                  "—"
                ),
            },
            {
              key: "actions",
              label: "Actions",
              primary: false,
              render: (u) => (
                <span className="az-row-actions">
                  <Button
                    size="sm"
                    variant="secondary"
                    aria-label={`Edit ${u.username}`}
                    onClick={() => setEditing(u)}
                  >
                    Edit
                  </Button>
                  {u.totpEnabled && (
                    <Button
                      size="sm"
                      variant="secondary"
                      aria-label={`Reset 2FA for ${u.username}`}
                      onClick={() => setResetting(u)}
                    >
                      Reset 2FA
                    </Button>
                  )}
                  {u.username !== me && (
                    <>
                      <Button
                        size="sm"
                        variant="secondary"
                        aria-label={`${u.enabled ? "Disable" : "Enable"} ${u.username}`}
                        onClick={() => void toggle(u)}
                      >
                        {u.enabled ? "Disable" : "Enable"}
                      </Button>
                      <Button
                        size="sm"
                        variant="danger"
                        aria-label={`Delete ${u.username}`}
                        onClick={() => setDeleting(u)}
                      >
                        Delete
                      </Button>
                    </>
                  )}
                </span>
              ),
            },
          ]}
        />
      )}
      {editing && (
        <UserDialog
          user={editing === "new" ? null : editing}
          extensions={list.extensions}
          users={list.users}
          onClose={() => setEditing(null)}
          onSaved={async (u, wasNew) => {
            upsert(u);
            setEditing(null);
            toast.show(
              wasNew ? `${u.username} created.` : `${u.username} saved.`,
            );
            // Changing your own role changes what this console may show.
            if (u.username === me) await refresh();
          }}
        />
      )}
      {resetting && (
        <ConfirmDialog
          title={`Reset two-factor authentication for ${resetting.username}?`}
          description="Their authenticator and recovery codes are removed and their sessions end. They sign in with the password alone, or set 2FA up again if it is required of them."
          confirmLabel="Reset 2FA"
          onClose={() => setResetting(null)}
          onConfirm={async () => {
            try {
              await resetUserTwoFactor(resetting.id);
            } catch (err) {
              setError(userError(err));
              return;
            }
            upsert({ ...resetting, totpEnabled: false });
            toast.show(`2FA reset for ${resetting.username}.`);
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete user ${deleting.username}?`}
          description="The user, their sessions, tokens and personal contacts are removed. Their extension and its voicemail stay."
          confirmLabel="Delete user"
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await deleteUser(deleting.id);
            setList((l) =>
              l.status === "ready"
                ? {
                    ...l,
                    users: l.users.filter(
                      (x) => String(x.id) !== String(deleting.id),
                    ),
                  }
                : l,
            );
            toast.show(`${deleting.username} deleted.`);
          }}
        />
      )}
      {toast.node}
    </>
  );
}

function userError(err: unknown): string {
  if (err instanceof ApiError) {
    if (err.code === "last_admin")
      return "At least one enabled user must remain administrator.";
    if (err.code === "self_disable")
      return "You cannot disable your own account.";
    if (err.code === "self_delete")
      return "You cannot delete your own account.";
    if (err.code === "two_factor_required")
      return "Set up two-factor authentication for your own account first.";
    if (err.code === "last_enrolled_admin")
      return "This is the only administrator with two-factor authentication. Use hello-control user 2fa-reset on the control plane instead.";
  }
  return errorMessage(err);
}

const FIELDS = [
  "username",
  "displayName",
  "email",
  "role",
  "password",
  "extensionIds",
];

/** The create / edit dialog. */
function UserDialog({
  user,
  extensions,
  users,
  onClose,
  onSaved,
}: {
  user: User | null;
  extensions: Extension[];
  users: User[];
  onClose: () => void;
  onSaved: (u: User, wasNew: boolean) => void | Promise<void>;
}) {
  const isNew = user === null;
  const [username, setUsername] = useState(user?.username ?? "");
  const [displayName, setDisplayName] = useState(user?.displayName ?? "");
  const [email, setEmail] = useState(user?.email ?? "");
  const [role, setRole] = useState<Role>(user?.role ?? "user");
  const [password, setPassword] = useState("");
  const [must, setMust] = useState(true);
  const [totpRequired, setTotpRequired] = useState(user?.totpRequired ?? false);
  const [exts, setExts] = useState<Set<string>>(
    new Set((user?.extensionIds ?? []).map(String)),
  );
  const [notify, setNotify] = useState(user?.voicemailEmail.notify ?? true);
  const [attach, setAttach] = useState(user?.voicemailEmail.attach ?? true);
  const [after, setAfter] = useState<VoicemailEmailAfter>(
    user?.voicemailEmail.after ?? "none",
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  // Which user holds each extension, so a taken one reads as such.
  const holder = useMemo(() => {
    const m = new Map<string, string>();
    for (const u of users)
      for (const id of u.extensionIds)
        if (!user || String(u.id) !== String(user.id))
          m.set(String(id), u.username);
    return m;
  }, [users, user]);

  function toggleExt(id: Id, on: boolean) {
    setExts((prev) => {
      const next = new Set(prev);
      if (on) next.add(String(id));
      else next.delete(String(id));
      return next;
    });
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setErrors({});
    setFormError(null);
    setBusy(true);
    // Keep the existing order, new ones after: the first is the primary one.
    const kept = (user?.extensionIds ?? []).filter((id) =>
      exts.has(String(id)),
    );
    const added = extensions
      .map((x) => x.id)
      .filter(
        (id) =>
          exts.has(String(id)) && !kept.some((k) => String(k) === String(id)),
      );
    const extensionIds = [...kept, ...added];
    const voicemailEmail = { notify, attach, after };
    try {
      let saved: User;
      if (isNew) {
        saved = await createUser({
          username: username.trim(),
          displayName: displayName.trim(),
          email: email.trim(),
          role,
          password,
          mustChangePassword: must,
          extensionIds,
          voicemailEmail,
        });
      } else {
        const change: UserChange = {
          displayName: displayName.trim(),
          email: email.trim(),
          role,
          extensionIds,
          voicemailEmail,
        };
        if (totpRequired !== (user.totpRequired ?? false))
          change.totpRequired = totpRequired;
        if (password !== "") {
          change.password = password;
          change.mustChangePassword = must;
        }
        saved = await updateUser(user.id, change);
      }
      await onSaved(saved, isNew);
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), FIELDS);
      setErrors(byKey);
      if (unmatched.length > 0 || Object.keys(byKey).length === 0)
        setFormError(
          err instanceof ApiError &&
            err.status === 409 &&
            err.code === "conflict"
            ? err.message
            : userError(err),
        );
      setBusy(false);
    }
  }

  return (
    <Modal
      title={isNew ? "Add user" : `Edit ${user.username}`}
      onClose={onClose}
      width={560}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="user-form" disabled={busy}>
            {isNew ? "Create user" : "Save"}
          </Button>
        </>
      }
    >
      <form
        id="user-form"
        onSubmit={(e) => void onSubmit(e)}
        noValidate
        className="pf-stack"
      >
        {formError && (
          <Alert tone="bad" title="Not saved">
            {formError}
          </Alert>
        )}
        <Input
          label="Login"
          hint="A username or an email address."
          value={username}
          disabled={!isNew}
          error={errors.username}
          autoComplete="off"
          autoFocus={isNew}
          onChange={(e) => setUsername(e.target.value)}
        />
        <Input
          label="Name"
          value={displayName}
          error={errors.displayName}
          onChange={(e) => setDisplayName(e.target.value)}
        />
        <Input
          label="Email"
          type="email"
          hint="Voicemail notifications are sent here."
          value={email}
          error={errors.email}
          onChange={(e) => setEmail(e.target.value)}
        />
        <Select
          label="Role"
          value={role}
          error={errors.role}
          options={ROLES.map((r) => ({
            value: r,
            label: r === "user" ? "user (extension self-service)" : r,
          }))}
          onChange={(e) => setRole(e.target.value as Role)}
        />
        <fieldset className="pf-stack">
          <legend className="az-field__label">Linked extensions</legend>
          <p className="az-field__hint">
            The user manages the address book, voicemail and forwarding of these
            extensions. The first one receives the personal contacts on its
            phones. An extension belongs to one user.
          </p>
          {extensions.length === 0 && (
            <p className="az-field__hint">There are no extensions yet.</p>
          )}
          {extensions.map((x) => (
            <Checkbox
              key={String(x.id)}
              label={`${x.number} ${x.name}`}
              hint={
                holder.has(String(x.id))
                  ? `Linked to ${holder.get(String(x.id))}`
                  : undefined
              }
              checked={exts.has(String(x.id))}
              disabled={holder.has(String(x.id))}
              onChange={(e) => toggleExt(x.id, e.target.checked)}
            />
          ))}
          {errors.extensionIds && (
            <span className="az-field__error">{errors.extensionIds}</span>
          )}
        </fieldset>
        <Input
          label={isNew ? "Initial password" : "Reset password"}
          type="text"
          autoComplete="off"
          spellCheck={false}
          hint={
            isNew
              ? "At least 10 characters. Hand it to the user; they choose their own at first sign-in."
              : "Leave empty to keep the current password."
          }
          value={password}
          error={errors.password}
          onChange={(e) => setPassword(e.target.value)}
        />
        <div>
          <Button
            variant="secondary"
            size="sm"
            icon="dices"
            onClick={() => setPassword(generatePassword())}
          >
            Generate a password
          </Button>
        </div>
        {(isNew || password !== "") && (
          <Switch
            label="Must choose a new password at next sign-in"
            checked={must}
            onChange={(e) => setMust(e.target.checked)}
          />
        )}
        {!isNew && (
          <Switch
            label="Require two-factor authentication"
            hint="The user must set up an authenticator at next sign-in."
            checked={totpRequired}
            onChange={(e) => setTotpRequired(e.target.checked)}
          />
        )}
        <fieldset className="pf-stack">
          <legend className="az-field__label">Voicemail to email</legend>
          <Switch
            label="Email new voicemail messages"
            checked={notify}
            onChange={(e) => setNotify(e.target.checked)}
          />
          <Switch
            label="Attach the recording"
            checked={attach}
            disabled={!notify}
            onChange={(e) => setAttach(e.target.checked)}
          />
          <Select
            label="After sending"
            value={after}
            disabled={!notify}
            options={(Object.keys(AFTER_LABELS) as VoicemailEmailAfter[]).map(
              (k) => ({
                value: k,
                label: AFTER_LABELS[k],
              }),
            )}
            onChange={(e) => setAfter(e.target.value as VoicemailEmailAfter)}
          />
        </fieldset>
      </form>
    </Modal>
  );
}

/** The roles whose users must use two-factor authentication (admin only). */
function TwoFactorPolicyCard() {
  const [roles, setRoles] = useState<Role[] | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  useEffect(() => {
    const ctl = new AbortController();
    getTwoFactorPolicy(ctl.signal)
      .then((p) => setRoles(p.require2faRoles))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setProblem(errorMessage(err));
      });
    return () => ctl.abort();
  }, []);

  async function toggle(role: Role, on: boolean) {
    if (roles === null || busy) return;
    const next = on ? [...roles, role] : roles.filter((r) => r !== role);
    setBusy(true);
    setProblem(null);
    try {
      const saved = await putTwoFactorPolicy(next);
      setRoles(saved.require2faRoles);
      toast.show("Two-factor policy saved.");
    } catch (err) {
      setProblem(userError(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <fieldset className="az-card pf-stack" aria-label="Two-factor policy">
      <legend className="az-field__label">Two-factor policy</legend>
      <p className="az-field__hint">
        Users of the roles ticked here must set up an authenticator at their
        next sign-in. API tokens and service accounts are not affected. Ticking
        administrator needs two-factor authentication on your own account first.
      </p>
      {problem && (
        <Alert tone="bad" title="Policy not changed">
          {problem}
        </Alert>
      )}
      {roles !== null &&
        ROLES.map((r) => (
          <Checkbox
            key={r}
            label={`Require for ${r}`}
            checked={roles.includes(r)}
            disabled={busy}
            onChange={(e) => void toggle(r, e.target.checked)}
          />
        ))}
      {toast.node}
    </fieldset>
  );
}
