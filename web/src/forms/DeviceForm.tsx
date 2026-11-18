// A device's settings in one drawer: the fields the Devices page edits in
// its table row (extension, media encryption, key exchange, ICE, enabled).
// The canvas opens it and stages the changed fields in its draft; the
// secret is never part of it (rotating it is on the Devices page).

import { useState, type FormEvent } from "react";
import {
  errorMessage,
  fieldErrors,
  SRTP_KEY_EXCHANGES,
  SRTP_POLICIES,
  type Device,
  type Extension,
  type FieldError,
  type SrtpKeyExchange,
  type SrtpPolicy,
} from "../api";
import {
  Button,
  Drawer,
  Select,
  Switch,
  useRestoreFocus,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { FormAlert } from "../pages/callflow/ui";
import { Can } from "../role";

const FORM_ID = "device-form";
const KEYS = ["extensionId", "srtp", "srtpKeyExchange", "mediaIce", "enabled"];

export function DeviceDrawer({
  device,
  extensions,
  onClose,
  commit,
}: {
  device: Device;
  extensions: Extension[];
  onClose: () => void;
  /** Stages the changed fields (the canvas draft). */
  commit: (body: object) => Promise<void>;
}) {
  useRestoreFocus();
  const [extensionId, setExtensionId] = useState(String(device.extensionId));
  const [srtp, setSrtp] = useState<SrtpPolicy>(device.srtp ?? "optional");
  const [kex, setKex] = useState<SrtpKeyExchange>(
    device.srtpKeyExchange ?? "sdes",
  );
  const [ice, setIce] = useState(device.mediaIce ?? false);
  const [enabled, setEnabled] = useState(device.enabled);
  const [busy, setBusy] = useState(false);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    setUnmatched([]);
    const ext = extensions.find((x) => String(x.id) === extensionId);
    if (!ext) {
      setErrors({ extensionId: "Choose an extension." });
      setFormError("Fix the highlighted fields.");
      return;
    }
    const patch: Record<string, unknown> = {};
    if (String(ext.id) !== String(device.extensionId)) {
      patch.extensionId = ext.id;
    }
    if (srtp !== (device.srtp ?? "optional")) patch.srtp = srtp;
    if (kex !== (device.srtpKeyExchange ?? "sdes")) patch.srtpKeyExchange = kex;
    if (ice !== (device.mediaIce ?? false)) patch.mediaIce = ice;
    if (enabled !== device.enabled) patch.enabled = enabled;
    if (Object.keys(patch).length === 0) {
      onClose();
      return;
    }
    setBusy(true);
    try {
      await commit(patch);
      onClose();
    } catch (err: unknown) {
      const mapped = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Drawer
      title={`Device ${device.sipUsername}`}
      description="The secret is rotated on the Devices page; it is never part of a draft."
      onClose={busy ? undefined : onClose}
      width={480}
      footer={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form={FORM_ID} disabled={busy}>
              Save device
            </Button>
          </Can>
        </>
      }
    >
      <form
        id={FORM_ID}
        className="cf-form"
        aria-label={`Edit device ${device.sipUsername}`}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <Select
          id="device-extension"
          label="Extension"
          value={extensionId}
          error={errors.extensionId}
          onChange={(e) => setExtensionId(e.target.value)}
          options={extensions.map((x) => ({
            value: String(x.id),
            label: `${x.number} — ${x.name}`,
          }))}
        />
        <Select
          id="device-srtp"
          label="Media encryption"
          value={srtp}
          error={errors.srtp}
          options={SRTP_POLICIES}
          onChange={(e) => setSrtp(e.target.value as SrtpPolicy)}
        />
        <Select
          id="device-kex"
          label="Key exchange"
          value={kex}
          error={errors.srtpKeyExchange}
          options={SRTP_KEY_EXCHANGES}
          onChange={(e) => setKex(e.target.value as SrtpKeyExchange)}
        />
        <Switch
          label="ICE"
          hint="Hello offers the device ICE-lite and provisions its ICE setting."
          labelPosition="end"
          checked={ice}
          onChange={(e) => setIce(e.target.checked)}
        />
        <Switch
          label="Enabled"
          hint="A disabled device gets 403 Forbidden to its REGISTER"
          labelPosition="end"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
        <FormAlert message={formError} unmatched={unmatched} />
      </form>
    </Drawer>
  );
}
