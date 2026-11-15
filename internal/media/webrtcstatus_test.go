package media

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func legOf(label string) (WebRTCLeg, bool) {
	for _, l := range WebRTCLegs(0) {
		if l.Call == label {
			return l, true
		}
	}
	return WebRTCLeg{}, false
}

// TestWebRTCLegDiagnostics fails if a WebRTC leg's ICE and DTLS state, its
// selected address and its SRTP failures are not what the Diagnostics view
// and the S-52 series say: the leg is listed while it lives and gone after its
// call ends, the ICE check moves hello_webrtc_ice_state_total{state=connected},
// the handshake is timed in hello_webrtc_dtls_handshake_seconds, and a packet
// that fails SRTP authentication counts in hello_srtp_auth_failures_total under
// the side it came from.
func TestWebRTCLegDiagnostics(t *testing.T) {
	reg := prometheus.NewRegistry()
	registerWebRTCMetrics(reg)
	registerSRTPAuthMetrics(reg)
	before := WebRTCStats()
	hist := func() uint64 {
		mfs, err := reg.Gather()
		if err != nil {
			t.Fatal(err)
		}
		for _, mf := range mfs {
			if mf.GetName() == "hello_webrtc_dtls_handshake_seconds" {
				return mf.GetMetric()[0].GetHistogram().GetSampleCount()
			}
		}
		return 0
	}
	h0 := hist()

	c := newWebRTCCall(t, nil, []string{"pcmu"}, []string{"PCMU"}, []string{"g711"}, 0)
	c.r.SetLabel("diag-call-1")
	l, ok := legOf("diag-call-1")
	if !ok {
		t.Fatal("the WebRTC leg is not listed")
	}
	if l.Side != "callee" || l.ICE != "checking" || l.DTLS != "connecting" || l.Remote != "" || l.Ringing {
		t.Fatalf("before the check: %+v", l)
	}
	if got := testutil.ToFloat64(gaugeOf(t, reg, "hello_webrtc_legs")); got < 1 {
		t.Fatalf("hello_webrtc_legs = %v with a live leg", got)
	}
	c.check(t)
	if err := c.browser.handshake(false, c.fp); err != nil {
		t.Fatalf("browser handshake: %v", err)
	}
	waitFor(t, "the leg to be keyed", func() bool {
		l, _ = legOf("diag-call-1")
		return l.DTLS == "connected"
	})
	if l.ICE != "connected" || !strings.HasPrefix(l.Remote, "127.0.0.1:") {
		t.Fatalf("after the handshake: %+v", l)
	}
	after := WebRTCStats()
	if after.ICE["connected"] <= before.ICE["connected"] || after.DTLSHandshakes <= before.DTLSHandshakes {
		t.Fatalf("counters did not move: before %+v after %+v", before, after)
	}
	if hist() <= h0 {
		t.Fatal("the handshake was not timed in hello_webrtc_dtls_handshake_seconds")
	}

	// A packet in the clear fails SRTP authentication on the browser's leg.
	authBefore := after.SRTPAuthFailures["from_callee"]
	plain := BuildRTP(0, 900, 900, 4242, false, bytes.Repeat([]byte{0xEE}, 20))
	_, _ = c.browser.conn.WriteToUDP(plain, loopback(c.node.mux.Port()))
	waitFor(t, "the SRTP failure to count", func() bool {
		return WebRTCStats().SRTPAuthFailures["from_callee"] > authBefore
	})
	if l, _ := legOf("diag-call-1"); l.SRTPAuthFailures == 0 {
		t.Fatalf("the leg shows no SRTP failure: %+v", l)
	}
	if got := testutil.ToFloat64(counterOf(t, reg, "hello_srtp_auth_failures_total", "direction", "from_callee")); got == 0 {
		t.Fatal("hello_srtp_auth_failures_total{direction=from_callee} did not move")
	}

	c.r.Close()
	if _, ok := legOf("diag-call-1"); ok {
		t.Fatal("the leg is still listed after its call ended")
	}
	if WebRTCStats().ICE["failed"] != after.ICE["failed"] {
		t.Fatal("a leg that connected counted as an ICE failure")
	}
}

