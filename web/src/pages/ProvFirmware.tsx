import { useEffect, useState, type FormEvent } from "react";
import { errorMessage } from "../api";
import {
  compareVersions,
  deleteFirmware,
  listFirmware,
  listFirmwareMinimums,
  MAX_FIRMWARE_BYTES,
  minimumForPin,
  setFirmwareMinimums,
  setFirmwarePins,
  uploadFirmware,
  VENDORS,
  vendorLabel,
  type Firmware,
  type FirmwareMinimum,
  type FirmwarePin,
  type Vendor,
} from "../api/prov";
import {
  Alert,
  Button,
  ConfirmDialog,
  EmptyState,
  IconButton,
  Input,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  type TableColumn,
  useToast,
} from "../design/azrty/components";
import { formatBytes, PhonesTabs } from "./phones/ui";
import { Can } from "../role";

type ListState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: Firmware[] };

const VENDOR_OPTIONS = VENDORS.map((v) => ({
  value: v,
  label: vendorLabel(v),
}));

const sameTarget = (
  a: { vendor: string; modelGlob: string },
  b: { vendor: string; modelGlob: string },
) => a.vendor === b.vendor && a.modelGlob === b.modelGlob;

const HOUR_OPTIONS = [
  { value: "", label: "Any time" },
  ...Array.from({ length: 24 }, (_, h) => ({
    value: String(h),
    label: `${String(h).padStart(2, "0")}:00`,
  })),
];

const toPins = (items: Firmware[]): FirmwarePin[] =>
  items.map((x) => ({
    vendor: x.vendor,
    modelGlob: x.modelGlob,
    firmwareId: x.id,
    ...(x.upgradeHour != null ? { upgradeHour: x.upgradeHour } : {}),
  }));

const formatTime = (iso: string) => {
  const d = new Date(iso);
  return Number.isNaN(d.getTime()) ? iso : d.toLocaleString();
};

