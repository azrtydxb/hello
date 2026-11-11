package sip

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

type countTx struct {
	sip.ServerTransaction
	n   atomic.Int32
	req *sip.Request
}

func (c *countTx) Origin() *sip.Request { return c.req }

func (c *countTx) Respond(*sip.Response) error { c.n.Add(1); return nil }

func okTo(t *testing.T, method sip.RequestMethod) (*sip.Request, *sip.Response) {
	t.Helper()
	req := sip.NewRequest(method, sip.Uri{User: "a", Host: "example.org"})
	req.AppendHeader(sip.NewHeader("Call-ID", "c1"))
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{User: "b", Host: "example.org"}, Params: sip.NewParams()})
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 7, MethodName: method})
	return req, sip.NewResponseFromRequest(req, 200, "OK", nil)
}

// withToTag gives req the To tag of a request inside a dialog.
func withToTag(req *sip.Request) *sip.Request {
	req.To().Params.Add("tag", "t1")
	return req
}

func fastTimers(t *testing.T) {
	t1, t2 := uasT1, uasT2
	uasT1, uasT2 = 10*time.Millisecond, 40*time.Millisecond
	t.Cleanup(func() { uasT1, uasT2 = t1, t2 })
}

// A 2xx to an INVITE is sent again until its ACK arrives, then no more.
func TestUASRetransmits2xxUntilACK(t *testing.T) {
	fastTimers(t)
	s := &Server{done: make(chan struct{})}
	inv, res := okTo(t, sip.INVITE)
	tx := &countTx{req: withToTag(inv)}
	s.retransmit2xx(tx, res)
	time.Sleep(120 * time.Millisecond)
	if n := tx.n.Load(); n < 2 {
		t.Fatalf("2xx retransmitted %d times in 120ms, want at least 2", n)
	}
	ack, _ := okTo(t, sip.ACK)
	ack.ReplaceHeader(&sip.CSeqHeader{SeqNo: inv.CSeq().SeqNo, MethodName: sip.ACK})
	s.noteAck(ack)
	time.Sleep(30 * time.Millisecond)
	n := tx.n.Load()
	time.Sleep(120 * time.Millisecond)
	if tx.n.Load() != n {
		t.Fatalf("2xx still retransmitted after the ACK: %d then %d", n, tx.n.Load())
	}
	s.bg.Wait()
}

// Only a 2xx to an INVITE is retransmitted; a BYE's 200 and a failure are not.
func TestUASDoesNotRetransmitOthers(t *testing.T) {
	fastTimers(t)
	s := &Server{done: make(chan struct{})}
	tx := &countTx{}
	_, bye := okTo(t, sip.BYE)
	s.retransmit2xx(tx, bye)
	inv, _ := okTo(t, sip.INVITE)
	s.retransmit2xx(tx, sip.NewResponseFromRequest(inv, 486, "Busy Here", nil))
	time.Sleep(100 * time.Millisecond)
	if n := tx.n.Load(); n != 0 {
		t.Fatalf("%d retransmissions for a BYE 200 and a 486, want none", n)
	}
}

// Shutdown ends the retransmission.
func TestUASRetransmissionStopsWithTheServer(t *testing.T) {
	fastTimers(t)
	s := &Server{done: make(chan struct{})}
	inv, res := okTo(t, sip.INVITE)
	s.retransmit2xx(&countTx{req: withToTag(inv)}, res)
	close(s.done)
	done := make(chan struct{})
	go func() { s.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the retransmission outlived the server")
	}
}

// The 2xx of a call's first INVITE is sipgo's dialog session's to retransmit:
// Server.send must not start a second loop for it.
func TestUASLeavesTheFirstInviteToSipgo(t *testing.T) {
	fastTimers(t)
	s := &Server{done: make(chan struct{})}
	inv, res := okTo(t, sip.INVITE) // no To tag
	tx := &countTx{req: inv}
	s.retransmit2xx(tx, res)
	time.Sleep(100 * time.Millisecond)
	if n := tx.n.Load(); n != 0 {
		t.Fatalf("%d retransmissions of a first INVITE's 2xx by Server.send, want none", n)
	}
	s.uasAcks.mu.Lock()
	defer s.uasAcks.mu.Unlock()
	if len(s.uasAcks.m) != 0 {
		t.Fatalf("state kept for a first INVITE: %v", s.uasAcks.m)
	}
}

// A dialog that ends before the ACK (BYE, CANCEL, hangup: unbind) stops its
// loop and leaves no state.
func TestUASDialogEndStopsRetransmission(t *testing.T) {
	fastTimers(t)
	s := &Server{done: make(chan struct{})}
	inv, res := okTo(t, sip.INVITE)
	tx := &countTx{req: withToTag(inv)}
	s.retransmit2xx(tx, res)
	time.Sleep(50 * time.Millisecond)
	s.uasAcks.endDialog("c1")
	waited := make(chan struct{})
	go func() { s.bg.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(2 * time.Second):
		t.Fatal("the retransmission outlived its dialog")
	}
	s.uasAcks.mu.Lock()
	defer s.uasAcks.mu.Unlock()
	if len(s.uasAcks.m) != 0 {
		t.Fatalf("state left after the dialog ended: %v", s.uasAcks.m)
	}
}
