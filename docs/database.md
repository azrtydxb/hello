# Database layer: momo

Hello's schema is declared by momo models (`internal/db/models`) and built by
momo migrations generated from them (`migrations/postgres`), which
`hello-control migrate up` applies with momo's migration engine
(`internal/migrate`). Until the cutover the schema was built by goose; those
migrations are frozen in `migrations/legacy` and replayed only to take over
a database or backup from before the cutover ("The cutover from goose",
below).

The query layer is moving to [momo](https://github.com/azrtydxb/momo), pinned
to an exact version in `go.mod` (v0.16.0), one package at a time. A package
that has moved runs its statements through a `*momo.DB` that `internal/db`
builds over the binary's existing `*sql.DB` (`pgxdriver.FromSQLDB`), so there
is no second connection pool. Statements are logged at debug level with the
component `db`, and at warn when slower than `db.SlowQuery`; argument values
are never logged.

Moved so far:

- `internal/crm` (`crm.PG`).
- The CDR writer (`internal/cdr`), which runs on hello-sip's pgx pool through
  `db.NewPool`, in the pool's own query execution mode.
- `internal/slack` (`slack.PG`).
- `internal/ai/proposal` (the proposal store) and `internal/ai/detect` (the
  detectors' reads).
- `internal/webhook` (`webhook.PG`), which takes its advisory locks through
  momo's `advisory` package with the same keys as before.
- `internal/events` (the event ingest and history), whose ingest
  transaction is momo's and whose hooks still get the `*sql.Tx` behind it.
- The directory sync store (`internal/directory`), the SCIM store
  (`internal/scim`) and the SSO store (`internal/sso`).
- `internal/store`, at its seam. Every domain is converted to `@name`
  binds, and every file but the raw exceptions below is in `namedFiles`:
  - core and configuration: `store.go`, `batch.go`, `config.go`, `dialspace.go`, `dirprovision.go`, `flow_canvas.go`
  - faxes, mail and backup: `faxes.go`, `mail.go`, `backup.go`
  - users and authentication: `users.go`, `totp.go`, `oauth.go`, `ldap.go`, `onboarding.go`, `sidekey.go`
  - phonebook: `phonebook.go`, `phonebook_lookup.go`, `personal_contacts.go`, `locations.go`
  - call features: `ivr.go`, `queues.go`, `queue_callbacks.go`, `followme.go`, `pickup.go`, `park.go`, `paging.go`, `ringmap.go`, `ringtag.go`, `conference.go`, `multicast.go`
  - voicemail and media: `pbx.go`, `voicemail_groups.go`, `media.go`, `moh.go`, `ringtones.go`, `tts.go`, `recording_links.go`, `video_recording.go`, `cdr.go`
  - provisioning: `prov.go`, `prov_admin.go`, `prov_handler.go`, `prov_lines.go`, `prov_push.go`, `prov_overrides.go`, `prov_books.go`, `prov_multicast.go`, `syslog.go`, `rtcpxr.go`, `clientquality.go`, `clients.go`
  - routing and time: `routing.go`, `timecond.go`, `blocklist.go`, `originate.go`, `boss.go`, `calendar_dnd.go`
  - security and door: `scep.go`, `wifi.go`, `door.go`, `door_open.go`, `door_camera.go`, `door_webhook.go`
  - AI: `messaging_ai.go`, `ai_core.go`, `ai_sessions.go`, `ai_findings.go`
  - messaging inbox: `messaging_inbox.go`, `messaging_media.go`, `messaging_privacy.go`, `messaging_deskphone.go`, `messaging_reports.go`
  - messaging core: `messages.go`, `messaging.go`, `messaging_route.go`, `messaging_route_views.go`, `messaging_carrier.go`, `messaging_events.go`, `messaging_calls.go`
  - webchat: `webchat.go`, `webchat_sites.go`

  Raw on purpose: the seam itself (`dbq.go`, which passes every statement
  through and begins transactions), `TryLock`'s pinned session-lock
  connection (`trylock.go`, which keeps its native `$1` and is not in
  `namedFiles`), and `SchemaVersion`'s read of the migration histories
  (`backup.go`, which binds nothing and goes straight to `database/sql`).

Every package above builds its handle with `db.New` (or `db.NewPool`) where
the binary constructs it, except `internal/store`, which builds its own in
`store.New`.

`internal/store` runs on momo at its seam (`internal/store/dbq.go`): its
pool and its transactions are types with `database/sql`'s method set
(`ExecContext`, `QueryContext`, `QueryRowContext`, `BeginTx`) whose every
statement is a momo raw statement, on the store's `*momo.DB` or on momo's
view of the open `*sql.Tx` (`db.AdoptTx`). Every converted file binds
`@name`; momo also accepts `$n` on PostgreSQL as positional arguments, which
only the raw exceptions above still use (one placeholder style per
statement: a `$n` statement cannot also use `?` or `@name`). The handle and the transaction types are
`momo.Querier`s, so code can also pass them straight to `momo.Exec`,
`momo.Query[T]`, `momo.Scalars[V]` and the builders. Transactions are still
begun by `database/sql` and adopted, so methods that take a caller's
`*sql.Tx` (the directory Provisioner's) keep it. Three things stay raw:
BEGIN, `TryLock`'s pinned session-lock connection, and `SchemaVersion`'s read
of the migration histories. `TestStatementsBuild` builds every constant statement
the store passes to the seam or to momo's raw functions on the offline
handle, from the package source, with the call site's `momo.Named` names.

### Converting an internal/store file to @name

One domain (a group of files) per pull request. Call sites keep going
through the seam: `s.q(ctx)`, `s.db` or the `*dbTx` keep their
`ExecContext`, `QueryContext` and `QueryRowContext` calls and their Scan
code; only the SQL text and the arguments change. For each file:

1. Replace every `$n` with `@name`, named after the column or the meaning
   (`@id`, `@extension_id`, `@cutoff`), and pass `momo.Named("name", v)`
   for each name instead of the positional value. Add the `momo` import.
   Order of the `Named` arguments does not matter; each name is passed once.
2. A `$n` used more than once becomes one `@name` used more than once.
   momo renders one bind per use, so PostgreSQL types each use on its own,
   where before it typed the single `$n` once. Cast every use to the type
   PostgreSQL inferred for the old `$n` wherever a use cannot type itself or
   could type differently:
   `@cutoff::timestamptz IS NULL OR created_at < @cutoff::timestamptz`,
   `(@kind::text = '' OR kind = @kind::text)`,
   `COALESCE(@dnd::boolean, dnd) ... CASE WHEN @dnd::boolean IS NULL`. A use
   compared with a column of the same type on each side needs no cast
   (`device_id = @id ... OR ... device_id = @id`).
3. Keep everything else as it was: the SQL text, `FOR UPDATE` and
   `SKIP LOCKED`, the advisory lock keys (the configuration lock is
   `lockConfig`, key `0x68656c6c6f636667`; the NOTIFY is `notifyConfig`),
   the Scan destinations and the error handling (`mapErr`,
   `errors.Is(err, sql.ErrNoRows)`, `requireRow`).
4. A statement assembled at run time (a WHERE built from fragments) binds by
   name too: each fragment carries its `@name` and the caller appends the
   matching `momo.Named` only with the fragment (a `Named` no `@name` uses
   fails the build, as does an `@name` without one). A helper that takes a caller's SQL fragment
   documents the names it binds (`boundPhone` binds `@id`).
5. Add the file to `namedFiles` in `internal/store/dbq_offline_test.go`.
   `TestStatementsBuild` then builds every constant statement of the file
   with its `Named` names (a misspelled or unused name fails) and refuses a
   `$n` left in it. Run `go test ./internal/store/`, then the DB tier in CI.
6. A single-row read written with `momo.Query[T]` or `momo.Scalars[V]`
   goes through `one(...)` (`dbq.go`), which returns `sql.ErrNoRows` for no
   row, as `QueryRowContext`'s Scan does, so `mapErr` and the no-row checks
   are unchanged (`ConfigRevision` is the example). A list read keeps its
   empty, non-nil slice.

## Writing a statement

- Hand-written SQL stays hand-written. Bind arguments by name, with `@name`
  and `momo.Named("name", value)`, not `$1`: adding a column renumbers
  nothing. Every `@name` must have its argument and every argument must be
  used, or the statement fails to build (`momo.ErrValidation`).
- Read rows with `momo.Query[T]` into a struct whose fields name the columns
  (`momo:"column:x"` tags, else snake case), `momo.Query[momo.RowN[...]]`
  for positional tuples, or `momo.Scalars[V]` for one column. An empty
  result is an empty slice, not `sql.ErrNoRows`; map it to the package's
  not-found error yourself.
- A statement inside a transaction that raw `database/sql` code began runs
  on `db.AdoptTx(m, tx)`; the caller still commits or rolls back.
- Errors keep the driver's error inside: `errors.As(err, &*pgconn.PgError)`
  still works, and momo adds kinds such as `momo.ErrUniqueViolation`.
- A statement worth freezing goes into a `momo.QueryRegistry` and a
  `momotest.QueryGolden` test, which writes the SQL PostgreSQL receives to
  `testdata/queries/postgres.sql` (rewrite after an intended change with
  `MOMO_UPDATE_GOLDEN=1`, then review the diff). See `internal/cdr`.
- A test that runs every method on `momo.NewOffline(dialect.Postgres)` proves
  that each statement builds without a database (see
  `internal/crm/pg_offline_test.go`), so the pull request tier catches a
  misspelled `@name`. A package whose statements mostly run inside
  transactions, which the offline handle stops at BEGIN, uses a recording
  stub driver instead (`internal/webhook/pg_offline_test.go`): it answers
  every statement with one row so each method runs down its longest path,
  and it pins the advisory lock statements and keys.

## Models

`internal/db/models` holds a momo model for every table of the schema. The
models are the source of the schema: a change to the schema starts with a
change to a model. Each `<table>_model.go` is an ordinary Go file (it starts
with the `// momo:fromdb` header the cutover's `momo generate --from-db`
wrote; the header no longer matters and may go stale). `momo_gen.go` and the
per-model packages are written by `momo generate`; never edit those.

The momo command runs from the repository root without a database. Build it
at the version `go.mod` pins, in its own module, so momo's MySQL and SQLite
drivers never enter Hello's `go.mod`:

```sh
MOMO=github.com/azrtydxb/momo/cmd/momo@$(go list -m -f '{{.Version}}' github.com/azrtydxb/momo)
momo() { go run "$MOMO" "$@"; }
```

## Changing the schema

1. Edit the model in `internal/db/models` (a new table is a new file there,
   modeled on the others), then run `momo generate`.
2. Run `momo migrate diff <name>` (`<name>` is lowercase letters, digits and
   underscores). It compares the models with the schema after the newest
   migration and writes `migrations/postgres/<version>_<name>.up.sql`, its
   `.snapshot.json` and the new `momo.sum` line. The version is the current
   UTC time, `YYYYMMDDHHMMSS`. Read the SQL: `diff` refuses a destructive
   change (a dropped column or table, a removed check value) without
   `--allow-destructive`, and writes a `_no_tx` migration next to the first
   when part of the change must run outside a transaction
   (`CREATE INDEX CONCURRENTLY`, the `VALIDATE CONSTRAINT` of a new check).
3. Run `momo migrate validate`, commit the model, the generated code and the
   migration files together, and dispatch the DB tier
   (`gh workflow run ci.yaml --ref <branch>`).

The pull request tier (`go` job, "momo models and migrations") runs
`momo generate --check`, `momo migrate validate` and
`momo migrate diff ci_probe`, and fails when the generated code is stale, the directory is
invalid, or the models describe a change that has no migration yet.
`TestMigrationsValid` (`internal/migrate`) runs the same validation in
`go test`. The DB tier applies the migrations to scratch databases and
compares the result with the newest snapshot (`TestMigrateFresh`,
`TestMigrateTakeover`, strict drift).

Rules that do not change with the engine: additive first (an older pod runs
against the new schema during a rolling update), and CHECK constraint
changes are additive only.

### Value-list checks (CheckIn)

An enum-like TEXT column is restricted with `c.CheckIn` in the model's
`MomoConfig`, one value per line:

```go
c.CheckIn(&m.Vendor,
	"fanvil",
	"grandstream",
	"yealink",
)
```

momo compares the list as a set: adding a value is one `AlterCheckIn`
change (`DROP CONSTRAINT` and `ADD CONSTRAINT ... NOT VALID` in one file,
`VALIDATE CONSTRAINT` in its `_no_tx` file), so the table is never without
the check. Removing a value is destructive and refused without
`--allow-destructive`; Hello does not remove values. Two branches that each
add a value to the same list merge as text (keep both lines), but their two
migrations both restate the whole list, so the branch that merges second
deletes its migration and runs `momo migrate diff` again after the merge
(below). This replaces the goose-era `pg_temp.hello_check_values` helper.
A check that is not a plain value list (a range, `IS NULL OR ...`) stays a
`c.Check(name, expr)`.

### Standalone sequences

A sequence no column owns is declared with `c.Sequence` on the model whose
column uses it: `prov_redirect_jobs_seq`, the default of
`prov_redirect_jobs.seq`, is declared in `ProvRedirectJob.MomoConfig`.
`diff` creates it before the tables that use it.

### Data migrations

A data step (a backfill, a seed row) is a hand-written migration:

```sh
momo migrate new --custom backfill_widgets
```

writes an `.up.sql` with only comments, a snapshot equal to the previous
one and the `momo.sum` line. Write the statements below the comments (each
ends in `;`), then run `momo migrate hash` and `momo migrate validate`. A
hand-written migration must not change the schema (`validate` refuses one
whose snapshot differs from its parent's); change the schema through a model
and `diff`, and put the backfill in its own custom migration after it. Add
`-- momo:no-transaction` to the leading comment block for a batched backfill
that commits as it goes. A step that needs Go code (it calls library code) is
a `migrate.GoMigration` registered in `internal/migrate` with
`migrate.WithGoMigrations`, plus its snapshot file; see momo's
`docs/migrations.md`, section 6a. A Go migration cannot be rolled back.

### Down files

`diff` writes no down file. A hand-written `<version>_<name>.down.sql` is
optional and only `Engine.Rollback` runs it (`momo migrate rollback` against
a lab database, never by `migrate up`); hash it with `momo migrate hash`. A
rollback undoes schema, not data, and marks the version `rolled_back`, so
the next `migrate up` applies it again. `TestMigrateRollback` exercises the
path on a toy migration. In production, fix forward.

### Parallel branches and renumbering after a lost race

`momo.sum` is format 2, one `<sha256>  <file>` line per file, and
`.gitattributes` merges it with `merge=union`, so two branches that each add
a migration merge without a conflict in it. After any merge that brought in
migrations, run `momo migrate hash` (it writes the lines sorted; review its
`changed:` list) and `momo migrate validate`:

- Two migrations that change different tables are independent: `validate`
  passes and both apply.
- Two that change the same table (or one depends on the other's object)
  fail `validate` with `ErrBranchConflict`, naming both. The branch that
  merges second fixes it before merging: delete its own generated
  migration (the `.up.sql` and `.snapshot.json`), run
  `momo migrate diff <name>` again on top of the merged chain, then `hash`
  and `validate`.
  `momo migrate rebase --onto <dir>` does the same for generated migrations
  given a checkout of the target branch's `migrations/postgres` (for
  example `git archive origin/main migrations/postgres | tar -x -C /tmp/main`).
- A migration whose version is older than one already applied somewhere
  (the other branch merged and deployed first) is refused at apply with
  `ErrOutOfOrder`. Move it after the newest one with
  `momo migrate rebase --renumber --applied <V1,V2,...>` (offline: the
  versions applied on main, read from `migrations/postgres` there), which
  renames the pending files, fixes their parents and `momo.sum`, and changes
  no SQL. A hand-written migration is renumbered the same way.
- Never rewrite a migration that has been applied anywhere (kw included);
  add a new one.

## Running migrations

`hello-control migrate up` (the `migrate` init container of every control
pod, `until hello-control migrate up; do sleep 3; done`) calls
`migrate.Up`, which looks at the database:

| The database has                       | Up does                                                                                                         |
| -------------------------------------- | --------------------------------------------------------------------------------------------------------------- |
| no `goose_db_version`, no momo history | applies every migration (a new install)                                                                         |
| `goose_db_version`, no momo history    | the takeover: the frozen goose migrations it lacks, then `Engine.Baseline` through `legacy_seed`, then the rest |
| momo history                           | applies what is pending                                                                                         |

Replicas starting together are safe: `Up` decides under a session advisory
lock of its own, and momo's `Apply` and `Baseline` take the history lock
(`pg_try_advisory_lock` on the FNV-64a of `momo:public.momo_migrations`),
each waiting up to five minutes; the losers find nothing pending.
`TestMigrateIdempotentConcurrent` races 16 runners over fresh and over
goose-built databases. Run the migrator on a direct connection, not through
a transaction-pooling proxy.

Each migration file runs in one transaction with its history row, unless it
says `-- momo:no-transaction`. `migrate.lock_timeout` in `momo.yaml` (30 s)
bounds how long a DDL statement waits for its table lock; a migration that
hits it rolls back and the init container's loop runs it again.
`statement_timeout` is left to the role.

`hello-control migrate status` prints one line per momo migration,
`<version> <state> <name>`, with the states of `Engine.Status`: `applied`,
`pending`, `dirty` (a migration failed or was interrupted; fix it by hand,
then `momo migrate resolve`), `mismatch` (an applied file changed),
`missing` (in the history, no file). On a goose database that was not taken
over yet, a first line says so.

## The cutover from goose

Goose built the schema up to `00107_scim`. Those files are frozen in
`migrations/legacy` (package `legacy`; `TestFrozen` pins their digest): no
file is added, edited or renumbered there. The momo chain starts with two
migrations that together are exactly what the goose set built:

- `<version>_init`: the whole schema, generated by `momo generate --from-db`
  from a database goose had migrated to 00107, with its snapshot (every
  later migration's base).
- `<version>_legacy_seed`: hand-written, schema-neutral, the rows the goose
  migrations inserted (settings singletons, built-in ring tags, phonebooks,
  firmware minimums) and every sequence position, from
  `pg_dump --data-only` of the same database.

A database goose built is taken over by recording both as applied without
running them: `Engine.Baseline` first compares the live schema with the
snapshot, including check, default, trigger and function text, and refuses
on any difference (nothing is recorded, the init container retries and the
pod does not start). `goose_db_version` stays, and is listed in
`migrate.exclude`, so an older image rolled back onto the database finds no
goose work and starts. Baseline's comparison creates a scratch schema, so
the migrator's role needs `CREATE` on the database; on kw the role in the
`hello-store` DSN owns the database.

`TestMigrateTakeover` takes over databases at 00107 and at 00100;
`TestMigrateInitParity` builds one database each way and compares the
inspected schemas and every table's rows; `TestAIAccessSchema` and
`TestAIAgentSchema` take over from 00007 and 00008, through the goose data
steps those versions still need.

The CI job `momo-bootstrap` (dispatch only:
`gh workflow run ci.yaml --ref <branch> -f momo_bootstrap=true`) rebuilds
the goose database and writes the models, `init` and `legacy_seed` into its
artifact. It was used once, for the cutover; rerunning it gives new
versions, which must never replace migrations that have been applied. After
a rerun, move the `c.Sequence("prov_redirect_jobs_seq")` call it puts on the
first model to `ProvRedirectJob`.

The frozen set and the takeover ship in the cutover release and the next
one only (decided 2026-11-17). After that, a pre-cutover install, or a
backup taken before the cutover, must first be upgraded or restored with an
image from that window.

Rolling the cutover release back: before its first start nothing changed.
After it, the schema is unchanged (Baseline runs no DDL) and `momo_migrations`
is an extra table, so the previous image runs; `DROP TABLE momo_migrations`
undoes the takeover fully. After the first migration authored with momo has
applied, roll forward instead.

### Backups across the cutover

`Manifest.SchemaVersion` is the newest applied migration: a 14-digit momo
version, or a goose one (at most 107) for a backup taken before the cutover.
Every goose version is older than every momo version, so a pre-cutover
backup restores into this binary and is then migrated by
`migrate.UpAfterRestore`: the restore brought back `goose_db_version` and
the old schema but not `momo_migrations`, so the target's own momo history
is dropped and the takeover runs on the restored schema
(`TestRestorePreCutoverBackup`). A backup of a newer schema than the
binary's is refused as before.

## Bumping momo

Pin the exact new version in `go.mod`, read momo's `CHANGELOG.md` and
`docs/upgrading.md` for every release in between, then run `momo generate`
and `momo migrate validate` with the new version (the CI steps and the
commands above read the version from `go.mod`). A new snapshot or `momo.sum`
format is a deliberate change: momo documents it, and every branch then
needs the same version.
