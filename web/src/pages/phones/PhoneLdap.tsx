import { useEffect, useState } from "react";
import { errorMessage } from "../../api";
import {
  getPhoneLdap,
  rotatePhoneLdapPassword,
  type Phone,
  type PhoneLdap as PhoneLdapState,
} from "../../api/prov";
import {
  Alert,
  Button,
  ConfirmDialog,
  Modal,
  useToast,
} from "../../design/azrty/components";
import { formatTime } from "../../format";
import { Can } from "../../role";
import { CopyField } from "./ui";

/**
 * The phone's LDAP directory (pbx-gaps S-24 to S-26): whether it is
 * provisioned for it, its bind DN, when it last queried, and the rotation
 * of its password, which is shown once.
 */
export function PhoneLdap({ phoneId }: { phoneId: Phone["id"] }) {
  const [state, setState] = useState<PhoneLdapState | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [confirm, setConfirm] = useState(false);
  const [shown, setShown] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    getPhoneLdap(phoneId, controller.signal)
      .then((s) => {
        setState(s);
        setError(null);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setError(errorMessage(err));
      });
    return () => controller.abort();
  }, [phoneId, reload]);

  const on = state?.served && state.supported && state.groupEnabled;
  return (
    <div className="prov-stack">
      <h2 className="inv-section-title">LDAP directory</h2>
      {error && (
        <Alert tone="bad" title="Could not load the LDAP state">
          {error}
        </Alert>
      )}
      {state && !state.served && (
        <p className="prov-muted">This server does not serve LDAP.</p>
      )}
      {state && state.served && !state.supported && (
        <p className="prov-muted">
          This model has no LDAP directory setting; it keeps its XML book.
        </p>
      )}
      {state && state.served && state.supported && !state.groupEnabled && (
        <p className="prov-muted">
          LDAP is off for this phone&apos;s group. Turn it on in the phone
          group.
        </p>
      )}
      {state && on && (
        <>
          <p className="prov-muted">
            Bind DN <span className="cf-mono">{state.bindDn}</span> on{" "}
            <span className="cf-mono">
              {state.host}:{state.port}
            </span>{" "}
            (LDAPS), base <span className="cf-mono">{state.base}</span>.{" "}
            {state.lastBindAt
              ? `Last queried ${formatTime(state.lastBindAt)}.`
              : "Directory not queried."}
            {state.previousValid
              ? " The previous password still works until the phone fetches its configuration."
              : ""}
          </p>
          <Can min="admin">
            <div className="prov-row">
              <Button
                variant="secondary"
                icon="key-round"
                onClick={() => setConfirm(true)}
              >
                Rotate LDAP password
              </Button>
            </div>
          </Can>
        </>
      )}
      {confirm && (
        <ConfirmDialog
          title="Rotate the LDAP password?"
          description="A new password is generated and shown once. The phone is sent a check-sync to take it; the old password keeps working until it has."
          confirmLabel="Rotate LDAP password"
          confirmIcon="key-round"
          confirmVariant="primary"
          errorTitle="Could not rotate the LDAP password"
          onConfirm={async () => {
            const r = await rotatePhoneLdapPassword(phoneId);
            setConfirm(false);
            setShown(r.password);
            setReload((n) => n + 1);
          }}
          onClose={() => setConfirm(false)}
        />
      )}
      {shown !== null && (
        <Modal
          title="LDAP password"
          description="Shown once. The phone takes it with its configuration."
          onClose={() => setShown(null)}
          actions={<Button onClick={() => setShown(null)}>Done</Button>}
        >
          <CopyField
            id="ldap-password"
            label="LDAP password"
            value={shown}
            what="Password"
            onCopied={toast.show}
            autoFocus
          />
        </Modal>
      )}
      {toast.node}
    </div>
  );
}
