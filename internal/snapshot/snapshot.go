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

// Extension is the PBX feature state (Phase 4) of one extension number.
// VoicemailBoxID is 0 when the extension has no voicemail box.
type Extension struct {
	Number, Name string
	DND          bool
	// ForwardAlways, ForwardBusy and ForwardNoAnswer are the forwarding
	// targets: empty means off; a number that is an extension or an
	// external number.
	ForwardAlways, ForwardBusy, ForwardNoAnswer string
	VoicemailEnabled                            bool
	VoicemailBoxID                              int64
	// RecordDefault records this extension's calls unless the caller
	// already records them (migration 00005; spec S-4).
	RecordDefault bool
}

// RingGroup is a ring or hunt group (contract 2). Strategy is one of
// ring-all, sequential, round-robin, longest-idle, weighted.
type RingGroup struct {
	ID          int64
	Name        string // dialled as a number: digits, dots, dashes
	Strategy    string
	Hunt        bool
	RingTimeout int // seconds the whole group rings
	MemberDelay int // seconds each sequential member rings before the next
	IgnoreDND   bool
	// FailureKind is none, voicemail (FailureTarget names an extension
	// whose box takes the call), external (FailureTarget is a number) or
	// announcement (FailureTarget names an announcement, migration 00005;
	// spec S-5).
	FailureKind, FailureTarget string
}

// RingGroupMember is one member of a group, ordered by Position.
type RingGroupMember struct {
	ExtensionID int64
	Extension   string // the extension number to ring
	Position    int
	Weight      int
	Delay       int // this member's extra delay in seconds
}

// FeatureCode is one DTMF feature code and the action it performs. Action
// is one of forward_always, forward_busy, forward_no_answer, dnd_on,
// dnd_off, voicemail, blind_transfer, attended_transfer and, since
// migration 00005, announcement (Argument names the announcement).
type FeatureCode struct {
	Code, Action, Argument string
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
	// exts is the per-extension feature state; groups maps a dialled group
	// name to its members; codes maps a dialled feature code; anns maps an
	// announcement name to its MinIO object.
	exts   map[string]Extension
	groups map[string]groupEntry
	codes  map[string]FeatureCode
	anns   map[string]string
	// routing is the trunk and route state; nil for a snapshot from New.
	routing *RoutingState
}

// groupEntry is one group with its members in position order.
type groupEntry struct {
	g       RingGroup
	members []RingGroupMember
}

