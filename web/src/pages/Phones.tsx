import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";
import {
  errorMessage,
  fieldErrors,
  listDevices,
  listExtensions,
  type Device,
  type Extension,
  type Id,
} from "../api";
import {
  createPhone,
  deletePhone,
  importErrorText,
  importPhones,
  listPhones,
  listTemplates,
  rearmPhone,
  rotatePhoneToken,
  updatePhone,
  VENDORS,
  vendorLabel,
  type ImportReport,
  type IssuedPhone,
  type Phone,
  type PhoneInput,
  type PhonePatch,
  type Template,
  type Vendor,
} from "../api/prov";
import {
  Alert,
  Badge,
  Button,
  Checkbox,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Input,
  Modal,
  PageHeader,
  SegmentedControl,
  Select,
  Spinner,
  Switch,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { mapFieldErrors, type ErrorMap } from "../forms";
import {
  CopyField,
  formatMac,
  manualRedirectStep,
  PhonesTabs,
  RedirectBadge,
  vendorModel,
} from "./phones/ui";
import "./phones/inventory.css";
import { Can } from "../role";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | {
      status: "ready";
      phones: Phone[];
      extensions: Extension[];
      devices: Device[];
      templates: Template[];
      /** When the list was loaded: "stale" is measured from here. */
      loadedAt: number;
    };

type Filter = "all" | "never" | "stale" | "flagged";

const FILTERS: { value: Filter; label: string }[] = [
  { value: "all", label: "All" },
  { value: "never", label: "Never fetched" },
  { value: "stale", label: "Stale" },
  { value: "flagged", label: "Flagged" },
];

// A phone re-checks its configuration every 24 h by default; one that has not
// fetched for twice that is stale. The settings API does not expose the
// resync interval, so the default is assumed here.
const STALE_MS = 48 * 60 * 60 * 1000;

/** A provisioning URL that has just been issued; held only while its dialog is open. */
export interface IssuedUrl {
  title: string;
  description: string;
  vendor: string;
  url: string;
  secretRotated: boolean;
}

/** An action on one phone waiting for confirmation. */
type Pending = { kind: "rotate" | "rearm" | "delete"; phone: Phone } | null;

/** Drop the one-time URL from a create/rotate/re-arm response before keeping the phone. */
export function withoutUrl(issued: IssuedPhone): Phone {
  const phone: Partial<IssuedPhone> = { ...issued };
  delete phone.provisioningUrl;
  delete phone.secretRotated;
  return phone as Phone;
}

/** The phone's name in prose: its label, else its MAC. */
export const phoneName = (p: Phone) => p.label || formatMac(p.mac);

/** The problem flags a phone carries, as badge texts. */
function flagsOf(p: Phone): { text: string; tone: "bad" | "warn" | "info" }[] {
  const out: { text: string; tone: "bad" | "warn" | "info" }[] = [];
  if (p.tokenExposed) out.push({ text: "Token exposed", tone: "bad" });
  if (p.uaMismatch) out.push({ text: "UA mismatch", tone: "warn" });
  if (p.bootReclaimed) out.push({ text: "Boot reclaimed", tone: "warn" });
  if (p.renderError) out.push({ text: "Render error", tone: "bad" });
  return out;
}

const isFlagged = (p: Phone) => flagsOf(p).length > 0;

function matches(p: Phone, filter: Filter, now: number): boolean {
  switch (filter) {
    case "never":
      return p.lastFetchAt === null;
    case "stale": {
      if (p.lastFetchAt === null) return false;
      const at = Date.parse(p.lastFetchAt);
      return !Number.isNaN(at) && now - at > STALE_MS;
    }
    case "flagged":
      return isFlagged(p);
    default:
      return true;
  }
}

/** A phone's flags as badges ("Armed" too while a boot hand-off is armed). */
export function PhoneFlags({ phone }: { phone: Phone }) {
  const flags = flagsOf(phone);
  if (flags.length === 0 && !phone.bootArmed) {
    return <span className="prov-muted">—</span>;
  }
  return (
    <span className="prov-flags">
      {flags.map((f) => (
        <Badge key={f.text} tone={f.tone}>
          {f.text}
        </Badge>
      ))}
      {phone.bootArmed && <Badge tone="info">Armed</Badge>}
    </span>
  );
}

/** What the issued-URL dialog says after a create, rotate or re-arm. */
export function issuedView(
  kind: "create" | "rotate" | "rearm",
  issued: IssuedPhone,
): IssuedUrl {
  const name = issued.label || formatMac(issued.mac);
  const base = {
    vendor: issued.vendor,
    url: issued.provisioningUrl,
    secretRotated: issued.secretRotated === true,
  };
  switch (kind) {
    case "create":
      return {
        ...base,
        title: "Phone added",
        description: `${name} fetches its configuration from this URL.`,
      };
    case "rotate":
      return {
        ...base,
        title: "Token rotated",
        description: `${name} needs this new URL.`,
      };
    default:
      return {
        ...base,
        title: "Phone re-armed",
        description: `${name}'s old token is revoked; its next DHCP boot gets the new one, or enter this URL by hand.`,
      };
  }
}

/** Phones: the provisioned inventory, its flags, and the one-time URLs. */
export function Phones() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [reload, setReload] = useState(0);
  const [filter, setFilter] = useState<Filter>("all");
  const [editing, setEditing] = useState<Phone | "new" | null>(null);
  const [importing, setImporting] = useState(false);
  const [pending, setPending] = useState<Pending>(null);
  // Component state only: gone when the dialog closes or the page unmounts.
  const [issued, setIssued] = useState<IssuedUrl | null>(null);
  const toast = useToast();

  useEffect(() => {
    const controller = new AbortController();
    Promise.all([
      listPhones(controller.signal),
      listExtensions(controller.signal),
      listDevices(controller.signal),
      // The template override is optional; the inventory works without it.
      listTemplates(controller.signal).catch(() => [] as Template[]),
    ])
      .then(([phones, extensions, devices, templates]) =>
        setList({
          status: "ready",
          phones,
          extensions,
          devices,
          templates,
          loadedAt: Date.now(),
        }),
      )
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, [reload]);

  function updatePhones(fn: (phones: Phone[]) => Phone[]) {
    setList((prev) =>
      prev.status === "ready" ? { ...prev, phones: fn(prev.phones) } : prev,
    );
  }

  function replacePhone(phone: Phone) {
    updatePhones((phones) =>
      phones.map((p) => (String(p.id) === String(phone.id) ? phone : p)),
    );
  }

  const ready = list.status === "ready" ? list : null;
  const devices = ready?.devices ?? [];
  const shown = ready
    ? ready.phones.filter((p) => matches(p, filter, ready.loadedAt))
    : [];

  const deviceName = (p: Phone) =>
    devices.find((d) => String(d.id) === String(p.deviceId))?.sipUsername;

  const columns: TableColumn<Phone>[] = [
    {
      key: "mac",
      label: "MAC",
      mono: true,
      render: (p) => <Link to={`/phones/${p.id}`}>{formatMac(p.mac)}</Link>,
    },
    { key: "model", label: "Model", render: (p) => vendorModel(p) },
    {
      key: "extension",
      label: "Extension",
      render: (p) => (
        <>
          <span className="az-table__mono">{p.extensionNumber || "—"}</span>
          <small className="prov-muted">
            {p.deviceId === null ? "Unbound" : (deviceName(p) ?? "")}
          </small>
        </>
      ),
    },
    { key: "label", label: "Label", render: (p) => p.label || "—" },
    {
      key: "lastFetch",
      label: "Last fetch",
      render: (p) =>
        p.lastFetchAt ? (
          <span className="prov-cell-sm">
            {formatTime(p.lastFetchAt)}
            <br />
            <span className="az-table__mono prov-muted">
              {p.lastFetchIp ?? ""}
            </span>
          </span>
        ) : (
          <span className="prov-muted">Never</span>
        ),
    },
    {
      key: "firmware",
      label: "Firmware",
      render: (p) => (
        <span className="prov-cell-sm">{p.firmwareSeen || "—"}</span>
      ),
    },
    {
      key: "redirect",
      label: "Redirect",
      render: (p) => <RedirectBadge status={p.redirectStatus} />,
    },
    { key: "flags", label: "Flags", render: (p) => <PhoneFlags phone={p} /> },
    {
      key: "enabled",
      label: "Enabled",
      render: (p) =>
        p.enabled ? (
          <Badge tone="good">Enabled</Badge>
        ) : (
          <Badge tone="outline">Disabled</Badge>
        ),
    },
    {
      key: "actions",
      label: "Actions",
      align: "right",
      render: (p) => (
        <div className="inv-actions">
          <IconButton
            icon="pencil"
            label={`Edit ${formatMac(p.mac)}`}
            size={15}
            onClick={() => setEditing(p)}
          />
          <Can min="admin">
            <IconButton
              icon="key-round"
              label={`Rotate token for ${formatMac(p.mac)}`}
              size={15}
              onClick={() => setPending({ kind: "rotate", phone: p })}
            />
          </Can>
          <Can min="admin">
            <IconButton
              icon="rotate-ccw"
              label={`Re-arm ${formatMac(p.mac)}`}
              size={15}
              onClick={() => setPending({ kind: "rearm", phone: p })}
            />
          </Can>
          <Can>
            <IconButton
              icon="trash-2"
              label={`Delete ${formatMac(p.mac)}`}
              size={15}
              onClick={() => setPending({ kind: "delete", phone: p })}
            />
          </Can>
        </div>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Phones"
        description="Phones that fetch their configuration from Hello. Provisioning URLs are shown once."
        actions={
          <>
            <Can>
              <Button
                variant="secondary"
                icon="upload"
                disabled={!ready}
                onClick={() => setImporting(true)}
              >
                Import CSV
              </Button>
            </Can>
            <Can>
              <Button
                icon="plus"
                disabled={!ready}
                onClick={() => setEditing("new")}
              >
                New phone
              </Button>
            </Can>
          </>
        }
      />
      <PhonesTabs />

      <div className="prov-stack">
        {list.status === "loading" && <Spinner label="Loading phones…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load phones">
            {list.message}
          </Alert>
        )}
        {ready && ready.phones.length === 0 && (
          <EmptyState
            icon="phone"
            title="No phones yet"
            description="Add a phone by its MAC, or import a CSV of them."
            action={
              <Can>
                <Button icon="plus" onClick={() => setEditing("new")}>
                  New phone
                </Button>
              </Can>
            }
          />
        )}
        {ready && ready.phones.length > 0 && (
          <>
            <SegmentedControl
              aria-label="Filter phones"
              options={FILTERS}
              value={filter}
              onChange={setFilter}
            />
            {shown.length === 0 ? (
              <p className="prov-muted">No phones match this filter.</p>
            ) : (
              <Table
                caption="Phones"
                columns={columns}
                rows={shown}
                rowKey={(p) => String(p.id)}
              />
            )}
          </>
        )}
      </div>

      {editing && ready && (
        <PhoneForm
          phone={editing === "new" ? null : editing}
          extensions={ready.extensions}
          devices={ready.devices}
          templates={ready.templates}
          onClose={() => setEditing(null)}
          onCreated={(created) => {
            updatePhones((phones) => [...phones, withoutUrl(created)]);
            setEditing(null);
            setIssued(issuedView("create", created));
          }}
          onUpdated={(updated) => {
            replacePhone(updated);
            setEditing(null);
            toast.show(`${phoneName(updated)} saved.`);
          }}
        />
      )}

      {pending?.kind === "rotate" && (
        <RotateTokenDialog
          phone={pending.phone}
          onClose={() => setPending(null)}
          onIssued={(p) => {
            replacePhone(withoutUrl(p));
            setPending(null);
            setIssued(issuedView("rotate", p));
          }}
        />
      )}
      {pending?.kind === "rearm" && (
        <RearmDialog
          phone={pending.phone}
          onClose={() => setPending(null)}
          onIssued={(p) => {
            replacePhone(withoutUrl(p));
            setPending(null);
            setIssued(issuedView("rearm", p));
          }}
        />
      )}
      {pending?.kind === "delete" && (
        <DeletePhoneDialog
          phone={pending.phone}
          onClose={() => setPending(null)}
          onDeleted={(p) => {
            updatePhones((phones) =>
              phones.filter((x) => String(x.id) !== String(p.id)),
            );
            setPending(null);
            toast.show(`Phone ${phoneName(p)} deleted.`);
          }}
        />
      )}

      {importing && (
        <ImportModal
          onClose={() => setImporting(false)}
          onImported={(created) => {
            setImporting(false);
            setReload((n) => n + 1);
            toast.show(
              `${created} ${created === 1 ? "phone" : "phones"} imported`,
            );
          }}
        />
      )}

      {issued && (
        <IssuedUrlModal
          issued={issued}
          onCopied={toast.show}
          onClose={() => setIssued(null)}
        />
      )}

      {toast.node}
    </section>
  );
}

/** Rotate a phone's token: rolling by default, or revoking the old one now. */
export function RotateTokenDialog({
  phone,
  onClose,
  onIssued,
}: {
  phone: Phone;
  onClose: () => void;
  onIssued: (issued: IssuedPhone) => void;
}) {
  const [immediate, setImmediate] = useState(false);
  return (
    <ConfirmDialog
      title={`Rotate the token of ${phoneName(phone)}?`}
      description="The phone needs the new provisioning URL. By default the rotation is rolling: the old token keeps working until the phone's first fetch with the new one, or until the grace period ends."
      confirmLabel="Rotate token"
      confirmIcon="key-round"
      confirmVariant="primary"
      errorTitle="Could not rotate the token"
      onConfirm={async () =>
        onIssued(await rotatePhoneToken(phone.id, immediate))
      }
      onClose={onClose}
    >
      <Checkbox
        label="Revoke the old token now"
        hint="Immediate: the old URL stops working at once, for when it has leaked."
        checked={immediate}
        onChange={(e) => setImmediate(e.target.checked)}
      />
    </ConfirmDialog>
  );
}

/** Re-arm a phone: revoke its token now and arm one more DHCP boot hand-off. */
export function RearmDialog({
  phone,
  onClose,
  onIssued,
}: {
  phone: Phone;
  onClose: () => void;
  onIssued: (issued: IssuedPhone) => void;
}) {
  return (
    <ConfirmDialog
      title={`Re-arm ${phoneName(phone)}?`}
      description="Re-arming revokes the current token at once and arms one more DHCP boot hand-off: the phone's next boot from the boot URL receives a new token. Use it for a factory-reset or replaced phone."
      confirmLabel="Re-arm"
      confirmIcon="rotate-ccw"
      confirmVariant="primary"
      errorTitle="Could not re-arm the phone"
      onConfirm={async () => onIssued(await rearmPhone(phone.id))}
      onClose={onClose}
    />
  );
}

/** Delete a phone after confirmation. */
export function DeletePhoneDialog({
  phone,
  onClose,
  onDeleted,
}: {
  phone: Phone;
  onClose: () => void;
  onDeleted: (phone: Phone) => void;
}) {
  return (
    <ConfirmDialog
      title={`Delete ${phoneName(phone)}?`}
      description="Its provisioning URL stops working. The bound device and extension stay. This cannot be undone."
      confirmLabel="Delete phone"
      errorTitle="Could not delete"
      onConfirm={async () => {
        await deletePhone(phone.id);
        onDeleted(phone);
      }}
      onClose={onClose}
    />
  );
}

/** The provisioning URL, shown once, with the vendor's manual step if any. */
export function IssuedUrlModal({
  issued,
  onCopied,
  onClose,
}: {
  issued: IssuedUrl;
  onCopied: (message: string) => void;
  onClose: () => void;
}) {
  const step = manualRedirectStep(issued.vendor);
  return (
    <Modal
      title={issued.title}
      description={issued.description}
      onClose={onClose}
      actions={<Button onClick={onClose}>Done</Button>}
    >
      <div className="prov-stack">
        <Alert tone="warn" title="Shown once">
          The URL carries the phone&apos;s token. It is stored hashed and cannot
          be shown again; rotate the token if it is lost.
        </Alert>
        {issued.secretRotated && (
          <Alert tone="info" title="Device secret rotated">
            The bound device&apos;s secret was rotated; re-provision or re-type
            any phone already using it.
          </Alert>
        )}
        <CopyField
          id="issued-url"
          label="Provisioning URL"
          value={issued.url}
          what="URL"
          onCopied={onCopied}
          autoFocus
        />
        {step && (
          <Alert tone="info" title="Manual step">
            {step}
          </Alert>
        )}
      </div>
    </Modal>
  );
}

const FORM_FIELDS = [
  "mac",
  "vendor",
  "model",
  "serial",
  "label",
  "extensionId",
  "deviceId",
  "blf",
  "templateId",
  "enabled",
] as const;

const NEW_DEVICE = "new";

function PhoneForm({
  phone,
  extensions,
  devices,
  templates,
  onClose,
  onCreated,
  onUpdated,
}: {
  phone: Phone | null;
  extensions: Extension[];
  devices: Device[];
  templates: Template[];
  onClose: () => void;
  onCreated: (phone: IssuedPhone) => void;
  onUpdated: (phone: Phone) => void;
}) {
  const editing = phone !== null;
  const [mac, setMac] = useState("");
  const [vendor, setVendor] = useState<string>(phone?.vendor ?? "yealink");
  const [model, setModel] = useState(phone?.model ?? "");
  const [serial, setSerial] = useState(phone?.serial ?? "");
  const [label, setLabel] = useState(phone?.label ?? "");
  const [extensionId, setExtensionId] = useState(
    phone?.extensionId == null ? "" : String(phone.extensionId),
  );
  const originalDevice = phone?.deviceId == null ? "" : String(phone.deviceId);
  const [deviceId, setDeviceId] = useState(
    editing ? originalDevice : NEW_DEVICE,
  );
  const [blf, setBlf] = useState<string[]>(phone?.blf ?? []);
  const [blfPick, setBlfPick] = useState("");
  const [templateId, setTemplateId] = useState(
    phone?.templateId == null ? "" : String(phone.templateId),
  );
  const [enabled, setEnabled] = useState(phone?.enabled ?? true);
  const [errors, setErrors] = useState<ErrorMap>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const extDevices = devices.filter(
    (d) => String(d.extensionId) === extensionId,
  );
  const bindsExisting =
    deviceId !== NEW_DEVICE && deviceId !== "" && deviceId !== originalDevice;
  const blfChoices = extensions.filter((x) => !blf.includes(x.number));

  const idOf = (value: string): Id | undefined =>
    [...extensions, ...devices, ...templates]
      .map((x) => x.id)
      .find((v) => String(v) === value);

  function moveBlf(i: number, by: number) {
    setBlf((keys) => {
      const j = i + by;
      if (j < 0 || j >= keys.length) return keys;
      const next = [...keys];
      [next[i], next[j]] = [next[j]!, next[i]!];
      return next;
    });
  }

  /** Only the fields that differ from the phone being edited. */
  function patchFor(p: Phone): PhonePatch {
    const patch: PhonePatch = {};
    if (vendor !== p.vendor) patch.vendor = vendor as Vendor;
    if (model !== p.model) patch.model = model;
    if (serial !== p.serial) patch.serial = serial;
    if (label !== p.label) patch.label = label;
    if (extensionId !== String(p.extensionId ?? "")) {
      const ext = idOf(extensionId);
      if (ext !== undefined) patch.extensionId = ext;
    }
    if (deviceId !== originalDevice) {
      const dev = idOf(deviceId);
      if (dev !== undefined) patch.deviceId = dev;
    }
    if (JSON.stringify(blf) !== JSON.stringify(p.blf)) patch.blf = blf;
    if (templateId !== String(p.templateId ?? "")) {
      patch.templateId = templateId === "" ? null : (idOf(templateId) ?? null);
    }
    if (enabled !== p.enabled) patch.enabled = enabled;
    return patch;
  }

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setFormError(null);
    const found: Record<string, string> = {};
    if (!editing && mac.trim() === "") found.mac = "Enter the phone's MAC.";
    if (model.trim() === "") found.model = "Enter the model.";
    const ext = idOf(extensionId);
    if (ext === undefined) found.extensionId = "Choose an extension.";
    setErrors(found);
    if (Object.keys(found).length > 0 || ext === undefined) return;
    setBusy(true);
    try {
      if (phone) {
        const patch = patchFor(phone);
        onUpdated(
          Object.keys(patch).length === 0
            ? phone
            : await updatePhone(phone.id, patch),
        );
      } else {
        const input: PhoneInput = {
          mac: mac.trim(),
          vendor: vendor as Vendor,
          model: model.trim(),
          label,
          extensionId: ext,
          blf,
          enabled,
        };
        if (serial.trim() !== "") input.serial = serial.trim();
        const dev = deviceId === NEW_DEVICE ? undefined : idOf(deviceId);
        if (dev !== undefined) input.deviceId = dev;
        onCreated(await createPhone(input));
      }
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), [
        ...FORM_FIELDS,
      ]);
      setErrors(byKey);
      if (Object.keys(byKey).length === 0 || unmatched.length > 0) {
        setFormError(
          unmatched.length > 0
            ? unmatched.map((f) => `${f.path}: ${f.message}`).join(" ")
            : errorMessage(err),
        );
      }
      setBusy(false);
    }
  }

  const title = editing ? `Edit ${phoneName(phone)}` : "New phone";

  return (
    <Modal
      title={title}
      description={
        editing
          ? undefined
          : "The provisioning URL is generated and shown once, after the phone is added."
      }
      onClose={onClose}
      width={640}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Can>
            <Button type="submit" form="phone-form" disabled={busy}>
              {editing ? "Save" : "Add phone"}
            </Button>
          </Can>
        </>
      }
    >
      <form
        id="phone-form"
        className="prov-stack"
        aria-label={title}
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        {formError && (
          <Alert
            tone="bad"
            title={
              editing ? "Could not save the phone" : "Could not add the phone"
            }
          >
            {formError}
          </Alert>
        )}
        {!editing && (
          <Input
            id="phone-mac"
            label="MAC"
            mono
            autoFocus
            autoComplete="off"
            spellCheck={false}
            placeholder="80:5e:c0:12:34:56"
            error={errors.mac}
            value={mac}
            onChange={(e) => setMac(e.target.value)}
          />
        )}
        <div className="prov-grid-2">
          <Select
            id="phone-vendor"
            label="Vendor"
            value={vendor}
            error={errors.vendor}
            onChange={(e) => setVendor(e.target.value)}
            options={VENDORS.map((v) => ({ value: v, label: vendorLabel(v) }))}
          />
          <Input
            id="phone-model"
            label="Model"
            placeholder="T54W"
            error={errors.model}
            value={model}
            onChange={(e) => setModel(e.target.value)}
          />
        </div>
        <div className="prov-grid-2">
          <Input
            id="phone-serial"
            label="Serial (optional)"
            mono
            hint="Yealink YMCS and Grandstream GDMS need it for the redirect."
            error={errors.serial}
            value={serial}
            onChange={(e) => setSerial(e.target.value)}
          />
          <Input
            id="phone-label"
            label="Label"
            placeholder="Front desk"
            error={errors.label}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
          />
        </div>
        <div className="prov-grid-2">
          <Select
            id="phone-extension"
            label="Extension"
            value={extensionId}
            error={errors.extensionId}
            onChange={(e) => {
              setExtensionId(e.target.value);
              if (!editing) setDeviceId(NEW_DEVICE);
            }}
            options={[
              { value: "", label: "Choose…" },
              ...extensions.map((x) => ({
                value: String(x.id),
                label: `${x.number} — ${x.name}`,
              })),
            ]}
          />
          <Select
            id="phone-device"
            label="Device"
            value={deviceId}
            error={errors.deviceId}
            onChange={(e) => setDeviceId(e.target.value)}
            options={[
              ...(editing
                ? originalDevice === ""
                  ? [{ value: "", label: "Unbound" }]
                  : extDevices.some((d) => String(d.id) === originalDevice)
                    ? []
                    : [{ value: originalDevice, label: "Current device" }]
                : [{ value: NEW_DEVICE, label: "Create a new device" }]),
              ...extDevices.map((d) => ({
                value: String(d.id),
                label: d.sipUsername,
              })),
            ]}
          />
        </div>
        {bindsExisting && (
          <Alert tone="warn" title="Existing device">
            Binding rotates the device secret; re-provision or re-type any phone
            already using it.
          </Alert>
        )}

        <fieldset className="inv-blf">
          <legend className="az-field__label">BLF keys</legend>
          {blf.length === 0 ? (
            <p className="prov-muted">No BLF keys.</p>
          ) : (
            <ol className="inv-blf__list" aria-label="BLF keys">
              {blf.map((n, i) => (
                <li key={n} className="inv-blf__item">
                  <span className="az-table__mono">{n}</span>
                  <span className="inv-blf__tools">
                    <IconButton
                      icon="arrow-up"
                      label={`Move ${n} up`}
                      size={14}
                      disabled={i === 0}
                      onClick={() => moveBlf(i, -1)}
                    />
                    <IconButton
                      icon="arrow-down"
                      label={`Move ${n} down`}
                      size={14}
                      disabled={i === blf.length - 1}
                      onClick={() => moveBlf(i, 1)}
                    />
                    <IconButton
                      icon="x"
                      label={`Remove ${n}`}
                      size={14}
                      onClick={() =>
                        setBlf((keys) => keys.filter((k) => k !== n))
                      }
                    />
                  </span>
                </li>
              ))}
            </ol>
          )}
          {errors.blf && (
            <p className="az-field__error" role="alert">
              {errors.blf}
            </p>
          )}
          <div className="prov-row">
            <Select
              id="phone-blf-add"
              label="Add BLF key"
              value={blfPick}
              onChange={(e) => setBlfPick(e.target.value)}
              options={[
                { value: "", label: "Choose…" },
                ...blfChoices.map((x) => ({
                  value: x.number,
                  label: `${x.number} — ${x.name}`,
                })),
              ]}
            />
            <Button
              variant="secondary"
              size="sm"
              icon="plus"
              disabled={blfPick === ""}
              onClick={() => {
                setBlf((keys) => [...keys, blfPick]);
                setBlfPick("");
              }}
            >
              Add key
            </Button>
          </div>
        </fieldset>

        {editing && (
          <Select
            id="phone-template"
            label="Template"
            hint="Automatic picks the template by vendor and model."
            value={templateId}
            error={errors.templateId}
            onChange={(e) => setTemplateId(e.target.value)}
            options={[
              { value: "", label: "Automatic" },
              ...templates.map((t) => ({
                value: String(t.id),
                label: `${t.name} (${vendorLabel(t.vendor)} ${t.modelGlob})`,
              })),
            ]}
          />
        )}
        <Switch
          label="Enabled"
          hint="A disabled phone gets no configuration"
          labelPosition="end"
          checked={enabled}
          onChange={(e) => setEnabled(e.target.checked)}
        />
      </form>
    </Modal>
  );
}

