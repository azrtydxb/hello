package prov

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// fakeStore is an in-memory Store with the contract's semantics: token
// matching through Tokens, an atomic ClaimBoot, injectable outages.
type fakeStore struct {
	mu        sync.Mutex
	now       time.Time
	grace     time.Duration
	public    *url.URL
	phones    map[int64]*fakePhone
	templates []Template
	firmware  map[string]Firmware
	fetches   []FetchRecord
	outage    error // returned by every read when set
	insertErr error
	nextID    int64
	// beforeMark, when set, runs as MarkFetched starts: the race window
	// between PhoneByToken and the write.
	beforeMark func()
}

type fakePhone struct {
	id                         int64
	mac, model                 string
	vendor                     Vendor
	enabled, bound             bool
	tokens                     Tokens
	plain                      string // test-only: the current token, to render the URL
	exposed, reclaimed, armed  bool
	uaMismatch                 bool
	fetched                    int
	lastFile, firmwareSeen     string
	sealedBroken               bool
	override                   *Template
	blf                        []BLFKey
	pinned                     *Firmware
	lastIP                     netip.Addr
	lastUA                     string
	firstFetch, lastFetch      time.Time
	handoffAt                  time.Time
	handoffIP                  netip.Addr
	admin, secret, user, label string
}

func newFakeStore() *fakeStore {
	u, _ := url.Parse("https://prov.hello.test")
	return &fakeStore{now: time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC), grace: 7 * 24 * time.Hour, public: u,
		phones: map[int64]*fakePhone{}, firmware: map[string]Firmware{}}
}

// addPhone adds an enabled, bound, armed phone and returns it with its
// token.
func (s *fakeStore) addPhone(v Vendor, model, mac string) (*fakePhone, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	plain, hash := NewToken()
	p := &fakePhone{id: s.nextID, mac: mac, model: model, vendor: v, enabled: true, bound: true, armed: true,
		tokens: Tokens{Hash: hash, Enc: []byte("sealed:" + string(hash[:4]))}, plain: plain,
		admin: "adm-" + mac, secret: "sec-" + mac, user: "1001-" + mac[6:], label: "1001",
		blf: []BLFKey{{Number: "1002", Label: "Bob", URI: "sip:1002@hello.test"}, {Number: "1003", Label: "Carol", URI: "sip:1003@hello.test"}}}
	s.phones[p.id] = p
	return p, plain
}

// rotate is the store side of rotate-token (and re-arm with arm).
func (s *fakeStore) rotate(id int64, immediate, arm bool) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	plain, hash := NewToken()
	p := s.phones[id]
	p.tokens = p.tokens.Rotate(hash, []byte("sealed-new"), s.now, s.grace, immediate)
	p.plain = plain
	if arm {
		p.armed, p.reclaimed = true, false
	}
	return plain
}

func (s *fakeStore) advance(d time.Duration) {
	s.mu.Lock()
	s.now = s.now.Add(d)
	s.mu.Unlock()
}

func (s *fakeStore) phone(id int64) fakePhone {
	s.mu.Lock()
	defer s.mu.Unlock()
	return *s.phones[id]
}

func (s *fakeStore) record(p *fakePhone) PhoneRecord {
	return PhoneRecord{ID: p.id, MAC: p.mac, Vendor: p.vendor, Model: p.model, Allowlisted: p.enabled && p.bound,
		HasPrevious: p.tokens.InGrace(s.now), BootArmed: p.armed}
}

func (s *fakeStore) PhoneByToken(_ context.Context, hash []byte) (PhoneRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outage != nil {
		return PhoneRecord{}, s.outage
	}
	for _, p := range s.phones {
		if via, ok := p.tokens.Match(hash, s.now); ok {
			r := s.record(p)
			r.ViaPrevious = via
			return r, nil
		}
	}
	return PhoneRecord{}, ErrNotFound
}

