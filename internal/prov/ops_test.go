package prov

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/vkconn"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/valkey-io/valkey-go"
)

// TestFetchAudit fails if a served or denied request leaves no row with
// the right result, if a row or a log line contains a token, or if a
// database failure blocks or fails the response (spec S-14).
func TestFetchAudit(t *testing.T) {
	e := newEnv(t)
	p, tok := e.s.addPhone(Yealink, "T54W", macA)
	stranger, _ := NewToken()
	cases := []struct {
		r    req
		want Result
	}{
		{req{path: devPath(tok, macA+".cfg")}, ResultServed},
		{req{path: devPath(stranger, macA+".cfg")}, ResultUnknownToken},
		{req{path: devPath(tok, macB+".cfg")}, ResultMACMismatch},
		{req{path: devPath(tok, macA+".cfg"), proto: "http"}, ResultPlainHTTP},
		{req{method: http.MethodPut, path: devPath(tok, macA+"-app.log"), body: "log"}, ResultUploadDiscarded},
		{req{path: "/p/boot/y000000000096.cfg", proto: "http"}, ResultBootServed},
		{req{path: "/p/boot/" + macA + ".cfg", proto: "http"}, ResultBootReclaim}, // disarmed by the first fetch
		{req{path: "/p/" + tok}, ResultNotFound},
	}
	for _, c := range cases {
		e.do(c.r)
	}
	rows := e.flush()
	if len(rows) != len(cases) {
		t.Fatalf("%d rows for %d requests", len(rows), len(cases))
	}
	for i, row := range rows {
		if row.Result != cases[i].want {
			t.Errorf("row %d (%s): %s, want %s", i, row.PathRedacted, row.Result, cases[i].want)
		}
		for _, secret := range []string{tok, stranger} {
			if strings.Contains(row.PathRedacted, secret) || strings.Contains(row.UserAgent, secret) {
				t.Errorf("row %d carries a token", i)
			}
		}
		if row.IP.String() != "192.168.10.50" {
			t.Errorf("row %d ip %v", i, row.IP)
		}
	}
	if rows[0].PhoneID != p.id || rows[0].Kind != KindDevice || rows[0].Bytes == 0 {
		t.Errorf("served row %+v", rows[0])
	}

	// A failing database drops rows and counts them; responses carry on.
	e.s.mu.Lock()
	e.s.insertErr = errors.New("db down")
	e.s.mu.Unlock()
	_, tok2 := e.s.addPhone(Yealink, "T54W", macB)
	start := time.Now()
	if w := e.do(req{path: devPath(tok2, macB+".cfg")}); w.Code != 200 {
		t.Fatalf("response failed with the audit database down: %d", w.Code)
	}
	if time.Since(start) > time.Second {
		t.Fatal("the response waited on the audit write")
	}
	e.flush()
	if got := metricValue(e.m.AuditDropped); got != 1 {
		t.Fatalf("dropped %v rows, want 1", got)
	}
	e.s.mu.Lock()
	e.s.insertErr = nil
	e.s.mu.Unlock()

	// A full queue drops without blocking.
	e.audit.queue = make(chan FetchRecord, 1)
	e.audit.Record(FetchRecord{PathRedacted: "/p/boot/a"})
	e.audit.Record(FetchRecord{PathRedacted: "/p/boot/b"})
	if got := metricValue(e.m.AuditDropped); got != 2 {
		t.Fatalf("dropped %v, want 2", got)
	}

	// Record redacts whatever the caller passed; Run flushes on stop.
	e.audit.queue = make(chan FetchRecord, 10)
	e.audit.every = 10 * time.Millisecond
	e.audit.Record(FetchRecord{PathRedacted: "/p/" + tok + "/x", UserAgent: strings.Repeat("é", 200)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.audit.Run(ctx); close(done) }()
	cancel()
	<-done
	last := e.last()
	if strings.Contains(last.PathRedacted, tok) || len(last.UserAgent) > 256 || !strings.HasSuffix(last.UserAgent, "é") {
		t.Fatalf("recorded %q / %d-byte UA", last.PathRedacted, len(last.UserAgent))
	}

	logs := e.logs.String()
	for _, secret := range []string{tok, tok2, stranger, p.secret, p.admin} {
		if strings.Contains(logs, secret) {
			t.Fatalf("a log line carries a secret: %s", logs)
		}
	}
}

// TestProvMetrics fails if served, denied, rate-limited and firmware
// requests, a render, a redirect operation or a dropped audit row do not
// move their metrics (spec S-17).
func TestProvMetrics(t *testing.T) {
	e := newEnvWith(t, nil, func(log *slog.Logger) *Limiter {
		return NewLimiter(nil, Limits{IPPerMin: 1000, DeniedPer10Min: 1000, PhonePerHour: 2}, log)
	})
	_, tok := e.s.addPhone(Yealink, "T54W", macA)
	fw := Firmware{Vendor: Yealink, Filename: "f.rom", ObjectKey: "k", SHA256: strings.Repeat("c", 64)}
	e.s.firmware["yealink/f.rom"] = fw
	e.o["k"] = []byte("firmware!")
	stranger, _ := NewToken()
	e.do(req{path: devPath(tok, macA+".cfg")})
	e.do(req{path: devPath(tok, macA+".cfg")})
	e.do(req{path: devPath(tok, macA+".cfg")}) // third in the hour: rate limited
	e.do(req{path: devPath(stranger, macA+".cfg")})
	e.do(req{path: devPath(tok, "fw/f.rom")})
	count := func(v, k string, r Result) float64 {
		return metricValue(e.m.Requests.WithLabelValues(v, k, string(r)))
	}
	for _, c := range []struct {
		v, k string
		r    Result
		want float64
	}{
		{"yealink", "device", ResultServed, 2},
		{"yealink", "device", ResultRateLimited, 1},
		{"unknown", "other", ResultUnknownToken, 1},
		{"yealink", "firmware", ResultServed, 1},
	} {
		if got := count(c.v, c.k, c.r); got != c.want {
			t.Errorf("requests{%s,%s,%s} = %v, want %v", c.v, c.k, c.r, got, c.want)
		}
	}
	if got := metricValue(e.m.FirmwareBytes.WithLabelValues("yealink")); got != 9 {
		t.Errorf("firmware bytes %v", got)
	}
	if got := metricValue(e.m.RenderSeconds); got != 2 {
		t.Errorf("render histogram not collected: %v", got)
	}
	e.m.RedirectOp(Snom, "register", "ok")
	if got := metricValue(e.m.RedirectOps.WithLabelValues("snom", "register", "ok")); got != 1 {
		t.Errorf("redirect ops %v", got)
	}
	e.m.SetPhones(1, 2, 3)
	if got := metricValue(e.m.Phones.WithLabelValues("stale")); got != 3 {
		t.Errorf("phones{stale} %v", got)
	}
	e.audit.queue = make(chan FetchRecord)
	e.do(req{path: "/p/boot/cfg.xml"})
	if got := metricValue(e.m.AuditDropped); got != 1 {
		t.Errorf("audit dropped %v", got)
	}
}

// testLimits are small enough to trip in a test.
var testLimits = Limits{IPPerMin: 5, DeniedPer10Min: 3, PhonePerHour: 2}

// TestProvRateLimit fails if the per-IP, denied-request or per-phone limits
// are not enforced (across two instances sharing Valkey, when Valkey is
// available), if a blocked IP is served within the block, or if the
// in-memory fallback is not applied when Valkey is down (spec S-15).
func TestProvRateLimit(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	t.Run("memory", func(t *testing.T) {
		// One replica: memory is per replica, so both handlers share it.
		l := NewLimiter(nil, testLimits, log)
		checkLimits(t, func(*slog.Logger) *Limiter { return l })
	})
	t.Run("valkey down", func(t *testing.T) {
		c, _ := vkconn.New(context.Background(), vkconn.Config{Addr: "127.0.0.1:1"}, log)
		if c == nil {
			t.Fatal("no client")
		}
		defer c.Close()
		l := NewLimiter(c, testLimits, log)
		start := time.Now()
		checkLimits(t, func(*slog.Logger) *Limiter { return l })
		if time.Since(start) > 10*time.Second {
			t.Fatal("the fallback waited on valkey")
		}
	})
	t.Run("valkey shared", func(t *testing.T) {
		addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
		if addr == "" {
			t.Skip("HELLO_TEST_VALKEY_ADDR not set")
		}
		c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true})
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		ctx := context.Background()
		keys, err := c.Do(ctx, c.B().Keys().Pattern("hello:prov:*").Build()).AsStrSlice()
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range keys {
			_ = c.Do(ctx, c.B().Del().Key(k).Build()).Error()
		}
		// Two handler instances (two replicas), each with its own memory,
		// alternate; only Valkey can make them agree.
		a, b := NewLimiter(c, testLimits, log), NewLimiter(c, testLimits, log)
		n := 0
		checkLimits(t, func(*slog.Logger) *Limiter {
			n++
			if n%2 == 1 {
				return a
			}
			return b
		})
		ttl, err := c.Do(ctx, c.B().Pttl().Key(keyBlock+"192.168.10.70").Build()).AsInt64()
		if err != nil || ttl <= 0 || ttl > BlockFor.Milliseconds() {
			t.Fatalf("block key ttl %d (%v)", ttl, err)
		}
	})
}

