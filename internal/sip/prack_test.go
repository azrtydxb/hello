package sip

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func hdr(r interface{ GetHeader(string) sip.Header }, name string) string {
	if h := r.GetHeader(name); h != nil {
		return h.Value()
	}
	return ""
}

// reliableRinger rings reliably (Require: 100rel, RSeq 1) and answers only
// once the PRACK arrives, reporting it on seen. A PBX that never PRACKs makes
// it give up after 3 s, with nothing on seen.
func reliableRinger(seen chan<- *sip.Request) calleeFn {
	return func(p *phone, req *sip.Request, tx sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		_ = dss.Respond(180, "Ringing", nil, sip.NewHeader("Require", "100rel"), sip.NewHeader("RSeq", "1"))
		select {
		case pr := <-p.pracks:
			seen <- pr
		case <-time.After(3 * time.Second):
		}
		_ = dss.RespondSDP([]byte(p.sdp))
	}
}

// TestPrackToPhone fails if Hello does not offer 100rel, or does not answer a
// phone's reliable 180 with a PRACK whose RAck names its RSeq and the INVITE.
func TestPrackToPhone(t *testing.T) {
	pbx := startPBX(t, callerDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	seen := make(chan *sip.Request, 1)
	b.setCallee(reliableRinger(seen))
	r := waitCall(t, dial(t.Context(), a, "200"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	inv := waitReq(t, b.invites, "INVITE to the phone")
	if !listHas(inv.GetHeaders("Supported"), "100rel") {
		t.Fatalf("INVITE to the phone lacks Supported: 100rel: %v", inv.GetHeaders("Supported"))
	}
	select {
	case pr := <-seen:
		want := "1 " + strconv.Itoa(int(inv.CSeq().SeqNo)) + " INVITE"
		if got := hdr(pr, "RAck"); got != want {
			t.Fatalf("RAck = %q, want %q", got, want)
		}
		if pr.CSeq().SeqNo <= inv.CSeq().SeqNo || pr.To().Params == nil {
			t.Fatalf("PRACK is not in the early dialog: CSeq %d, To %s", pr.CSeq().SeqNo, pr.To().String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the phone's reliable 180 was never acknowledged with a PRACK")
	}
	// The ACK of the 2xx carries the INVITE's CSeq number, not the PRACK's
	// (RFC 3261 section 13.2.2.4): a strict phone drops it otherwise.
	ack := waitReq(t, b.acks, "ACK to the phone")
	if ack.CSeq().SeqNo != inv.CSeq().SeqNo {
		t.Fatalf("ACK CSeq = %d, want the INVITE's %d", ack.CSeq().SeqNo, inv.CSeq().SeqNo)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestPrackToTrunk fails if a carrier's reliable provisional response on a
// trunk leg is not acknowledged with a PRACK, or if the INVITE does not offer
// 100rel.
func TestPrackToTrunk(t *testing.T) {
	pbx, _, primary, _, a := twoCarriers(t, nil)
	primary.reliable.Store(true)
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	inv := waitReq(t, primary.invites, "INVITE to the carrier")
	if !listHas(inv.GetHeaders("Supported"), "100rel") {
		t.Fatalf("trunk INVITE lacks Supported: 100rel")
	}
	pr := waitReq(t, primary.pracks, "PRACK to the carrier")
	if got, want := hdr(pr, "RAck"), "7 "+strconv.Itoa(int(inv.CSeq().SeqNo))+" INVITE"; got != want {
		t.Fatalf("RAck = %q, want %q", got, want)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

type seenResp struct {
	code int
	rseq string
	req  string
	at   time.Time
}

// reliableCaller dials ext with Require: 100rel and acknowledges the first
// reliable provisional response with a PRACK after delay. It returns what it
// heard, when the PRACK went out and when the 200 arrived.
func reliableCaller(t *testing.T, p *phone, ext string, delay time.Duration) (heard []seenResp, prackAt, answerAt time.Time, err error) {
	t.Helper()
	req := p.inviteReq(ext)
	req.AppendHeader(sip.NewHeader("Supported", "100rel"))
	req.AppendHeader(sip.NewHeader("Require", "100rel"))
	dcs, err := p.dua.WriteInvite(t.Context(), req)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	var mu sync.Mutex
	prackSent := false
	err = dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{Username: p.user, Password: p.pass, OnResponse: func(r *sip.Response) error {
		mu.Lock()
		defer mu.Unlock()
		heard = append(heard, seenResp{code: r.StatusCode, rseq: hdr(r, "RSeq"), req: hdr(r, "Require"), at: time.Now()})
		if r.StatusCode == 180 && !prackSent {
			prackSent = true
			rseq, cseq := hdr(r, "RSeq"), r.CSeq().SeqNo
			go func() {
				time.Sleep(delay)
				pr := sip.NewRequest(sip.PRACK, req.Recipient)
				pr.AppendHeader(sip.NewHeader("RAck", rseq+" "+strconv.Itoa(int(cseq))+" INVITE"))
				pr.AppendHeader(sip.NewHeader("Route", "<sip:"+p.pbx+";lr>"))
				mu.Lock()
				prackAt = time.Now()
				mu.Unlock()
				if ptx, e := dcs.TransactionRequest(t.Context(), pr); e == nil {
					select {
					case <-ptx.Responses():
					case <-time.After(2 * time.Second):
					}
					ptx.Terminate()
				}
			}()
		}
		return nil
	}})
	mu.Lock()
	defer mu.Unlock()
	answerAt = time.Now()
	if err == nil {
		_ = dcs.Ack(t.Context())
		defer hangup(t, dcs)
	}
	return heard, prackAt, answerAt, err
}

// TestPrackFromCaller fails if a caller that says Require: 100rel gets a
// plain 180, if the 180 is not retransmitted until the PRACK arrives, if the
// PRACK is refused, or if the 200 overtakes the PRACK.
func TestPrackFromCaller(t *testing.T) {
	pbx := startPBX(t, callerDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	// b rings and answers while the PRACK is still outstanding: Hello must
	// hold the 200 for it.
	b.setCallee(func(p *phone, _ *sip.Request, _ sip.ServerTransaction, dss *sipgo.DialogServerSession) {
		_ = dss.Respond(180, "Ringing", nil)
		time.Sleep(200 * time.Millisecond)
		_ = dss.RespondSDP([]byte(p.sdp))
	})
	heard, prackAt, answerAt, err := reliableCaller(t, a, "200", 1200*time.Millisecond)
	if err != nil {
		t.Fatalf("call: %v", err)
	}
	var rings []seenResp
	for _, h := range heard {
		if h.code == 180 {
			rings = append(rings, h)
		}
	}
	if len(rings) == 0 {
		t.Fatalf("no 180 heard: %+v", heard)
	}
	if !strings.EqualFold(rings[0].req, "100rel") || rings[0].rseq == "" {
		t.Fatalf("180 is not reliable: Require %q RSeq %q", rings[0].req, rings[0].rseq)
	}
	if len(rings) < 2 || rings[1].rseq != rings[0].rseq {
		t.Fatalf("180 was not retransmitted with the same RSeq while unacknowledged: %+v", rings)
	}
	if !answerAt.After(prackAt) {
		t.Fatalf("the 200 (%s) arrived before the PRACK (%s)", answerAt, prackAt)
	}
	pbx.nextCDR(t)
}

// TestNoPrackWithoutRequire fails if a caller that does not require 100rel
// gets a reliable provisional response.
func TestNoPrackWithoutRequire(t *testing.T) {
	pbx := startPBX(t, callerDevices())
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	done := make(chan struct{})
	close(done)
	b.setCallee(answerAfter(done))
	req := a.inviteReq("200")
	req.AppendHeader(sip.NewHeader("Supported", "100rel"))
	var rseq, require string
	dcs, err := a.dua.WriteInvite(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	err = dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{Username: a.user, Password: a.pass, OnResponse: func(r *sip.Response) error {
		if r.StatusCode == 180 {
			rseq, require = hdr(r, "RSeq"), hdr(r, "Require")
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	_ = dcs.Ack(t.Context())
	if rseq != "" || require != "" {
		t.Fatalf("180 to a caller that only supports 100rel is reliable: Require %q RSeq %q", require, rseq)
	}
	hangup(t, dcs)
	pbx.nextCDR(t)
}
