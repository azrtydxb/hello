# Backup and restore

Hello takes an encrypted copy of its database and its stored audio every night,
checks the copy after writing it, and keeps the last 14. A restore puts a copy
back, from the console or from the command line when the console is not
available. Everything is in Settings, Backups (administrators only).

## What a backup holds

One archive per run, named `hello-backup-<utc>.hbk`, with three parts:

| Part            | Content                                                                                                                                                                                |
| --------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `manifest.json` | The Hello version, the schema version, the creation time, counts, the SHA-256 and size of each part, and the key fingerprint                                                           |
| `objects.json`  | The objects in the voicemail, recordings and announcements buckets (voicemail audio and cached prompts, recordings, announcements, ringtones and music on hold): key, size and SHA-256 |
| `db.dump`       | `pg_dump -Fc` of the whole database (configuration, users, CDRs, audit, voicemail rows, provisioning)                                                                                  |

The objects are not inside the archive. Each is copied once to
`objects/<sha256>` in the backup bucket by a server-side copy, so audio never
passes through hello-control, and an object already stored under its hash is
not copied again. Archives are under `archives/`. When an archive is removed by
retention, the objects no remaining archive lists are removed with it.

Phone firmware (bucket `hello-firmware`) is not backed up: it is uploaded from
the vendor and can be uploaded again.

## Encryption

Each part is cut into 1 MiB chunks, and each chunk is sealed with AES-256-GCM
under a random 96-bit nonce. The key is derived from `HELLO_SECRET_KEY` with
HKDF-SHA256 and the info string `hello-backup-v1`; it is never written into the
archive. The archive carries a fingerprint (the first 8 bytes of the SHA-256 of
the derived key) and a restore refuses an archive whose fingerprint differs.
The chunk's position and part name are authenticated, so a chunk cannot be
moved, reordered or cut off without the restore noticing.

**Keep a copy of `HELLO_SECRET_KEY` outside the cluster.** Without the key, a
backup cannot be read. The same key also opens the sealed trunk passwords in the
restored database, so a restore onto a cluster with a different key would leave
those unusable even if the archive could be read.

## Schedule, destination and retention

Settings, Backups holds:

- **Scheduled backups** and a **cron** schedule (five fields, server time).
  The default `0 2 * * *` is every day at 02:00. A run missed while Hello was
  down starts at the next pass.
- **Destination.** The bundled MinIO bucket `hello-backups` (the default; the
  bucket is created when missing), or an external S3-compatible service with
  endpoint, bucket, region, path-style addressing and an access key and
  secret key. The keys are sealed in the database (AAD `backup:s3`) and are
  never shown again. Objects reach an external service through hello-control,
  because two different servers cannot copy between themselves.
- **Archives to keep.** The newest 14 by default.
- **Email when a backup fails twice.** An address to tell, through the email
  settings.

Every hello-control replica runs the scheduler, and the run is taken under a
PostgreSQL advisory lock, so exactly one replica backs up. The schedule and the
retry are decided from the run history in PostgreSQL, so losing the replica
that was running does not lose the backup: another starts it.

A failed scheduled run is tried once more after 15 minutes. A second failure
sends the email (when SMTP and an address are set). The metric
`hello_backup_last_success_timestamp` holds the Unix time of the last
successful backup on every replica; alert when `time() - hello_backup_last_success_timestamp`
passes about 26 hours.

**Verification.** Each run ends by reading the stored archive back: the manifest
is parsed and every part is decrypted and compared with the SHA-256 in the
manifest. A mismatch marks the run `failed` (class `verify`) and keeps the
archive for inspection. The Verify button on a stored backup does the same
check again at any time (the API's `dry_run`).

## Restore from the console

Settings, Backups, Restore on a succeeded backup. The dialog asks you to type
`RESTORE <id>`. Verify first: it checks the manifest and every part and tells
how many objects a restore would bring back, and writes nothing.

A restore:

1. verifies the manifest and every part hash;
2. refuses an archive from a newer schema than the running version, or from
   another `HELLO_SECRET_KEY`, or one whose backup store has lost an object;
3. enters maintenance: every API write answers `503 maintenance` on every
   replica (reads still work, calls keep running on the configuration
   snapshot already in memory, and no configuration is accepted);
4. runs `pg_restore --single-transaction --clean --if-exists`, so the database
   is replaced in one transaction and a failure leaves it as it was;
5. restores each object that is missing or whose SHA-256 differs, by
   server-side copy, and never deletes an object the backup does not list;
6. applies the migrations newer than the backup, when the running version is
   newer;
7. makes the run history match the backup bucket and raises the configuration
   revision above both the backup's and the previous one, so every SIP node
   rebuilds its snapshot from the restored data;
8. leaves maintenance and writes an audit row with the backup id, the counts and
   the outcome.

Maintenance times out of itself after an hour if the node doing the restore
dies. Sessions and API tokens come back as they were at the time of the backup,
so anyone signed in since may need to sign in again.

## Restore from the command line

When hello-control cannot start (for example the database is empty), run in any
hello-control pod or image with the database, MinIO and key available:

```
HELLO_SECRET_KEY=... hello-control backup restore \
  --from archives/hello-backup-20261026T020000Z.hbk
```

The destination is the bundled MinIO bucket `hello-backups` (`--bucket` picks
another). For an external S3 destination add `--s3-endpoint https://s3.example.com`
(and `--s3-region`, `--s3-path-style`) and put the credentials in
`HELLO_BACKUP_S3_ACCESS_KEY` and `HELLO_BACKUP_S3_SECRET_KEY`. The command asks
for `RESTORE` to be typed; `--yes` skips the question, and `--dry-run` only
verifies. An empty database needs nothing first: the restore creates the tables.

## Requirements and limits

- The image carries `postgresql-client`; `pg_dump` and `pg_restore` must be at
  least the PostgreSQL server's major version. Set `HELLO_BACKUP_PG_DUMP` and
  `HELLO_BACKUP_PG_RESTORE` for other paths.
- The dump and the archive are staged on disk before upload: `HELLO_BACKUP_TMP`
  (the chart mounts an emptyDir of `control.backup.stagingSize`, default 4Gi,
  there). Size it for the database dump plus its encrypted copy.
- A restore replaces the whole database. Changes made after the backup are
  lost; objects made after it are kept.
- Audit rows, logs and metric labels carry the backup id and counts only: no
  names, numbers or secrets.

## API

All routes are admin-only and in `GET /api/v1/openapi.json`:

| Route                                 | Purpose                                                             |
| ------------------------------------- | ------------------------------------------------------------------- |
| `GET`, `PUT /api/v1/backups/settings` | Schedule, destination, retention and the failure email address      |
| `GET /api/v1/backups`                 | The run history                                                     |
| `POST /api/v1/backups`                | A manual run; `202` with the run, `409 backup_busy` while another   |
| `DELETE /api/v1/backups/{id}`         | Remove the archive and its row                                      |
| `POST /api/v1/backups/{id}/restore`   | `{"confirm":"RESTORE <id>"}`; `?dry_run=true` verifies, counts only |

Refusals: `409 schema_newer`, `409 key_mismatch`, `409 backup_invalid`.