// checkLimits drives two handler instances over one store, alternating
// requests between them, and checks every limit.
func checkLimits(t *testing.T, mk func(*slog.Logger) *Limiter) {
	t.Helper()
	s := newFakeStore()
	envs := []*env{newEnvWith(t, s, mk), newEnvWith(t, s, mk)}
	i := 0
	do := func(r req) int {
		e := envs[i%2]
		i++
		return e.do(r).Code
	}
	_, tok := s.addPhone(Yealink, "T54W", macA)
	stranger, _ := NewToken()

	// Per IP: five a minute.
	for n := range 5 {
		if c := do(req{path: "/p/boot/cfg.xml", client: "192.168.10.61"}); c != 200 {
			t.Fatalf("request %d from a fresh IP: %d", n+1, c)
		}
	}
	if c := do(req{path: "/p/boot/cfg.xml", client: "192.168.10.61"}); c != http.StatusTooManyRequests {
		t.Fatalf("sixth request in a minute: %d", c)
	}
	if c := do(req{path: "/p/boot/cfg.xml", client: "192.168.10.62"}); c != 200 {
		t.Fatalf("another IP was limited: %d", c)
	}

	// Denied: three allowed, the fourth blocks the IP for every path.
	for range 4 {
		if c := do(req{path: devPath(stranger, macA+".cfg"), client: "192.168.10.70"}); c != 404 {
			t.Fatalf("denial answered %d", c)
		}
	}
	if c := do(req{path: devPath(tok, macA+".cfg"), client: "192.168.10.70"}); c != http.StatusTooManyRequests {
		t.Fatalf("a blocked IP was served: %d", c)
	}
	if c := do(req{path: "/p/ca.crt", client: "192.168.10.70"}); c != http.StatusTooManyRequests {
		t.Fatalf("a blocked IP reached another path: %d", c)
	}

	// Per phone: two config files an hour, from any IP.
	for n := range 2 {
		if c := do(req{path: devPath(tok, macA+".cfg"), client: "192.168.10.8" + string(rune('0'+n))}); c != 200 {
			t.Fatalf("config %d: %d", n+1, c)
		}
	}
	if c := do(req{path: devPath(tok, macA+".cfg"), client: "192.168.10.89"}); c != http.StatusTooManyRequests {
		t.Fatalf("third config in an hour: %d", c)
	}
	var limited int
	for _, e := range envs {
		for _, row := range e.flush() {
			if row.Result == ResultRateLimited {
				limited++
			}
		}
	}
	if limited == 0 {
		t.Fatal("rate-limited requests were not audited")
	}
}