/** Firmware: upload files, pin one per vendor and model glob, delete unpinned ones. */
export function ProvFirmware() {
  const [list, setList] = useState<ListState>({ status: "loading" });
  const [deleting, setDeleting] = useState<Firmware | null>(null);
  const [pinError, setPinError] = useState<string | null>(null);
  const [pinBusy, setPinBusy] = useState(false);
  const [minimums, setMinimums] = useState<FirmwareMinimum[]>([]);
  const toast = useToast();
  const { show } = toast;

  useEffect(() => {
    const controller = new AbortController();
    listFirmware(controller.signal)
      .then((items) => setList({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!controller.signal.aborted) {
          setList({ status: "error", message: errorMessage(err) });
        }
      });
    return () => controller.abort();
  }, []);

  // The rules are advice for the page; the server enforces them, so a
  // failed load only costs the early refusal.
  useEffect(() => {
    const controller = new AbortController();
    listFirmwareMinimums(controller.signal)
      .then(setMinimums)
      .catch(() => undefined);
    return () => controller.abort();
  }, []);

  const items = list.status === "ready" ? list.items : [];

  function onUploaded(f: Firmware) {
    setList((prev) =>
      prev.status === "ready"
        ? { status: "ready", items: [...prev.items, f] }
        : prev,
    );
    show(`Firmware ${f.version} uploaded.`);
  }

  /** Sends the complete pin set; on success the list shows exactly it. */
  async function savePins(next: Firmware[], done: string) {
    setPinError(null);
    setPinBusy(true);
    try {
      await setFirmwarePins(toPins(next));
      const byId = new Map(next.map((x) => [String(x.id), x]));
      setList((prev) =>
        prev.status === "ready"
          ? {
              status: "ready",
              items: prev.items.map((x) => {
                const n = byId.get(String(x.id));
                return {
                  ...x,
                  pinned: n !== undefined,
                  upgradeHour: n?.upgradeHour ?? null,
                };
              }),
            }
          : prev,
      );
      show(done);
    } catch (err) {
      setPinError(errorMessage(err));
    } finally {
      setPinBusy(false);
    }
  }

  async function onPin(f: Firmware, pin: boolean) {
    // The server takes the complete set; one pin per vendor and model glob.
    const others = items.filter((x) => x.pinned && !sameTarget(x, f));
    if (pin) {
      const floor = minimumForPin(minimums, String(f.vendor), f.modelGlob);
      if (floor !== "" && compareVersions(f.version, floor) < 0) {
        setPinError(
          `Version ${f.version} is below the minimum ${floor} for ${vendorLabel(f.vendor)} ${f.modelGlob}.`,
        );
        return;
      }
    }
    await savePins(
      pin ? [...others, { ...f, upgradeHour: null }] : others,
      pin
        ? `Pinned ${f.version} for ${vendorLabel(f.vendor)} ${f.modelGlob}.`
        : `Unpinned ${f.version}.`,
    );
  }

  async function onHour(f: Firmware, hour: string) {
    const next = items
      .filter((x) => x.pinned)
      .map((x) =>
        x.id === f.id
          ? { ...x, upgradeHour: hour === "" ? null : Number(hour) }
          : x,
      );
    await savePins(
      next,
      hour === ""
        ? `${f.version} upgrades at any time.`
        : `${f.version} upgrades at ${hour.padStart(2, "0")}:00.`,
    );
  }

  async function onDelete(f: Firmware) {
    await deleteFirmware(f.id);
    setList((prev) =>
      prev.status === "ready"
        ? {
            status: "ready",
            items: prev.items.filter((x) => String(x.id) !== String(f.id)),
          }
        : prev,
    );
    setDeleting(null);
    show(`Firmware ${f.filename} deleted.`);
  }

  const columns: TableColumn<Firmware>[] = [
    { key: "vendor", label: "Vendor", render: (f) => vendorLabel(f.vendor) },
    { key: "modelGlob", label: "Model glob", mono: true },
    { key: "version", label: "Version", mono: true },
    { key: "filename", label: "File", mono: true },
    {
      key: "size",
      label: "Size",
      align: "right",
      render: (f) => formatBytes(f.size),
    },
    {
      key: "sha256",
      label: "SHA-256",
      mono: true,
      render: (f) => (
        <span title={f.sha256}>
          {f.sha256.length > 16 ? `${f.sha256.slice(0, 12)}…` : f.sha256}
        </span>
      ),
    },
    {
      key: "uploadedAt",
      label: "Uploaded",
      render: (f) => formatTime(f.uploadedAt),
    },
    {
      key: "pinned",
      label: "Pinned",
      render: (f) => (
        <Can fallback={f.pinned ? "On" : "Off"}>
          <Switch
            checked={f.pinned}
            disabled={pinBusy}
            aria-label={`Pin ${f.version} for ${vendorLabel(f.vendor)} ${f.modelGlob}`}
            onChange={(e) => void onPin(f, e.target.checked)}
          />
        </Can>
      ),
    },
    {
      key: "upgradeHour",
      label: "Upgrade window",
      render: (f) =>
        f.pinned && f.vendor === "grandstream" ? (
          <Can
            fallback={
              f.upgradeHour == null
                ? "Any time"
                : `${String(f.upgradeHour).padStart(2, "0")}:00`
            }
          >
            <Select
              aria-label={`Upgrade hour for ${f.version}`}
              options={HOUR_OPTIONS}
              value={f.upgradeHour == null ? "" : String(f.upgradeHour)}
              disabled={pinBusy}
              onChange={(e) => void onHour(f, e.target.value)}
            />
          </Can>
        ) : (
          <span className="prov-muted">—</span>
        ),
    },
    {
      key: "actions",
      label: <span className="visually-hidden">Actions</span>,
      align: "right",
      render: (f) => (
        <Can>
          <IconButton
            icon="trash-2"
            label={`Delete ${f.filename}`}
            onClick={() => setDeleting(f)}
          />
        </Can>
      ),
    },
  ];

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="Firmware"
        description="Firmware files phones upgrade to. Pin one file per vendor and model glob; phones matching it fetch that version."
      />
      <PhonesTabs />
      <div className="prov-stack">
        <UploadForm onUploaded={onUploaded} />
        {list.status === "loading" && <Spinner label="Loading firmware…" />}
        {list.status === "error" && (
          <Alert tone="bad" title="Could not load firmware">
            {list.message}
          </Alert>
        )}
        {pinError && (
          <Alert tone="bad" title="Could not change pins">
            {pinError}
          </Alert>
        )}
        {list.status === "ready" && list.items.length === 0 && (
          <EmptyState
            icon="hard-drive-download"
            title="No firmware yet"
            description="Upload a vendor firmware file to pin it for a model."
          />
        )}
        {list.status === "ready" && list.items.length > 0 && (
          <Table
            caption="Firmware files"
            columns={columns}
            rows={list.items}
            rowKey={(f) => String(f.id)}
          />
        )}
      </div>
      <MinimumsCard rules={minimums} onSaved={setMinimums} />
      {deleting && (
        <ConfirmDialog
          title={`Delete ${deleting.filename}?`}
          description="Phones that already run it keep it. A pinned file cannot be deleted; unpin it first."
          confirmLabel="Delete firmware"
          onConfirm={() => onDelete(deleting)}
          onClose={() => setDeleting(null)}
        />
      )}
      {toast.node}
    </section>
  );
}

