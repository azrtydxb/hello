package sip

import (
	"strings"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/emiago/sipgo/sip"
)

// transportHeader carries the transport a phone's request arrived on at the
// edge proxy (udp, tcp or tls), set by Kamailio from its received protocol
// and honoured only from a trusted proxy, like X-Hello-Client. The edge is
// where TLS ends: hello-sip itself speaks UDP to Kamailio, so this header is
// the only way it learns that the phone leg was TLS.
const transportHeader = "X-Hello-Transport"

// Phone-leg transports.
const (
	transportUDP = "udp"
	transportTCP = "tcp"
	transportTLS = "tls"
)

// normTransport maps a stored or received transport to udp, tcp or tls
// (anything unknown, including a binding written before this field was
// real, is udp).
func normTransport(t string) string {
	switch strings.ToLower(strings.TrimSpace(t)) {
	case transportTCP:
		return transportTCP
	case transportTLS, "sips":
		return transportTLS
	}
	return transportUDP
}

// clientTransport is the transport of the phone leg of req: X-Hello-Transport
// when the datagram comes from a trusted proxy, otherwise udp (hello-sip
// listens on UDP only; a request that reached it directly was not TLS).
func (s *Server) clientTransport(req *sip.Request) string {
	if s.fromTrustedProxy(req) {
		if h := req.GetHeader(transportHeader); h != nil {
			return normTransport(h.Value())
		}
	}
	return transportUDP
}

// isSIPS reports whether the Request-URI asks for a secure path (RFC 3261
// 26.2.2): the sips scheme, every hop of which must be TLS.
func isSIPS(req *sip.Request) bool {
	return strings.EqualFold(req.Recipient.Scheme, "sips")
}

// bindingOnTLS reports whether a binding's phone registered over TLS, i.e.
// whether a request sent to it leaves Kamailio over a TLS connection. The
// SRTP phase offers crypto only on such legs.
func bindingOnTLS(b livestate.Binding) bool { return normTransport(b.Transport) == transportTLS }

// secureTargets keeps the bindings reachable over TLS.
func secureTargets(bs []livestate.Binding) []livestate.Binding {
	var out []livestate.Binding
	for _, b := range bs {
		if bindingOnTLS(b) {
			out = append(out, b)
		}
	}
	return out
}

// Reasons for hello_sip_sips_refused_total.
const (
	secureCallerNotTLS = "caller_not_tls"
	secureNoTLSBinding = "no_tls_binding"
	secureNoTLSRoute   = "no_tls_route"
)

// refuseSecure answers a sips: request that has no TLS path with 480
// (Temporarily Unavailable) and reason, logging neither headers nor bodies.
func (s *Server) refuseSecure(req *sip.Request, tx sip.ServerTransaction, reason, why string) {
	s.m.SecureRefused.WithLabelValues(reason).Inc()
	s.logInviteReject(req, "", sip.StatusTemporarilyUnavailable, why)
	s.respond(tx, req, sip.StatusTemporarilyUnavailable, "Secure Transport Required")
}
