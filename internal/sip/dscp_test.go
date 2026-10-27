package sip

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/emiago/sipgo/sip"
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

// TestDSCPOutboundOnMarkedSocket fails if a request the server originates
// leaves from anything but the marked listening socket. sipgo opens sockets
// of its own only when no connection to the forced local address exists, and
// offers no hook to mark them, so the mark covers originated SIP (trunk
// REGISTER, INVITE, NOTIFY) only while those requests reuse the listener.
func TestDSCPOutboundOnMarkedSocket(t *testing.T) {
	p := startPBX(t, nil)
	if err := media.MarkDSCP(p.conn, 26); err != nil {
		t.Skipf("cannot mark: %v", err)
	}
	peer, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = peer.Close() }()
	paddr := peer.LocalAddr().(*net.UDPAddr)
	for range 3 { // fresh remote each time is not needed: reuse must hold on repeats
		req := sip.NewRequest(sip.OPTIONS, sip.Uri{Scheme: "sip", User: "x", Host: "127.0.0.1", Port: paddr.Port})
		if err := p.srv.client.WriteRequest(req, p.srv.addViaFromListener); err != nil {
			t.Fatal(err)
		}
		_ = peer.SetReadDeadline(time.Now().Add(5 * time.Second))
		buf := make([]byte, 4096)
		_, from, err := peer.ReadFromUDP(buf)
		if err != nil {
			t.Fatal(err)
		}
		if want := p.conn.LocalAddr().(*net.UDPAddr).Port; from.Port != want {
			t.Fatalf("originated request left from port %d, not the marked SIP socket %d", from.Port, want)
		}
	}
}
