import { useEffect, useState, type FormEvent } from "react";
import {
  createDevice,
  deleteDevice,
  errorMessage,
  listDevices,
  listExtensions,
  rotateDeviceSecret,
  SIP_USERNAME_PATTERN,
  updateDevice,
  type Device,
  type DeviceWithSecret,
  type Extension,
} from "../api";
import { ConfirmButton } from "../components/ConfirmButton";
import { SecretDialog } from "../components/SecretDialog";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; devices: Device[]; extensions: Extension[] };

/** A secret that has just been issued; held only while its dialog is open. */
interface IssuedSecret {
  title: string;
  sipUsername: string;
  secret: string;
}

/** Drop the secret from a create/rotate response before keeping the device. */
function withoutSecret({
  secret: _secret,
  ...device
}: DeviceWithSecret): Device {
  void _secret;
  return device;
}

/** Devices: list, create, enable/disable, rotate secret and delete. */
export function Devices() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [actionError, setActionError] = useState<string | null>(null);
  // Component state only: it is gone when the dialog closes or the page
  // unmounts, and is never written anywhere else.
  const [issued, setIssued] = useState<IssuedSecret | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      listDevices(controller.signal),
      listExtensions(controller.signal),
    ])
      .then(([devices, extensions]) =>
        setList({ status: "ready", devices, extensions }),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  function updateDevices(fn: (devices: Device[]) => Device[]) {
    setList((prev) =>
      prev.status === "ready" ? { ...prev, devices: fn(prev.devices) } : prev,
    );
  }

  function replaceDevice(device: Device) {
    updateDevices((devices) =>
      devices.map((d) => (d.id === device.id ? device : d)),
    );
  }

  async function run(what: string, action: () => Promise<void>) {
    setActionError(null);
    try {
      await action();
    } catch (err) {
      setActionError(`Could not ${what}: ${errorMessage(err)}`);
    }
  }

  const extensions = list.status === "ready" ? list.extensions : [];
  const numberOf = (id: Device["extensionId"]) =>
    extensions.find((e) => e.id === id)?.number ?? "—";

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Devices</h1>
      {list.status === "ready" && (
        <CreateDevice
          extensions={list.extensions}
          onCreated={(created) => {
            updateDevices((devices) => [...devices, withoutSecret(created)]);
            setIssued({
              title: "Device created",
              sipUsername: created.sipUsername,
              secret: created.secret,
            });
          }}
        />
      )}

      <h2 id="devices-list">All devices</h2>
      {actionError && (
        <p role="alert" className="error">
          {actionError}
        </p>
      )}
      {list.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading devices…
        </p>
      )}
      {list.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not load devices.</strong>
          <p>{list.message}</p>
        </div>
      )}
      {list.status === "ready" && list.devices.length === 0 && (
        <p className="muted">No devices yet.</p>
      )}
      {list.status === "ready" && list.devices.length > 0 && (
        <table aria-labelledby="devices-list">
          <thead>
            <tr>
              <th scope="col">SIP username</th>
              <th scope="col">Extension</th>
              <th scope="col">Status</th>
              <th scope="col">
                <span className="visually-hidden">Actions</span>
              </th>
            </tr>
          </thead>
          <tbody>
            {list.devices.map((device) => (
              <tr key={device.id}>
                <th scope="row">
                  <code>{device.sipUsername}</code>
                </th>
                <td>{numberOf(device.extensionId)}</td>
                <td>{device.enabled ? "Enabled" : "Disabled"}</td>
                <td className="row-actions">
                  <button
                    type="button"
                    aria-label={`${device.enabled ? "Disable" : "Enable"} ${device.sipUsername}`}
                    onClick={() =>
                      void run("update the device", async () => {
                        replaceDevice(
                          await updateDevice(device.id, {
                            enabled: !device.enabled,
                          }),
                        );
                      })
                    }
                  >
                    {device.enabled ? "Disable" : "Enable"}
                  </button>
                  <ConfirmButton
                    label="Rotate secret"
                    accessibleLabel={`Rotate secret for ${device.sipUsername}`}
                    prompt={`Replace the secret of ${device.sipUsername}? The phone stops registering until it gets the new one.`}
                    confirmLabel="Rotate"
                    onConfirm={() =>
                      run("rotate the secret", async () => {
                        const rotated = await rotateDeviceSecret(device.id);
                        replaceDevice(withoutSecret(rotated));
                        setIssued({
                          title: "Secret rotated",
                          sipUsername: rotated.sipUsername,
                          secret: rotated.secret,
                        });
                      })
                    }
                  />
                  <ConfirmButton
                    label="Delete"
                    accessibleLabel={`Delete ${device.sipUsername}`}
                    prompt={`Delete ${device.sipUsername}?`}
                    confirmLabel="Delete device"
                    onConfirm={() =>
                      run("delete the device", async () => {
                        await deleteDevice(device.id);
                        updateDevices((devices) =>
                          devices.filter((d) => d.id !== device.id),
                        );
                      })
                    }
                  />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}

      {issued && (
        <SecretDialog
          title={issued.title}
          subject={issued.sipUsername}
          secret={issued.secret}
          onClose={() => setIssued(null)}
        />
      )}
    </section>
  );
}

function CreateDevice({
  extensions,
  onCreated,
}: {
  extensions: Extension[];
  onCreated: (device: DeviceWithSecret) => void;
}) {
  const [extensionId, setExtensionId] = useState("");
  const [sipUsername, setSipUsername] = useState("");
  const [enabled, setEnabled] = useState(true);
  const [errors, setErrors] = useState<{
    extension?: string;
    sipUsername?: string;
  }>({});
  const [serverError, setServerError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  if (extensions.length === 0) {
    return (
      <p className="muted">
        Create an extension first: every device belongs to one.
      </p>
    );
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setServerError(null);
    const ext = extensions.find((x) => String(x.id) === extensionId);
    const found: typeof errors = {};
    if (!ext) found.extension = "Choose an extension.";
    if (!SIP_USERNAME_PATTERN.test(sipUsername)) {
      found.sipUsername =
        "Use 1 to 64 letters, digits, dots, underscores or hyphens.";
    }
    setErrors(found);
    if (!ext || found.sipUsername) return;
    setBusy(true);
    try {
      const created = await createDevice({
        extensionId: ext.id,
        sipUsername,
        enabled,
      });
      setSipUsername("");
      onCreated(created);
    } catch (err) {
      setServerError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      className="inline-form"
      aria-labelledby="new-device"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="new-device">New device</h2>
      <div className="fields">
        <div className="field">
          <label htmlFor="dev-extension">Extension</label>
          <select
            id="dev-extension"
            value={extensionId}
            onChange={(e) => setExtensionId(e.target.value)}
            aria-invalid={errors.extension ? true : undefined}
            aria-describedby={
              errors.extension ? "dev-extension-error" : undefined
            }
          >
            <option value="">Choose…</option>
            {extensions.map((ext) => (
              <option key={ext.id} value={String(ext.id)}>
                {ext.number} — {ext.name}
              </option>
            ))}
          </select>
          {errors.extension && (
            <p id="dev-extension-error" className="field-error">
              {errors.extension}
            </p>
          )}
        </div>
        <div className="field">
          <label htmlFor="dev-username">SIP username</label>
          <input
            id="dev-username"
            value={sipUsername}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setSipUsername(e.target.value)}
            aria-invalid={errors.sipUsername ? true : undefined}
            aria-describedby={
              errors.sipUsername ? "dev-username-error" : "dev-username-hint"
            }
          />
          {errors.sipUsername ? (
            <p id="dev-username-error" className="field-error">
              {errors.sipUsername}
            </p>
          ) : (
            <p id="dev-username-hint" className="hint">
              Letters, digits, <code>.</code> <code>_</code> <code>-</code>; up
              to 64.
            </p>
          )}
        </div>
        <div className="field checkbox">
          <input
            id="dev-enabled"
            type="checkbox"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
          <label htmlFor="dev-enabled">Enabled</label>
        </div>
      </div>
      {serverError && (
        <p role="alert" className="error">
          Could not create the device: {serverError}
        </p>
      )}
      <button type="submit" className="primary" disabled={busy}>
        Create device
      </button>
    </form>
  );
}
