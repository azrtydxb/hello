package sip

import (
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestClientMetrics fails if the client series of S-52 do not move:
// hello_client_registrations by device kind after web and desk devices
// register, hello_client_auth_failures_total for an expired credential and
// for a revoked device's name, and, during a video call between two browsers,
// the video slot (hello_video_sessions) and the WebRTC legs with their ICE
// and DTLS state, which go away when the call ends. Push metrics are not
// built (clients items 7 to 9, not now).
func TestClientMetrics(t *testing.T) {
	devs := append(webDevices(), webDev(12, "100"))
	pbx := startWebPBX(t, devs, true, withClientKeys(derivedKey, ""))
	authFail := func(reason string) float64 {
		return testutil.ToFloat64(pbx.m.ClientAuthFailures.WithLabelValues(reason))
	}
	regs := func(kind string) float64 { return testutil.ToFloat64(pbx.m.ClientRegistrations.WithLabelValues(kind)) }

	expired0, unknown0 := authFail("expired"), authFail("unknown_device")
	if got := registerStatus(derivedPhone(t, pbx, "w12", time.Now().Add(-time.Minute), derivedKey), AlgSHA256); got != 401 {
		t.Fatalf("expired credential = %d, want 401", got)
	}
	if authFail("expired") != expired0+1 {
		t.Fatal("hello_client_auth_failures_total{reason=expired} did not move")
	}
	if got := registerStatus(derivedPhone(t, pbx, "w99", time.Now().Add(time.Hour), derivedKey), AlgSHA256); got == 200 {
		t.Fatal("a device that is not in the snapshot registered")
	}
	if authFail("unknown_device") != unknown0+1 {
		t.Fatal("hello_client_auth_failures_total{reason=unknown_device} did not move")
	}
	if got := registerStatus(derivedPhone(t, pbx, "w12", time.Now().Add(time.Hour), derivedKey), AlgSHA256); got != 200 {
		t.Fatalf("valid credential = %d, want 200", got)
	}

	before := media.WebRTCStats()
	_, _, r, _, _, _ := webVideoPair(t, pbx)
	pbx.srv.recountRegistrations(t.Context())
	// a1 is a desk device (it registered over WSS, but its kind is desk); w1
	// and w12 are web devices.
	if regs("web") != 2 || regs("desk") != 1 || regs("mobile") != 0 {
		t.Fatalf("hello_client_registrations web %v desk %v mobile %v, want 2, 1, 0", regs("web"), regs("desk"), regs("mobile"))
	}
	if media.VideoSessions() == 0 {
		t.Fatal("hello_video_sessions did not move during a video call")
	}
	callID := r.dcs.InviteRequest.CallID().Value()
	eventually(t, "both WebRTC legs keyed", func() bool {
		up := 0
		for _, l := range media.WebRTCLegs(0) {
			if l.Call == callID && l.ICE == "connected" && l.DTLS == "connected" && l.Video && l.Remote != "" {
				up++
			}
		}
		return up == 2
	})
	if after := media.WebRTCStats(); after.ICE["connected"] < before.ICE["connected"]+2 || after.DTLSHandshakes < before.DTLSHandshakes+2 {
		t.Fatalf("WebRTC counters did not move: %+v -> %+v", before, after)
	}
	hangup(t, r.dcs)
	eventually(t, "the legs and the video slot released", func() bool {
		for _, l := range media.WebRTCLegs(0) {
			if l.Call == callID {
				return false
			}
		}
		return media.VideoSessions() == 0
	})
}
