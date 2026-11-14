package api

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/internal/turn"
)

// statusStore is the account fake with client devices of every kind and the
// candidate summary.
type statusStore struct {
	*acctFake
	devices []store.Device
	stats   store.ClientCandidateStats
}

func (s *statusStore) ListDevices(_ context.Context, kind string) ([]store.Device, error) {
	var out []store.Device
	for _, d := range s.devices {
		if kind == "" || d.Kind == kind {
			out = append(out, d)
		}
	}
	return out, nil
}

func (s *statusStore) ClientCandidateStats(_ context.Context, since time.Time) (store.ClientCandidateStats, error) {
	st := s.stats
	st.Since = since
	return st, nil
}

type statusLive struct {
	bindings []livestate.Binding
	calls    []livestate.Call
}

func (l statusLive) AllBindings(context.Context) ([]livestate.Binding, error) { return l.bindings, nil }
func (l statusLive) Calls(context.Context) ([]livestate.Call, error)          { return l.calls, nil }
func (l statusLive) DeviceStates(context.Context) ([]livestate.DeviceState, error) {
	return nil, nil
}

type statusWebRTC struct {
	sts  []livestate.WebRTCNodeStatus
	down bool
}

func (w statusWebRTC) WebRTCStatuses(context.Context) ([]livestate.WebRTCNodeStatus, error) {
	if w.down {
		return nil, errFakeDown
	}
	return w.sts, nil
}

