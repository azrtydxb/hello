import { useState, type FormEvent } from "react";
import {
  ApiError,
  errorMessage,
  fieldErrors,
  type VoicemailEmailAfter,
} from "../../api";
import { AFTER_LABELS, updateProfile } from "../../api/users";
import { useAuth } from "../../auth";
import {
  Alert,
  Button,
  Input,
  PageHeader,
  Select,
  Switch,
  useToast,
} from "../../design/azrty/components";
import { mapFieldErrors } from "../../forms";
import { PasswordForm } from "../ChangePassword";
import { TwoFactorCard } from "../TwoFactor";

/** The user's own name, email, voicemail-email options and password. */
export function MyProfile() {
  const { state, refresh } = useAuth();
  const toast = useToast();
  if (state.status !== "signedIn") return null;
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="My phone"
        title="Profile"
        description={`Signed in as ${state.username}.`}
      />
      <div className="pf-grid2">
        <ProfileForm
          key={state.me.email ?? ""}
          onSaved={async () => {
            await refresh();
            toast.show("Profile saved.");
          }}
        />
        <div className="az-card pf-stack">
          <h2 className="vm-box__title">Password</h2>
          <PasswordForm onDone={() => toast.show("Password changed.")} />
        </div>
        <TwoFactorCard onChange={() => void refresh()} />
      </div>
      {toast.node}
    </section>
  );
}

function ProfileForm({ onSaved }: { onSaved: () => void | Promise<void> }) {
  const { state } = useAuth();
  const me = state.status === "signedIn" ? state.me : null;
  const [name, setName] = useState(me?.displayName ?? "");
  const [email, setEmail] = useState(me?.email ?? "");
  const [notify, setNotify] = useState(me?.voicemailEmail?.notify ?? true);
  const [attach, setAttach] = useState(me?.voicemailEmail?.attach ?? true);
  const [after, setAfter] = useState<VoicemailEmailAfter>(
    me?.voicemailEmail?.after ?? "none",
  );
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setErrors({});
    setFormError(null);
    try {
      await updateProfile({
        displayName: name.trim(),
        email: email.trim(),
        voicemailEmail: { notify, attach, after },
      });
      await onSaved();
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), [
        "displayName",
        "email",
      ]);
      setErrors(byKey);
      if (unmatched.length > 0 || Object.keys(byKey).length === 0)
        setFormError(err instanceof ApiError ? err.message : errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  const canMail = me?.emailDelivery !== false;
  return (
    <form
      onSubmit={(e) => void onSubmit(e)}
      noValidate
      className="az-card pf-stack"
      aria-label="Profile"
    >
      <h2 className="vm-box__title">Details</h2>
      {formError && <Alert tone="bad">{formError}</Alert>}
      <Input
        label="Name"
        value={name}
        error={errors.displayName}
        onChange={(e) => setName(e.target.value)}
      />
      <Input
        label="Email"
        type="email"
        hint="New voicemail messages are emailed here."
        value={email}
        error={errors.email}
        onChange={(e) => setEmail(e.target.value)}
      />
      <fieldset className="pf-stack">
        <legend className="az-field__label">Voicemail to email</legend>
        {!canMail && (
          <p className="az-field__hint">
            Email is not set up on this system yet; ask an administrator.
          </p>
        )}
        <Switch
          label="Email me new voicemail"
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
      <div>
        <Button type="submit" icon="save" disabled={busy}>
          Save profile
        </Button>
      </div>
    </form>
  );
}
