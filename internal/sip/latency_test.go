package sip

import (
	"os"
	"slices"
	"testing"
	"time"
)

// TestRegisterLatency measures authenticated REGISTER handling (challenge
// plus authenticated request, Valkey-backed state) against the spec's
// "under 20ms p99" constraint. It is a measurement, not a CI gate: it runs
// only with HELLO_BENCH=1 and HELLO_TEST_VALKEY_ADDR set.
func TestRegisterLatency(t *testing.T) {
	if os.Getenv("HELLO_BENCH") != "1" {
		t.Skip("HELLO_BENCH=1 not set")
	}
	st, th, _ := valkeyState(t)
	pbx := startPBX(t, ringAllDevices(), func(_ *Config, d *Deps) { d.State, d.Throttle = st, th })
	p := newPhone(t, pbx, "a1", "pa")
	p.register(t) // warm up
	const n = 1000
	lat := make([]time.Duration, 0, n)
	for range n {
		start := time.Now()
		if res := p.authDo(p.registerReq(300), AlgSHA256, p.pass); res.StatusCode != 200 {
			t.Fatalf("register = %d", res.StatusCode)
		}
		lat = append(lat, time.Since(start))
	}
	slices.Sort(lat)
	p50, p99 := lat[n/2], lat[n*99/100]
	t.Logf("REGISTER with digest, %d runs: p50 %s, p99 %s (two round trips each)", n, p50, p99)
	if p99 > 20*time.Millisecond {
		t.Errorf("p99 %s exceeds the 20ms constraint", p99)
	}
}
