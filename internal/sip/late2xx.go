package sip

import (
	"sync"
	"time"

	"github.com/emiago/sipgo/sip"
)

// lateAnswerWindow is how long a fork whose INVITE ended without a 2xx
// still waits for one: a UAS retransmits its 2xx for 64*T1 (RFC 3261
// §13.3.1.4), so a 2xx that crossed the CANCEL arrives within it.
var lateAnswerWindow = 64 * sip.T1

// lateForks holds the forks whose INVITE ended without a 2xx, by Call-ID
// (each fork has its own), until lateAnswerWindow has passed. The zero
// value is ready to use.
//
// A callee's 2xx can cross Hello's CANCEL and reach Hello after the 487: a
// forking proxy or SBC in front of the phone forwards a 2xx even after it
// has forwarded the 487 (RFC 3261 §16.7), and two UDP datagrams can swap
// places. sipgo ends the INVITE transaction on that 487 (WaitAnswer
// terminates it once the CANCEL completes), so the 2xx no longer matches a
// transaction and reaches the transaction layer's unhandled-response
// handler. The callee is now in a confirmed dialog that no one hangs up,
// so Hello must still ACK it, then BYE it (RFC 3261 §13.2.2.4, §15).
type lateForks struct {
	mu sync.Mutex
	m  map[string]*leg
}

// add keeps l, whose INVITE ended without a 2xx, for lateAnswerWindow.
func (f *lateForks) add(callID string, l *leg) {
	f.mu.Lock()
	if f.m == nil {
		f.m = map[string]*leg{}
	}
	f.m[callID] = l
	f.mu.Unlock()
	time.AfterFunc(lateAnswerWindow, func() {
		f.mu.Lock()
		if f.m[callID] == l {
			delete(f.m, callID)
		}
		f.mu.Unlock()
	})
}

// take removes and returns the fork a 2xx to INVITE belongs to: the same
// Call-ID, Hello's From tag and the CSeq of the fork's last INVITE. A
// 2xx that does not match is left alone.
func (f *lateForks) take(res *sip.Response) *leg {
	cid, from, cseq := res.CallID(), res.From(), res.CSeq()
	if cid == nil || from == nil || cseq == nil {
		return nil
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	l := f.m[cid.Value()]
	if l == nil {
		return nil
	}
	inv := l.session().InviteRequest
	tag, _ := from.Params.Get("tag")
	want, _ := inv.From().Params.Get("tag")
	if tag != want || cseq.SeqNo != inv.CSeq().SeqNo {
		return nil
	}
	delete(f.m, cid.Value())
	return l
}

// awaitLateAnswer keeps a fork whose INVITE ended without a 2xx, so a 2xx
// that crossed its CANCEL or final response is still ACKed and hung up.
func (s *Server) awaitLateAnswer(l *leg) {
	dcs := l.session()
	if dcs == nil || dcs.InviteRequest == nil {
		return
	}
	s.late.add(dcs.InviteRequest.CallID().Value(), l)
}

// endLateAnswer ACKs, then BYEs, a 2xx to INVITE that arrived after its
// fork's transaction had ended without one. It reports whether res was
// such a 2xx.
func (s *Server) endLateAnswer(res *sip.Response) bool {
	if !res.IsSuccess() || res.CSeq() == nil || res.CSeq().MethodName != sip.INVITE {
		return false
	}
	l := s.late.take(res)
	if l == nil {
		return false
	}
	l.mu.Lock()
	dcs := l.dcs
	// The fork's drive has returned: nothing else writes its session, and
	// a losing fork's dialog is never matched or read by the call.
	dcs.InviteResponse = res
	l.mu.Unlock()
	s.log.Info("2xx arrived after its fork ended: ACK and BYE", "call_id", res.CallID().Value(), "code", res.StatusCode)
	go func() {
		defer contain(s.log, "late 2xx")
		l.bye()
	}()
	return true
}
