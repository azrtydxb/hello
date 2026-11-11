package sip

import (
	"context"
	"errors"
	"time"

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
	ctx, cancel := s.untilShutdown(ctx)
	defer cancel()
	return requireResponse(s.client.Do(ctx, req))
}

// untilShutdown returns ctx, also cancelled when Serve begins to shut down.
// Serve closes the user agent after that, which ends the transactions then in
// flight, but sipgo leaves a transaction created on a closed layer waiting for
// its ctx: a NOTIFY or OPTIONS that was being set up as shutdown began (a poll
// tick that won its select against ctx.Done, a handler's goBG task) then
// held Serve's wait for background work for the full timeout of its ctx
// (byeTimeout, 35 s). Shutdown is signalled before the agent closes, so no
// request can outlive it.
func (s *Server) untilShutdown(ctx context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-s.done:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// doDigest answers a 401/407 challenge res to req. Like do it never returns
// (nil, nil).
func (s *Server) doDigest(ctx context.Context, req *sip.Request, res *sip.Response, auth sipgo.DigestAuth) (*sip.Response, error) {
	ctx, cancel := s.untilShutdown(ctx)
	defer cancel()
	return requireResponse(s.client.DoDigestAuth(ctx, req, res, auth))
}

// timeoutUntilShutdown is untilShutdown for a background request: a ctx of
// its own, bounded by d, for the BYE, CANCEL and INFO sends that run on
// goroutines Serve waits for (or that a call's teardown leaves behind).
func (s *Server) timeoutUntilShutdown(d time.Duration) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	ctx, stop := s.untilShutdown(ctx)
	return ctx, func() { stop(); cancel() }
}
