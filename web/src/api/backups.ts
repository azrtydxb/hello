// Client for backup and restore (admin): the schedule and destination, the
// run history, a manual run and the restore.
import { request } from "../api";

export type BackupKind = "minio" | "s3";

/** GET /api/v1/backups/settings: the S3 credentials are never returned. */
export interface BackupSettings {
  enabled: boolean;
  /** Five-field cron expression in server time. */
  cron: string;
  kind: BackupKind;
  bucket: string;
  s3Endpoint: string;
  s3Bucket: string;
  s3Region: string;
  s3PathStyle: boolean;
  /** Whether S3 credentials are stored. */
  s3KeysSet: boolean;
  keepCount: number;
  notifyEmail: string;
  updatedAt: string;
}

/** PUT /api/v1/backups/settings body. */
export interface BackupSettingsInput {
  enabled: boolean;
  cron: string;
  kind: BackupKind;
  s3Endpoint: string;
  s3Bucket: string;
  s3Region: string;
  s3PathStyle: boolean;
  /** Send both keys to replace them, both empty to remove them, neither to keep them. */
  s3AccessKey?: string;
  s3SecretKey?: string;
  keepCount: number;
  notifyEmail: string;
}

export type BackupStatus = "running" | "succeeded" | "failed";

export interface BackupRun {
  id: number;
  kind: "scheduled" | "manual";
  status: BackupStatus;
  attempt: number;
  startedAt: string;
  finishedAt: string | null;
  dbBytes: number;
  objectCount: number;
  objectBytes: number;
  archiveBytes: number;
  objectKey: string;
  errorClass: string;
}

/** Counts only; nothing in it names a user, number or secret. */
export interface RestoreReport {
  dryRun: boolean;
  helloVersion: string;
  schemaVersion: number;
  createdAt: string;
  dbBytes: number;
  objects: number;
  objectBytes: number;
  objectsToRestore: number;
  objectsMissingInBackup: number;
}

export const getBackupSettings = (signal?: AbortSignal) =>
  request<BackupSettings>("GET", "/api/v1/backups/settings", { signal });

export const putBackupSettings = (input: BackupSettingsInput) =>
  request<BackupSettings>("PUT", "/api/v1/backups/settings", { body: input });

export const listBackups = (signal?: AbortSignal) =>
  request<{ items: BackupRun[] }>("GET", "/api/v1/backups", { signal }).then(
    (r) => r.items,
  );

export const runBackup = () => request<BackupRun>("POST", "/api/v1/backups");

export const deleteBackup = (id: number) =>
  request<void>("DELETE", `/api/v1/backups/${id}`);

/** The text a restore of backup `id` must be confirmed with. */
export const restoreConfirmText = (id: number) => `RESTORE ${id}`;

/** Verifies the archive and returns counts; writes nothing. */
export const verifyBackup = (id: number) =>
  request<RestoreReport>("POST", `/api/v1/backups/${id}/restore?dry_run=true`);

export const restoreBackup = (id: number) =>
  request<RestoreReport>("POST", `/api/v1/backups/${id}/restore`, {
    body: { confirm: restoreConfirmText(id) },
  });
