// Package snapshot holds hello-sip's in-memory, revisioned copy of the
// enabled devices and their extensions. It is loaded from PostgreSQL at
// startup and reloaded on a hello_config NOTIFY or a periodic poll, so the SIP
// request path never queries the database.
package snapshot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// Device is one enabled device and the extension it belongs to. The HA1
// values are digest secrets: never log them.
type Device struct {
	ID            int64
	Username      string
	Realm         string
	HA1MD5        string
	HA1SHA256     string
	Extension     string // extension number
	ExtensionName string
}

// LogValue keeps the HA1 values out of logs.
func (d Device) LogValue() slog.Value {
	return slog.GroupValue(slog.Int64("id", d.ID), slog.String("username", d.Username), slog.String("extension", d.Extension))
}

// Snapshot is an immutable view of the configuration at one revision.
type Snapshot struct {
	Revision int64
	// Skipped lists usernames whose realm did not match the SIP domain.
	Skipped []string

	byUser map[string]Device
	byExt  map[string][]Device
	// known holds extension numbers that exist but have no enabled device,
	// so a call to them is 480 rather than 404.
	known map[string]bool
}

// New builds a snapshot from devices; devices whose realm differs from
// domain are skipped and listed in Skipped.
func New(revision int64, domain string, devices []Device) *Snapshot {
	s := &Snapshot{Revision: revision, byUser: map[string]Device{}, byExt: map[string][]Device{}}
	for _, d := range devices {
		if d.Realm != domain {
			s.Skipped = append(s.Skipped, d.Username)
			continue
		}
		s.byUser[d.Username] = d
		s.byExt[d.Extension] = append(s.byExt[d.Extension], d)
	}
	return s
}

// DeviceByUsername returns the enabled device with that SIP username.
func (s *Snapshot) DeviceByUsername(username string) (Device, bool) {
	d, ok := s.byUser[username]
	return d, ok
}

// DevicesForExtension returns the enabled devices of an extension number.
func (s *Snapshot) DevicesForExtension(number string) []Device {
	return s.byExt[number]
}

// HasExtension reports whether number is a configured extension, with or
// without enabled devices.
func (s *Snapshot) HasExtension(number string) bool {
	return len(s.byExt[number]) > 0 || s.known[number]
}

// WithExtensions marks extension numbers as configured even when they have
// no enabled device; it returns s for chaining.
func (s *Snapshot) WithExtensions(numbers ...string) *Snapshot {
	if s.known == nil {
		s.known = map[string]bool{}
	}
	for _, n := range numbers {
		s.known[n] = true
	}
	return s
}

// Usernames returns every device username in the snapshot.
func (s *Snapshot) Usernames() []string {
	out := make([]string, 0, len(s.byUser))
	for u := range s.byUser {
		out = append(out, u)
	}
	return out
}

// Querier is the subset of *pgx.Conn (or a pool) Load needs.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// The snapshot query from the shared contracts, starting from extensions so
// that an extension without enabled devices is still known (its device
// columns are NULL).
const snapshotQuery = `SELECT (SELECT config_revision FROM schema_info),
       d.id, d.sip_username, d.realm, d.ha1_md5, d.ha1_sha256, e.number, e.name
FROM extensions e LEFT JOIN devices d ON d.extension_id = e.id AND d.enabled`

