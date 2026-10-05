import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";
import {
  deleteDevice,
  errorMessage,
  listDevices,
  listExtensions,
  SIP_USERNAME_PATTERN,
  updateDevice,
  type Device,
  type Extension,
} from "../api";
import {
  bindingsOf,
  createDevice,
  loadLiveDirectory,
  rotateDeviceSecret,
  type IssuedDevice,
  type LiveDirectory,
} from "../api/directory";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import "./directory/directory.css";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; devices: Device[]; extensions: Extension[] };

/** A secret that has just been issued; held only while its dialog is open. */
interface Issued {
  title: string;
  description: string;
  sipUsername: string;
  secret: string;
}

/** A destructive action waiting for confirmation. */
type Pending = { kind: "rotate" | "delete"; device: Device } | null;

/** Drop the secret from a create/rotate response before keeping the device. */
function withoutSecret(issued: IssuedDevice): Device {
  const { id, extensionId, sipUsername, enabled, createdAt, updatedAt } =
    issued;
  return { id, extensionId, sipUsername, enabled, createdAt, updatedAt };
}

const plural = (n: number, one: string, many: string) =>
  `${n} ${n === 1 ? one : many}`;

/** Devices: SIP credentials, their registration, and the one-time secret. */
export function Devices() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [actionError, setActionError] = useState<string | null>(null);
  const [creating, setCreating] = useState(false);
  const [pending, setPending] = useState<Pending>(null);
  // Component state only: gone when the dialog closes or the page unmounts.
  const [issued, setIssued] = useState<Issued | null>(null);
  const toast = useToast();
  const liveState = usePolling(loadLiveDirectory, LIVE_REFRESH_MS);
  const live: LiveDirectory =
    liveState.status === "loading" ? {} : (liveState.data ?? {});

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

  const extensions = list.status === "ready" ? list.extensions : [];
  const extensionOf = (d: Device) =>
    extensions.find((e) => String(e.id) === String(d.extensionId));

  async function onToggle(device: Device, enabled: boolean) {
    setActionError(null);
    try {
      replaceDevice(await updateDevice(device.id, { enabled }));
      toast.show(`${device.sipUsername} ${enabled ? "enabled" : "disabled"}.`);
    } catch (err) {
      setActionError(
        `Could not update ${device.sipUsername}: ${errorMessage(err)}`,
      );
    }
  }

  /** Runs a confirmed rotation or delete; a failure stays in the dialog. */
  async function onConfirm({ kind, device }: NonNullable<Pending>) {
    setActionError(null);
    if (kind === "rotate") {
      const rotated = await rotateDeviceSecret(device.id);
      replaceDevice(withoutSecret(rotated));
      setPending(null);
      setIssued({
        title: "Secret rotated",
        description: `${rotated.sipUsername} stops registering until it has the new secret.`,
        sipUsername: rotated.sipUsername,
        secret: rotated.secret,
      });
    } else {
      await deleteDevice(device.id);
      updateDevices((devices) => devices.filter((d) => d.id !== device.id));
      setPending(null);
      toast.show(`Device ${device.sipUsername} deleted.`);
    }
  }

  const columns: TableColumn<Device>[] = [
    { key: "sipUsername", label: "SIP username", mono: true },
    {
      key: "extension",
      label: "Extension",
      render: (d) => {
        const ext = extensionOf(d);
        return ext ? (
          <>
            <span className="az-table__mono">{ext.number}</span>
            <small>{ext.name}</small>
          </>
        ) : (
          "—"
        );
      },
    },
    {
      key: "registration",
      label: "Registration",
      render: (d) => {
        if (!live.bindings) return "—";
        const n = bindingsOf(live.bindings, d.sipUsername).length;
        return n > 0 ? (
          <Badge tone="good" dot>
            {plural(n, "contact", "contacts")}
          </Badge>
        ) : (
          <Badge tone="outline" dot>
            No contact
          </Badge>
        );
      },
    },
    {
      key: "userAgent",
      label: "User agent",
      render: (d) => (
        <span className="dir-cell-sm">
          {(live.bindings &&
            bindingsOf(live.bindings, d.sipUsername)[0]?.userAgent) ||
            "—"}
        </span>
      ),
    },
    {
      key: "enabled",
      label: "Enabled",
      render: (d) => (
        <Switch
          aria-label={`Enabled: ${d.sipUsername}`}
          checked={d.enabled}
          onChange={(e) => void onToggle(d, e.target.checked)}
        />
      ),
    },
    {
      key: "actions",
      label: "Actions",
      align: "right",
      render: (d) => (
        <div className="dir-actions">
          <Button
            variant="secondary"
            size="sm"
            icon="key-round"
            aria-label={`Rotate secret for ${d.sipUsername}`}
            onClick={() => setPending({ kind: "rotate", device: d })}
          >
            Rotate secret
          </Button>
          <IconButton
            icon="trash-2"
            label={`Delete device ${d.sipUsername}`}
            size={15}
            onClick={() => setPending({ kind: "delete", device: d })}
          />
        </div>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Devices"
        description="SIP credentials. Every device belongs to one extension; secrets are shown once."
        actions={
          <Button
            icon="plus"
            disabled={list.status !== "ready"}
            onClick={() => setCreating(true)}
          >
            New device
          </Button>
        }
      />

      <div className="dir-stack">
        {actionError && <Alert tone="bad">{actionError}</Alert>}
        {list.status === "loading" && <Spinner label="Loading devices…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load devices.">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.devices.length === 0 && (
          <EmptyState
            icon="smartphone"
            title="No devices yet"
            description={
              list.extensions.length === 0
                ? "Every device belongs to an extension. Add an extension first."
                : "A device is a SIP username and secret a phone registers with."
            }
            action={
              list.extensions.length === 0 ? (
                <Link to="/extensions" className="az-btn az-btn--secondary">
                  Open extensions
                </Link>
              ) : (
                <Button icon="plus" onClick={() => setCreating(true)}>
                  New device
                </Button>
              )
            }
          />
        )}
        {list.status === "ready" && list.devices.length > 0 && (
          <Table
            caption="Devices"
            columns={columns}
            rows={list.devices}
            rowKey={(d) => String(d.id)}
          />
        )}
      </div>

      {creating && (
        <NewDevice
          extensions={extensions}
          onClose={() => setCreating(false)}
          onCreated={(created) => {
            updateDevices((devices) => [...devices, withoutSecret(created)]);
            setCreating(false);
            setIssued({
              title: "Device created",
              description: created.sipDomain
                ? `Enter the username and secret in the phone, with ${created.sipDomain} as the domain.`
                : "Enter the username and secret in the phone.",
              sipUsername: created.sipUsername,
              secret: created.secret,
            });
          }}
        />
      )}

      {pending && (
        <ConfirmDialog
          title={
            pending.kind === "rotate"
              ? `Rotate the secret of ${pending.device.sipUsername}?`
              : `Delete ${pending.device.sipUsername}?`
          }
          description={
            pending.kind === "rotate"
              ? "The phone stops registering until it has the new secret."
              : "The phone can no longer register. This cannot be undone."
          }
          confirmLabel={
            pending.kind === "rotate" ? "Rotate secret" : "Delete device"
          }
          confirmIcon={pending.kind === "rotate" ? "key-round" : "trash-2"}
          confirmVariant={pending.kind === "rotate" ? "primary" : "danger"}
          errorTitle={
            pending.kind === "rotate"
              ? "Could not rotate the secret"
              : "Could not delete"
          }
          onConfirm={() => onConfirm(pending)}
          onClose={() => setPending(null)}
        />
      )}

      {issued && (
        <SecretModal
          issued={issued}
          onCopied={toast.show}
          onClose={() => setIssued(null)}
        />
      )}

      {toast.node}
    </section>
  );
}

function NewDevice({
  extensions,
  onClose,
  onCreated,
}: {
  extensions: Extension[];
  onClose: () => void;
  onCreated: (device: IssuedDevice) => void;
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
      onCreated(
        await createDevice({ extensionId: ext.id, sipUsername, enabled }),
      );
    } catch (err) {
      setServerError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title="New device"
      description="The secret is generated and shown once, after the device is created."
      onClose={onClose}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          {extensions.length > 0 && (
            <Button type="submit" form="new-device-form" disabled={busy}>
              Create device
            </Button>
          )}
        </>
      }
    >
      {extensions.length === 0 ? (
        <Alert tone="info" title="Add an extension first">
          Every device belongs to one extension.{" "}
          <Link to="/extensions">Open extensions</Link>
        </Alert>
      ) : (
        <form
          id="new-device-form"
          className="dir-stack"
          aria-label="New device"
          onSubmit={(e) => void onSubmit(e)}
          noValidate
        >
          {serverError && (
            <Alert tone="bad" title="Could not create the device.">
              {serverError}
            </Alert>
          )}
          <Select
            id="dev-extension"
            label="Extension"
            value={extensionId}
            error={errors.extension}
            autoFocus
            onChange={(e) => setExtensionId(e.target.value)}
            options={[
              { value: "", label: "Choose…" },
              ...extensions.map((x) => ({
                value: String(x.id),
                label: `${x.number} — ${x.name}`,
              })),
            ]}
          />
          <Input
            id="dev-username"
            label="SIP username"
            mono
            autoComplete="off"
            spellCheck={false}
            placeholder="front-desk-3"
            hint="Letters, digits, dots, underscores or hyphens; up to 64."
            error={errors.sipUsername}
            value={sipUsername}
            onChange={(e) => setSipUsername(e.target.value)}
          />
          <Switch
            label="Enabled"
            hint="A disabled device gets 403 Forbidden to its REGISTER"
            labelPosition="end"
            checked={enabled}
            onChange={(e) => setEnabled(e.target.checked)}
          />
        </form>
      )}
    </Modal>
  );
}