// TestWebRTCStatus fails if GET /api/v1/webrtc/status does not combine its
// sources as the Diagnostics "Clients" tab needs: client devices enrolled and
// registered by kind and transport, every node's WebRTC legs with their call's
// parties and whether TURN relays them, each TURN server's check (reused, not
// repeated on every poll), the reported candidate types, the refused REGISTERs
// of client devices (not the first challenge), and the merged last errors; if
// a viewer cannot read it or a user can; if a source that is down hides the
// rest; or if a secret reaches the answer.
func TestWebRTCStatus(t *testing.T) {
	f, _, _, _ := newAcctFake(t)
	now := time.Now().UTC()
	ss := &statusStore{acctFake: f,
		devices: []store.Device{
			{ID: 31, SIPUsername: "w31", Kind: "web", Label: "Chrome", Enabled: true},
			{ID: 32, SIPUsername: "w32", Kind: "web", Label: "Old laptop", Enabled: false},
			{ID: 33, SIPUsername: "w33", Kind: "web", Label: "Edge", Enabled: true},
		},
		stats: store.ClientCandidateStats{Reports: 4, Local: map[string]int{"host": 3, "relay": 1}, Remote: map[string]int{"host": 4}, Relayed: 1},
	}
	live := statusLive{
		bindings: []livestate.Binding{
			{Device: "w31", Extension: "101", Transport: "wss", Source: "10.0.0.5:51000", UserAgent: "Hello softphone", ReceivedNode: "sip-1", Expires: now.Add(time.Hour)},
			{Device: "101-desk", Extension: "101", Transport: "udp", ReceivedNode: "sip-1", Expires: now.Add(time.Hour)},
		},
		calls: []livestate.Call{{SIPCallID: "call-1@x", From: "101", To: "102"}},
	}
	web := statusWebRTC{sts: []livestate.WebRTCNodeStatus{{
		Node: "sip-1", Port: 21100, Hosts: []string{"192.0.2.10"}, UpdatedAt: now,
		Legs: []livestate.WebRTCLeg{
			{Call: "call-1@x", Side: "caller", ICE: "connected", DTLS: "connected", Remote: "198.51.100.7:49200", Since: now},
			{Call: "call-2@x", Side: "callee", ICE: "checking", DTLS: "connecting", Since: now},
		},
		Failures: []livestate.WebRTCFailure{{At: now.Add(-time.Minute), Call: "call-9@x", Side: "callee", Kind: "dtls", Reason: "timeout", Detail: "the DTLS handshake did not finish in time"}},
		Counters: livestate.WebRTCCounters{ICE: map[string]uint64{"connected": 5}, DTLSFailures: map[string]uint64{"timeout": 1}},
	}}}
	diag := newFakeDiag()
	diag.attempts["w33"] = []livestate.RegisterAttempt{
		{At: now.Add(-10 * time.Second), Device: "w33", Code: 403, Reason: "Forbidden", Source: "10.0.0.6:5000", Node: "sip-2", Credentials: true},
		{At: now.Add(-20 * time.Second), Device: "w33", Code: 401, Reason: "Unauthorized", Credentials: true},
		{At: now.Add(-30 * time.Second), Device: "w33", Code: 401, Reason: "Unauthorized"},
		{At: now.Add(-40 * time.Second), Device: "w33", Code: 200, Reason: "OK", Credentials: true},
	}
	diag.attempts["w32"] = []livestate.RegisterAttempt{{At: now, Device: "w32", Code: 403, Credentials: true}} // disabled: not read
	var probes atomic.Int32
	prober := &turnProber{probe: func(_ context.Context, uri string, _ time.Duration) turn.Probe {
		probes.Add(1)
		return turn.Probe{URL: uri, Transport: "udp", Method: "stun", Reachable: true, Addrs: []string{"198.51.100.7"}}
	}}
	cfg := Config{Store: ss, Clients: ss, ClientAuth: testClientAuth(), SIPDomain: testDomain, Live: live, WebRTC: web, Diagnostics: diag, turnProbe: prober}
	h := Handler(cfg)

	if rec := acctCall(h, "alice", "GET", "/api/v1/webrtc/status", nil); rec.Code != 403 {
		t.Fatalf("a user = %d, want 403", rec.Code)
	}
	rec := acctCall(h, "dave", "GET", "/api/v1/webrtc/status", nil)
	got := wantJSON(t, rec, 200, "status")
	for _, secret := range []string{testTurnSecret, testClientKey} {
		if strings.Contains(rec.Body.String(), secret) {
			t.Fatal("a secret reached the status")
		}
	}
	clients := got["clients"].(map[string]any)
	if e := clients["enrolled"].(map[string]any); e["web"] != float64(2) || e["mobile"] != float64(0) {
		t.Errorf("enrolled = %v, want web 2 (the disabled one not counted)", e)
	}
	if r := clients["registered"].(map[string]any); r["web"] != float64(1) || r["desk"] != float64(1) {
		t.Errorf("registered = %v", r)
	}
	if tr := clients["byTransport"].(map[string]any); tr["wss"] != float64(1) || tr["udp"] != float64(0) {
		t.Errorf("client transports = %v (a desk phone is not a client)", tr)
	}
	regs := got["registrations"].([]any)
	if len(regs) != 1 || regs[0].(map[string]any)["label"] != "Chrome" || regs[0].(map[string]any)["transport"] != "wss" {
		t.Errorf("registrations = %v", regs)
	}
	legs := got["legs"].([]any)
	if len(legs) != 2 {
		t.Fatalf("legs = %v", legs)
	}
	l0 := legs[0].(map[string]any)
	if l0["turn"] != true || l0["from"] != "101" || l0["to"] != "102" || l0["node"] != "sip-1" || legs[1].(map[string]any)["turn"] != false {
		t.Errorf("legs = %v", legs)
	}
	tu := got["turn"].(map[string]any)
	if tu["configured"] != true || tu["ttlSeconds"] != float64(3600) || tu["relayedLegs"] != float64(1) || len(tu["servers"].([]any)) != 2 {
		t.Errorf("turn = %v", tu)
	}
	if got["push"].(map[string]any)["configured"] != false {
		t.Errorf("push = %v", got["push"])
	}
	if c := got["candidates"].(map[string]any); c["reports"] != float64(4) || c["relayed"] != float64(1) {
		t.Errorf("candidates = %v", c)
	}
	af := got["authFailures"].([]any)
	if len(af) != 2 || af[0].(map[string]any)["code"] != float64(403) || af[1].(map[string]any)["code"] != float64(401) || af[0].(map[string]any)["label"] != "Edge" {
		t.Errorf("auth failures = %v, want the 403 then the 401 with credentials", af)
	}
	le := got["lastErrors"].([]any)
	if len(le) != 3 || le[0].(map[string]any)["kind"] != "register" || le[2].(map[string]any)["kind"] != "dtls" || le[2].(map[string]any)["node"] != "sip-1" {
		t.Errorf("last errors = %v", le)
	}
	if n := got["nodes"].([]any); len(n) != 1 || n[0].(map[string]any)["legs"] != float64(2) {
		t.Errorf("nodes = %v", n)
	}
	if u := got["unavailable"].([]any); len(u) != 0 {
		t.Errorf("unavailable = %v", u)
	}

	// The TURN check is reused by the next poll.
	acctCall(h, "dave", "GET", "/api/v1/webrtc/status", nil)
	if probes.Load() != 2 {
		t.Errorf("TURN probes = %d, want 2 (one per server, reused)", probes.Load())
	}

	// A source that is down is named; the rest still answers.
	cfg.WebRTC = statusWebRTC{down: true}
	h2 := Handler(cfg)
	got = wantJSON(t, acctCall(h2, "dave", "GET", "/api/v1/webrtc/status", nil), 200, "webrtc down")
	if u := got["unavailable"].([]any); len(u) != 1 || u[0] != "webrtc" || len(got["registrations"].([]any)) != 1 || len(got["legs"].([]any)) != 0 {
		t.Errorf("with the WebRTC status down: unavailable %v registrations %v legs %v", u, got["registrations"], got["legs"])
	}
}

// TestRegisterFailed fails if the normal digest exchange (a first 401
// without credentials, a stale nonce) counts as a client failure.
func TestRegisterFailed(t *testing.T) {
	for _, c := range []struct {
		a    livestate.RegisterAttempt
		want bool
	}{
		{livestate.RegisterAttempt{Code: 200}, false},
		{livestate.RegisterAttempt{Code: 401}, false},
		{livestate.RegisterAttempt{Code: 401, Credentials: true, Stale: true}, false},
		{livestate.RegisterAttempt{Code: 401, Credentials: true}, true},
		{livestate.RegisterAttempt{Code: 403}, true},
		{livestate.RegisterAttempt{Code: 503}, true},
	} {
		if got := registerFailed(c.a); got != c.want {
			t.Errorf("%+v: %v, want %v", c.a, got, c.want)
		}
	}
}