// Load reads the snapshot for domain in one round trip (plus one when no
// extension exists, to learn the revision).
func Load(ctx context.Context, db Querier, domain string) (*Snapshot, error) {
	rows, err := db.Query(ctx, snapshotQuery)
	if err != nil {
		return nil, fmt.Errorf("snapshot: query: %w", err)
	}
	var (
		rev     int64
		devices []Device
		bare    []string
		anyRow  bool
	)
	for rows.Next() {
		anyRow = true
		var (
			d                              Device
			id                             *int64
			user, realm, ha1MD5, ha1SHA256 *string
		)
		if err := rows.Scan(&rev, &id, &user, &realm, &ha1MD5, &ha1SHA256, &d.Extension, &d.ExtensionName); err != nil {
			rows.Close()
			return nil, fmt.Errorf("snapshot: scan: %w", err)
		}
		if id == nil { // extension with no enabled device
			bare = append(bare, d.Extension)
			continue
		}
		d.ID, d.Username, d.Realm, d.HA1MD5, d.HA1SHA256 = *id, *user, *realm, *ha1MD5, *ha1SHA256
		devices = append(devices, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("snapshot: rows: %w", err)
	}
	if !anyRow {
		if err := db.QueryRow(ctx, "SELECT config_revision FROM schema_info").Scan(&rev); err != nil {
			return nil, fmt.Errorf("snapshot: revision: %w", err)
		}
	}
	return New(rev, domain, devices).WithExtensions(bare...), nil
}

// Channel is the NOTIFY channel hello-control signals revisions on.
const Channel = "hello_config"

// Watcher keeps the current snapshot fresh: it LISTENs on hello_config and
// also polls every PollInterval, so a NOTIFY lost during a reconnect is
// caught. On any database error it keeps the last good snapshot.
type Watcher struct {
	Config       *pgx.ConnConfig
	Domain       string
	PollInterval time.Duration // default 30s
	Log          *slog.Logger
	// ReloadFailures, when set, counts failed connects and loads.
	ReloadFailures prometheus.Counter
	// OnReload, when set, is called after a load that produced a new
	// revision (old is nil on the first load). It runs on the watcher
	// goroutine, so it must not block for long.
	OnReload func(old, cur *Snapshot)

	cur atomic.Pointer[Snapshot]
	mu  sync.Mutex // serialises reload
}

// Current returns the latest good snapshot, or nil before the first load.
func (w *Watcher) Current() *Snapshot { return w.cur.Load() }

// Ready reports whether a snapshot has been loaded.
func (w *Watcher) Ready() bool { return w.cur.Load() != nil }

// Set installs s as current, as a successful load would; for tests and
// callers that load out of band.
func (w *Watcher) Set(s *Snapshot) { w.install(s) }

func (w *Watcher) install(s *Snapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	old := w.cur.Load()
	if old != nil && old.Revision == s.Revision {
		return
	}
	w.cur.Store(s)
	if w.Log != nil {
		w.Log.Info("configuration snapshot loaded", "revision", s.Revision, "devices", len(s.byUser))
		for _, u := range s.Skipped {
			w.Log.Warn("device skipped: realm does not match HELLO_SIP_DOMAIN", "device", u)
		}
	}
	if w.OnReload != nil {
		w.OnReload(old, s)
	}
}

// Run watches until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	poll := w.PollInterval
	if poll <= 0 {
		poll = 30 * time.Second
	}
	backoff := time.Second
	for ctx.Err() == nil {
		err := w.session(ctx, poll)
		if ctx.Err() != nil {
			return
		}
		w.fail("snapshot watcher disconnected", err)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, poll)
	}
}

// session connects, LISTENs, loads, and then reloads on every notification
// or poll tick until an error occurs.
func (w *Watcher) session(ctx context.Context, poll time.Duration) error {
	if w.Config == nil {
		return errors.New("no database configuration")
	}
	conn, err := pgx.ConnectConfig(ctx, w.Config)
	if err != nil {
		return err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return err
	}
	for {
		if err := w.load(ctx, conn); err != nil {
			return err
		}
		wctx, cancel := context.WithTimeout(ctx, poll)
		_, err := conn.WaitForNotification(wctx)
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		// A notification or a poll tick: reload either way.
	}
}

func (w *Watcher) load(ctx context.Context, conn Querier) error {
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s, err := Load(lctx, conn, w.Domain)
	if err != nil {
		return err
	}
	w.install(s)
	return nil
}

func (w *Watcher) fail(msg string, err error) {
	if w.ReloadFailures != nil {
		w.ReloadFailures.Inc()
	}
	if w.Log != nil {
		w.Log.Warn(msg, "error", err, "kept_revision", w.revision())
	}
}

func (w *Watcher) revision() int64 {
	if s := w.cur.Load(); s != nil {
		return s.Revision
	}
	return -1
}
