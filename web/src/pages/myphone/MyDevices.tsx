import { useCallback, useEffect, useState, type FormEvent } from "react";
import { ApiError, errorMessage } from "../../api";
import {
  createMyDevice,
  listMyDevices,
  revokeMyDevice,
  type DeviceCredentials,
  type MyDevice,
} from "../../api/clients";
import { updateProfile } from "../../api/users";
import { useAuth } from "../../auth";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  type TableColumn,
  useToast,
} from "../../design/azrty/components";
import { formatTime } from "../../format";
import "../directory/directory.css";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; devices: MyDevice[] };

/** A device's credentials, held only while their dialog is open. */
interface Issued extends DeviceCredentials {
  label: string;
}

/**
 * The signed-in user's web devices (clients-webrtc-mobile-video S-24): the
 * browsers that can ring and place calls for their extension. A device is
 * created and revoked here; its SIP password is derived, shown once, and
 * expires, so a lost one stops working by itself.
 */
export function MyDevices() {
  const { state, refresh } = useAuth();
  const toast = useToast();
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [issued, setIssued] = useState<Issued | null>(null);
  const [revoking, setRevoking] = useState<MyDevice | null>(null);

  const load = useCallback((signal?: AbortSignal) => {
    listMyDevices(signal)
      .then((devices) => setList({ status: "ready", devices }))
      .catch((err: unknown) => {
        if (!signal?.aborted)
          setList({ status: "error", message: errorMessage(err) });
      });
  }, []);

  useEffect(() => {
    const ctl = new AbortController();
    load(ctl.signal);
    return () => ctl.abort();
  }, [load]);

  if (state.status !== "signedIn") return null;
  const me = state.me;
  const extensions = me.extensions ?? [];

  async function onRing(on: boolean) {
    try {
      await updateProfile({ ringWeb: on });
      await refresh();
      toast.show(on ? "Web devices will ring." : "Web devices will not ring.");
    } catch (err) {
      toast.show(errorMessage(err));
    }
  }

  const columns: TableColumn<MyDevice>[] = [
    { key: "label", label: "Name" },
    {
      key: "extension",
      label: "Extension",
      render: (d) => (
        <span className="az-table__mono">{d.extensionNumber}</span>
      ),
    },
    { key: "platform", label: "Browser", render: (d) => d.platform || "—" },
    {
      key: "registered",
      label: "Registered",
      render: (d) =>
        d.registered === null ? (
          "—"
        ) : d.registered ? (
          <Badge tone="good" dot>
            Registered
          </Badge>
        ) : (
          <Badge tone="outline" dot>
            Not registered
          </Badge>
        ),
    },
    {
      key: "lastSeen",
      label: "Last seen",
      render: (d) => (d.lastSeenAt ? formatTime(d.lastSeenAt) : "—"),
    },
    {
      key: "actions",
      label: "Actions",
      align: "right",
      render: (d) => (
        <Button
          variant="secondary"
          size="sm"
          icon="trash-2"
          aria-label={`Revoke ${d.label}`}
          onClick={() => setRevoking(d)}
        >
          Revoke
        </Button>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="My phone"
        title="Web devices"
        description="Browsers that can take and place calls on your extension. A device's password is made when you add it, expires on its own and is never stored; revoke a device to stop it at once."
      />
      <div className="dir-stack">
        <div className="az-card pf-stack">
          <h2 className="vm-box__title">Ringing</h2>
          <Switch
            label="Ring my web devices when my extension is called"
            checked={me.ringWeb !== false}
            onChange={(e) => void onRing(e.target.checked)}
          />
        </div>

        <AddDevice
          extensions={extensions}
          onAdded={(device, creds) => {
            setIssued({ ...creds, label: device.label });
            load();
          }}
        />

        {list.status === "loading" && <Spinner label="Loading devices…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load your devices">
            {list.message}
          </Alert>
        )}
        {list.status === "ready" && list.devices.length === 0 && (
          <EmptyState
            icon="monitor-smartphone"
            title="No web devices yet"
            description="Add this browser to take calls on your extension without a desk phone."
          />
        )}
        {list.status === "ready" && list.devices.length > 0 && (
          <Table
            caption="Web devices"
            columns={columns}
            rows={list.devices}
            rowKey={(d) => String(d.id)}
          />
        )}
      </div>

      {revoking && (
        <ConfirmDialog
          title={`Revoke ${revoking.label}?`}
          description="The device stops registering and its credentials are refused. You can add it again later."
          confirmLabel="Revoke device"
          onConfirm={async () => {
            await revokeMyDevice(revoking.id);
            setRevoking(null);
            toast.show(`${revoking.label} revoked.`);
            load();
          }}
          onClose={() => setRevoking(null)}
        />
      )}
      {issued && (
        <CredentialsModal
          issued={issued}
          onCopied={toast.show}
          onClose={() => setIssued(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

function AddDevice({
  extensions,
  onAdded,
}: {
  extensions: { id: string | number; number: string; name: string }[];
  onAdded: (device: MyDevice, creds: DeviceCredentials) => void;
}) {
  const [label, setLabel] = useState("This browser");
  const [extensionId, setExtensionId] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setError(null);
    try {
      const made = await createMyDevice({
        label: label.trim(),
        platform: navigator.userAgent.slice(0, 64),
        ...(extensionId ? { extensionId } : {}),
      });
      onAdded(made.device, made);
    } catch (err) {
      setError(
        err instanceof ApiError && err.code === "device_limit"
          ? "You have the most devices allowed. Revoke one first."
          : errorMessage(err),
      );
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={(e) => void onSubmit(e)}
      noValidate
      className="az-card pf-stack"
      aria-label="Add a web device"
    >
      <h2 className="vm-box__title">Add a web device</h2>
      {error && <Alert tone="bad">{error}</Alert>}
      <Input
        label="Name"
        hint="Shown in this list, for example the computer and browser."
        value={label}
        onChange={(e) => setLabel(e.target.value)}
      />
      {extensions.length > 1 && (
        <Select
          label="Extension"
          value={extensionId}
          options={[
            { value: "", label: `${extensions[0]?.number} (first)` },
            ...extensions.map((x) => ({
              value: String(x.id),
              label: `${x.number} ${x.name}`,
            })),
          ]}
          onChange={(e) => setExtensionId(e.target.value)}
        />
      )}
      <div>
        <Button type="submit" icon="plus" disabled={busy || !label.trim()}>
          Add web device
        </Button>
      </div>
    </form>
  );
}

function CredentialsModal({
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
      await navigator.clipboard.writeText(issued.sipPassword);
      onCopied("Password copied.");
    } catch {
      onCopied(
        "Copying is not available here; select the password and copy it.",
      );
    }
  }
  return (
    <Modal
      title={`${issued.label} added`}
      description={`These credentials work until ${formatTime(issued.expiresAt)}. The browser softphone refreshes them itself.`}
      onClose={onClose}
      actions={<Button onClick={onClose}>Done</Button>}
    >
      <Alert tone="warn" title="Shown once">
        The password is derived and not stored, so it cannot be shown again.
      </Alert>
      <dl className="dir-secret">
        <dt className="az-field__label">SIP username</dt>
        <dd className="az-table__mono">{issued.sipUsername}</dd>
        <dt className="az-field__label" id="web-secret-label">
          SIP password
        </dt>
        <dd className="dir-secret__box">
          <span
            className="dir-secret__value"
            aria-labelledby="web-secret-label"
            role="textbox"
            aria-readonly="true"
          >
            {issued.sipPassword}
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
        </dd>
        <dt className="az-field__label">Realm</dt>
        <dd className="az-table__mono">{issued.realm}</dd>
        {issued.wssUrl && (
          <>
            <dt className="az-field__label">WebSocket address</dt>
            <dd className="az-table__mono">{issued.wssUrl}</dd>
          </>
        )}
      </dl>
    </Modal>
  );
}
