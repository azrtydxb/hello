package sip

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"golang.org/x/net/ipv4"
)

// TestDSCPMarking fails if the SIP socket is not marked with the configured
// DSCP (default CS3 26; spec S-21).
func TestDSCPMarking(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	for _, dscp := range []int{26, 24, 0} {
		c, err := ListenUDP(context.Background(), "127.0.0.1:0", dscp, log)
		if err != nil {
			t.Fatal(err)
		}
		tos, err := ipv4.NewPacketConn(c.(*net.UDPConn)).TOS()
		_ = c.Close()
		if err != nil {
			t.Skipf("cannot read IP_TOS on this platform: %v", err)
		}
		if tos != dscp<<2 {
			t.Errorf("SIP socket DSCP %d: TOS %#x, want %#x", dscp, tos, dscp<<2)
		}
	}
}
