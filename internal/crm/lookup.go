package crm

import (
	"context"
	"encoding/json"
	"log/slog"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Lookup timings (spec I-47): a hit is cached 5 minutes, a miss 60 seconds,
// and a lookup at ringing time is bounded to 1.5 seconds.
const (
	PositiveTTL   = 5 * time.Minute
	NegativeTTL   = 60 * time.Second
	LookupTimeout = 1500 * time.Millisecond
	// MinLookupDigits is the shortest number looked up; extension numbers
	// never leave Hello.
	MinLookupDigits = 6
	// breakerFails failures in a row stop lookups at a connector for
	// breakerPause.
	breakerFails = 3
	breakerPause = 30 * time.Second
	listTTL      = 15 * time.Second
	cachePrefix  = "hello:crm:lookup:"
)

// Cache is the lookup cache (Valkey in production).
type Cache interface {
	Get(ctx context.Context, key string) ([]byte, bool)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration)
}

// MemCache is an in-memory Cache.
type MemCache struct {
	Now func() time.Time
	mu  sync.Mutex
	m   map[string]memEntry
}

type memEntry struct {
	v   []byte
	exp time.Time
}

func (c *MemCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Get implements Cache.
func (c *MemCache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[key]
	if !ok || !e.exp.After(c.now()) {
		return nil, false
	}
	return e.v, true
}

// Set implements Cache.
func (c *MemCache) Set(_ context.Context, key string, val []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]memEntry{}
	}
	c.m[key] = memEntry{val, c.now().Add(ttl)}
}

// NormalizeNumber reduces a phone number to digits, with a leading + kept
// for an international number ("0032..." becomes "+32..."). ok is false for
// a number too short to look up or with letters in it (an extension, a name).
func NormalizeNumber(s string) (string, bool) {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '+' && i == 0:
			b.WriteRune(r)
		case strings.ContainsRune(" -.()/", r):
		default:
			return "", false
		}
	}
	n := b.String()
	if rest, ok := strings.CutPrefix(n, "00"); ok && !strings.HasPrefix(n, "+") {
		n = "+" + rest
	}
	if len(strings.TrimPrefix(n, "+")) < MinLookupDigits {
		return "", false
	}
	return n, true
}

func digitsOf(n string) string { return strings.TrimPrefix(n, "+") }

// Lookup finds the CRM contact of a number across the active connectors,
// through a cache, and never makes the caller wait longer than asked.
type Lookup struct {
	Repo   Repo
	Tokens *TokenManager
	Cache  Cache
	// Timeout bounds one CRM query (LookupTimeout when zero).
	Timeout time.Duration
	Log     *slog.Logger
	Now     func() time.Time

	mu       sync.Mutex
	list     []ConnectorRow
	listAt   time.Time
	inflight map[string]*flight
	breaker  map[int64]*breaker
}

type flight struct {
	done chan struct{}
	c    *Contact
}

type breaker struct {
	fails int
	until time.Time
}

func (l *Lookup) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func (l *Lookup) log() *slog.Logger {
	if l.Log != nil {
		return l.Log
	}
	return slog.New(slog.DiscardHandler)
}

func (l *Lookup) timeout() time.Duration {
	if l.Timeout > 0 {
		return l.Timeout
	}
	return LookupTimeout
}

// scoped is whether a connector signs in per user.
func (l *Lookup) scoped(r ConnectorRow) bool {
	c, ok := l.Tokens.Providers(r.Kind)
	return ok && c.Capabilities().UserScope
}

// connectors returns the active organisation-level connectors that can look
// contacts up (the per-user ones are asked by FindForUser).
func (l *Lookup) connectors(ctx context.Context) []ConnectorRow {
	var out []ConnectorRow
	for _, r := range l.lookupConnectors(ctx) {
		if !l.scoped(r) {
			out = append(out, r)
		}
	}
	return out
}

// lookupConnectors returns the active connectors that can look contacts up,
// re-read every listTTL.
func (l *Lookup) lookupConnectors(ctx context.Context) []ConnectorRow {
	l.mu.Lock()
	if l.list != nil && l.now().Sub(l.listAt) < listTTL {
		out := l.list
		l.mu.Unlock()
		return out
	}
	l.mu.Unlock()
	rows, err := l.Repo.ActiveConnectors(ctx, nil)
	if err != nil {
		l.log().Warn("crm: listing connectors for lookup", "error", err)
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.list
	}
	out := []ConnectorRow{}
	for _, r := range rows {
		if c, ok := l.Tokens.Providers(r.Kind); ok && c.Capabilities().Lookup {
			out = append(out, r)
		}
	}
	l.mu.Lock()
	l.list, l.listAt = out, l.now()
	l.mu.Unlock()
	return out
}

