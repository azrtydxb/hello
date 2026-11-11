// Reliable provisional responses (RFC 3262, phone-feature-parity S-17).
//
// A B2BUA has two independent dialogs, so 100rel is negotiated and acknowledged
// hop by hop; nothing is relayed unchanged.
//
//   - Toward a phone or trunk (Hello is the UAC) every INVITE offers
//     "Supported: 100rel". When the far end sends a provisional response with
//     "Require: 100rel" and an RSeq, Hello answers it with a PRACK carrying
//     "RAck: <rseq> <cseq> INVITE", whether or not the caller asked for 100rel.
//     A retransmitted provisional (the same RSeq) is not acknowledged twice.
//   - Toward the caller (Hello is the UAS) provisional responses are sent
//     reliably (Require: 100rel, RSeq) when the caller's INVITE carries
//     "Require: 100rel": they are retransmitted at T1, 2*T1, ... until the
//     caller's PRACK arrives or 64*T1 passes. The 2xx waits for the PRACK of
//     the last reliable provisional (RFC 3262 section 3). A caller that only
//     lists 100rel in Supported gets the plain responses, as phones keep 100rel
//     off (Grandstream P272 = 0).
package sip

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

const (
	prackT1 = 500 * time.Millisecond
	// prackGiveUp is 64*T1: a reliable provisional unacknowledged for this
	// long is dropped (RFC 3262 section 3).
	prackGiveUp = 64 * prackT1
)

// relState is a call's reliable provisional response toward the caller. mu
// is its own: the PRACK handler and the retransmit timer reach it without
// the call's.
type relState struct {
	mu    sync.Mutex
	rseq  uint32        // the last RSeq sent
	cseq  uint32        // CSeq of the INVITE the RSeq belongs to
	acked chan struct{} // closed when the outstanding response is acknowledged; nil: none outstanding
	stop  chan struct{} // closed to end the retransmissions
	done  chan struct{} // closed when the retransmit goroutine has returned
}

// callerRequires100rel reports whether the caller's INVITE says
// "Require: 100rel": every provisional response to it must be reliable.
func (c *call) callerRequires100rel() bool {
	return c.inv != nil && listHas(c.inv.GetHeaders("Require"), "100rel")
}

// respondProv sends a provisional response (not 100) to the caller, reliably
// when the caller requires it. It returns once the response is sent.
func (c *call) respondProv(code int, reason string, body []byte, hdrs ...sip.Header) error {
	if !c.callerRequires100rel() {
		return c.dss.Respond(code, reason, body, hdrs...)
	}
	c.settleReliable(false) // a newer response supersedes an unacknowledged one
	c.rel.mu.Lock()
	c.rel.rseq++
	rseq := c.rel.rseq
	cseq := uint32(c.inv.CSeq().SeqNo)
	acked, stop, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c.rel.cseq, c.rel.acked, c.rel.stop, c.rel.done = cseq, acked, stop, done
	c.rel.mu.Unlock()

	res := sip.NewResponseFromRequest(c.dss.InviteRequest, code, reason, body)
	for _, h := range hdrs {
		res.AppendHeader(h)
	}
	res.AppendHeader(sip.NewHeader("Require", "100rel"))
	res.AppendHeader(sip.NewHeader("RSeq", strconv.FormatUint(uint64(rseq), 10)))
	if err := c.dss.WriteResponse(res); err != nil {
		c.settleReliable(false)
		return err
	}
	c.addTrace(fmt.Sprintf("%d %s sent reliably (RSeq %d)", code, reason, rseq))
	go c.retransmitReliable(res, acked, stop, done)
	return nil
}

// retransmitReliable resends res at T1, 2*T1, ... until acked, stop or 64*T1.
func (c *call) retransmitReliable(res *sip.Response, acked, stop, done chan struct{}) {
	defer close(done)
	defer contain(c.s.log, "reliable provisional")
	wait := prackT1
	for total := time.Duration(0); total < prackGiveUp; total += wait {
		t := time.NewTimer(wait)
		select {
		case <-acked:
			t.Stop()
			return
		case <-stop:
			t.Stop()
			return
		case <-c.s.done:
			t.Stop()
			return
		case <-t.C:
		}
		if err := c.dss.WriteResponse(res); err != nil {
			return
		}
		wait *= 2
	}
	c.addTrace("Reliable provisional response was never acknowledged (no PRACK within 64*T1)")
}

