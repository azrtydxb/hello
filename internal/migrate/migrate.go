// Package migrate applies Hello's PostgreSQL migrations (migrations.FS) with
// momo's migration engine, and takes over a database that goose built before
// the cutover.
//
// Up decides by what the database holds:
//
//   - no goose_db_version and no momo history (a new install): apply every
//     migration, the init migration first;
//   - goose_db_version and no momo history (an install, or a restored backup,
//     from before the cutover): bring it to the newest frozen goose migration
//     (migrations/legacy), record every momo migration up to legacy_seed as
//     applied without running it (Engine.Baseline, which first checks that the
//     live schema is the snapshot's), then apply the rest;
//   - momo history: apply what is pending.
//
// goose_db_version is left in place, so an older image rolled back onto a
// taken-over database finds no goose work and starts.
//
// Concurrent hello-control replicas are safe: the decision runs under a
// session advisory lock of its own, and Engine.Baseline and Engine.Apply take
// momo's history lock, so each migration runs once and the others wait.
package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"time"

	"github.com/azrtydxb/momo/driver/pgxdriver"
	momomigrate "github.com/azrtydxb/momo/migrate"
	momopg "github.com/azrtydxb/momo/migrate/postgres"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	hello "github.com/azrtydxb/hello"
	"github.com/azrtydxb/hello/migrations"
	"github.com/azrtydxb/hello/migrations/legacy"
)

const (
	// LockWait is how long a replica waits for another one's migration run:
	// the takeover lock, goose's lock and momo's history lock each.
	LockWait = 5 * time.Minute

	// TakeoverName is the name of the momo migration a goose-built database
	// is recorded at: init (the schema of legacy.Latest) and legacy_seed (the
	// rows those goose migrations inserted).
	TakeoverName = "legacy_seed"

	// takeoverLockID is the session advisory lock Up holds while it decides
	// and takes over ("hellomig"). momo's history lock and goose's are other
	// keys, so neither waits on it.
	takeoverLockID int64 = 0x68656c6c6f6d6967

	lockPoll = 200 * time.Millisecond
)

// Engine returns momo's migration engine for migrations.FS on db, with the
// migrate settings of momo.yaml.
func Engine(db *sql.DB) (*momomigrate.Engine, error) { return EngineFor(db, migrations.FS) }

// EngineFor is Engine for another migration directory (a test's).
func EngineFor(db *sql.DB, dir fs.FS) (*momomigrate.Engine, error) {
	opts, err := momomigrate.ConfigFromFile(hello.MomoConfig, "momo.yaml")
	if err != nil {
		return nil, fmt.Errorf("migrate: %w", err)
	}
	opts = append(opts, momomigrate.WithDialect(momopg.New()), momomigrate.WithLockWait(LockWait))
	return momomigrate.NewEngine(pgxdriver.FromSQLDB(db), dir, opts...)
}

// Up brings db to the newest migration and returns how many migrations ran:
// goose migrations of the takeover plus momo migrations. A baselined
// migration does not run and is not counted.
func Up(ctx context.Context, db *sql.DB) (int, error) { return up(ctx, db, false) }

// UpAfterRestore is Up for a database a backup was just restored into, the
// backup taken at schema version from (backup Manifest.SchemaVersion). A
// backup from before the cutover (a goose version, IsLegacyVersion) brought
// back goose_db_version and the schema of that version but not
// momo_migrations, so the database's own momo history no longer describes it:
// it is dropped first, and Up takes the restored schema over from goose.
func UpAfterRestore(ctx context.Context, db *sql.DB, from int64) (int, error) {
	return up(ctx, db, IsLegacyVersion(from))
}

// IsLegacyVersion reports whether v is a goose migration version (00001 to
// legacy.Latest) rather than a momo one (a 14-digit timestamp).
func IsLegacyVersion(v int64) bool { return v > 0 && v <= legacy.Latest }

func up(ctx context.Context, db *sql.DB, forget bool) (n int, err error) {
	e, err := Engine(db)
	if err != nil {
		return 0, err
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = conn.Close() }()
	unlock, err := takeoverLock(ctx, conn)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, unlock()) }()

	if forget {
		if _, err := conn.ExecContext(ctx, `DROP TABLE IF EXISTS momo_migrations`); err != nil {
			return 0, fmt.Errorf("migrate: drop the momo history the restore replaced: %w", err)
		}
	}
	st, err := readState(ctx, conn)
	if err != nil {
		return 0, err
	}
	if st.goose && !st.momo {
		if n, err = LegacyUp(ctx, db); err != nil {
			return n, err
		}
		v, err := TakeoverVersion()
		if err != nil {
			return n, err
		}
		if err := e.Baseline(ctx, v, false); err != nil {
			return n, fmt.Errorf("migrate: take over the goose schema at %s_%s: %w", v, TakeoverName, err)
		}
	}
	rep, err := e.Apply(ctx, momomigrate.ApplyOptions{LockWait: LockWait})
	if err != nil {
		return n, fmt.Errorf("migrate up: %w", err)
	}
	return n + len(rep.Applied), nil
}

