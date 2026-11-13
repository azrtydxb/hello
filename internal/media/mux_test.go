package media

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/pion/stun/v3"
)

func loopback(p int) *net.UDPAddr { return &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: p} }

func newTestMux(t *testing.T) *Mux {
	t.Helper()
	m, err := ListenMux(0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m
}

func udpPeer(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

// check builds an ICE binding request as a browser sends it to a leg whose
// local ufrag is local.
func check(t *testing.T, local, remote string) []byte {
	t.Helper()
	m, err := stun.Build(stun.TransactionID, stun.BindingRequest, stun.NewUsername(local+":"+remote),
		stun.NewShortTermIntegrity("0123456789abcdefghijklmn"), stun.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	return m.Raw
}

func readEP(t *testing.T, ep *MuxEndpoint) ([]byte, *net.UDPAddr) {
	t.Helper()
	select {
	case d := <-ep.in:
		return d.data, d.from
	case <-time.After(2 * time.Second):
		t.Fatal("endpoint got nothing")
		return nil, nil
	}
}

func expectNothing(t *testing.T, ep *MuxEndpoint) {
	t.Helper()
	select {
	case d := <-ep.in:
		t.Fatalf("endpoint got %d unexpected bytes", len(d.data))
	case <-time.After(150 * time.Millisecond):
	}
}

var rtpDatagram = []byte{0x80, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 1, 9, 9}

func TestMuxRoutesByUfragThenAddress(t *testing.T) {
	m := newTestMux(t)
	a, _ := m.NewEndpoint()
	b, _ := m.NewEndpoint()
	if err := a.SetUfrag("aaaa1111"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetUfrag("bbbb2222"); err != nil {
		t.Fatal(err)
	}
	pa, pb := udpPeer(t), udpPeer(t)
	dst := loopback(m.Port())

	// Media before any check is nobody's: a check binds an address.
	_, _ = pa.WriteToUDP(rtpDatagram, dst)
	expectNothing(t, a)

	_, _ = pa.WriteToUDP(check(t, "aaaa1111", "peerA"), dst)
	_, _ = pb.WriteToUDP(check(t, "bbbb2222", "peerB"), dst)
	for ep, peer := range map[*MuxEndpoint]*net.UDPConn{a: pa, b: pb} {
		got, from := readEP(t, ep)
		if !muxIsSTUN(got) || from.Port != peer.LocalAddr().(*net.UDPAddr).Port {
			t.Errorf("first check: %d bytes from %v", len(got), from)
		}
	}
	dtlsRecord := append([]byte{22, 0xfe, 0xfd, 0, 0, 0, 0, 0, 0, 0, 0, 0, 3}, 1, 2, 3)
	_, _ = pa.WriteToUDP(dtlsRecord, dst)
	_, _ = pa.WriteToUDP(rtpDatagram, dst)
	_, _ = pb.WriteToUDP(rtpDatagram, dst)
	if got, _ := readEP(t, a); len(got) != len(dtlsRecord) {
		t.Errorf("a: dtls record %d bytes", len(got))
	}
	if got, _ := readEP(t, a); len(got) != len(rtpDatagram) {
		t.Errorf("a: rtp %d bytes", len(got))
	}
	if got, _ := readEP(t, b); len(got) != len(rtpDatagram) {
		t.Errorf("b: rtp %d bytes", len(got))
	}
	expectNothing(t, a)
	expectNothing(t, b)

	// Writes leave from the shared port.
	if _, err := a.WriteToUDP([]byte("to-a"), pa.LocalAddr().(*net.UDPAddr)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	_ = pa.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, from, err := pa.ReadFromUDP(buf)
	if err != nil || string(buf[:n]) != "to-a" || from.Port != m.Port() {
		t.Errorf("write: %q from %v err %v", buf[:n], from, err)
	}
}

func TestMuxIgnoresUnknownAndMalformedChecks(t *testing.T) {
	m := newTestMux(t)
	a, _ := m.NewEndpoint()
	_ = a.SetUfrag("aaaa1111")
	p := udpPeer(t)
	dst := loopback(m.Port())
	resp, _ := stun.Build(stun.TransactionID, stun.BindingSuccess, stun.NewUsername("aaaa1111:x"))
	bad := [][]byte{
		check(t, "zzzz9999", "x"),
		check(t, "aaaa1111", "x")[:12],
		make([]byte, 20),
		{0, 1, 0, 0, 0x21, 0x12, 0xa4, 0x42, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
		resp.Raw, // a response, not a check
	}
	for _, b := range bad {
		_, _ = p.WriteToUDP(b, dst)
	}
	expectNothing(t, a)
	_, _ = p.WriteToUDP(rtpDatagram, dst)
	expectNothing(t, a)
	if m.Stats().Unrouted < uint64(len(bad)) {
		t.Errorf("unrouted %d", m.Stats().Unrouted)
	}
}

func TestMuxUfragUniqueAndFreedOnClose(t *testing.T) {
	m := newTestMux(t)
	a, _ := m.NewEndpoint()
	b, _ := m.NewEndpoint()
	if err := a.SetUfrag("aaaa1111"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetUfrag("aaaa1111"); !errors.Is(err, ErrMuxUfragInUse) {
		t.Errorf("second holder of a ufrag: %v", err)
	}
	if err := a.SetUfrag(""); err == nil {
		t.Error("empty ufrag accepted")
	}
	// An ICE restart: the new ufrag routes, the old one is free.
	if err := a.SetUfrag("cccc3333"); err != nil {
		t.Fatal(err)
	}
	if err := b.SetUfrag("aaaa1111"); err != nil {
		t.Errorf("a released ufrag could not be taken: %v", err)
	}
	p := udpPeer(t)
	_, _ = p.WriteToUDP(check(t, "cccc3333", "x"), loopback(m.Port()))
	readEP(t, a)
	_ = a.Close()
	if st := m.Stats(); st.Endpoints != 1 {
		t.Errorf("endpoints %d after one close", st.Endpoints)
	}
	if _, _, err := a.ReadFromUDP(make([]byte, 10)); err == nil {
		t.Error("read on a closed endpoint")
	}
	if _, err := a.WriteToUDP([]byte("x"), loopback(9)); err == nil {
		t.Error("write on a closed endpoint")
	}
	_, _ = p.WriteToUDP(rtpDatagram, loopback(m.Port()))
	expectNothing(t, b)
}

func TestMuxAddressMovesBetweenEndpoints(t *testing.T) {
	m := newTestMux(t)
	a, _ := m.NewEndpoint()
	b, _ := m.NewEndpoint()
	_ = a.SetUfrag("aaaa1111")
	_ = b.SetUfrag("bbbb2222")
	p := udpPeer(t)
	dst := loopback(m.Port())
	_, _ = p.WriteToUDP(check(t, "aaaa1111", "x"), dst)
	readEP(t, a)
	_, _ = p.WriteToUDP(check(t, "bbbb2222", "x"), dst)
	readEP(t, b)
	_, _ = p.WriteToUDP(rtpDatagram, dst)
	readEP(t, b)
	expectNothing(t, a)
	if m.Stats().AddrRebinds != 1 {
		t.Errorf("rebinds %d", m.Stats().AddrRebinds)
	}
}

func TestMuxBoundsAddressesPerEndpoint(t *testing.T) {
	m := newTestMux(t)
	a, _ := m.NewEndpoint()
	_ = a.SetUfrag("aaaa1111")
	dst := loopback(m.Port())
	for i := 0; i < muxMaxAddrs+5; i++ {
		p := udpPeer(t)
		_, _ = p.WriteToUDP(check(t, "aaaa1111", "x"), dst)
		readEP(t, a)
	}
	m.mu.RLock()
	n, bound := len(m.byAddr), len(a.addrs)
	m.mu.RUnlock()
	if n > muxMaxAddrs || bound > muxMaxAddrs {
		t.Errorf("table holds %d addresses (%d on the endpoint), limit %d", n, bound, muxMaxAddrs)
	}
}

func TestMuxEndpointLimitAndClose(t *testing.T) {
	m := newTestMux(t)
	var eps []*MuxEndpoint
	for i := 0; i < muxMaxEndpoints; i++ {
		e, err := m.NewEndpoint()
		if err != nil {
			t.Fatalf("endpoint %d: %v", i, err)
		}
		eps = append(eps, e)
	}
	if _, err := m.NewEndpoint(); !errors.Is(err, ErrMuxFull) {
		t.Errorf("past the limit: %v", err)
	}
	_ = eps[0].Close()
	if _, err := m.NewEndpoint(); err != nil {
		t.Errorf("after a close: %v", err)
	}
	done := make(chan error, 1)
	go func() { _, _, err := eps[1].ReadFromUDP(make([]byte, 10)); done <- err }()
	_ = m.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("reader woke without an error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the mux left a reader blocked")
	}
	if _, err := m.NewEndpoint(); err == nil {
		t.Error("endpoint on a closed mux")
	}
}

// TestRelayOverMux runs two calls, each between a plain phone leg and a leg on
// the one shared port: the browser's check names its leg, then RTP flows both
// ways through the relay with its own rewrite, and the calls stay apart. The
// leg carries plaintext here; DTLS-SRTP keys the same leg once the leg's
// Secure is bound to the endpoint (the seam to #276).
func TestRelayOverMux(t *testing.T) {
	m := newTestMux(t)
	type call struct {
		r       *Relay
		phone   *relayPeer
		browser *relayPeer
		ufrag   string
	}
	var calls []*call
	for i, uf := range []string{"callAAAA", "callBBBB"} {
		lo := testRelayMin + 100*(i+1)
		r, err := NewRelay(lo, lo+50)
		if err != nil {
			t.Fatal(err)
		}
		pa, err := r.AddLeg("a")
		if err != nil {
			t.Fatal(err)
		}
		pb, ep, err := r.AddMuxLeg("b", m)
		if err != nil {
			t.Fatal(err)
		}
		if pb != m.Port() || r.LegPort("b") != m.Port() {
			t.Fatalf("mux leg port %d, want %d", pb, m.Port())
		}
		if err := ep.SetUfrag(uf); err != nil {
			t.Fatal(err)
		}
		r.SetPayloadTypes("a", 0, 101)
		r.SetPayloadTypes("b", 0, 101)
		phone := startPeer(t, loopback(pa))
		phone.only = pa
		bc := udpPeer(t)
		browser := &relayPeer{conn: bc, to: loopback(m.Port()), ssrc: uint32(900 + i), only: m.Port()}
		r.SetTarget("a", phone.addr())
		r.SetTarget("b", browser.addr())
		r.Start()
		t.Cleanup(r.Close)
		calls = append(calls, &call{r: r, phone: phone, browser: browser, ufrag: uf})
	}
	for _, c := range calls {
		_, _ = c.browser.conn.WriteToUDP(check(t, c.ufrag, "browser"), loopback(m.Port()))
	}
	for i, c := range calls {
		payload := []byte{byte(i), 1, 2, 3, 4, 5, 6, 7, 8, 9}
		c.phone.send(0, payload, false)
		got := c.browser.recv(t)
		if got.PayloadType != 0 || got.Payload[0] != payload[0] {
			t.Errorf("call %d: browser got pt %d payload %v", i, got.PayloadType, got.Payload)
		}
		back := []byte{byte(10 + i), 9, 8, 7, 6, 5, 4, 3, 2, 1}
		c.browser.send(0, back, false)
		if got = c.phone.recv(t); got.Payload[0] != back[0] {
			t.Errorf("call %d: phone got payload %v, want %v", i, got.Payload, back)
		}
	}
	// Closing one relay leaves the other's leg and the shared socket up.
	calls[0].r.Close()
	if st := m.Stats(); st.Endpoints != 1 {
		t.Errorf("endpoints %d after one call ended", st.Endpoints)
	}
	calls[1].phone.send(0, []byte{42, 1, 2, 3, 4, 5, 6, 7, 8, 9}, false)
	if got := calls[1].browser.recv(t); got.Payload[0] != 42 {
		t.Errorf("second call broke when the first ended: %v", got.Payload)
	}
}
