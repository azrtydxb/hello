package media

import (
	"net"
	"testing"
	"time"
)

// TestRelayLastRTP fails if LastRTP does not start at the relay's start, does
// not advance when a peer's datagram arrives, or moves for a datagram the
// source policy refuses (a stranger must not keep a dead call alive).
func TestRelayLastRTP(t *testing.T) {
	r, err := NewRelay(23100, 23199)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	port, err := r.AddLeg("a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AddLeg("b"); err != nil {
		t.Fatal(err)
	}
	if !r.LastRTP().IsZero() {
		t.Fatal("LastRTP before Start must be the zero time")
	}
	r.SetTarget("a", udp("203.0.113.5", 4000)) // a public SDP address: 127.0.0.1 is a stranger
	r.Start()
	started := r.LastRTP()
	if started.IsZero() || time.Since(started) > 5*time.Second {
		t.Fatalf("LastRTP after Start = %v, want about now", started)
	}
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	pkt := make([]byte, 172)
	pkt[0] = 0x80
	if _, err := conn.Write(pkt); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if got := r.LastRTP(); !got.Equal(started) {
		t.Fatalf("a refused stranger moved LastRTP from %v to %v", started, got)
	}
	r.SetTarget("a", udp("127.0.0.1", 4000))
	if _, err := conn.Write(pkt); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !r.LastRTP().After(started) {
		if time.Now().After(deadline) {
			t.Fatal("LastRTP did not advance for the peer's datagram")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
