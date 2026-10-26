package sip

import (
	"context"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// holdCallerBye holds every caller BYE between its acceptance and the
// hangup, so the answer's "no ACK" error is seen first, as it is when a
// BYE overtakes its ACK and the race goes that way.
func holdCallerBye(pbx *testPBX) {
	hold := func() { time.Sleep(300 * time.Millisecond) }
	pbx.srv.callerByeHook.Store(&hold)
}

// byeWithoutAck dials ext from p, takes the 200 and answers it with a BYE,
// never sending the ACK.
func byeWithoutAck(t *testing.T, p *phone, ext string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	dcs, err := p.dua.WriteInvite(ctx, p.inviteReq(ext))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dcs.Close() })
	if err := dcs.WaitAnswer(ctx, sipgo.AnswerOptions{Username: p.user, Password: p.pass}); err != nil {
		t.Fatal(err)
	}
	inv := dcs.InviteRequest
	target := *inv.Recipient.Clone()
	if ct := dcs.InviteResponse.Contact(); ct != nil {
		target = *ct.Address.Clone()
	}
	bye := sip.NewRequest(sip.BYE, target)
	bye.AppendHeader(&sip.FromHeader{Address: *inv.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: tagOfTest(inv.From())}}})
	bye.AppendHeader(&sip.ToHeader{Address: *inv.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: toTagOf(dcs)}}})
	callID := sip.CallIDHeader(inv.CallID().Value())
	bye.AppendHeader(&callID)
	bye.AppendHeader(&sip.CSeqHeader{SeqNo: inv.CSeq().SeqNo + 1, MethodName: sip.BYE})
	bye.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	bye.SetTransport("UDP")
	if res := p.do(bye); res.StatusCode != sip.StatusOK {
		t.Fatalf("BYE before ACK: %d", res.StatusCode)
	}
}

// TestCallByeBeforeAck: a caller whose BYE reaches Hello before its ACK
// (UDP reordering, or a phone that hangs up at once) is recorded as the
// caller's hangup, not as a system failure for a missing ACK, and the
// callee's leg is still hung up.
func TestCallByeBeforeAck(t *testing.T) {
	pbx := startPBX(t, []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1")})
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	b.setCallee(answerAfter(nil))
	holdCallerBye(pbx)
	byeWithoutAck(t, a, "200")
	waitReq(t, b.byes, "BYE to the callee")
	cd := pbx.nextCDR(t)
	if cd.TerminationSide != cdr.SideCaller || cd.FinalStatus != sip.StatusOK {
		t.Fatalf("CDR = side %q status %d reason %q, want the caller's hangup", cd.TerminationSide, cd.FinalStatus, cd.FailureReason)
	}
	eventually(t, "no live calls", func() bool { return len(liveCalls(t, pbx)) == 0 })
}

// TestPageByeBeforeAck: the same race on a page ends it as the caller's
// hangup and still releases the members.
func TestPageByeBeforeAck(t *testing.T) {
	pbx := startPBX(t, pageDevices(), withPaging(pageGroup("normal"), pageExts()...))
	ph := registerAll(t, pbx, "a1", "b1")
	holdCallerBye(pbx)
	byeWithoutAck(t, ph["a1"], "*60")
	waitReq(t, ph["b1"].byes, "BYE to the member")
	cd := pbx.nextCDR(t)
	if cd.Kind != cdr.KindPage || cd.TerminationSide != cdr.SideCaller || cd.FinalStatus != sip.StatusOK {
		t.Fatalf("CDR = kind %q side %q status %d reason %q, want the caller's page hangup", cd.Kind, cd.TerminationSide, cd.FinalStatus, cd.FailureReason)
	}
	eventually(t, "no live calls", func() bool { return len(liveCalls(t, pbx)) == 0 })
}
