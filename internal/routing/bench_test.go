package routing

import (
	"fmt"
	"os"
	"slices"
	"sync"
	"testing"
	"time"
)

// latencyConfig has 100 outbound routes — a mix of prefix and regex
// matches, number and caller-ID transforms, schedules and source
// extensions — where the numbers under test match only the last ones, so
// each decision walks (nearly) every route.
func latencyConfig() Config {
	cfg := Config{Extensions: map[string]string{"101": "+97142220101"}}
	for id := int64(1); id <= 4; id++ {
		cfg.Trunks = append(cfg.Trunks, Trunk{ID: id, Name: fmt.Sprintf("trunk-%d", id), Mode: "ip", Enabled: true,
			DefaultCallerID: "+9714000000" + fmt.Sprint(id),
			Destinations: []Destination{
				{Host: fmt.Sprintf("10.0.%d.1", id), Port: 5060, Priority: 0, Weight: 2},
				{Host: fmt.Sprintf("10.0.%d.2", id), Port: 5060, Priority: 0, Weight: 1},
				{Host: fmt.Sprintf("10.0.%d.3", id), Port: 5060, Priority: 1, Weight: 1},
			}})
	}
	sched := &Schedule{TimeZone: "Asia/Dubai", Windows: []Window{{Days: []time.Weekday{0, 1, 2, 3, 4}, Start: "08:00", End: "18:00"}}}
	for i := range 98 {
		r := OutboundRoute{ID: int64(i + 1), Position: i, Name: fmt.Sprintf("r%02d", i), Enabled: true, Trunks: []int64{1, 2, 3}}
		if i%2 == 0 {
			r.MatchKind, r.Match = "prefix", fmt.Sprintf("8%02d", i)
		} else {
			r.MatchKind, r.Match = "regex", fmt.Sprintf(`^7%02d[0-9]{4,8}$`, i)
			r.Number = Transform{Regex: `^7([0-9]+)$`, Template: "+9717${1}"}
		}
		if i%5 == 0 {
			r.Schedule = sched
		}
		cfg.Outbound = append(cfg.Outbound, r)
	}
	cfg.Outbound = append(cfg.Outbound,
		OutboundRoute{ID: 99, Position: 98, Name: "restricted", MatchKind: "regex", Match: `^05[0-9]{8}$`, Enabled: true,
			SourceExtensions: []string{"999"}, Trunks: []int64{1}},
		OutboundRoute{ID: 100, Position: 99, Name: "UAE Mobile", MatchKind: "regex", Match: `^05[0-9]{8}$`, Enabled: true,
			Schedule: sched,
			Number:   Transform{Regex: `^0(5[0-9]{8})$`, Template: "+971${1}"},
			CallerID: Transform{Strip: 1, Prefix: "00"},
			Trunks:   []int64{1, 2, 3, 4}, FailoverCodes: []int{503}},
	)
	return cfg
}

func latencyCalls(t testing.TB, n int) []Call {
	mon := at(t, "2026-10-05 10:00")
	calls := make([]Call, n)
	for i := range calls {
		calls[i] = Call{FromExtension: "101", Number: fmt.Sprintf("05%08d", i*7919%100000000), At: mon}
	}
	return calls
}

func latencyUsable(id int64, _ bool) (bool, string) {
	if id == 2 {
		return false, "unhealthy"
	}
	return true, ""
}

// TestRoutingDecisionLatency measures Decide against the spec's "under 1ms
// p99 for 100 routes" constraint. It is a measurement, not a CI gate: it
// runs only with HELLO_BENCH=1.
func TestRoutingDecisionLatency(t *testing.T) {
	if os.Getenv("HELLO_BENCH") != "1" {
		t.Skip("HELLO_BENCH=1 not set")
	}
	cfg := latencyConfig()
	if len(cfg.Outbound) != 100 {
		t.Fatalf("%d routes, want 100", len(cfg.Outbound))
	}
	tbl := mustCompile(t, cfg)
	calls := latencyCalls(t, 20000)
	for _, c := range calls[:1000] { // warm up
		tbl.Decide(c, latencyUsable)
	}
	lat := make([]time.Duration, 0, len(calls))
	for _, c := range calls {
		start := time.Now()
		d := tbl.Decide(c, latencyUsable)
		lat = append(lat, time.Since(start))
		if d.Kind != KindOutbound || d.Route != "UAE Mobile" {
			t.Fatalf("decision %+v", outcomeOf(d))
		}
	}
	slices.Sort(lat)
	n := len(lat)
	p50, p99, worst := lat[n/2], lat[n*99/100], lat[n-1]
	t.Logf("Decide over 100 routes, %d calls: p50 %s, p99 %s, max %s", n, p50, p99, worst)
	if p99 > time.Millisecond {
		t.Errorf("p99 %s exceeds the 1ms constraint", p99)
	}
}

func BenchmarkDecide(b *testing.B) {
	tbl := mustCompile(b, latencyConfig())
	calls := latencyCalls(b, 1024)
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		tbl.Decide(calls[i%len(calls)], latencyUsable)
		i++
	}
}

func BenchmarkDecideParallel(b *testing.B) {
	tbl := mustCompile(b, latencyConfig())
	calls := latencyCalls(b, 1024)
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			tbl.Decide(calls[i%len(calls)], latencyUsable)
			i++
		}
	})
}

// TestDecideConcurrent runs decisions from many goroutines on one Table;
// with -race it catches any write to shared state during Decide.
func TestDecideConcurrent(t *testing.T) {
	tbl := mustCompile(t, latencyConfig())
	calls := latencyCalls(t, 200)
	want := make([]Decision, len(calls))
	for i, c := range calls {
		want[i] = tbl.Decide(c, latencyUsable)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for i, c := range calls {
				d := tbl.Decide(c, latencyUsable)
				if !equalOutcome(outcomeOf(d), outcomeOf(want[i])) || !slices.Equal(d.Trace, want[i].Trace) {
					t.Errorf("call %d: concurrent decision differs", i)
					return
				}
			}
		})
	}
	wg.Wait()
}
