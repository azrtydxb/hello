import { useState, type FormEvent } from "react";
import { ApiError, errorMessage, fieldErrors } from "../../api";
import { updateMyExtension, type SelfExtension } from "../../api/self";
import {
  Alert,
  Button,
  Input,
  PageHeader,
  Switch,
  useToast,
} from "../../design/azrty/components";
import { mapFieldErrors } from "../../forms";
import { WithExtension } from "./useMyExtensions";

/** Forwarding, do-not-disturb and the display label of the user's extension. */
export function MyForwarding() {
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="My phone"
        title="Call forwarding"
        description="Do not disturb, forwarding and the name your extension shows."
      />
      <WithExtension>
        {(ext, replace) => <ForwardingForm ext={ext} onSaved={replace} />}
      </WithExtension>
    </section>
  );
}

const KEYS = ["name", "forwardAlways", "forwardBusy", "forwardNoAnswer"];

function ForwardingForm({
  ext,
  onSaved,
}: {
  ext: SelfExtension;
  onSaved: (e: SelfExtension) => void;
}) {
  const [name, setName] = useState(ext.name);
  const [dnd, setDnd] = useState(ext.dnd);
  const [always, setAlways] = useState(ext.forwardAlways);
  const [busy, setBusy] = useState(ext.forwardBusy);
  const [noAnswer, setNoAnswer] = useState(ext.forwardNoAnswer);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const toast = useToast();

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (saving) return;
    setSaving(true);
    setErrors({});
    setFormError(null);
    try {
      const saved = await updateMyExtension(ext.id, {
        name: name.trim(),
        dnd,
        forwardAlways: always.trim(),
        forwardBusy: busy.trim(),
        forwardNoAnswer: noAnswer.trim(),
      });
      onSaved(saved);
      toast.show("Saved.");
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(byKey);
      if (unmatched.length > 0 || Object.keys(byKey).length === 0)
        setFormError(err instanceof ApiError ? err.message : errorMessage(err));
    } finally {
      setSaving(false);
    }
  }

  const hint = "A number such as 102 or +3212345678; empty is off.";
  return (
    <form onSubmit={(e) => void onSubmit(e)} noValidate className="pf-stack">
      {formError && (
        <Alert tone="bad" title="Not saved">
          {formError}
        </Alert>
      )}
      <Input
        label="Display name"
        hint={`Shown for extension ${ext.number} in the directory.`}
        value={name}
        error={errors.name}
        onChange={(e) => setName(e.target.value)}
      />
      <Switch
        label="Do not disturb"
        hint="Callers go to voicemail instead of ringing."
        checked={dnd}
        onChange={(e) => setDnd(e.target.checked)}
      />
      <Input
        label="Forward all calls to"
        hint={hint}
        value={always}
        error={errors.forwardAlways}
        onChange={(e) => setAlways(e.target.value)}
      />
      <Input
        label="Forward when busy to"
        hint={hint}
        value={busy}
        error={errors.forwardBusy}
        onChange={(e) => setBusy(e.target.value)}
      />
      <Input
        label="Forward when unanswered to"
        hint={hint}
        value={noAnswer}
        error={errors.forwardNoAnswer}
        onChange={(e) => setNoAnswer(e.target.value)}
      />
      <div>
        <Button type="submit" icon="save" disabled={saving}>
          Save
        </Button>
      </div>
      {toast.node}
    </form>
  );
}