// New builds a snapshot from devices; devices whose realm differs from
// domain are skipped and listed in Skipped.
func New(revision int64, domain string, devices []Device) *Snapshot {
	s := &Snapshot{Revision: revision, byUser: map[string]Device{}, byExt: map[string][]Device{},
		exts: map[string]Extension{}, groups: map[string]groupEntry{}, codes: map[string]FeatureCode{},
		anns: map[string]string{}, known: map[string]bool{}}
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

// WithExtensionData installs per-extension feature state (tests and Load);
// it returns s for chaining. An entry also marks the number as known, so an
// extension without enabled devices stays 480 rather than 404.
func (s *Snapshot) WithExtensionData(exts []Extension) *Snapshot {
	for _, e := range exts {
		s.exts[e.Number] = e
		s.known[e.Number] = true
	}
	return s
}

// WithRingGroups installs groups; each member list must belong to the
// preceding group and arrives in position order (tests and Load).
func (s *Snapshot) WithRingGroups(groups []RingGroup, members [][]RingGroupMember) *Snapshot {
	for i, g := range groups {
		s.groups[g.Name] = groupEntry{g: g, members: members[i]}
	}
	return s
}

// WithFeatureCodes installs the DTMF feature codes (tests and Load).
func (s *Snapshot) WithFeatureCodes(codes []FeatureCode) *Snapshot {
	for _, c := range codes {
		s.codes[c.Code] = c
	}
	return s
}

// WithAnnouncements installs the announcement set: name -> MinIO object
// (tests and Load). It returns s for chaining.
func (s *Snapshot) WithAnnouncements(anns map[string]string) *Snapshot {
	for n, o := range anns {
		s.anns[n] = o
	}
	return s
}

// Announcement returns the MinIO object of a named announcement; ok is
// false without one (spec failure mode: the missing-WAV step is skipped).
func (s *Snapshot) Announcement(name string) (string, bool) {
	o, ok := s.anns[name]
	return o, ok
}

// Extension returns the feature state of an extension number; ok is false
// for a number that is not an extension at all.
func (s *Snapshot) Extension(number string) (Extension, bool) {
	e, ok := s.exts[number]
	return e, ok
}

// RingGroup returns the group dialled as number (its name) with its members
// ordered by position (contract 2).
func (s *Snapshot) RingGroup(number string) (RingGroup, []RingGroupMember, bool) {
	e, ok := s.groups[number]
	return e.g, e.members, ok
}

// FeatureCode returns the feature code dialled as code.
func (s *Snapshot) FeatureCode(code string) (FeatureCode, bool) {
	c, ok := s.codes[code]
	return c, ok
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
// columns are NULL). The Phase 4 feature columns and the extension's
// voicemail box (LEFT JOIN: box-less extensions stay voicemail-disabled)
// ride along in the same row, so one query keeps devices and features on one
// revision.
const snapshotQuery = `SELECT (SELECT config_revision FROM schema_info),
       d.id, d.sip_username, d.realm, d.ha1_md5, d.ha1_sha256, e.number, e.name,
       e.dnd, e.forward_always, e.forward_busy, e.forward_no_answer, e.voicemail_enabled, v.id,
       e.record_default
FROM extensions e
LEFT JOIN devices d ON d.extension_id = e.id AND d.enabled
LEFT JOIN voicemail_boxes v ON v.extension_id = e.id
ORDER BY d.id`

// Load reads the snapshot for domain in one round trip per table (plus one
// when no extension exists, to learn the revision): devices with their
// extension features, then ring groups, then feature codes.
func Load(ctx context.Context, db Querier, domain string) (*Snapshot, error) {
	rows, err := db.Query(ctx, snapshotQuery)
	if err != nil {
		return nil, fmt.Errorf("snapshot: query: %w", err)
	}
	var (
		rev     int64
		devices []Device
		exts    []Extension
		seen    = map[string]bool{}
		bare    []string
		anyRow  bool
	)
	for rows.Next() {
		anyRow = true
		var (
			d                              Device
			id                             *int64
			user, realm, ha1MD5, ha1SHA256 *string
			e                              Extension
			boxID                          *int64
		)
		if err := rows.Scan(&rev, &id, &user, &realm, &ha1MD5, &ha1SHA256, &d.Extension, &d.ExtensionName,
			&e.DND, &e.ForwardAlways, &e.ForwardBusy, &e.ForwardNoAnswer, &e.VoicemailEnabled, &boxID,
			&e.RecordDefault); err != nil {
			rows.Close()
			return nil, fmt.Errorf("snapshot: scan: %w", err)
		}
		e.Number, e.Name = d.Extension, d.ExtensionName
		if boxID != nil {
			e.VoicemailBoxID = *boxID
		} else {
			// Box-less: no anchor is ever set up for this extension, whatever
			// the voicemail_enabled column says (the LEFT JOIN left it NULL).
			e.VoicemailEnabled = false
		}
		if id == nil { // extension with no enabled device
			bare = append(bare, d.Extension)
		} else {
			d.ID, d.Username, d.Realm, d.HA1MD5, d.HA1SHA256 = *id, *user, *realm, *ha1MD5, *ha1SHA256
			devices = append(devices, d)
		}
		if !seen[e.Number] {
			seen[e.Number] = true
			exts = append(exts, e)
		}
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
	groups, members, err := loadRingGroups(ctx, db)
	if err != nil {
		return nil, err
	}
	codes, err := loadFeatureCodes(ctx, db)
	if err != nil {
		return nil, err
	}
	anns, err := loadAnnouncements(ctx, db)
	if err != nil {
		return nil, err
	}
	return New(rev, domain, devices).WithExtensions(bare...).
		WithExtensionData(exts).WithRingGroups(groups, members).WithFeatureCodes(codes).
		WithAnnouncements(anns), nil
}

// loadAnnouncements reads the announcement set (name -> MinIO object), so
// playing one stays off the database path.
func loadAnnouncements(ctx context.Context, db Querier) (map[string]string, error) {
	rows, err := db.Query(ctx, `SELECT name, minio_object FROM announcements ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: announcements: %w", err)
	}
	defer rows.Close()
	anns := map[string]string{}
	for rows.Next() {
		var name, obj string
		if err := rows.Scan(&name, &obj); err != nil {
			return nil, fmt.Errorf("snapshot: scan announcement: %w", err)
		}
		anns[name] = obj
	}
	return anns, rows.Err()
}

// loadRingGroups reads the groups and their members (in position order) as
// parallel slices for WithRingGroups.
func loadRingGroups(ctx context.Context, db Querier) ([]RingGroup, [][]RingGroupMember, error) {
	rows, err := db.Query(ctx, `SELECT id, name, strategy, hunt, ring_timeout, member_delay, ignore_dnd, failure_kind, failure_target
FROM ring_groups ORDER BY id`)
	if err != nil {
		return nil, nil, fmt.Errorf("snapshot: ring groups: %w", err)
	}
	var (
		groups  []RingGroup
		members [][]RingGroupMember
	)
	for rows.Next() {
		var (
			g              RingGroup
			timeout, delay int32
			ext            *string
		)
		if err := rows.Scan(&g.ID, &g.Name, &g.Strategy, &g.Hunt, &timeout, &delay, &g.IgnoreDND, &g.FailureKind, &ext); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("snapshot: scan ring group: %w", err)
		}
		g.RingTimeout, g.MemberDelay = int(timeout), int(delay)
		if ext != nil {
			g.FailureTarget = *ext
		}
		groups = append(groups, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("snapshot: ring groups: %w", err)
	}
	for _, g := range groups {
		memberRows, err := db.Query(ctx, `SELECT m.extension_id, e.number, m.position, m.weight, m.delay
FROM ring_group_members m JOIN extensions e ON e.id = m.extension_id
WHERE m.group_id = $1 ORDER BY m.position`, g.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("snapshot: ring group members: %w", err)
		}
		var ms []RingGroupMember
		for memberRows.Next() {
			var (
				m                   RingGroupMember
				pos, w8, memberBusy int32
			)
			if err := memberRows.Scan(&m.ExtensionID, &m.Extension, &pos, &w8, &memberBusy); err != nil {
				memberRows.Close()
				return nil, nil, fmt.Errorf("snapshot: scan ring group member: %w", err)
			}
			m.Position, m.Weight, m.Delay = int(pos), int(w8), int(memberBusy)
			ms = append(ms, m)
		}
		if err := memberRows.Err(); err != nil {
			memberRows.Close()
			return nil, nil, fmt.Errorf("snapshot: ring group members: %w, at group %d", err, g.ID)
		}
		memberRows.Close()
		members = append(members, ms)
	}
	return groups, members, nil
}

// loadFeatureCodes reads the DTMF feature codes, so dialling one stays off
// the database path.
func loadFeatureCodes(ctx context.Context, db Querier) ([]FeatureCode, error) {
	rows, err := db.Query(ctx, `SELECT code, action, argument FROM feature_codes ORDER BY code`)
	if err != nil {
		return nil, fmt.Errorf("snapshot: feature codes: %w", err)
	}
	defer rows.Close()
	var codes []FeatureCode
	for rows.Next() {
		var c FeatureCode
		if err := rows.Scan(&c.Code, &c.Action, &c.Argument); err != nil {
			return nil, fmt.Errorf("snapshot: scan feature code: %w", err)
		}
		codes = append(codes, c)
	}
	return codes, rows.Err()
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
	// Revision, when set, holds the configuration revision of the snapshot
	// in use (hello_config_revision): set just before the snapshot serves
	// requests, so a reader that sees revision N knows N's devices
	// authenticate on this node.
	Revision prometheus.Gauge
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
	if w.Revision != nil {
		w.Revision.Set(float64(s.Revision))
	}
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

// Codes returns the feature codes, longest first, so a code that is
// another's prefix matches deterministically.
func (s *Snapshot) Codes() []FeatureCode {
	out := make([]FeatureCode, 0, len(s.codes))
	for _, c := range s.codes {
		out = append(out, c)
	}
	sortCodes(out)
	return out
}

// sortCodes orders longest code first.
func sortCodes(codes []FeatureCode) {
	for i := 1; i < len(codes); i++ {
		for j := i; j > 0 && len(codes[j].Code) > len(codes[j-1].Code); j-- {
			codes[j], codes[j-1] = codes[j-1], codes[j]
		}
	}
}
