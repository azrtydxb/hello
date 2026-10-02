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
	"maps"
	"net"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/secret"
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
	// routing is the trunk and route state; nil for a snapshot from New.
	routing *RoutingState
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

	// InitialBackoff is the first reconnect delay after a failure (1s); it
	// doubles up to PollInterval and resets after a session that loaded.
	InitialBackoff time.Duration

	// ConfigInvalid, when set, is 1 while the current revision's routing
	// does not compile and routing runs on the last good table;
	// InvalidRevision holds that revision (0 when routing is current).
	ConfigInvalid   prometheus.Gauge
	InvalidRevision prometheus.Gauge
	// DNSFailures, when set, counts trunk destination lookups that failed
	// (hello_dns_resolve_failures_total); the previous result is kept.
	DNSFailures prometheus.Counter

	// Box opens sealed trunk passwords; without it every trunk with a
	// password is misconfigured.
	Box *secret.Box
	// Resolver resolves trunk destinations (net.DefaultResolver); DNS runs
	// every ResolveInterval (30s) on its own goroutine, never on the call
	// path.
	Resolver        Resolver
	ResolveInterval time.Duration
	resolveOnceInit sync.Once
	resolveNow      chan struct{}
	resolved        atomic.Pointer[map[string][]string]

	cur atomic.Pointer[Snapshot]
	mu  sync.Mutex // serialises reload
}

// Current returns the latest good snapshot, or nil before the first load.
func (w *Watcher) Current() *Snapshot { return w.cur.Load() }

// Ready reports whether a snapshot has been loaded.
func (w *Watcher) Ready() bool { return w.cur.Load() != nil }

