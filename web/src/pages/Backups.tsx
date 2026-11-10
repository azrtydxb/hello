import { useCallback, useState, type FormEvent } from "react";
import { ApiError, errorMessage, fieldErrors } from "../api";
import {
  deleteBackup,
  getBackupSettings,
  listBackups,
  putBackupSettings,
  restoreBackup,
  restoreConfirmText,
  runBackup,
  verifyBackup,
  type BackupKind,
  type BackupRun,
  type BackupSettings,
  type RestoreReport,
} from "../api/backups";
import {
  Alert,
  Badge,
  Button,
  ConfirmDialog,
  Input,
  PageHeader,
  Select,
  Spinner,
  Switch,
  Table,
  useToast,
  type BadgeTone,
  type TableColumn,
} from "../design/azrty/components";
import { formatTime } from "../format";
import { mapFieldErrors } from "../forms";
import { useCan } from "../role";
import { LIVE_REFRESH_MS, usePolling } from "../usePolling";
import "./platform/platform.css";

const KIND_OPTIONS = [
  { value: "minio", label: "Hello's own MinIO (bucket hello-backups)" },
  { value: "s3", label: "An S3-compatible service" },
];

const STATUS_TONE: Record<BackupRun["status"], BadgeTone> = {
  running: "info",
  succeeded: "good",
  failed: "bad",
};

/** Bytes as KiB, MiB or GiB. */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KiB", "MiB", "GiB", "TiB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

/** Backups: the schedule, the destination, the archives, and the restore (admin only). */
export function Backups() {
  const isAdmin = useCan("admin");
  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Platform"
        title="Backups"
        description="An encrypted copy of the database and the stored audio, taken on a schedule and checked after every run."
      />
      {isAdmin ? (
        <Content />
      ) : (
        <Alert tone="warn" title="Administrators only">
          Your role cannot manage backups.
        </Alert>
      )}
    </section>
  );
}

function Content() {
  const load = useCallback(async (signal: AbortSignal) => {
    const [settings, runs] = await Promise.all([
      getBackupSettings(signal),
      listBackups(signal),
    ]);
    return { settings, runs };
  }, []);
  // The list is small; a run in progress shows up within a few seconds.
  const polled = usePolling(load, LIVE_REFRESH_MS);
  if (polled.status === "loading") return <Spinner label="Loading backups…" />;
  if (!polled.data)
    return (
      <Alert tone="bad" title="Could not load the backups">
        {polled.status === "error" ? polled.message : ""}
      </Alert>
    );
  const { settings, runs } = polled.data;
  return (
    <div className="pf-stack">
      {polled.status === "error" && (
        <Alert tone="warn" title="Could not refresh">
          {polled.message}
        </Alert>
      )}
      <SettingsForm initial={settings} onSaved={polled.reload} />
      <Runs runs={runs} onChanged={polled.reload} />
    </div>
  );
}

const KEYS = [
  "cron",
  "keepCount",
  "notifyEmail",
  "s3Endpoint",
  "s3Bucket",
  "s3Region",
  "s3AccessKey",
  "s3SecretKey",
  "kind",
];

