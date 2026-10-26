package sip

import (
	"context"
	"testing"

	"github.com/emiago/sipgo/sip"
)

// endedTx is a client transaction that ends without a response and without
// an error: the state in which sipgo's Client.Do returns (nil, nil).
type endedTx struct{ done chan struct{} }

func newEndedTx() *endedTx {
	t := &endedTx{done: make(chan struct{})}
	close(t.done)
	return t
}

func (t *endedTx) Terminate()                             {}
func (t *endedTx) OnTerminate(sip.FnTxTerminate) bool     { return false }
func (t *endedTx) Done() <-chan struct{}                  { return t.done }
func (t *endedTx) Err() error                             { return nil }
func (t *endedTx) Responses() <-chan *sip.Response        { return nil }
func (t *endedTx) OnRetransmission(sip.FnTxResponse) bool { return false }

type endedRequester struct{ calls int }

func (r *endedRequester) Request(context.Context, *sip.Request) (sip.ClientTransaction, error) {
	r.calls++
	return newEndedTx(), nil
}

// TestClientTransactionWithoutResponse fails (with a nil pointer panic in
// notifyReferDialog) if a transfer NOTIFY whose transaction ends without a
// response is not handled as a send failure, or if Server.do returns
// (nil, nil).
func TestClientTransactionWithoutResponse(t *testing.T) {
	pbx := startPBX(t, callerDevices())
	rq := &endedRequester{}
	pbx.srv.client.TxRequester = rq

	req := sip.NewRequest(sip.OPTIONS, sip.Uri{Host: "127.0.0.1", Port: 5099})
	if res, err := pbx.srv.do(context.Background(), req); err == nil || res != nil {
		t.Fatalf("do = (%v, %v), want (nil, error)", res, err)
	}

	inv := sip.NewRequest(sip.INVITE, sip.Uri{User: "100", Host: "127.0.0.1", Port: 5099})
	from := &sip.FromHeader{Address: sip.Uri{User: "100", Host: "127.0.0.1"}, Params: sip.HeaderParams{{K: "tag", V: "ft"}}}
	to := &sip.ToHeader{Address: sip.Uri{User: "200", Host: "127.0.0.1"}}
	callID := sip.CallIDHeader("regress-nil-response")
	inv.AppendHeader(from)
	inv.AppendHeader(to)
	inv.AppendHeader(&callID)
	inv.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.INVITE})
	res := sip.NewResponseFromRequest(inv, sip.StatusOK, "OK", nil)

	c := &call{s: pbx.srv}
	c.notifyReferDialog(inv, res, "SIP/2.0 200 OK", true, true) // must not panic
	if rq.calls < 2 {
		t.Fatalf("requester called %d times, want the NOTIFY to have been attempted", rq.calls)
	}
}
