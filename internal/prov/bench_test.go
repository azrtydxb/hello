package prov

import (
	"log/slog"
	"net/http"
	"os"
	"slices"
	"testing"
	"time"
)

// TestProvLatency (HELLO_BENCH=1) fails if a cached-ETag re-check takes 5
// ms or more at p99, or a full render 20 ms or more (spec Constraints),
// measured through the handler on the Yealink built-in with twelve BLF
// keys.
func TestProvLatency(t *testing.T) {
	if os.Getenv("HELLO_BENCH") != "1" {
		t.Skip("HELLO_BENCH=1 not set")
	}
	// Limits off: the measurement is the handler, not the limiter.
	e := newEnvWith(t, nil, func(log *slog.Logger) *Limiter { return NewLimiter(nil, Limits{}, log) })
	p, tok := e.s.addPhone(Yealink, "T54W", macA)
	e.s.phones[p.id].blf = SampleData(true).BLF
	path := devPath(tok, macA+".cfg")
	etag := e.do(req{path: path}).Header().Get("ETag")
	p99 := func(inm string, wantCode int) time.Duration {
		d := make([]time.Duration, 2000)
		for i := range d {
			if i%200 == 0 {
				e.flush()
			}
			start := time.Now()
			if c := e.do(req{path: path, inm: inm}).Code; c != wantCode {
				t.Fatalf("status %d", c)
			}
			d[i] = time.Since(start)
		}
		slices.Sort(d)
		return d[len(d)*99/100]
	}
	recheck, full := p99(etag, http.StatusNotModified), p99("", http.StatusOK)
	t.Logf("p99: ETag re-check %v, full render %v", recheck, full)
	if recheck >= 5*time.Millisecond || full >= 20*time.Millisecond {
		t.Fatalf("p99 re-check %v (limit 5ms), render %v (limit 20ms)", recheck, full)
	}
}

func BenchmarkRender(b *testing.B) {
	tmpl := Builtins()[0]
	d := SampleData(true)
	d.Phone.Vendor = Yealink
	for b.Loop() {
		if _, err := Render(b.Context(), tmpl, d.Phone.MAC+".cfg", d); err != nil {
			b.Fatal(err)
		}
	}
}
