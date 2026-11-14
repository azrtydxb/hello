package livestate

import (
	"context"
	"strconv"
	"testing"
	"time"
)

// TestWebRTCStatusPublish fails if a node's WebRTC status does not round-trip,
// is not capped, outlives its TTL or a delete, or is listed out of node order.
func TestWebRTCStatusPublish(t *testing.T) {
	s, ctx := store(t), context.Background()
	legs := make([]WebRTCLeg, WebRTCLegsCap+5)
	for i := range legs {
		legs[i] = WebRTCLeg{Call: "c" + strconv.Itoa(i), Side: "caller", ICE: "connected", DTLS: "connected"}
	}
	b := WebRTCNodeStatus{Node: "sip-b", Port: 21100, Hosts: []string{"10.0.0.2"}, Legs: legs,
		Counters: WebRTCCounters{ICE: map[string]uint64{"connected": 3}}, UpdatedAt: time.Now().UTC()}
	a := WebRTCNodeStatus{Node: "sip-a", Port: 21100, Failures: []WebRTCFailure{{Kind: "dtls", Reason: "timeout"}}}
	if err := s.PutWebRTCStatus(ctx, b, time.Minute); err != nil {
		t.Fatal(err)
	}
	if err := s.PutWebRTCStatus(ctx, a, 1100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	got, err := s.WebRTCStatuses(ctx)
	if err != nil || len(got) != 2 || got[0].Node != "sip-a" || got[1].Node != "sip-b" {
		t.Fatalf("statuses = %+v, %v", got, err)
	}
	if len(got[1].Legs) != WebRTCLegsCap || got[1].Counters.ICE["connected"] != 3 || got[0].Failures[0].Reason != "timeout" {
		t.Fatalf("round trip lost data: %+v", got)
	}
	time.Sleep(1500 * time.Millisecond)
	if got, _ := s.WebRTCStatuses(ctx); len(got) != 1 {
		t.Fatalf("an expired node is still listed: %+v", got)
	}
	if err := s.DeleteWebRTCStatus(ctx, "sip-b"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.WebRTCStatuses(ctx); len(got) != 0 {
		t.Fatalf("a deleted node is still listed: %+v", got)
	}
}
