# Database layer: goose and momo

Hello's schema is built by goose: the SQL files in `migrations/`, applied by
`hello-control migrate up` (`internal/migrate`). That does not change.

The query layer is moving to [momo](https://github.com/azrtydxb/momo), pinned
to an exact version in `go.mod` (v0.14.0), one package at a time. A package
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
- `internal/store`, at its seam. Its files move to `@name` binds one
  domain at a time (below); the files still on `$n` are those not listed
  here. Converted so far:
  - core and configuration: `store.go`, `batch.go`, `config.go`, `dialspace.go`, `dirprovision.go`, `flow_canvas.go`
  - phonebook: `phonebook.go`, `phonebook_lookup.go`, `personal_contacts.go`, `locations.go`
  - security and door: `scep.go`, `wifi.go`, `door.go`, `door_open.go`, `door_camera.go`, `door_webhook.go`
- `internal/store`, at its seam, with the files of the core and
  configuration domain converted to `@name` binds: `store.go`, `batch.go`,
  `config.go`, `dialspace.go`, `dirprovision.go` and `flow_canvas.go`, and
  of the faxes, mail and backup domain: `faxes.go`, `mail.go` and
  `backup.go`. Its other files still bind `$n` and move one domain at a time (below).
  `config.go`, `dialspace.go`, `dirprovision.go` and `flow_canvas.go`; and
  those of the users and authentication domain: `users.go`, `totp.go`,
  `oauth.go`, `ldap.go`, `onboarding.go` and `sidekey.go`. Its other files
  still bind `$n` and move one domain at a time (below).
  those of the phonebook domain: `phonebook.go`, `phonebook_lookup.go`,
  `personal_contacts.go` and `locations.go`; and those of the call
  features domain: `ivr.go`, `queues.go`, `queue_callbacks.go`,
  `followme.go`, `pickup.go`, `park.go`, `paging.go`, `ringmap.go`,
  `ringtag.go`, `conference.go` and `multicast.go`. Its other files still bind
  `personal_contacts.go` and `locations.go`; and those of the voicemail and
  media domain: `pbx.go`, `voicemail_groups.go`, `media.go`, `moh.go`,
  `ringtones.go`, `tts.go`, `recording_links.go`, `video_recording.go` and
  `cdr.go`. Its other files still bind
  `$n` and move one domain at a time (below).
  `personal_contacts.go` and `locations.go`; and those of the
  provisioning domain: `prov.go`, `prov_admin.go`, `prov_handler.go`,
  `prov_lines.go`, `prov_push.go`, `prov_overrides.go`, `prov_books.go`,
  `prov_multicast.go`, `syslog.go`, `rtcpxr.go`, `clientquality.go` and
  `clients.go`. Its other files still bind `$n` and move one domain at a
  time (below).
  `personal_contacts.go` and `locations.go`; and those of the routing and
  time domain: `routing.go`, `timecond.go`, `blocklist.go`, `originate.go`,
  `boss.go` and `calendar_dnd.go`. Its other files still bind `$n` and move
  one domain at a time (below).

Every package above builds its handle with `db.New` (or `db.NewPool`) where
the binary constructs it, except `internal/store`, which builds its own in
`store.New`.

`internal/store` runs on momo at its seam (`internal/store/dbq.go`): its
pool and its transactions are types with `database/sql`'s method set
(`ExecContext`, `QueryContext`, `QueryRowContext`, `BeginTx`) whose every
statement is a momo raw statement, on the store's `*momo.DB` or on momo's
view of the open `*sql.Tx` (`db.AdoptTx`). A converted file binds `@name`;
an unconverted one still binds `$n`, which momo accepts on PostgreSQL as
positional arguments (one placeholder style per statement: a `$n` statement
cannot also use `?` or `@name`). The handle and the transaction types are
`momo.Querier`s, so code can also pass them straight to `momo.Exec`,
`momo.Query[T]`, `momo.Scalars[V]` and the builders. Transactions are still
begun by `database/sql` and adopted, so methods that take a caller's
`*sql.Tx` (the directory Provisioner's) keep it. Three things stay raw:
BEGIN, `TryLock`'s pinned session-lock connection, and `SchemaVersion`'s read
of `goose_db_version`. `TestStatementsBuild` builds every constant statement
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

`internal/db/models` holds momo models of the tables a moved package uses,
generated from a database goose migrated: `momo.yaml` lists them in
`introspect.only`. The model files (`<table>.go`, with a
`// momo:fromdb sha256=` header) are written by `momo generate --from-db`,
and `momo_gen.go` (and the per-model packages) by `momo generate`. Never edit
them by hand.

### Regenerating the models

A goose migration that changes a modeled table must regenerate the models in
the same pull request. `TestMomoModelsMatchSchema` (in `test/integration`,
run by the integration job) migrates a scratch database with goose and
fails until the models match it, printing the expected files.

Against a database goose migrated to the latest version (not on a
developer Mac: use a lab or CI database), from the repository root:

```sh
HELLO_MODELS_DATABASE_URL=postgres://... \
  go run github.com/azrtydxb/momo/cmd/momo@v0.14.0 generate --from-db HELLO_MODELS_DATABASE_URL --no-snapshot --overwrite
go run github.com/azrtydxb/momo/cmd/momo@v0.14.0 generate
```

The first command writes the model files, the second the generated code.
`go run ...@v0.14.0` builds the momo command in its own module, so momo's
MySQL and SQLite drivers never enter Hello's `go.mod`. `--no-snapshot` keeps
momo from writing an init migration: goose owns migrations.

Without a database at hand, the failing integration run's log has the
expected content of every stale model file; copy it, then run the second
command.

To model another table, add it to `introspect.only` in `momo.yaml` and
regenerate. A foreign key to a table that has no model cannot be declared,
so the generator leaves it out with a warning; the test lists the known ones
(`droppedConstraints`) and fails on a new one so it is looked at.

### Bumping momo

Pin the exact new version in `go.mod`, read momo's `CHANGELOG.md` and
`docs/upgrading.md` for every release in between, update the version in the
commands above, in `momo.yaml` and in the test, and regenerate.
