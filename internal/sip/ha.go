package sip

import (
	"net"
	"net/netip"
	"strings"

	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/emiago/sipgo/sip"
)

// Lifecycle is the node's state as the SIP node needs it;
// *lifecycle.Machine satisfies it.
type Lifecycle interface {
	State() (cluster.State, string)
}

// clientHeader carries a phone's real source from a trusted edge proxy
// (Kamailio) after its NAT fix-up (plan contract 7).
const clientHeader = "X-Hello-Client"

// fromTrustedProxy reports whether req's datagram came from a configured
// trusted proxy (HELLO_SIP_TRUSTED_PROXIES).
func (s *Server) fromTrustedProxy(req *sip.Request) bool {
	if len(s.cfg.TrustedProxies) == 0 {
		return false
	}
	host, _, err := net.SplitHostPort(req.Source())
	if err != nil {
		return false
	}
	ip, err := netip.ParseAddr(host)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	for _, p := range s.cfg.TrustedProxies {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// clientSource is the request's client address: X-Hello-Client when the
// datagram comes from a trusted proxy, otherwise the datagram's source.
// An X-Hello-Client from anywhere else is ignored.
func (s *Server) clientSource(req *sip.Request) string {
	if s.fromTrustedProxy(req) {
		if h := req.GetHeader(clientHeader); h != nil {
			if ap, err := netip.ParseAddrPort(strings.TrimSpace(h.Value())); err == nil {
				return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port()).String()
			}
		}
	}
	return req.Source()
}

// clientIP is clientSource without the port: the key of the failed-auth
// throttle, the digest nonce binding and trunk source validation.
func (s *Server) clientIP(req *sip.Request) string {
	src := s.clientSource(req)
	host, _, err := net.SplitHostPort(src)
	if err != nil {
		return src
	}
	return host
}

// state is the node's lifecycle state (READY when no lifecycle is wired).
func (s *Server) state() (cluster.State, string) {
	if s.deps.Lifecycle == nil {
		return cluster.Ready, ""
	}
	return s.deps.Lifecycle.State()
}

// refuseIfNotReady answers 503 with Retry-After: 5 to a new initial INVITE
// or REGISTER unless the node is READY (JOINING, UNHEALTHY and DRAINING
// nodes take no new work), so the phone or the balancer moves it to another
// node (plan contract 6). In-dialog requests, CANCEL and ACK never come
// here.
func (s *Server) refuseIfNotReady(req *sip.Request, tx sip.ServerTransaction) bool {
	if st, _ := s.state(); st == cluster.Ready {
		return false
	}
	s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable", sip.NewHeader("Retry-After", "5"))
	return true
}

// isSelfProbe reports whether an out-of-dialog OPTIONS (no To tag) is the
// balancer's health probe (plan contract 5): its Request-URI names this
// node's advertised host:port, or has no user part and is not the SIP
// domain (Kamailio probes sip:hello-sip-N:5060, whatever the advertised
// address is). A phone's keepalive to the domain is not a probe.
func (s *Server) isSelfProbe(req *sip.Request) bool {
	if _, ok := req.To().Params.Get("tag"); ok {
		return false
	}
	if req.Recipient.User == "" && !strings.EqualFold(req.Recipient.Host, s.cfg.Domain) {
		return true
	}
	port := req.Recipient.Port
	if port == 0 {
		port = sip.DefaultPort("udp")
	}
	return strings.EqualFold(req.Recipient.Host, s.advHost) && port == s.advPort
}

// Drain makes the node stop taking trunk work: its trunk holders stop
// (unregistering) and release their leases at once, so another node takes
// over registration and health checks. Undrain resumes it.
func (s *Server) Drain() { s.trunks.setDraining(true) }

// Undrain lets the node take trunk leases again after a cancelled drain.
func (s *Server) Undrain() { s.trunks.setDraining(false) }

// HangupAll ends every call this node owns: connected calls get BYE on
// both legs, calls still ringing are cancelled with 503; each CDR records
// side system and reason (e.g. "drain timeout").
func (s *Server) HangupAll(reason string) {
	s.mu.Lock()
	calls := make([]*call, 0, len(s.calls))
	for c := range s.calls {
		calls = append(calls, c)
	}
	s.mu.Unlock()
	for _, c := range calls {
		c.terminate(reason)
	}
}

// terminate ends the call for a system reason. A connected call gets BYE
// on both legs; one still being set up is aborted through its setup
// goroutine (the only one that may answer the caller), and if it connects
// meanwhile it is then ended as connected.
func (c *call) terminate(reason string) {
	c.abort(reason)
	c.mu.Lock()
	connected := c.connected
	c.mu.Unlock()
	if connected {
		c.endBoth(reason)
	}
}
