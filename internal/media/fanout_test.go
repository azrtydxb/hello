package media

import (
	"net"
	"testing"
	"time"
)

// TestFanoutOneWay: the caller's audio reaches every member, rewritten into
// one outbound stream with the payload intact; what a member sends back is
// dropped and never reaches the caller; other payload types are not fanned
// out; a removed member stops receiving. It fails if a member misses audio,
// the caller hears a member, telephone events leak, or a removed member
// still receives.
func TestFanoutOneWay(t *testing.T) {
	f, err := NewFanout(31000, 31100, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if f.CallerPort() == f.MemberPort() {
		t.Fatal("caller and member sockets share a port")
	}
	callerDst := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.CallerPort()}
	caller := startPeer(t, callerDst)
	m1 := startPeer(t, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.MemberPort()})
	m2 := startPeer(t, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.MemberPort()})
	f.AddTarget("m1", m1.addr())
	f.AddTarget("m2", m2.addr())
	if f.Targets() != 2 {
		t.Fatalf("targets = %d, want 2", f.Targets())
	}

	payload := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	caller.send(0, payload, false)
	var ssrcs [2]uint32
	for i, m := range []*relayPeer{m1, m2} {
		pkt := m.recv(t)
		if pkt.PayloadType != 0 || string(pkt.Payload) != string(payload) {
			t.Fatalf("member %d got pt %d payload %v, want the caller's audio untouched", i+1, pkt.PayloadType, pkt.Payload)
		}
		if pkt.SSRC == 777 {
			t.Fatalf("member %d saw the caller's SSRC, the stream is not rewritten", i+1)
		}
		ssrcs[i] = pkt.SSRC
	}
	if ssrcs[0] != ssrcs[1] {
		t.Fatalf("members got different streams %v", ssrcs)
	}

	// A telephone event or another codec from the caller is not fanned out.
	caller.send(101, []byte{1, 0, 0, 0}, false)
	caller.send(8, payload, false)
	caller.send(0, payload, false)
	if pkt := m1.recv(t); pkt.PayloadType != 0 {
		t.Fatalf("member got pt %d after a telephone event and PCMA, want only PCMU", pkt.PayloadType)
	}

	// One-way: the member's audio goes nowhere.
	m1.to = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: f.MemberPort()}
	m1.send(0, payload, false)
	_ = caller.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	buf := make([]byte, rtpMaxPacket)
	if n, _, err := caller.conn.ReadFromUDP(buf); err == nil {
		t.Fatalf("the caller received %d bytes from a member", n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for f.Stats().Inbound == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if f.Stats().Inbound == 0 {
		t.Fatal("a member's datagram was not counted as dropped")
	}

	// A removed member stops receiving.
	f.RemoveTarget("m2")
	_ = m2.conn.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	for {
		if _, _, err := m2.conn.ReadFromUDP(buf); err != nil {
			break
		}
	}
	caller.send(0, payload, false)
	_ = m2.conn.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, _, err := m2.conn.ReadFromUDP(buf); err == nil {
		t.Fatal("a removed member still received audio")
	}
	if got := f.Stats().Packets; got < 3 {
		t.Fatalf("packets fanned out = %d, want at least 3", got)
	}
}

// TestFanoutClose: Close releases both ports and is safe twice.
func TestFanoutClose(t *testing.T) {
	f, err := NewFanout(31200, 31210, 8)
	if err != nil {
		t.Fatal(err)
	}
	cp := f.CallerPort()
	f.Close()
	f.Close()
	c, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: cp})
	if err != nil {
		t.Fatalf("caller port %d not released: %v", cp, err)
	}
	_ = c.Close()
	f.AddTarget("late", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9})
	if f.Targets() != 0 {
		t.Fatal("a closed fan-out took a target")
	}
}
