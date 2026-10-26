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
	ip, err := netip.ParseAddr(s.clientIP(req))
	if err != nil {
		return nil, false
	}
	return snap.Routing().Router.TrunkForSource(ip, req.Recipient.User) // the engine unmaps IPv4-in-IPv6
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

// trunkNameCallerID reports whether cid is the caller-ID ladder's last rung
// — the source trunk's name in angle brackets (spec S-4) — returning it for
// presentation. On a phone leg it is a display name, never a URI user part:
// "<" and ">" are not user-part characters and would break the URI.
func trunkNameCallerID(cid string) (string, bool) {
	if len(cid) > 2 && cid[0] == '<' && cid[len(cid)-1] == '>' {
		return cid, true
	}
	return "", false
}

// privacyID reports whether the request asks for identity privacy
// (Privacy: id, RFC 3323): its caller is withheld whatever the From says.
func privacyID(req *sip.Request) bool {
	for _, h := range req.GetHeaders("Privacy") {
		for _, tok := range strings.FieldsFunc(h.Value(), func(r rune) bool { return r == ';' || r == ',' || r == ' ' }) {
			if strings.EqualFold(tok, "id") {
				return true
			}
		}
	}
	return false
}

// rejectScreened turns a screened call away with the trunk's reject code,
// or plays the matching entry's announcement first. The call is never
// forked or rung, and its CDR is marked blocked.
func (s *Server) rejectScreened(c *call, req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, v routing.Verdict) {
	c.trace.Add(v.Trace)
	s.m.BlockedCalls.WithLabelValues(v.Reason).Inc()
	c.mu.Lock()
	c.blocked = v.Reason
	c.mu.Unlock()
	if v.Entry != nil && v.Entry.Announcement != "" {
		c.trace.Add("Playing the blocked-call announcement")
		s.dispatch(c, req, tx, snap, routing.Decision{
			Kind: routing.KindInbound, DestKind: "announcement", DestTarget: v.Entry.Announcement,
		})
		return
	}
	s.respond(tx, req, v.Code, statusText(v.Code))
	c.record(v.Code, cdr.SideSystem, "blocked: "+v.Reason, ResultBlocked)
}

// inboundCall routes a call arriving from trunk t: the inbound routes pick
// an extension (Phase 1 ring-all), an external number (outbound routing as
// the trunk) or a SIP URI. Carriers are trusted by source address, not by
// digest (spec: out of scope).
func (s *Server) inboundCall(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, t *routing.Trunk) {
	c := s.newCall(req)
	from := req.From()
	c.direction, c.trunkName = cdr.DirectionInbound, t.Name
	if isPageOrIntercomCode(snap, req.Recipient.User) {
		// Auto-answer turns a phone into a listening device: only an
		// authenticated internal caller may use it (spec P-16).
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		c.record(sip.StatusForbidden, cdr.SideSystem, "page or intercom code dialled from a trunk", ResultFailed)
		return
	}
	c.callerNum, c.callerName = from.Address.User, from.DisplayName
	c.trace.Add(fmt.Sprintf("Source %s identifies trunk %s", s.clientIP(req), t.Name))
	// Screening comes before anything is taken or rung: a screened call
	// uses no trunk slot and reaches no phone (pbx-gaps S-7, S-8).
	if v := snap.Routing().Blocklist.Check(t, from.Address.User, privacyID(req), s.cfg.Dialing); v.Blocked {
		s.rejectScreened(c, req, tx, snap, v)
		return
	} else if v.Trace != "" {
		c.trace.Add(v.Trace)
	}
	// An inbound call counts against its source trunk's max_calls too.
	if ok, why := c.acquireSlot(t, false, c.id+":in"); !ok {
		if why == "full" {
			c.trace.Add(fmt.Sprintf("Trunk %s is at capacity", t.Name))
			s.m.TrunkCalls.WithLabelValues(t.Name, TrunkFull).Inc()
		} else {
			c.trace.Add(fmt.Sprintf("Trunk %s skipped: %s", t.Name, why))
		}
		s.respond(tx, req, sip.StatusServiceUnavailable, "Service Unavailable")
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "source trunk "+why, ResultUnavailable)
		return
	}
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
