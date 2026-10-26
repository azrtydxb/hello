package sip

import (
	"context"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// TestIVRByeBeforeAck: a caller whose BYE reaches the menu before its ACK
// (UDP reordering, or a phone that hangs up at once) is recorded as the
// caller's hangup, not as a system failure for a missing ACK.
func TestIVRByeBeforeAck(t *testing.T) {
	pbx, _ := ivrPBX(t, 5, ivrMenu("main", "800", nil))
	a, _, _ := registerABC(t, pbx)
	// Hold the BYE between its acceptance and the hangup, so the answer's
	// "no ACK" error is seen first, as it is when the race goes that way.
	hold := func() { time.Sleep(300 * time.Millisecond) }
	pbx.srv.callerByeHook.Store(&hold)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	req := a.inviteReq("800")
	dcs, err := a.dua.WriteInvite(ctx, req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = dcs.Close() }()
	if err := dcs.WaitAnswer(ctx, sipgo.AnswerOptions{Username: a.user, Password: a.pass}); err != nil {
		t.Fatal(err)
	}
	// The 200 is answered with a BYE and never ACKed.
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
	if res := a.do(bye); res.StatusCode != sip.StatusOK {
		t.Fatalf("BYE before ACK: %d", res.StatusCode)
	}
	rec := lastCDRFor(t, pbx, "800")
	if rec.TerminationSide != cdr.SideCaller {
		t.Fatalf("side %q status %d trace:\n%s", rec.TerminationSide, rec.FinalStatus, traceText(rec.Trace))
	}
	eventually(t, "no live calls", func() bool { return len(liveCalls(t, pbx)) == 0 })
}