// settleReliable ends the outstanding reliable provisional response before
// the next response. With wait true (before a 2xx) it first gives the caller's
// PRACK up to 64*T1 to arrive; with false it only stops the retransmissions.
// It returns once the retransmit goroutine is gone, so a late retransmission
// can never follow the response the caller sends next.
func (c *call) settleReliable(wait bool) {
	c.rel.mu.Lock()
	acked, stop, done := c.rel.acked, c.rel.stop, c.rel.done
	c.rel.mu.Unlock()
	if done == nil {
		return
	}
	if wait {
		t := time.NewTimer(prackGiveUp)
		select {
		case <-acked:
		case <-done:
		case <-c.s.done:
		case <-t.C:
		}
		t.Stop()
	}
	c.rel.mu.Lock()
	if c.rel.done == done {
		c.rel.acked, c.rel.stop, c.rel.done = nil, nil, nil
		select {
		case <-stop:
		default:
			close(stop)
		}
	}
	c.rel.mu.Unlock()
	<-done
}

// ackReliable matches a PRACK against the outstanding response; it reports
// whether the RAck names it.
func (c *call) ackReliable(rack string) bool {
	f := strings.Fields(rack)
	if len(f) != 3 || !strings.EqualFold(f[2], string(sip.INVITE)) {
		return false
	}
	rseq, err1 := strconv.ParseUint(f[0], 10, 32)
	cseq, err2 := strconv.ParseUint(f[1], 10, 32)
	if err1 != nil || err2 != nil {
		return false
	}
	c.rel.mu.Lock()
	defer c.rel.mu.Unlock()
	if c.rel.acked == nil || uint32(rseq) != c.rel.rseq || uint32(cseq) != c.rel.cseq {
		return false
	}
	select {
	case <-c.rel.acked:
	default:
		close(c.rel.acked)
	}
	return true
}

// handlePrack serves a caller's PRACK: 200 when its RAck names the
// outstanding reliable response (or one already acknowledged, as PRACKs
// are retransmitted), 481 when no dialog or response matches.
func (s *Server) handlePrack(req *sip.Request, tx sip.ServerTransaction) {
	ref, ok := s.matchDialog(req)
	if !ok || ref.leg != nil {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	h := req.GetHeader("RAck")
	if h == nil || !ref.c.ackReliable(h.Value()) {
		if h != nil && ref.c.rackSeen(h.Value()) {
			s.respond(tx, req, sip.StatusOK, "OK") // a retransmitted PRACK
			return
		}
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	ref.c.addTrace("PRACK received from the caller")
	s.respond(tx, req, sip.StatusOK, "OK")
}

// rackSeen reports whether rack names a response already acknowledged.
func (c *call) rackSeen(rack string) bool {
	f := strings.Fields(rack)
	if len(f) != 3 {
		return false
	}
	rseq, err := strconv.ParseUint(f[0], 10, 32)
	if err != nil {
		return false
	}
	c.rel.mu.Lock()
	defer c.rel.mu.Unlock()
	return c.rel.rseq != 0 && uint32(rseq) <= c.rel.rseq
}

// addPrackHeaders makes the fork's INVITE offer 100rel.
func (l *leg) addPrackHeaders(req *sip.Request) {
	req.AppendHeader(sip.NewHeader("Supported", "100rel"))
}

// acknowledgeProvisional answers a provisional response that requires 100rel
// with a PRACK on the fork's early dialog. Retransmissions and out-of-order
// RSeq values are not acknowledged (RFC 3262 section 4).
func (l *leg) acknowledgeProvisional(dcs *sipgo.DialogClientSession, r *sip.Response) {
	if r.StatusCode <= 100 || r.StatusCode >= 200 || !listHas(r.GetHeaders("Require"), "100rel") {
		return
	}
	h := r.GetHeader("RSeq")
	cs := r.CSeq()
	if h == nil || cs == nil {
		return
	}
	n, err := strconv.ParseUint(strings.TrimSpace(h.Value()), 10, 32)
	if err != nil {
		return
	}
	l.mu.Lock()
	last := l.lastRSeq
	if last != 0 && uint32(n) != last+1 {
		l.mu.Unlock()
		return
	}
	l.lastRSeq = uint32(n)
	l.mu.Unlock()

	target := dcs.InviteRequest.Recipient
	if ct := r.Contact(); ct != nil {
		target = ct.Address
	}
	req := sip.NewRequest(sip.PRACK, *target.Clone())
	req.AppendHeader(sip.NewHeader("RAck", fmt.Sprintf("%d %d %s", n, cs.SeqNo, cs.MethodName)))
	ctx, cancel := context.WithTimeout(context.Background(), prackGiveUp)
	ptx, err := dcs.TransactionRequest(ctx, req)
	if err != nil {
		cancel()
		l.c.s.log.Debug("PRACK failed", "correlation_id", l.c.id, "error", err)
		return
	}
	l.c.addTrace(fmt.Sprintf("PRACK sent for %d (RSeq %d)", r.StatusCode, n))
	go func() {
		defer cancel()
		defer ptx.Terminate()
		defer contain(l.c.s.log, "PRACK")
		select {
		case <-ptx.Responses():
		case <-ptx.Done():
		case <-ctx.Done():
		}
	}()
}