// LegacyUp applies the frozen goose migrations that db lacks, as a
// pre-cutover hello-control did, and returns how many it applied. Up calls
// it during the takeover; tests use it to build a pre-cutover database.
func LegacyUp(ctx context.Context, db *sql.DB) (int, error) {
	// Retry every second for up to five minutes: a waiting replica starts soon
	// after the one holding the lock finishes.
	locker, err := lock.NewPostgresSessionLocker(lock.WithLockTimeout(1, uint64(LockWait/time.Second)))
	if err != nil {
		return 0, err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, legacy.FS, goose.WithSessionLocker(locker))
	if err != nil {
		return 0, err
	}
	res, err := p.Up(ctx)
	if err != nil {
		return len(res), fmt.Errorf("migrate: goose up the frozen set: %w", err)
	}
	return len(res), nil
}

// TakeoverVersion is the version of the legacy_seed migration.
func TakeoverVersion() (string, error) {
	files, err := momomigrate.Discover(migrations.FS)
	if err != nil {
		return "", err
	}
	v := ""
	for _, f := range files {
		if f.Name == TakeoverName && f.Kind == "up" {
			if v != "" {
				return "", fmt.Errorf("migrate: two %s migrations (%s and %s)", TakeoverName, v, f.Version)
			}
			v = f.Version
		}
	}
	if v == "" {
		return "", fmt.Errorf("migrate: no %s migration in migrations/postgres", TakeoverName)
	}
	return v, nil
}

// state is which migration histories a database holds.
type state struct {
	goose        bool  // goose_db_version exists
	gooseVersion int64 // its newest applied version
	momo         bool  // momo_migrations has a migration row
}

func readState(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}) (state, error) {
	var st state
	var momoTable bool
	if err := q.QueryRowContext(ctx,
		`SELECT to_regclass('goose_db_version') IS NOT NULL, to_regclass('momo_migrations') IS NOT NULL`,
	).Scan(&st.goose, &momoTable); err != nil {
		return st, fmt.Errorf("migrate: read the migration histories: %w", err)
	}
	if st.goose {
		var v sql.NullInt64
		if err := q.QueryRowContext(ctx, `SELECT max(version_id) FROM goose_db_version WHERE is_applied`).Scan(&v); err != nil {
			return st, fmt.Errorf("migrate: read goose_db_version: %w", err)
		}
		st.gooseVersion = v.Int64
	}
	if momoTable {
		if err := q.QueryRowContext(ctx,
			`SELECT EXISTS (SELECT 1 FROM momo_migrations WHERE version <> '__momo_meta__')`,
		).Scan(&st.momo); err != nil {
			return st, fmt.Errorf("migrate: read momo_migrations: %w", err)
		}
	}
	return st, nil
}

// takeoverLock takes the takeover lock on conn, polling until LockWait, and
// returns its release.
func takeoverLock(ctx context.Context, conn *sql.Conn) (func() error, error) {
	deadline := time.Now().Add(LockWait)
	for {
		var ok bool
		if err := conn.QueryRowContext(ctx, `SELECT pg_try_advisory_lock($1)`, takeoverLockID).Scan(&ok); err != nil {
			return nil, fmt.Errorf("migrate: take the migration lock: %w", err)
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("migrate: another hello-control held the migration lock for %s", LockWait)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
	return func() error {
		// Release even when ctx ended; closing the connection would too.
		uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		_, err := conn.ExecContext(uctx, `SELECT pg_advisory_unlock($1)`, takeoverLockID)
		return err
	}, nil
}

// Report is what Status found.
type Report struct {
	// Goose is the newest applied goose migration when goose_db_version
	// exists, else 0.
	Goose int64
	// TakenOver is set when momo's history holds a migration: Up applies
	// momo migrations only. Unset with Goose above 0, the next Up takes the
	// database over.
	TakenOver bool
	// Versions is each momo migration and its state (Engine.Status).
	Versions []momomigrate.VersionStatus
}

// Status reports the migration state of db without changing it.
func Status(ctx context.Context, db *sql.DB) (*Report, error) {
	e, err := Engine(db)
	if err != nil {
		return nil, err
	}
	st, err := readState(ctx, db)
	if err != nil {
		return nil, err
	}
	vs, err := e.Status(ctx)
	if err != nil {
		return nil, err
	}
	return &Report{Goose: st.gooseVersion, TakenOver: st.momo, Versions: vs}, nil
}
