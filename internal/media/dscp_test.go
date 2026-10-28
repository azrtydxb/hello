package media

import (
	"net"
	"testing"

	"golang.org/x/net/ipv4"
)

func tosOf(t *testing.T, c *net.UDPConn) int {
	t.Helper()
	tos, err := ipv4.NewPacketConn(c).TOS()
	if err != nil {
		t.Skipf("cannot read IP_TOS on this platform: %v", err)
	}
	return tos
}

// TestDSCPMarking fails if a relay, anchor, fanout or party RTP socket is
// not marked with the configured DSCP (default EF 46; spec S-21).
func TestDSCPMarking(t *testing.T) {
	t.Cleanup(func() { SetRTPDSCP(DefaultRTPDSCP) })
	for _, dscp := range []int{DefaultRTPDSCP, 34, 0} {
		SetRTPDSCP(dscp)
		want := dscp << 2

		r, err := NewRelay(40000, 40100)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.AddLeg("a"); err != nil {
			t.Fatal(err)
		}
		if got := tosOf(t, r.legs[0].conn); got != want {
			t.Errorf("relay leg DSCP %d: TOS %#x, want %#x", dscp, got, want)
		}
		r.Close()

		c, err := bindRTP()
		if err != nil {
			t.Fatal(err)
		}
		if got := tosOf(t, c); got != want {
			t.Errorf("anchor socket DSCP %d: TOS %#x, want %#x", dscp, got, want)
		}
		_ = c.Close()

		f, err := bindFrom(40200, 40300, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := tosOf(t, f); got != want {
			t.Errorf("fanout socket DSCP %d: TOS %#x, want %#x", dscp, got, want)
		}
		_ = f.Close()
	}
}

func TestSetRTPDSCPIgnoresOutOfRange(t *testing.T) {
	t.Cleanup(func() { SetRTPDSCP(DefaultRTPDSCP) })
	SetRTPDSCP(64)
	SetRTPDSCP(-1)
	if RTPDSCP() != DefaultRTPDSCP {
		t.Fatalf("RTPDSCP = %d after out-of-range sets", RTPDSCP())
	}
	if err := MarkDSCP(nil, 64); err == nil {
		t.Fatal("MarkDSCP accepted 64")
	}
}
