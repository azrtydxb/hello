package api

import (
	"testing"

	"github.com/azrtydxb/hello/internal/turn"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestClientSeries fails if hello_turn_credentials_issued_total does not count
// each TURN credential handed out (the ICE servers route and an enrolment) and
// only those, or if hello_webrtc_candidate_type_total does not count a quality
// report under its local candidate type (unknown words as unknown).
func TestClientSeries(t *testing.T) {
	f, _, _, _ := newAcctFake(t)
	cfg := testClientAuth()
	cfg.TURNIssued = prometheus.NewCounter(prometheus.CounterOpts{Name: "issued"})
	cfg.CandidateTypes = prometheus.NewCounterVec(prometheus.CounterOpts{Name: "candidates"}, []string{"type"})
	h := Handler(Config{Store: f, SIPDomain: testDomain, Clients: f, ClientAuth: cfg})

	wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "x"}), 201, "enrol")
	wantJSON(t, acctCall(h, "alice", "GET", "/api/v1/me/ice-servers", nil), 200, "ice")
	if got := testutil.ToFloat64(cfg.TURNIssued); got != 2 {
		t.Fatalf("TURN credentials counted %v, want 2 (enrolment and ICE servers)", got)
	}
	stunOnly := cfg
	stunOnly.ICE = turn.Config{STUN: []string{"stun:turn1.test:3478"}}
	h2 := Handler(Config{Store: f, SIPDomain: testDomain, Clients: f, ClientAuth: stunOnly})
	wantJSON(t, acctCall(h2, "bob", "GET", "/api/v1/me/ice-servers", nil), 200, "stun only")
	if got := testutil.ToFloat64(cfg.TURNIssued); got != 2 {
		t.Fatalf("a STUN-only answer counted a TURN credential (%v)", got)
	}

	for _, local := range []string{"relay", "host", "weird"} {
		body := map[string]any{"deviceId": 1, "codec": "opus", "candidateLocal": local, "candidateRemote": "host", "durationMs": 1000,
			"lossPct": 0, "jitterMs": 1, "rttMs": 10}
		if rec := acctCall(h, "alice", "POST", "/api/v1/me/calls/c-"+local+"/quality", body); rec.Code != 204 {
			t.Fatalf("report = %d %s", rec.Code, rec.Body)
		}
	}
	for typ, want := range map[string]float64{"relay": 1, "host": 1, "unknown": 1, "srflx": 0} {
		if got := testutil.ToFloat64(cfg.CandidateTypes.WithLabelValues(typ)); got != want {
			t.Errorf("candidate type %s = %v, want %v", typ, got, want)
		}
	}
}