/** A dry run's report, kept with the text it checked. */
interface Checked {
  csv: string;
  report: ImportReport;
}

function ImportModal({
  onClose,
  onImported,
}: {
  onClose: () => void;
  onImported: (created: number) => void;
}) {
  const [csv, setCsv] = useState("");
  const [checked, setChecked] = useState<Checked | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const current = checked !== null && checked.csv === csv ? checked : null;
  const canImport = current !== null && current.report.ok && !busy;

  async function onCheck() {
    setError(null);
    setBusy(true);
    try {
      setChecked({ csv, report: await importPhones(csv, true) });
    } catch (err) {
      setChecked(null);
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  async function onImport() {
    setError(null);
    setBusy(true);
    try {
      const report = await importPhones(csv, false);
      onImported(report.created ?? 0);
    } catch (err) {
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  const columns: TableColumn<ImportReport["rows"][number]>[] = [
    { key: "line", label: "Line", mono: true, render: (r) => String(r.line) },
    { key: "mac", label: "MAC", mono: true, render: (r) => r.mac || "—" },
    {
      key: "result",
      label: "Result",
      render: (r) =>
        r.errors.length === 0 ? (
          <Badge tone="good">OK</Badge>
        ) : (
          <span className="inv-import__error">
            {r.errors.map(importErrorText).join("; ")}
          </span>
        ),
    },
  ];

  return (
    <Modal
      title="Import phones"
      description="One phone per line: mac,vendor,model,extension[,label][,blf]. Check the file first; the import adds every row or none."
      onClose={busy ? undefined : onClose}
      width={720}
      actions={
        <>
          <Button variant="secondary" disabled={busy} onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="secondary"
            disabled={busy || csv.trim() === ""}
            onClick={() => void onCheck()}
          >
            Check
          </Button>
          <Button disabled={!canImport} onClick={() => void onImport()}>
            Import
          </Button>
        </>
      }
    >
      <div className="prov-stack">
        {error && (
          <Alert tone="bad" title="Import failed">
            {error}
          </Alert>
        )}
        <div className="az-field">
          <label className="az-field__label" htmlFor="import-csv">
            CSV
          </label>
          <textarea
            id="import-csv"
            className="az-input prov-textarea"
            rows={8}
            spellCheck={false}
            placeholder="805ec0123456,yealink,T54W,101,Front desk,102 103"
            value={csv}
            onChange={(e) => setCsv(e.target.value)}
          />
        </div>
        <div className="az-field">
          <label className="az-field__label" htmlFor="import-file">
            Or load a file
          </label>
          <input
            id="import-file"
            type="file"
            accept=".csv,text/csv,text/plain"
            onChange={(e) => {
              const file = e.target.files?.[0];
              if (file) void file.text().then(setCsv);
            }}
          />
        </div>
        {current && (
          <>
            {current.report.ok ? (
              <Alert tone="good" title="Ready to import">
                Every row can be imported.
              </Alert>
            ) : (
              <Alert tone="warn" title="Fix the rows with errors">
                Nothing is imported until every row is valid.
              </Alert>
            )}
            <Table
              caption="Check result"
              columns={columns}
              rows={current.report.rows}
              rowKey={(r) => String(r.line)}
            />
          </>
        )}
      </div>
    </Modal>
  );
}
