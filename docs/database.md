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

Moved so far: `internal/crm` (`crm.PG`).

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
- A test that runs every method on `momo.NewOffline(dialect.Postgres)` proves
  that each statement builds without a database (see
  `internal/crm/pg_offline_test.go`), so the pull request tier catches a
  misspelled `@name`.

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