// Invalidate drops the cached connector list (after a settings change).
func (l *Lookup) Invalidate() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.list = nil
	l.mu.Unlock()
}

// Find returns the contact for number, or nil. It waits at most budget (the
// lookup itself continues up to the CRM timeout in the background and fills
// the cache, so the next call is answered at once). A zero budget waits for
// the CRM timeout.
func (l *Lookup) Find(ctx context.Context, number string, budget time.Duration) *Contact {
	n, ok := NormalizeNumber(number)
	if !ok || l.Tokens == nil {
		return nil
	}
	key := cachePrefix + digitsOf(n)
	if l.Cache != nil {
		if raw, hit := l.Cache.Get(ctx, key); hit {
			var c *Contact
			if json.Unmarshal(raw, &c) == nil {
				return c
			}
		}
	}
	if len(l.connectors(ctx)) == 0 {
		return nil
	}
	if budget <= 0 {
		budget = l.timeout()
	}
	f := l.start(key, func(ctx context.Context) (*Contact, bool) { return l.query(ctx, n) })
	t := time.NewTimer(budget)
	defer t.Stop()
	select {
	case <-f.done:
		return f.c
	case <-t.C:
		return nil
	case <-ctx.Done():
		return nil
	}
}

// start runs (or joins) the query for key.
func (l *Lookup) start(key string, run func(ctx context.Context) (*Contact, bool)) *flight {
	l.mu.Lock()
	if f, ok := l.inflight[key]; ok {
		l.mu.Unlock()
		return f
	}
	f := &flight{done: make(chan struct{})}
	if l.inflight == nil {
		l.inflight = map[string]*flight{}
	}
	l.inflight[key] = f
	l.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), l.timeout())
		defer cancel()
		c, definitive := run(ctx)
		f.c = c
		if definitive && l.Cache != nil {
			raw, _ := json.Marshal(c)
			ttl := NegativeTTL
			if c != nil {
				ttl = PositiveTTL
			}
			l.Cache.Set(context.Background(), key, raw, ttl)
		}
		l.mu.Lock()
		delete(l.inflight, key)
		l.mu.Unlock()
		close(f.done)
	}()
	return f
}

func (l *Lookup) open(id int64) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	b := l.breaker[id]
	return b != nil && b.fails >= breakerFails && l.now().Before(b.until)
}

func (l *Lookup) record(id int64, ok bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.breaker == nil {
		l.breaker = map[int64]*breaker{}
	}
	b := l.breaker[id]
	if b == nil {
		b = &breaker{}
		l.breaker[id] = b
	}
	if ok {
		b.fails = 0
		return
	}
	b.fails++
	b.until = l.now().Add(breakerPause)
}

// query asks the connectors in order; definitive is false when a connector
// failed, so a miss is not cached as "no such contact".
func (l *Lookup) query(ctx context.Context, number string) (*Contact, bool) {
	definitive := true
	for _, row := range l.connectors(ctx) {
		if l.open(row.ID) {
			definitive = false
			continue
		}
		conn, cc, err := l.Tokens.Conn(ctx, row)
		if err != nil {
			l.record(row.ID, false)
			definitive = false
			continue
		}
		c, err := conn.Lookup(ctx, cc, number)
		if err != nil {
			l.log().Warn("crm: lookup failed", "connector", row.ID, "kind", row.Kind, "error", err)
			l.record(row.ID, false)
			definitive = false
			continue
		}
		l.record(row.ID, true)
		if c != nil {
			return c, true
		}
	}
	return nil, definitive
}

// userKey is the breaker key of one user's connection to a connector.
func userKey(connectorID, userID int64) int64 { return -(connectorID<<32 | userID&0xffffffff) }

