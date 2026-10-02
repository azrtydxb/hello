package sip

import (
	"fmt"
	"net/netip"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// trunkSource is source validation (S-6): the trunk whose source CIDRs or
// resolved destination addresses contain the request's source IP.
func (s *Server) trunkSource(req *sip.Request, snap *snapshot.Snapshot) (*routing.Trunk, bool) {
	ip, err := netip.ParseAddr(sourceIP(req))
	if err != nil {
		return nil, false
	}
	return snap.Routing().Router.TrunkForSource(ip.Unmap(), req.Recipient.User)
}

// looksLikePhone reports whether an INVITE without credentials claims to
// come from a device of this domain, so it gets a digest challenge rather
// than a 403: From in the SIP domain, or From naming a known device.
func (s *Server) looksLikePhone(req *sip.Request, snap *snapshot.Snapshot) bool {
	from := req.From().Address
	if strings.EqualFold(from.Host, s.cfg.Domain) {
		return true
	}
	_, ok := snap.DeviceByUsername(from.User)
	return ok
}

// inboundCall routes a call arriving from trunk t: the inbound routes pick
// an extension (Phase 1 ring-all), an external number (outbound routing as
// the trunk) or a SIP URI. Carriers are trusted by source address, not by
// digest (spec: out of scope).
func (s *Server) inboundCall(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, t *routing.Trunk) {
	c := s.newCall(req)
	from := req.From()
	c.direction, c.trunkName = cdr.DirectionInbound, t.Name
	c.callerNum, c.callerName = from.Address.User, from.DisplayName
	c.trace.Add(fmt.Sprintf("Source %s identifies trunk %s", sourceIP(req), t.Name))
	dec := s.decide(snap.Routing(), routing.Call{
		FromTrunk: t.ID, Number: req.Recipient.User, CallerID: from.Address.User,
		SIPDomain: req.Recipient.Host, Header: headerOf(req), At: time.Now(),
	})
	for _, st := range dec.Trace {
		c.trace.Add(st.Text) // renumbered after the source step
	}
	dec.Trace = nil
	if dec.CallerID != "" {
		c.callerNum = dec.CallerID // the route's normalised caller ID
	}
	s.dispatch(c, req, tx, snap, dec)
}
