package sip

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Edge routing (RFC 3327 Path, RFC 5626-style flow tokens).
//
// A phone behind NAT is only reachable over the flow it registered on, i.e.
// from the node that handled its REGISTER. The registrar stores a Path URI
// <sip:{advertised};lr;hflow={token}> naming that node and the flow. Another
// node forking to the binding sends the INVITE with that URI as its Route;
// the registering node, seeing a Route with hflow, acts as an edge proxy:
// it pops the Route and relays the request over the flow (or, for requests
// coming from the phone, onwards along the route), record-routing itself on
// the initial INVITE so in-dialog requests take the same path.

const flowMACLen = 16

// flowToken signs a flow (source address and transport) so only a node with
// the cluster's secret can mint one; a forged token would otherwise turn a
// node into an open relay.
func (s *Server) flowToken(source, transport string) string {
	payload := source + "|" + transport
	raw := append([]byte(payload+"|"), s.flowMAC(payload)...)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func (s *Server) flowMAC(payload string) []byte {
	m := hmac.New(sha256.New, s.cfg.NonceSecret)
	m.Write([]byte("hello-flow\x00")) // domain-separate from nonces
	m.Write([]byte(payload))
	return m.Sum(nil)[:flowMACLen]
}

// parseFlowToken returns the flow a token names, if its signature is valid.
func (s *Server) parseFlowToken(tok string) (source, transport string, ok bool) {
	raw, err := base64.RawURLEncoding.DecodeString(tok)
	if err != nil || len(raw) < flowMACLen+4 || raw[len(raw)-flowMACLen-1] != '|' {
		return "", "", false
	}
	payload, mac := raw[:len(raw)-flowMACLen-1], raw[len(raw)-flowMACLen:]
	if !hmac.Equal(mac, s.flowMAC(string(payload))) {
		return "", "", false
	}
	source, transport, found := strings.Cut(string(payload), "|")
	if !found || bytes.ContainsRune(payload, 0) {
		return "", "", false
	}
	if _, _, err := net.SplitHostPort(source); err != nil {
		return "", "", false
	}
	return source, transport, true
}

// pathURI is the Path (and Record-Route) value for a flow through this node.
func (s *Server) pathURI(token string) string {
	return "<sip:" + s.cfg.AdvertisedAddr + ";lr;hflow=" + token + ">"
}

// edgeToken returns the hflow token of req's top Route, if it has one.
func edgeToken(req *sip.Request) (string, bool) {
	r := req.Route()
	if r == nil {
		return "", false
	}
	return r.Address.UriParams.Get("hflow")
}

// hostPort is a URI's host:port, defaulting the port for its transport.
func hostPort(u sip.Uri) string {
	port := u.Port
	if port == 0 {
		port = sip.DefaultPort("udp")
	}
	return net.JoinHostPort(u.Host, strconv.Itoa(port))
}

// proxy relays a request whose top Route carries a flow token.
func (s *Server) proxy(req *sip.Request, tx sip.ServerTransaction, token string) {
	flow, _, ok := s.parseFlowToken(token)
	if !ok {
		s.log.Warn("edge: invalid flow token", "method", req.Method.String(), "source", req.Source())
		if !req.IsAck() {
			s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		}
		return
	}
	out := req.Clone()
	out.RemoveHeader("Route") // the top one: ours
	if mf := out.MaxForwards(); mf != nil {
		if mf.Val() <= 1 {
			if !req.IsAck() {
				s.respond(tx, req, sip.StatusTooManyHops, "Too Many Hops")
			}
			return
		}
		mf.Dec()
	}
	fromFlow := req.Source() == flow
	dest := flow
	if fromFlow {
		// From the phone towards the cluster: next Route, else Request-URI.
		if r := out.Route(); r != nil {
			dest = hostPort(r.Address)
		} else {
			dest = hostPort(out.Recipient)
		}
	}
	if req.IsInvite() && !isInDialog(req) {
		out.PrependHeader(sip.NewHeader("Record-Route", s.pathURI(token)))
	}
	out.SetDestination(dest)
	s.laddr.Copy(&out.Laddr)

	if req.IsAck() {
		// ACK for a 2xx is its own transaction: forward statelessly.
		if err := s.client.WriteRequest(out, sipgo.ClientRequestAddVia); err != nil {
			s.log.Debug("edge: ACK forward failed", "error", err)
		}
		return
	}
	s.proxyStateful(req, tx, out)
}

// proxyStateful forwards out on a client transaction and relays its
// responses back on tx. For an INVITE it relays CANCEL downstream, and a 2xx
// that can no longer be relayed (the caller cancelled) is ACKed and ended
// here so the phone does not keep a dangling dialog.
func (s *Server) proxyStateful(req *sip.Request, tx sip.ServerTransaction, out *sip.Request) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	ctx2, err := s.client.TransactionRequest(ctx, out, sipgo.ClientRequestAddVia)
	cancel()
	if err != nil {
		s.log.Debug("edge: forward failed", "method", req.Method.String(), "error", err)
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable")
		return
	}
	isInvite := req.IsInvite()
	if isInvite {
		cancelReq := s.cancelFor(out)
		if !tx.OnCancel(func(*sip.Request) {
			s.m.request(sip.CANCEL.String())
			go s.sendCancel(cancelReq)
		}) {
			go s.sendCancel(cancelReq)
		}
		ctx2.OnRetransmission(func(r *sip.Response) {
			if r.IsSuccess() {
				s.relayResponse(tx, req, r)
			}
		})
	}
	for {
		select {
		case res := <-ctx2.Responses():
			if res.StatusCode == sip.StatusTrying {
				continue // hop by hop; the transaction layer sent our own
			}
			if !s.relayResponse(tx, req, res) && isInvite && res.IsSuccess() {
				go s.endOrphan(out, res)
			}
			if !res.IsProvisional() {
				return
			}
		case <-ctx2.Done():
			s.respond(tx, req, sip.StatusRequestTimeout, "Request Timeout")
			return
		}
	}
}

