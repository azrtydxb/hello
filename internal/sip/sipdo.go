package sip

import (
	"context"
	"errors"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// errNoResponse reports that a client transaction ended without a final
// response and without an error.
var errNoResponse = errors.New("sip: transaction ended without a response")

// requireResponse turns sipgo's (nil, nil) into an error, so a caller that
// checks only err can never dereference a nil *sip.Response.
//
// sipgo v1.6.0 Client.Do (client.go), DialogClientSession.Do and
// DialogServerSession.Do all end with
//
//	case <-tx.Done():
//		return nil, tx.Err()
//
// tx.Err() returns the transaction FSM error, which is nil when the
// transaction ended without failing (terminated locally, e.g. while the
// server shuts down), so Do returns (nil, nil).
func requireResponse(res *sip.Response, err error) (*sip.Response, error) {
	if err == nil && res == nil {
		return nil, errNoResponse
	}
	return res, err
}

// do sends req on the shared client and waits for its final response. Unlike
// sipgo's Client.Do it never returns (nil, nil).
func (s *Server) do(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	return requireResponse(s.client.Do(ctx, req))
}

// doDigest answers a 401/407 challenge res to req. Like do it never returns
// (nil, nil).
func (s *Server) doDigest(ctx context.Context, req *sip.Request, res *sip.Response, auth sipgo.DigestAuth) (*sip.Response, error) {
	return requireResponse(s.client.DoDigestAuth(ctx, req, res, auth))
}
