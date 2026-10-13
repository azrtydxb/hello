package integration

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/test/sipua"
)

// Trunk-to-internal dialing in the lab (trunk plan Task 4, spec
// .procoder/specs/trunk-internal-numbers.md S-8): a carrier places an
// inbound call addressed to an extension number, the trunk's internal
// dialing policy decides what it may dial, and the audio really flows both
// ways through the media anchor. The carrier's offer names the target
// node's own address (see fakecarrier), so the anchor's legs — and the
// INVITE the extension's phone receives — name hello-sip-1's edge address,
// which this host and the carriers both reach.

// labHostIP is the address the containers reach this host on: the lab's
// HELLO_LAB_CONTACT_HOST (the edge network's gateway), the same value the
// lab phones advertise as their Contact host.
func labHostIP() string {
	if h := os.Getenv("HELLO_LAB_CONTACT_HOST"); h != "" {
		return h
	}
	return "10.89.53.1"
}

// rtpPeer is a minimal RTP endpoint for the lab tests: a host-side UDP
// socket that sends G.711 frames (payload 0x2a, SSRC 0x2a2a2a2a) to a peer
// address and counts the RTP that comes back from any other SSRC.
type rtpPeer struct {
	conn *net.UDPConn

	mu       sync.Mutex
	got      int
	payload  []byte // the payload bytes of the first packet received
	peerAddr *net.UDPAddr
}

