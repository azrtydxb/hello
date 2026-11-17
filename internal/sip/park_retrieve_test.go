package sip

import (
	"net"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
)

// TestCallParkRetrieveAsParkerLeaves: a retrieve can land while the park
// goroutine is still finishing with the parker, between the parker's BYE
// and the release of the parker's relay leg. That leg is the one the
// retriever is attached to. It fails if the park goroutine then clears the
// retriever's media target (the retriever stops hearing the parked party) or
// starts the music on hold over the retrieved call.
func TestCallParkRetrieveAsParkerLeaves(t *testing.T) {
	st := parkStore(t)
	pbx := startPBX(t, threeDevices(), withMedia(false), withPark(st, "sip-a", testLot))
	a, b, c := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1"), newPhone(t, pbx, "c1", "pc")
	for _, p := range []*phone{a, b, c} {
		p.register(t)
		p.setCallee(answerAfter(nil))
	}
	reached, release := make(chan struct{}), make(chan struct{})
	hold := func() {
		close(reached)
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}
	pbx.srv.parkerGoneHook.Store(&hold)

	pc := establish(t, a, b)
	parkBy(t, a, pc, "*74702")
	occupied(t, st, "702", "100")
	waitReq(t, a.byes, "BYE to the parker")
	select {
	case <-reached:
	case <-time.After(10 * time.Second):
		t.Fatal("the park goroutine did not finish the parker's BYE")
	}

	// The retrieve lands while the park goroutine is held right after the
	// parker's BYE.
	r := waitCall(t, dial(t.Context(), c, "*75702"))
	if r.err != nil {
		t.Fatalf("retrieve: %v", r.err)
	}
	feedC := startRTPFeed(t, r.dcs.InviteResponse.Body())
	// The retriever's media address is the one its SDP names. Listening
	// there sees what the relay sends before any packet from the retriever
	// has re-latched the leg to the feed's socket.
	offer, err := media.ParseAudioSDP([]byte(c.sdpText()))
	if err != nil {
		t.Fatal(err)
	}
	atC, err := net.ListenPacket("udp", net.JoinHostPort(offer.Address, strconv.Itoa(offer.Port)))
	if err != nil {
		t.Fatalf("listen on the retriever's SDP address: %v", err)
	}
	t.Cleanup(func() { _ = atC.Close() })
	stop := make(chan struct{})
	toB := countRTP(pc.feedB.conn, stop)
	close(release)

	// Nobody talks for a while: the parked party must hear nothing, which
	// rules out music on hold started over the retrieved call.
	time.Sleep(800 * time.Millisecond)
	close(stop)
	time.Sleep(150 * time.Millisecond) // the counter's last read returns
	if n := toB.Load(); n != 0 {
		t.Errorf("the retrieved party received %d RTP packets while nobody talked, want 0 (music on hold)", n)
	}

	// The parked party talks first: the retriever's leg must still point
	// at the retriever's SDP address, which the retrieve set, not have been
	// cleared as the parker's.
	pc.feedB.packet(0, []byte("from-b-to-the-retriever....."), false)
	if !hears(atC, "from-b-to-the-retriever.....", 3*time.Second) {
		t.Fatal("the retriever's SDP address did not get the parked party's audio (its relay leg was cleared)")
	}
	// Both ways once the retriever talks too.
	feedC.packet(0, make([]byte, 160), false)
	time.Sleep(100 * time.Millisecond)
	pc.feedB.packet(0, []byte("from-b-to-the-retriever-2..."), false)
	if !hears(feedC.conn, "from-b-to-the-retriever-2...", 3*time.Second) {
		t.Fatal("the retriever did not hear the parked party")
	}
	feedC.packet(0, []byte("from-c-to-the-parked-party.."), false)
	if !hears(pc.feedB.conn, "from-c-to-the-parked-party..", 3*time.Second) {
		t.Fatal("the parked party did not hear the retriever")
	}

	hangup(t, r.dcs)
	waitReq(t, b.byes, "BYE to the parked party when the retriever hangs up")
	pbx.nextCDR(t)
	pbx.nextCDR(t)
}