func (s *fakeStore) MarkFetched(_ context.Context, id int64, hash []byte, st FetchState) error {
	if s.beforeMark != nil {
		s.beforeMark() // a concurrent administrator change, outside the lock
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outage != nil {
		return s.outage
	}
	p := s.phones[id]
	if _, ok := p.tokens.Match(hash, s.now); !ok {
		return nil // the token was replaced since the request matched it
	}
	if p.firstFetch.IsZero() {
		p.firstFetch = st.At
	}
	p.lastFetch, p.lastIP, p.lastUA, p.lastFile = st.At, st.IP, st.UserAgent, st.File
	if st.FirmwareSeen != "" {
		p.firmwareSeen = st.FirmwareSeen
	}
	p.uaMismatch = p.uaMismatch || st.UAMismatch
	p.fetched++
	p.armed = false
	return nil
}

func (s *fakeStore) PromoteToken(_ context.Context, id int64, hash []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if via, ok := s.phones[id].tokens.Match(hash, s.now); !ok || via {
		return nil // no longer the current token
	}
	s.phones[id].tokens = s.phones[id].tokens.Promote()
	return nil
}

func (s *fakeStore) FlagTokenExposed(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phones[id].exposed = true
	return nil
}

func (s *fakeStore) PhoneByMAC(_ context.Context, mac string) (PhoneRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outage != nil {
		return PhoneRecord{}, s.outage
	}
	for _, p := range s.phones {
		if p.mac == mac {
			return s.record(p), nil
		}
	}
	return PhoneRecord{}, ErrNotFound
}

func (s *fakeStore) ClaimBoot(_ context.Context, mac string, ip netip.Addr) (PhoneRecord, *ProvInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outage != nil {
		return PhoneRecord{}, nil, s.outage
	}
	for _, p := range s.phones {
		if p.mac != mac {
			continue
		}
		rec := s.record(p)
		switch {
		case p.armed && rec.Allowlisted:
			if p.sealedBroken {
				return rec, nil, ErrSealed
			}
			p.armed, p.handoffAt, p.handoffIP = false, s.now, ip
			return rec, &ProvInfo{URL: DeviceURL(s.public, p.plain), CAURL: CAURL(s.public), ResyncSeconds: ResyncSeconds(p.mac, 24*time.Hour)}, nil
		case !p.armed && !p.handoffAt.IsZero() && s.now.Sub(p.handoffAt) <= BootHandoffGrace &&
			(!ip.IsValid() || !p.handoffIP.IsValid() || ip == p.handoffIP):
			return rec, &ProvInfo{URL: DeviceURL(s.public, p.plain), CAURL: CAURL(s.public), ResyncSeconds: ResyncSeconds(p.mac, 24*time.Hour)}, nil
		case !p.armed:
			p.reclaimed = true
		}
		return rec, nil, nil
	}
	return PhoneRecord{}, nil, ErrNotFound
}

func (s *fakeStore) RenderInputs(_ context.Context, id int64) (RenderData, Template, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outage != nil {
		return RenderData{}, Template{}, s.outage
	}
	p, ok := s.phones[id]
	if !ok {
		return RenderData{}, Template{}, ErrNotFound
	}
	if p.sealedBroken {
		return RenderData{}, Template{}, ErrSealed
	}
	d := RenderData{
		Phone:  Phone{MAC: p.mac, MACUpper: strings.ToUpper(p.mac), Vendor: p.vendor, Model: p.model, AdminPassword: p.admin},
		Line:   Line{Username: p.user, AuthName: p.user, Password: p.secret, DisplayName: "Alice Example", Label: p.label, Domain: "hello.test", VoicemailCode: "*97"},
		Server: Server{Host: "192.168.10.101", Port: 30508, Transport: "udp", Expiry: 3600},
		BLF:    p.blf,
		Prov:   ProvInfo{URL: DeviceURL(s.public, p.plain), CAURL: CAURL(s.public), ResyncSeconds: ResyncSeconds(p.mac, 24*time.Hour)},
		Time:   TimeInfo{Zone: "Europe/Brussels", NTP: "pool.ntp.org"},
	}
	if p.pinned != nil {
		d.Firmware = &FirmwareInfo{URL: FirmwareURL(s.public, p.plain, p.pinned.Filename), Version: p.pinned.Version}
	}
	t, ok := Resolve(d.Phone, p.override, append(append([]Template{}, s.templates...), Builtins()...))
	if !ok {
		return RenderData{}, Template{}, ErrNoTemplate
	}
	return d, t, nil
}

func (s *fakeStore) FirmwareByName(_ context.Context, v Vendor, name string) (Firmware, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.outage != nil {
		return Firmware{}, s.outage
	}
	f, ok := s.firmware[string(v)+"/"+name]
	if !ok {
		return Firmware{}, ErrNotFound
	}
	return f, nil
}

func (s *fakeStore) InsertFetches(_ context.Context, rows []FetchRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.insertErr != nil {
		return s.insertErr
	}
	s.fetches = append(s.fetches, rows...)
	return nil
}

// fakeOpener serves firmware bodies from memory.
type fakeOpener map[string][]byte

type nopSeekCloser struct{ *bytes.Reader }

func (nopSeekCloser) Close() error { return nil }

func (o fakeOpener) OpenFirmware(_ context.Context, key string) (io.ReadSeekCloser, error) {
	b, ok := o[key]
	if !ok {
		return nil, errors.New("minio: unavailable")
	}
	return nopSeekCloser{bytes.NewReader(b)}, nil
}

// env is a handler over a fake store, with its audit, metrics and logs.
type env struct {
	t     *testing.T
	s     *fakeStore
	h     http.Handler
	audit *Audit
	m     *Metrics
	logs  *bytes.Buffer
	l     *Limiter
	o     fakeOpener
}

var trustedProxy = netip.MustParsePrefix("10.42.0.0/16")

func newEnv(t *testing.T, mod ...func(*Options)) *env {
	t.Helper()
	return newEnvWith(t, nil, nil, mod...)
}

// newEnvWith builds an env on store s (a new one when nil) and a limiter
// made by mkLimiter (memory with generous limits when nil).
func newEnvWith(t *testing.T, s *fakeStore, mkLimiter func(*slog.Logger) *Limiter, mod ...func(*Options)) *env {
	t.Helper()
	if s == nil {
		s = newFakeStore()
	}
	e := &env{t: t, s: s, logs: &bytes.Buffer{}, o: fakeOpener{}}
	log := slog.New(slog.NewTextHandler(syncWriter{w: e.logs}, &slog.HandlerOptions{Level: slog.LevelDebug}))
	e.m = NewMetrics(prometheus.NewRegistry())
	e.audit = NewAudit(e.s, e.m, log)
	if mkLimiter == nil {
		mkLimiter = func(log *slog.Logger) *Limiter {
			return NewLimiter(nil, Limits{IPPerMin: 1000, DeniedPer10Min: 1000, PhonePerHour: 1000}, log)
		}
	}
	e.l = mkLimiter(log)
	opt := Options{PublicURL: e.s.public, TrustedProxies: []netip.Prefix{trustedProxy}, Resync: 24 * time.Hour,
		Audit: e.audit, Metrics: e.m, Log: log}
	for _, f := range mod {
		f(&opt)
	}
	e.h = NewHandler(e.s, e.l, e.o, opt)
	return e
}

type syncWriter struct{ w *bytes.Buffer }

var logMu sync.Mutex

func (s syncWriter) Write(p []byte) (int, error) {
	logMu.Lock()
	defer logMu.Unlock()
	return s.w.Write(p)
}

// req describes one request: through the trusted ingress over HTTPS from
// client 192.168.10.50 unless changed.
type req struct {
	method, path, ua, inm, proto, client, accept string
	body                                         string
	peer                                         string // TCP peer; default the trusted ingress
	tls                                          bool   // TLS on the listener itself
	headers                                      map[string]string
}

func (e *env) do(r req) *httptest.ResponseRecorder {
	e.t.Helper()
	if r.method == "" {
		r.method = http.MethodGet
	}
	if r.proto == "" {
		r.proto = "https"
	}
	if r.client == "" {
		r.client = "192.168.10.50"
	}
	hr := httptest.NewRequest(r.method, r.path, strings.NewReader(r.body))
	hr.RemoteAddr = "10.42.1.7:40000"
	if r.peer != "" {
		hr.RemoteAddr = r.peer
	}
	if r.tls {
		hr.TLS = &tls.ConnectionState{}
	}
	hr.Header.Set("X-Forwarded-Proto", r.proto)
	hr.Header.Set("X-Forwarded-For", r.client)
	if r.ua != "" {
		hr.Header.Set("User-Agent", r.ua)
	}
	if r.inm != "" {
		hr.Header.Set("If-None-Match", r.inm)
	}
	for k, v := range r.headers {
		hr.Header.Set(k, v)
	}
	if r.accept != "" {
		hr.Header.Set("Accept", r.accept)
	}
	w := httptest.NewRecorder()
	e.h.ServeHTTP(w, hr)
	return w
}

// flush writes every queued audit row and returns all rows written so far.
func (e *env) flush() []FetchRecord {
	var batch []FetchRecord
	for {
		select {
		case r := <-e.audit.queue:
			batch = append(batch, r)
			continue
		default:
		}
		break
	}
	e.audit.write(batch)
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	return append([]FetchRecord(nil), e.s.fetches...)
}

// last returns the newest audit row.
func (e *env) last() FetchRecord {
	e.t.Helper()
	rows := e.flush()
	if len(rows) == 0 {
		e.t.Fatal("no audit row")
	}
	return rows[len(rows)-1]
}

func devPath(token, file string) string { return "/p/" + token + "/" + file }
