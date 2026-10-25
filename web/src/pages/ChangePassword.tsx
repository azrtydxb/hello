import { useState, type FormEvent } from "react";
import { ApiError, errorMessage, fieldErrors } from "../api";
import { changePassword } from "../api/users";
import { useAuth } from "../auth";
import { HelloLogo } from "../brand";
import { Alert, Button, Input } from "../design/azrty/components";
import { mapFieldErrors } from "../forms";

/** The password policy the API enforces (10 to 128 characters). */
export const PASSWORD_MIN = 10;

/**
 * Current password, new password and a confirmation. `onDone` runs after the
 * server accepted the change.
 */
export function PasswordForm({
  onDone,
}: {
  onDone: () => void | Promise<void>;
}) {
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [confirm, setConfirm] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setFormError(null);
    const local: Record<string, string> = {};
    if (next.length < PASSWORD_MIN) {
      local.newPassword = `Use at least ${PASSWORD_MIN} characters.`;
    }
    if (confirm !== next) local.confirm = "The two passwords differ.";
    if (Object.keys(local).length > 0) {
      setErrors(local);
      return;
    }
    setErrors({});
    setBusy(true);
    try {
      await changePassword(current, next);
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), [
        "currentPassword",
        "newPassword",
      ]);
      setErrors(byKey);
      if (unmatched.length > 0 || Object.keys(byKey).length === 0) {
        setFormError(
          err instanceof ApiError && err.status === 403
            ? "The password can only be changed from a signed-in browser session."
            : errorMessage(err),
        );
      }
      setBusy(false);
      return;
    }
    setBusy(false);
    setCurrent("");
    setNext("");
    setConfirm("");
    await onDone();
  }

  return (
    <form onSubmit={(e) => void onSubmit(e)} noValidate className="pf-stack">
      {formError && (
        <Alert tone="bad" title="Password not changed">
          {formError}
        </Alert>
      )}
      <Input
        label="Current password"
        type="password"
        autoComplete="current-password"
        value={current}
        error={errors.currentPassword}
        onChange={(e) => setCurrent(e.target.value)}
        required
      />
      <Input
        label="New password"
        type="password"
        autoComplete="new-password"
        value={next}
        hint={`At least ${PASSWORD_MIN} characters.`}
        error={errors.newPassword}
        onChange={(e) => setNext(e.target.value)}
        required
      />
      <Input
        label="Repeat the new password"
        type="password"
        autoComplete="new-password"
        value={confirm}
        error={errors.confirm}
        onChange={(e) => setConfirm(e.target.value)}
        required
      />
      <div>
        <Button type="submit" icon="key-round" disabled={busy}>
          Change password
        </Button>
      </div>
    </form>
  );
}

/** Shown instead of the app while an administrator-set password is in force. */
export function ForcedPasswordChange() {
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
              Choose a new password
            </h1>
            <p className="signin__subtitle">
              An administrator set your current password. Choose your own to
              continue.
            </p>
          </div>
          <PasswordForm onDone={refresh} />
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