func newRTPPeer(t *testing.T) *rtpPeer {
	t.Helper()
	conn, err := (&net.ListenConfig{}).ListenPacket(context.Background(), "udp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	udp, ok := conn.(*net.UDPConn)
	if !ok {
		t.Fatal("not a UDP socket")
	}
	t.Cleanup(func() { _ = udp.Close() })
	p := &rtpPeer{conn: udp}
	go p.loop(t)
	return p
}

func (p *rtpPeer) loop(t *testing.T) {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()
	buf := make([]byte, 1500)
	seq, ts := uint16(1), uint32(160)
	for {
		select {
		case <-t.Context().Done():
			return
		case <-tick.C:
			peer := p.peer()
			if peer == nil {
				continue
			}
			seq, ts = seq+1, ts+160
			pkt := media.BuildRTP(8, seq, ts, 0x2a2a2a2a, false, bytes.Repeat([]byte{0x2a}, 160))
			if _, err := p.conn.WriteToUDP(pkt, peer); err != nil && !errors.Is(err, net.ErrClosed) {
				return
			}
		default:
		}
		_ = p.conn.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
		n, _, err := p.conn.ReadFromUDP(buf)
		if err != nil {
			continue
		}
		if pkt, ok := media.ParseRTP(buf[:n]); ok && pkt.SSRC != 0x2a2a2a2a {
			p.mu.Lock()
			p.got++
			if p.payload == nil {
				p.payload = append([]byte(nil), pkt.Payload...)
			}
			p.mu.Unlock()
		}
	}
}

// peer returns the address to send to, or nil before the first one is set.
func (p *rtpPeer) peer() *net.UDPAddr {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.peerAddr
}

// port is the local port the peer's socket is bound on.
func (p *rtpPeer) port() int { return p.conn.LocalAddr().(*net.UDPAddr).Port }

// sendTo aims the peer's audio at addr.
func (p *rtpPeer) sendTo(addr *net.UDPAddr) {
	p.mu.Lock()
	p.peerAddr = addr
	p.mu.Unlock()
}

// stats reports how many foreign RTP packets arrived and the first one's
// payload bytes.
func (p *rtpPeer) stats() (got int, payload []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.got, p.payload
}

// internalTrunk creates an IP-authenticated trunk whose destination is the
// backup carrier's address — so the backup carrier's calls arrive as this
// trunk's, the control plane having resolved the destination host to the
// carrier container's address, which is the INVITE's source — and whose
// internal dialing policy is patterns, the given ones.
func (lc *labClient) internalTrunk(patterns []string) labTrunk {
	lc.t.Helper()
	suffix := randDigits(6)
	var tr labTrunk
	lc.must("POST", "/api/v1/trunks", map[string]any{
		"name": "internal-" + suffix, "mode": "ip", "optionsInterval": 5,
		"destinations":    []map[string]any{{"host": "carrier-backup", "port": 5060, "priority": 0, "weight": 1}},
		"internalDialing": map[string]any{"mode": "patterns", "patterns": patterns},
		"enabled":         true,
	}, &tr, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", fmt.Sprintf("/api/v1/trunks/%d", tr.ID), nil, nil, 204) })
	return tr
}

// fixedExtension creates an extension with an exact number (the lab's
// random-number helpers would never collide with it) and a desk device.
func (lc *labClient) fixedExtension(number string) labDevice {
	lc.t.Helper()
	var ext struct{ ID int64 }
	lc.must("POST", "/api/v1/extensions", map[string]string{"number": number, "name": "lab " + number}, &ext, 201)
	user := "desk-" + number
	var dev struct {
		ID     int64
		Secret string
	}
	lc.must("POST", "/api/v1/devices", map[string]any{"extensionId": ext.ID, "sipUsername": user}, &dev, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", "/api/v1/extensions/"+lc.extensionID(number), nil, nil, 204) })
	lc.waitSnapshots(10 * time.Second)
	remember(dev.Secret)
	return labDevice{ID: dev.ID, Extension: number, User: user, Secret: dev.Secret}
}

// placeCall places an inbound call through a lab carrier and returns the
// response the carrier reported.
func placeCall(t *testing.T, carrier string, body map[string]any) map[string]any {
	t.Helper()
	var res map[string]any
	carrierDo(t, carrier, "POST", "/call", body, &res)
	return res
}

func TestTrunkInternalLab(t *testing.T) {
	lc := newLabClient(t)
	tr := lc.internalTrunk([]string{"1XX", "10XX"})
	d := lc.fixedExtension("1012")
	// One "any" route: the policy alone decides what this trunk may dial.
	var route struct{ ID int64 }
	lc.must("POST", "/api/v1/routes/inbound", map[string]any{
		"name": "lab-int-" + randDigits(6), "didKind": "any", "trunkId": tr.ID,
		"destinationKind": "internal", "enabled": true,
	}, &route, 201)
	lc.t.Cleanup(func() { _ = lc.do("DELETE", fmt.Sprintf("/api/v1/routes/inbound/%d", route.ID), nil, nil, 204) })

	p := phone(t, d, labSIP1)
	register(t, p)

	// The extension's phone is a host-side RTP endpoint: it answers with
	// its own socket's address as the containers see it (the anchor's relay
	// aims leg b there), and sends its audio to the media address the
	// INVITE's offer names — the anchor's leg b on hello-sip-1.
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	peer := newRTPPeer(t)
	answered := make(chan *sipua.Incoming, 1)
	go func() {
		in, err := p.Next(ctx)
		if err != nil {
			close(answered)
			return
		}
		_ = in.Ring()
		// The answer names this host's socket as the containers see it: the
		// anchor's relay aims leg b there until the peer's packets latch it.
		_ = in.Answer(media.BuildAudioSDP(labHostIP(), peer.port(), 8, 0, 0))
		// The INVITE's offer names the anchor's leg b on the node; the
		// peer's audio towards it latches the relay and makes the trunk's
		// direction flow.
		if offer, err := media.ParseAudioSDP(in.Request.Body()); err == nil {
			peer.sendTo(&net.UDPAddr{IP: net.ParseIP(offer.Address), Port: offer.Port})
		}
		answered <- in
	}()

	num := placeCall(t, backupHTTP, map[string]any{
		"from": "+97145557777", "to": "1012", "target": "hello-sip-1:5060",
		"rtp": true, "hangupAfterMs": 2000,
	})
	if num["status"].(float64) != 200 {
		t.Fatalf("inbound call to sip:1012@ = %v, want 200 (extension 1012 rang and answered)", num)
	}
	if in := <-answered; in == nil {
		t.Fatal("extension 1012 never rang")
	}
	sent, sentOK := num["rtpSent"].(float64)
	got, gotOK := num["rtpReceived"].(float64)
	if !sentOK || !gotOK || sent < 10 || got < 5 {
		t.Fatalf("carrier rtp counters = %v; want audio both ways", num)
	}
	pgot, payload := peer.stats()
	if pgot < 5 {
		t.Fatalf("the extension's phone received %d RTP packets, want audio from the trunk", pgot)
	}
	for _, b := range payload {
		if b != 0x55 { // the carrier's PCMA silence marker
			t.Fatalf("the phone's audio payload byte %#02x, want the carrier's silence marker 0x55", b)
		}
	}

	// A DID outside the policy: no pattern matches 2012, so the internal
	// route is skipped and the call is rejected 404.
	out := placeCall(t, backupHTTP, map[string]any{
		"from": "+97145557777", "to": "2012", "target": "hello-sip-1:5060",
	})
	if out["status"].(float64) != 404 {
		t.Fatalf("out-of-pattern DID 2012 = %v, want 404", out)
	}

	// The traces: the answered call shows the verdict naming the winning
	// pattern, the internal lookup and the caller-ID ladder's first rung;
	// the rejected call shows the policy skip and the rejection.
	find := func(source, dest string, status int) (int64, error) {
		for _, c := range lc.cdrsTo(dest) {
			if c.Source == source && c.FinalStatus == status {
				return c.ID, nil
			}
		}
		return 0, errors.New("no CDR yet")
	}
	var id int64
	eventually(t, 10*time.Second, "CDR for the answered call", func() error {
		var err error
		id, err = find("+97145557777", "1012", 200)
		return err
	})
	joined := strings.Join(lc.cdrTrace(id), "\n")
	for _, want := range []string{
		"Trunk " + tr.Name + " may dial 1012: pattern 10XX",
		"Destination: internal",
		"Internal lookup 1012 -> extension 1012",
		"Caller ID +97145557777 as received",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("trace lacks %q:\n%s", want, joined)
		}
	}
	var rejected int64
	eventually(t, 10*time.Second, "CDR for the rejected call", func() error {
		var err error
		rejected, err = find("+97145557777", "2012", 404)
		return err
	})
	joined = strings.Join(lc.cdrTrace(rejected), "\n")
	for _, want := range []string{
		"Trunk " + tr.Name + " may not dial 2012: no pattern matches",
		"internal dialing to 2012 not allowed on trunk " + tr.Name,
		"No inbound route matched DID 2012",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("rejected trace lacks %q:\n%s", want, joined)
		}
	}
}
