package sip

import (
	"context"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// reasonElsewhere is the Reason (RFC 3326) on the CANCEL of a ringing leg
// whose call was answered somewhere else, here by a pickup: phones show
// "answered elsewhere" instead of a missed call.
const reasonElsewhere = `SIP;cause=200;text="Call completed elsewhere"`

// elsewhereRequester sends requests through the transaction layer as the
// default client does, adding reasonElsewhere to a CANCEL that has no
// Reason. sipgo builds a leg's CANCEL itself when the leg's context is
// cancelled (DialogClientSession.inviteCancel), so a leg cancelled because
// of a pickup is switched to a dialog UA whose client uses this requester.
type elsewhereRequester struct {
	ua *sipgo.UserAgent
}

func (r elsewhereRequester) Request(ctx context.Context, req *sip.Request) (sip.ClientTransaction, error) {
	if req.IsAck() {
		// A late 2xx on a cancelled leg is ACKed and hung up through the
		// same dialog: an ACK is no transaction (Client.WriteRequest).
		return nil, r.ua.TransportLayer().WriteMsg(req)
	}
	if req.IsCancel() && req.GetHeader("Reason") == nil {
		req.AppendHeader(sip.NewHeader("Reason", reasonElsewhere))
	}
	return r.ua.TransactionLayer().Request(ctx, req)
}

// newElsewhereUA is a copy of the fork dialog UA whose CANCELs carry
// reasonElsewhere. Only the requester differs: the client keeps its
// address, NAT and connection settings.
func newElsewhereUA(ua *sipgo.UserAgent, client *sipgo.Client, base *sipgo.DialogUA) *sipgo.DialogUA {
	cl := *client
	cl.TxRequester = elsewhereRequester{ua: ua}
	d := *base
	d.Client = &cl
	return &d
}

// cancelForksElsewhere cancels every ringing leg as cancelForks does, with
// the CANCELs saying the call was completed elsewhere. A leg whose INVITE
// is still being sent switches when its dialog exists (drive).
func (c *call) cancelForksElsewhere() {
	c.mu.Lock()
	legs := c.legs
	c.mu.Unlock()
	for _, l := range legs {
		l.mu.Lock()
		l.elsewhere = true
		if l.dcs != nil {
			l.dcs.UA = c.s.uacElsewhere
		}
		l.mu.Unlock()
		l.cancel()
	}
}