// TestMemLimiterWindow fails if the in-memory window does not slide or
// a block does not end.
func TestMemLimiterWindow(t *testing.T) {
	m := newMemLimiter()
	now := time.Unix(1000, 0)
	for range 2 {
		if !m.window("k", 2, time.Minute, now) {
			t.Fatal("refused under the limit")
		}
	}
	if m.window("k", 2, time.Minute, now.Add(59*time.Second)) {
		t.Fatal("allowed over the limit")
	}
	if !m.window("k", 2, time.Minute, now.Add(61*time.Second)) {
		t.Fatal("window did not slide")
	}
	m.denied("ip", 1, now)
	m.denied("ip", 1, now)
	if !m.isBlocked("ip", now.Add(BlockFor-time.Second)) || m.isBlocked("ip", now.Add(BlockFor)) {
		t.Fatal("block window wrong")
	}
}

// metricValue reads one counter, gauge or histogram (its sample count).
func metricValue(c prometheus.Collector) float64 {
	ch := make(chan prometheus.Metric, 1)
	c.Collect(ch)
	var d dto.Metric
	if err := (<-ch).Write(&d); err != nil {
		panic(err)
	}
	switch {
	case d.Counter != nil:
		return d.Counter.GetValue()
	case d.Gauge != nil:
		return d.Gauge.GetValue()
	case d.Histogram != nil:
		return float64(d.Histogram.GetSampleCount())
	}
	return -1
}