function SettingsForm({
  initial,
  onSaved,
}: {
  initial: BackupSettings;
  onSaved: () => void;
}) {
  const [saved, setSaved] = useState(initial);
  const [enabled, setEnabled] = useState(initial.enabled);
  const [cron, setCron] = useState(initial.cron);
  const [kind, setKind] = useState<BackupKind>(initial.kind);
  const [endpoint, setEndpoint] = useState(initial.s3Endpoint);
  const [bucket, setBucket] = useState(initial.s3Bucket);
  const [region, setRegion] = useState(initial.s3Region);
  const [pathStyle, setPathStyle] = useState(initial.s3PathStyle);
  const [access, setAccess] = useState("");
  const [secret, setSecret] = useState("");
  const [clearKeys, setClearKeys] = useState(false);
  const [keep, setKeep] = useState(String(initial.keepCount));
  const [notify, setNotify] = useState(initial.notifyEmail);
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const toast = useToast();

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    if (busy) return;
    setBusy(true);
    setErrors({});
    setFormError(null);
    const keys =
      access !== "" || secret !== ""
        ? { s3AccessKey: access, s3SecretKey: secret }
        : clearKeys
          ? { s3AccessKey: "", s3SecretKey: "" }
          : {};
    try {
      const next = await putBackupSettings({
        enabled,
        cron: cron.trim(),
        kind,
        s3Endpoint: endpoint.trim(),
        s3Bucket: bucket.trim(),
        s3Region: region.trim(),
        s3PathStyle: pathStyle,
        keepCount: Number(keep),
        notifyEmail: notify.trim(),
        ...keys,
      });
      setSaved(next);
      setAccess("");
      setSecret("");
      setClearKeys(false);
      toast.show("Backup settings saved.");
      onSaved();
    } catch (err) {
      const { byKey, unmatched } = mapFieldErrors(fieldErrors(err), KEYS);
      setErrors(byKey);
      if (unmatched.length > 0 || Object.keys(byKey).length === 0)
        setFormError(err instanceof ApiError ? err.message : errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <form
      onSubmit={(e) => void onSubmit(e)}
      noValidate
      className="az-card pf-stack"
      aria-label="Backup settings"
    >
      <h2 className="vm-box__title">Schedule and destination</h2>
      {formError && (
        <Alert tone="bad" title="Not saved">
          {formError}
        </Alert>
      )}
      <Switch
        label="Scheduled backups"
        checked={enabled}
        onChange={(e) => setEnabled(e.target.checked)}
      />
      <Input
        label="Schedule (cron)"
        hint="Five fields: minute hour day-of-month month day-of-week, in server time. 0 2 * * * is daily at 02:00."
        value={cron}
        error={errors.cron}
        mono
        onChange={(e) => setCron(e.target.value)}
      />
      <Input
        label="Archives to keep"
        inputMode="numeric"
        hint="Older archives are removed after each successful run."
        value={keep}
        error={errors.keepCount}
        onChange={(e) => setKeep(e.target.value)}
      />
      <Select
        label="Destination"
        value={kind}
        error={errors.kind}
        options={KIND_OPTIONS}
        onChange={(e) => setKind(e.target.value as BackupKind)}
      />
      {kind === "s3" && (
        <>
          <Input
            label="S3 endpoint"
            hint="https://host[:port]"
            value={endpoint}
            error={errors.s3Endpoint}
            onChange={(e) => setEndpoint(e.target.value)}
          />
          <Input
            label="S3 bucket"
            value={bucket}
            error={errors.s3Bucket}
            onChange={(e) => setBucket(e.target.value)}
          />
          <Input
            label="S3 region"
            hint="Leave empty when the service does not use regions."
            value={region}
            error={errors.s3Region}
            onChange={(e) => setRegion(e.target.value)}
          />
          <Switch
            label="Path-style addressing"
            checked={pathStyle}
            onChange={(e) => setPathStyle(e.target.checked)}
          />
          <Input
            label="Access key"
            autoComplete="off"
            placeholder={saved.s3KeysSet ? "•••••••• (stored)" : ""}
            value={access}
            error={errors.s3AccessKey}
            disabled={clearKeys}
            onChange={(e) => setAccess(e.target.value)}
          />
          <Input
            label="Secret key"
            type="password"
            autoComplete="new-password"
            placeholder={saved.s3KeysSet ? "•••••••• (stored)" : ""}
            hint={
              saved.s3KeysSet
                ? "Credentials are stored and never shown. Leave both empty to keep them."
                : "No credentials are stored."
            }
            value={secret}
            error={errors.s3SecretKey}
            disabled={clearKeys}
            onChange={(e) => setSecret(e.target.value)}
          />
          {saved.s3KeysSet && (
            <label className="az-check">
              <input
                type="checkbox"
                className="az-check__input"
                checked={clearKeys}
                onChange={(e) => setClearKeys(e.target.checked)}
              />
              <span className="az-check__box" />
              <span className="az-check__text">
                <span className="az-check__label">
                  Remove the stored credentials
                </span>
              </span>
            </label>
          )}
        </>
      )}
      <Input
        label="Email when a backup fails twice"
        type="email"
        hint="Needs the email settings. Leave empty for no email; the metric hello_backup_last_success_timestamp is always kept."
        value={notify}
        error={errors.notifyEmail}
        onChange={(e) => setNotify(e.target.value)}
      />
      <div>
        <Button type="submit" icon="save" disabled={busy}>
          Save
        </Button>
      </div>
    </form>
  );
}

function reportText(r: RestoreReport): string {
  return `${r.objects} objects (${formatBytes(r.objectBytes)}), database ${formatBytes(r.dbBytes)}, schema ${r.schemaVersion}, made by ${r.helloVersion || "an unknown version"}. ${r.objectsToRestore} objects would be restored.`;
}

function Runs({
  runs,
  onChanged,
}: {
  runs: BackupRun[];
  onChanged: () => void;
}) {
  const [starting, setStarting] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [note, setNote] = useState<string | null>(null);
  const [restoring, setRestoring] = useState<BackupRun | null>(null);
  const [deleting, setDeleting] = useState<BackupRun | null>(null);

  async function start() {
    setStarting(true);
    setError(null);
    try {
      await runBackup();
      onChanged();
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setStarting(false);
    }
  }

  async function verify(r: BackupRun) {
    setError(null);
    setNote(null);
    try {
      const rep = await verifyBackup(r.id);
      setNote(`Backup ${r.id} verified: ${reportText(rep)}`);
    } catch (err) {
      setError(`Backup ${r.id}: ${errorMessage(err)}`);
    }
  }

  const columns: TableColumn<BackupRun>[] = [
    {
      key: "started",
      label: "Started",
      render: (r) => formatTime(r.startedAt),
    },
    { key: "kind", label: "Kind", render: (r) => r.kind },
    {
      key: "status",
      label: "Status",
      render: (r) => (
        <Badge tone={STATUS_TONE[r.status]}>
          {r.status === "failed" && r.errorClass
            ? `failed: ${r.errorClass}`
            : r.status}
        </Badge>
      ),
    },
    {
      key: "size",
      label: "Archive",
      align: "right",
      render: (r) => (r.archiveBytes ? formatBytes(r.archiveBytes) : "—"),
    },
    {
      key: "objects",
      label: "Objects",
      align: "right",
      render: (r) => r.objectCount,
    },
    {
      key: "actions",
      label: "",
      align: "right",
      render: (r) => (
        <span className="pf-actions">
          {r.status === "succeeded" && (
            <>
              <Button
                type="button"
                variant="ghost"
                icon="shield-check"
                aria-label={`Verify backup ${r.id}`}
                onClick={() => void verify(r)}
              >
                Verify
              </Button>
              <Button
                type="button"
                variant="ghost"
                icon="archive-restore"
                aria-label={`Restore backup ${r.id}`}
                onClick={() => setRestoring(r)}
              >
                Restore
              </Button>
            </>
          )}
          {r.status !== "running" && (
            <Button
              type="button"
              variant="ghost"
              icon="trash-2"
              aria-label={`Delete backup ${r.id}`}
              onClick={() => setDeleting(r)}
            >
              Delete
            </Button>
          )}
        </span>
      ),
    },
  ];

  return (
    <div className="az-card pf-stack">
      <h2 className="vm-box__title">Backups</h2>
      <div>
        <Button
          type="button"
          icon="play"
          disabled={starting || runs.some((r) => r.status === "running")}
          onClick={() => void start()}
        >
          Back up now
        </Button>
      </div>
      {error && (
        <Alert tone="bad" title="Something went wrong">
          {error}
        </Alert>
      )}
      {note && (
        <Alert tone="good" title="Verified">
          {note}
        </Alert>
      )}
      {runs.length === 0 ? (
        <p className="pf-muted">No backups yet.</p>
      ) : (
        <Table
          columns={columns}
          rows={runs}
          rowKey={(r) => r.id}
          caption="Backups"
        />
      )}
      {restoring && (
        <RestoreDialog
          run={restoring}
          onClose={() => setRestoring(null)}
          onDone={() => {
            setRestoring(null);
            onChanged();
          }}
        />
      )}
      {deleting && (
        <ConfirmDialog
          title={`Delete backup ${deleting.id}?`}
          description="The archive is removed from the destination."
          confirmLabel="Delete backup"
          errorTitle="Could not delete the backup"
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await deleteBackup(deleting.id);
            setDeleting(null);
            onChanged();
          }}
        />
      )}
    </div>
  );
}

function RestoreDialog({
  run,
  onClose,
  onDone,
}: {
  run: BackupRun;
  onClose: () => void;
  onDone: () => void;
}) {
  const expected = restoreConfirmText(run.id);
  const [typed, setTyped] = useState("");
  return (
    <ConfirmDialog
      title={`Restore backup ${run.id}?`}
      description={`This replaces the database with the backup taken ${formatTime(run.startedAt)}. Changes made since are lost, and changes are refused while it runs. Audio that is missing or changed is restored; nothing is deleted.`}
      confirmLabel="Restore"
      confirmIcon="archive-restore"
      errorTitle="Could not restore"
      onClose={onClose}
      onConfirm={async () => {
        if (typed !== expected) throw new Error(`Type ${expected} to confirm.`);
        await restoreBackup(run.id);
        onDone();
      }}
    >
      <Input
        label={`Type ${expected} to confirm`}
        value={typed}
        autoComplete="off"
        onChange={(e) => setTyped(e.target.value)}
      />
    </ConfirmDialog>
  );
}