// FindForUser looks number up in the contacts of one user: the Outlook and
// Google connectors the user signed in to. The budget works as in Find.
func (l *Lookup) FindForUser(ctx context.Context, userID int64, number string, budget time.Duration) *Contact {
	n, ok := NormalizeNumber(number)
	if !ok || l.Tokens == nil || userID == 0 {
		return nil
	}
	key := cachePrefix + "u" + strconv.FormatInt(userID, 10) + ":" + digitsOf(n)
	if l.Cache != nil {
		if raw, hit := l.Cache.Get(ctx, key); hit {
			var c *Contact
			if json.Unmarshal(raw, &c) == nil {
				return c
			}
		}
	}
	var scoped []ConnectorRow
	for _, r := range l.lookupConnectors(ctx) {
		if l.scoped(r) {
			scoped = append(scoped, r)
		}
	}
	if len(scoped) == 0 {
		return nil
	}
	if budget <= 0 {
		budget = l.timeout()
	}
	f := l.start(key, func(ctx context.Context) (*Contact, bool) { return l.queryUser(ctx, scoped, userID, n) })
	t := time.NewTimer(budget)
	defer t.Stop()
	select {
	case <-f.done:
		return f.c
	case <-t.C:
		return nil
	case <-ctx.Done():
		return nil
	}
}

// queryUser asks the connectors the user is signed in to.
func (l *Lookup) queryUser(ctx context.Context, rows []ConnectorRow, userID int64, number string) (*Contact, bool) {
	conns, err := l.Repo.UserConnectionsOf(ctx, userID)
	if err != nil {
		return nil, false
	}
	definitive := true
	for _, row := range rows {
		i := slices.IndexFunc(conns, func(c ConnectionRow) bool { return c.ConnectorID == row.ID && c.Status == StatusConnected })
		if i < 0 {
			continue
		}
		bk := userKey(row.ID, userID)
		if l.open(bk) {
			definitive = false
			continue
		}
		conn, cc, err := l.Tokens.ConnFor(ctx, row, userID)
		if err != nil {
			l.record(bk, false)
			definitive = false
			continue
		}
		c, err := conn.Lookup(ctx, cc, number)
		if err != nil {
			l.log().Warn("crm: contact lookup failed", "connector", row.ID, "kind", row.Kind, "error", err)
			l.record(bk, false)
			definitive = false
			continue
		}
		l.record(bk, true)
		if c != nil {
			return c, true
		}
	}
	return nil, definitive
}

// NameFor returns the caller-ID name for number from the contacts of the
// user who owns extension ext (the callee), within budget. The extension to
// user answer is cached for a minute so a call costs no query.
func (l *Lookup) NameFor(ctx context.Context, ext, number string, budget time.Duration) (string, bool) {
	if ext == "" || l.Tokens == nil {
		return "", false
	}
	uid := l.userOf(ctx, ext)
	c := l.FindForUser(ctx, uid, number, budget)
	if c == nil {
		return "", false
	}
	name := c.Name
	if name == "" {
		name = c.Company
	}
	if name = DisplayName(name); name == "" {
		return "", false
	}
	return name, true
}

func (l *Lookup) userOf(ctx context.Context, ext string) int64 {
	key := cachePrefix + "ext:" + ext
	if l.Cache != nil {
		if raw, hit := l.Cache.Get(ctx, key); hit {
			id, _ := strconv.ParseInt(string(raw), 10, 64)
			return id
		}
	}
	u, ok, err := l.Repo.UserForExtension(ctx, ext)
	if err != nil {
		return 0
	}
	if !ok {
		u.ID = 0
	}
	if l.Cache != nil {
		l.Cache.Set(ctx, key, []byte(strconv.FormatInt(u.ID, 10)), time.Minute)
	}
	return u.ID
}

// Name returns the caller-ID name for number within budget: the contact's
// name, cleaned for use as a SIP display name.
func (l *Lookup) Name(ctx context.Context, number string, budget time.Duration) (string, bool) {
	c := l.Find(ctx, number, budget)
	if c == nil {
		return "", false
	}
	name := c.Name
	if name == "" {
		name = c.Company
	}
	if name = DisplayName(name); name == "" {
		return "", false
	}
	return name, true
}

// DisplayName removes what would break a SIP display name (quotes,
// backslashes, control characters) and shortens it to 64 characters.
func DisplayName(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r == '"' || r == '\\' || r == '<' || r == '>' || unicode.IsControl(r) || !unicode.IsPrint(r) {
			continue
		}
		if n++; n > 64 {
			break
		}
		b.WriteRune(r)
	}
	return strings.TrimSpace(b.String())
}
