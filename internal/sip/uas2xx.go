package sip

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
)

// RFC 3261 13.3.1.4: a UAS that answers an INVITE with a 2xx retransmits it,
// from T1 doubling up to T2, until the ACK arrives or 64*T1 have passed. The
// INVITE server transaction ends at the 2xx (RFC 6026), so it is the user
// agent's job; sipgo's dialog sessions do it for the call's first INVITE
// (DialogServerSession.WriteResponse) and this does it for every 2xx Hello
// sends itself to an INVITE: the re-INVITEs of a call, which the caller's
// UAC sends no more than once after a provisional answer. Without it a 2xx
// lost on the way - in the seconds the edge's VIP is between two nodes, or
// to any packet loss - is never repeated, the phone keeps the re-INVITE
// pending until it gives up, and the call is torn down or its media is left
// on the old session.
var (
	uasT1 = sip.T1
	uasT2 = sip.T2
)

// ackKey identifies the INVITE an ACK answers: its dialog and CSeq number.
func ackKey(callID string, seq uint32) string {
	return callID + "\x00" + strconv.FormatUint(uint64(seq), 10)
}

// uasAcks are the 2xx being retransmitted, by ackKey; the channel is closed
// by the ACK.
type uasAcks struct {
	mu sync.Mutex
	m  map[string]chan struct{}
}

func (u *uasAcks) add(key string) chan struct{} {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.m == nil {
		u.m = map[string]chan struct{}{}
	}
	if ch, ok := u.m[key]; ok {
		return ch // the 2xx of a retransmitted INVITE: one retransmission loop
	}
	ch := make(chan struct{})
	u.m[key] = ch
	return ch
}

func (u *uasAcks) ack(key string) {
	u.mu.Lock()
	ch, ok := u.m[key]
	delete(u.m, key)
	u.mu.Unlock()
	if ok {
		close(ch)
	}
}

// endDialog stops the retransmission of every 2xx of a dialog that ended (a
// BYE, a CANCEL, a hangup, all of which unbind it) before its ACK.
func (u *uasAcks) endDialog(callID string) {
	u.mu.Lock()
	var stop []chan struct{}
	for k, ch := range u.m {
		if strings.HasPrefix(k, callID+"\x00") {
			stop = append(stop, ch)
			delete(u.m, k)
		}
	}
	u.mu.Unlock()
	for _, ch := range stop {
		close(ch)
	}
}

func (u *uasAcks) drop(key string, ch chan struct{}) {
	u.mu.Lock()
	if u.m[key] == ch {
		delete(u.m, key)
	}
	u.mu.Unlock()
}

// noteAck stops the retransmission of the 2xx req acknowledges.
func (s *Server) noteAck(req *sip.Request) {
	if req.CallID() == nil || req.CSeq() == nil {
		return
	}
	s.uasAcks.ack(ackKey(req.CallID().Value(), req.CSeq().SeqNo))
}

// retransmit2xx starts retransmitting res, the 2xx just sent on tx, until its
// ACK. It returns at once.
func (s *Server) retransmit2xx(tx sip.ServerTransaction, res *sip.Response) {
	if tx == nil || res == nil || !res.IsSuccess() || res.CallID() == nil || res.CSeq() == nil || res.CSeq().MethodName != sip.INVITE {
		return
	}
	// Only the 2xx of an in-dialog INVITE: the first INVITE's is answered
	// through its sipgo dialog session (or, for a few services, a raw
	// transaction), and DialogServerSession.WriteResponse retransmits it
	// itself; doing it here too would send it twice as often and race that
	// loop for the ACK.
	if o := originOf(tx); o == nil || o.To() == nil {
		return
	} else if _, ok := o.To().Params.Get("tag"); !ok {
		return
	}
	key := ackKey(res.CallID().Value(), res.CSeq().SeqNo)
	acked := s.uasAcks.add(key)
	s.goBG(func() {
		defer s.uasAcks.drop(key, acked)
		deadline := time.NewTimer(64 * uasT1)
		defer deadline.Stop()
		wait := uasT1
		for {
			t := time.NewTimer(wait)
			select {
			case <-acked:
				t.Stop()
				return
			case <-deadline.C:
				t.Stop()
				return
			case <-s.done:
				t.Stop()
				return
			case <-t.C:
			}
			if err := tx.Respond(res); err != nil {
				s.log.Debug("2xx retransmission failed", "call_id", key, "error", err)
				return
			}
			wait = min(2*wait, uasT2)
		}
	})
}

// originOf is the request tx answers (sipgo's transactions know it, though
// the interface does not say so), looking through Hello's wrapper.
func originOf(tx sip.ServerTransaction) *sip.Request {
	switch v := tx.(type) {
	case *sessionHeaderTx:
		return originOf(v.ServerTransaction)
	case interface{ Origin() *sip.Request }:
		return v.Origin()
	}
	return nil
}