// relayResponse sends a downstream response upstream without our Via; false
// means the server transaction no longer accepts it.
func (s *Server) relayResponse(tx sip.ServerTransaction, req *sip.Request, res *sip.Response) bool {
	up := sip.CopyResponse(res)
	up.RemoveHeader("Via")
	up.SetDestination(req.Source())
	if err := tx.Respond(up); err != nil {
		s.log.Debug("edge: response relay failed", "code", res.StatusCode, "error", err)
		return false
	}
	s.m.response(res.StatusCode)
	return true
}

// cancelFor builds the CANCEL for a forwarded INVITE (same top Via).
func (s *Server) cancelFor(inv *sip.Request) *sip.Request {
	c := sip.NewRequest(sip.CANCEL, *inv.Recipient.Clone())
	c.AppendHeader(sip.HeaderClone(inv.Via()))
	mf := sip.MaxForwardsHeader(70)
	c.AppendHeader(&mf)
	c.AppendHeader(sip.HeaderClone(inv.From()))
	c.AppendHeader(sip.HeaderClone(inv.To()))
	c.AppendHeader(sip.HeaderClone(inv.CallID()))
	c.AppendHeader(&sip.CSeqHeader{SeqNo: inv.CSeq().SeqNo, MethodName: sip.CANCEL})
	c.SetBody(nil)
	c.SetDestination(inv.Destination())
	s.laddr.Copy(&c.Laddr)
	return c
}

func (s *Server) sendCancel(c *sip.Request) {
	defer contain(s.log, "edge cancel")
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	tx, err := s.client.TransactionRequest(ctx, c, func(*sipgo.Client, *sip.Request) error { return nil })
	if err != nil {
		s.log.Debug("edge: CANCEL failed", "error", err)
		return
	}
	defer tx.Terminate()
	select {
	case <-tx.Responses():
	case <-tx.Done():
	case <-ctx.Done():
	}
}

// endOrphan ACKs and BYEs a 2xx the caller can no longer receive.
func (s *Server) endOrphan(inv *sip.Request, res *sip.Response) {
	defer contain(s.log, "edge orphan")
	target := *inv.Recipient.Clone()
	if c := res.Contact(); c != nil {
		target = *c.Address.Clone()
	}
	build := func(m sip.RequestMethod, seq uint32) *sip.Request {
		r := sip.NewRequest(m, target)
		mf := sip.MaxForwardsHeader(70)
		r.AppendHeader(&mf)
		r.AppendHeader(sip.HeaderClone(inv.From()))
		r.AppendHeader(sip.HeaderClone(res.To()))
		r.AppendHeader(sip.HeaderClone(inv.CallID()))
		r.AppendHeader(&sip.CSeqHeader{SeqNo: seq, MethodName: m})
		r.AppendHeader(sip.HeaderClone(&s.contact))
		r.SetBody(nil)
		r.SetDestination(inv.Destination())
		s.laddr.Copy(&r.Laddr)
		return r
	}
	seq := inv.CSeq().SeqNo
	if err := s.client.WriteRequest(build(sip.ACK, seq), sipgo.ClientRequestAddVia); err != nil {
		s.log.Debug("edge: orphan ACK failed", "error", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	tx, err := s.client.TransactionRequest(ctx, build(sip.BYE, seq+1), sipgo.ClientRequestAddVia)
	if err != nil {
		return
	}
	defer tx.Terminate()
	select {
	case <-tx.Responses():
	case <-tx.Done():
	case <-ctx.Done():
	}
}