function SecretModal({
  issued,
  onCopied,
  onClose,
}: {
  issued: Issued;
  onCopied: (message: string) => void;
  onClose: () => void;
}) {
  async function onCopy() {
    try {
      if (!navigator.clipboard?.writeText) throw new Error("no clipboard");
      await navigator.clipboard.writeText(issued.secret);
      onCopied("Secret copied.");
    } catch {
      onCopied("Copying is not available here; select the secret and copy it.");
    }
  }

  return (
    <Modal
      title={issued.title}
      description={issued.description}
      onClose={onClose}
      actions={<Button onClick={onClose}>Done</Button>}
    >
      <Alert tone="warn" title="Shown once">
        Copy it into the phone now. It is stored hashed and cannot be shown
        again; rotate it if it is lost.
      </Alert>
      <div className="dir-secret">
        <span className="az-field__label" id="dev-secret-label">
          SIP secret for {issued.sipUsername}
        </span>
        <div className="dir-secret__box">
          <span
            className="dir-secret__value"
            aria-labelledby="dev-secret-label"
            role="textbox"
            aria-readonly="true"
          >
            {issued.secret}
          </span>
          <Button
            variant="secondary"
            size="sm"
            icon="copy"
            autoFocus
            onClick={() => void onCopy()}
          >
            Copy
          </Button>
        </div>
      </div>
    </Modal>
  );
}