// TestWebRTCDTLSFailureDiagnostics fails if a handshake refused for a
// fingerprint mismatch is not counted under that reason, kept in the recent
// failures with its call and side, and shown on the leg.
func TestWebRTCDTLSFailureDiagnostics(t *testing.T) {
	before := WebRTCStats()
	other, err := newCert()
	if err != nil {
		t.Fatal(err)
	}
	claim := other.fp
	c := newWebRTCCall(t, &claim, []string{"pcmu"}, []string{"PCMU"}, []string{"g711"}, 0)
	c.r.SetLabel("diag-call-2")
	c.check(t)
	go func() { _ = c.browser.handshake(false, c.fp) }()
	select {
	case <-c.failed:
	case <-time.After(8 * time.Second):
		t.Fatal("the mismatched handshake did not fail")
	}
	if got := WebRTCStats().DTLSFailures["fingerprint"]; got <= before.DTLSFailures["fingerprint"] {
		t.Fatal("hello_webrtc_dtls_failures_total{reason=fingerprint} did not move")
	}
	l, ok := legOf("diag-call-2")
	if !ok || l.DTLS != "failed" || l.DTLSReason != "fingerprint" {
		t.Fatalf("leg after the failure: %+v (listed %v)", l, ok)
	}
	found := false
	for _, f := range WebRTCRecentFailures() {
		if f.Call == "diag-call-2" && f.Kind == "dtls" && f.Reason == "fingerprint" && f.Side == "callee" && f.Detail != "" {
			found = true
		}
	}
	if !found {
		t.Fatalf("the failure is not in the recent list: %+v", WebRTCRecentFailures())
	}
}

// TestWebRTCICEFailure fails if a leg whose DTLS started but that never saw an
// ICE check is not counted as an ICE failure when its call ends, or if an
// alternate leg dropped because another fork answered is.
func TestWebRTCICEFailure(t *testing.T) {
	before := WebRTCStats()
	mux := newTestMux(t)
	r, err := NewRelay(testRelayMin+390, testRelayMin+399)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.AddMuxLeg("a", mux); err != nil {
		t.Fatal(err)
	}
	r.SetLabel("diag-call-3")
	l := r.findLeg("a")
	// The DTLS session began long enough ago that a check was due.
	l.wt.event(watchDTLS, "started", 0)
	l.wt.dtlsAt.Store(time.Now().Add(-time.Minute).UnixNano())
	r.Close()
	if got := WebRTCStats().ICE["failed"]; got != before.ICE["failed"]+1 {
		t.Fatalf("ICE failures %d, want %d", got, before.ICE["failed"]+1)
	}

	r2, err := NewRelay(testRelayMin+390, testRelayMin+399)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r2.AddMuxLeg("a", mux); err != nil {
		t.Fatal(err)
	}
	r2.SetLabel("diag-call-4")
	r2.findLeg("a").parked.Store(true) // stands for an alternate leg that never answered
	r2.findLeg("a").wt.event(watchDTLS, "started", 0)
	r2.findLeg("a").wt.dtlsAt.Store(time.Now().Add(-time.Minute).UnixNano())
	r2.Close()
	if got := WebRTCStats().ICE["failed"]; got != before.ICE["failed"]+1 {
		t.Fatalf("a fork that never answered counted as an ICE failure (%d)", got)
	}
}

func gaugeOf(t *testing.T, reg *prometheus.Registry, name string) prometheus.Collector {
	t.Helper()
	return collectorOf(t, reg, name, "", "")
}

func counterOf(t *testing.T, reg *prometheus.Registry, name, label, value string) prometheus.Collector {
	t.Helper()
	return collectorOf(t, reg, name, label, value)
}

// collectorOf wraps one gathered series so testutil.ToFloat64 reads it.
func collectorOf(t *testing.T, reg *prometheus.Registry, name, label, value string) prometheus.Collector {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			match := label == ""
			for _, lp := range m.GetLabel() {
				if lp.GetName() == label && lp.GetValue() == value {
					match = true
				}
			}
			if !match {
				continue
			}
			v := m.GetGauge().GetValue() + m.GetCounter().GetValue()
			return prometheus.NewGaugeFunc(prometheus.GaugeOpts{Name: "x"}, func() float64 { return v })
		}
	}
	t.Fatalf("no series %s{%s=%q}", name, label, value)
	return nil
}