/** The upload card: vendor, model glob, version and file, with progress. */
function UploadForm({ onUploaded }: { onUploaded: (f: Firmware) => void }) {
  const [vendor, setVendor] = useState<Vendor>("yealink");
  const [modelGlob, setModelGlob] = useState("*");
  const [version, setVersion] = useState("");
  const [file, setFile] = useState<File | null>(null);
  // A new key clears the file input after an upload.
  const [fileKey, setFileKey] = useState(0);
  const [progress, setProgress] = useState<number | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    setError(null);
    if (!file) return setError("Choose a firmware file.");
    if (file.size > MAX_FIRMWARE_BYTES) {
      return setError(
        `The file is ${formatBytes(file.size)}; the limit is ${formatBytes(MAX_FIRMWARE_BYTES)}.`,
      );
    }
    if (modelGlob.trim() === "") return setError("Enter a model glob.");
    if (version.trim() === "") return setError("Enter the version.");
    setProgress(0);
    try {
      const created = await uploadFirmware(
        { vendor, modelGlob: modelGlob.trim(), version: version.trim(), file },
        setProgress,
      );
      setFile(null);
      setFileKey((k) => k + 1);
      setVersion("");
      onUploaded(created);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setProgress(null);
    }
  }

  const busy = progress !== null;
  return (
    <form
      className="prov-card"
      aria-labelledby="fw-upload-title"
      onSubmit={(e) => void onSubmit(e)}
      noValidate
    >
      <h2 id="fw-upload-title" className="prov-card__title">
        Upload firmware
      </h2>
      <div className="prov-grid-2">
        <Select
          id="fw-vendor"
          label="Vendor"
          options={VENDOR_OPTIONS}
          value={vendor}
          disabled={busy}
          onChange={(e) => setVendor(e.target.value as Vendor)}
        />
        <Input
          id="fw-glob"
          label="Model glob"
          mono
          hint="Which models it is for, e.g. T5* or *."
          value={modelGlob}
          disabled={busy}
          onChange={(e) => setModelGlob(e.target.value)}
        />
        <Input
          id="fw-version"
          label="Version"
          mono
          value={version}
          disabled={busy}
          onChange={(e) => setVersion(e.target.value)}
        />
        <Input
          key={fileKey}
          id="fw-file"
          label="Firmware file"
          type="file"
          hint={`Up to ${formatBytes(MAX_FIRMWARE_BYTES)}.`}
          disabled={busy}
          onChange={(e) => {
            setError(null);
            setFile(e.target.files?.[0] ?? null);
          }}
        />
      </div>
      {busy && (
        <progress
          className="prov-progress"
          aria-label="Upload progress"
          max={1}
          value={progress}
        />
      )}
      {error && (
        <Alert tone="bad" title="Could not upload">
          {error}
        </Alert>
      )}
      <div className="prov-row">
        <Can>
          <Button type="submit" icon="upload" disabled={busy}>
            {busy ? "Uploading…" : "Upload"}
          </Button>
        </Can>
      </div>
    </form>
  );
}

/** The minimum and recommended firmware versions per vendor and model glob. */
function MinimumsCard({
  rules,
  onSaved,
}: {
  rules: FirmwareMinimum[];
  onSaved: (rules: FirmwareMinimum[]) => void;
}) {
  const [edit, setEdit] = useState<FirmwareMinimum[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const rows = edit ?? rules;

  const change = (i: number, patch: Partial<FirmwareMinimum>) =>
    setEdit(rows.map((r, j) => (j === i ? { ...r, ...patch } : r)));

  async function save() {
    setError(null);
    setBusy(true);
    try {
      await setFirmwareMinimums(rows);
      onSaved(rows);
      setEdit(null);
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="prov-card" aria-labelledby="fw-min-title">
      <h2 id="fw-min-title" className="prov-card__title">
        Minimum versions
      </h2>
      <p className="prov-muted">
        A firmware below the minimum cannot be pinned and is not given to
        phones. The recommended version is the lab build to upload and pin;
        Hello ships no vendor firmware.
      </p>
      {rows.length === 0 && <span className="prov-muted">No rules.</span>}
      {rows.map((r, i) => (
        <div className="prov-grid-2" key={`${r.vendor}/${r.modelGlob}`}>
          <Input
            id={`fw-min-${i}-min`}
            label={`${vendorLabel(r.vendor)} ${r.modelGlob} minimum`}
            mono
            value={r.minVersion}
            onChange={(e) => change(i, { minVersion: e.target.value })}
          />
          <Input
            id={`fw-min-${i}-rec`}
            label={`${vendorLabel(r.vendor)} ${r.modelGlob} recommended`}
            mono
            value={r.recommended}
            onChange={(e) => change(i, { recommended: e.target.value })}
          />
        </div>
      ))}
      {error && (
        <Alert tone="bad" title="Could not save the minimums">
          {error}
        </Alert>
      )}
      <Can>
        <div className="prov-row">
          <Button disabled={busy || edit === null} onClick={() => void save()}>
            Save minimums
          </Button>
        </div>
      </Can>
    </section>
  );
}