func (w *Watcher) install(s *Snapshot) {
	w.mu.Lock()
	defer w.mu.Unlock()
	old := w.cur.Load()
	if old != nil && old.Revision == s.Revision {
		return
	}
	if rs := s.routing; rs != nil && len(rs.Errors) > 0 {
		// Keep routing on the last good table rather than a broken one.
		kept := "none (internal calls only)"
		if old != nil && old.routing != nil { // a stored routing state always has a router
			rs.Router, rs.Config, rs.Misconfigured, rs.Resolved = old.routing.Router, old.routing.Config, old.routing.Misconfigured, old.routing.Resolved
			kept = "the last good revision"
		} else {
			rs.Router = stubRouter{cfg: rs.Config}
		}
		if w.Log != nil {
			w.Log.Error("routing configuration does not compile; routing is frozen on "+kept,
				"revision", s.Revision, "error_count", len(rs.Errors), "errors", summarise(rs.Errors))
		}
		w.setInvalid(1, s.Revision)
	} else if s.routing != nil {
		w.setInvalid(0, 0)
	}
	w.cur.Store(s)
	w.triggerResolve()
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

func (w *Watcher) initialBackoff() time.Duration {
	if w.InitialBackoff > 0 {
		return w.InitialBackoff
	}
	return time.Second
}

// Run watches until ctx is cancelled.
func (w *Watcher) Run(ctx context.Context) {
	poll := w.PollInterval
	if poll <= 0 {
		poll = 30 * time.Second
	}
	backoff := w.initialBackoff()
	for ctx.Err() == nil {
		loaded, err := w.session(ctx, poll)
		if ctx.Err() != nil {
			return
		}
		if loaded {
			// The session worked for a while: a fresh outage starts over.
			backoff = w.initialBackoff()
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
func (w *Watcher) session(ctx context.Context, poll time.Duration) (loaded bool, err error) {
	if w.Config == nil {
		return false, errors.New("no database configuration")
	}
	conn, err := pgx.ConnectConfig(ctx, w.Config)
	if err != nil {
		return false, err
	}
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = conn.Close(cctx)
	}()
	if _, err := conn.Exec(ctx, "LISTEN "+Channel); err != nil {
		return false, err
	}
	for {
		if err := w.load(ctx, conn); err != nil {
			return loaded, err
		}
		loaded = true
		wctx, cancel := context.WithTimeout(ctx, poll)
		_, err := conn.WaitForNotification(wctx)
		cancel()
		if ctx.Err() != nil {
			return loaded, ctx.Err()
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			return loaded, err
		}
		// A notification or a poll tick: reload either way.
	}
}

func (w *Watcher) load(ctx context.Context, conn Querier) error {
	lctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	s, err := w.loadAll(lctx, conn)
	if err != nil {
		return err
	}
	w.install(s)
	return nil
}

// loadAll reads devices and routing in one read-only transaction (when the
// connection can open one), so both come from the same revision.
func (w *Watcher) loadAll(ctx context.Context, conn Querier) (*Snapshot, error) {
	q := conn
	if b, ok := conn.(beginner); ok {
		tx, err := b.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
		if err != nil {
			return nil, err
		}
		defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
		q = tx
	}
	s, err := Load(ctx, q, w.Domain)
	if err != nil {
		return nil, err
	}
	cfg, bad, err := loadRouting(ctx, q, w.Box, w.Log)
	if err != nil {
		return nil, err
	}
	var resolved map[string][]string
	if r := w.resolved.Load(); r != nil {
		resolved = *r
	}
	return s.WithRouting(buildRouting(cfg, bad, resolved)), nil
}

func (w *Watcher) setInvalid(v float64, rev int64) {
	if w.ConfigInvalid != nil {
		w.ConfigInvalid.Set(v)
	}
	if w.InvalidRevision != nil {
		w.InvalidRevision.Set(float64(rev))
	}
}

// summarise lists the first field errors (paths and messages only; the
// engine's messages never carry configuration values like passwords).
func summarise(errs []routing.FieldError) string {
	const shown = 5
	var b strings.Builder
	for i, e := range errs {
		if i == shown {
			fmt.Fprintf(&b, "; and %d more", len(errs)-shown)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(e.Path + ": " + e.Message)
	}
	return b.String()
}

func (w *Watcher) kick() chan struct{} {
	w.resolveOnceInit.Do(func() { w.resolveNow = make(chan struct{}, 1) })
	return w.resolveNow
}

func (w *Watcher) triggerResolve() {
	select {
	case w.kick() <- struct{}{}:
	default:
	}
}

// RunResolver resolves trunk destinations every ResolveInterval and after
// each new revision, recompiling the routing table (same revision) when the
// results change. It never blocks the call path.
func (w *Watcher) RunResolver(ctx context.Context) {
	interval := w.ResolveInterval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	var r Resolver = net.DefaultResolver
	if w.Resolver != nil {
		r = w.Resolver
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		w.resolveOnce(ctx, r)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-w.kick():
		}
	}
}

func (w *Watcher) resolveOnce(ctx context.Context, r Resolver) {
	cur := w.cur.Load()
	if cur == nil || cur.routing == nil {
		return
	}
	res, failed := resolveAll(ctx, r, cur.routing.Config.Trunks, cur.routing.Resolved)
	if len(failed) > 0 {
		if w.DNSFailures != nil {
			w.DNSFailures.Add(float64(len(failed)))
		}
		if w.Log != nil {
			w.Log.Warn("trunk destination lookup failed; keeping the previous addresses", "destinations", strings.Join(failed, ", "))
		}
	}
	w.resolved.Store(&res)
	if maps.EqualFunc(res, cur.routing.Resolved, slices.Equal) {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	cur = w.cur.Load()
	next := *cur
	next.routing = buildRouting(cur.routing.Config, cur.routing.Misconfigured, res)
	if len(next.routing.Errors) > 0 {
		return // it compiled before; keep it rather than fail on DNS data
	}
	// A DNS-only rebuild of a frozen (last good) table keeps the current
	// revision's compile errors: they mark routing as frozen, which makes
	// calls check this revision's extensions before the old table.
	next.routing.Errors = cur.routing.Errors
	w.cur.Store(&next)
	if w.Log != nil {
		w.Log.Info("trunk destinations resolved", "revision", next.Revision, "destinations", len(res))
	}
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
