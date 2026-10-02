package sip

import (
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// defaultExpires applies when neither the Contact nor the request carries
// an expiry (RFC 3261 §10.2.1.1).
const defaultExpires = 3600

type contactReq struct {
	uri     string
	expires int
}

// handleRegister authenticates the device, applies its contacts to the AOR
// and answers with every current binding (RFC 3261 §10.3).
func (s *Server) handleRegister(req *sip.Request, tx sip.ServerTransaction) {
	dev, _, ok := s.authenticate(req, tx)
	if !ok {
		return
	}
	if req.To().Address.User != dev.Username {
		// A device may only register its own AOR.
		s.respond(tx, req, sip.StatusForbidden, "Forbidden")
		return
	}
	aor := s.aor(dev.Username)
	contacts, wildcard, code, reason := s.parseContacts(req)
	if code != 0 {
		var hdrs []sip.Header
		if code == sip.StatusIntervalToBrief {
			hdrs = append(hdrs, sip.NewHeader("Min-Expires", strconv.Itoa(int(s.cfg.MinExpires/time.Second))))
		}
		s.respond(tx, req, code, reason, hdrs...)
		return
	}
	if wildcard {
		ctx, cancel := s.stateCtx()
		err := s.deps.State.DeleteAOR(ctx, aor)
		cancel()
		if err != nil {
			s.stateDown(tx, req, "delete_aor", err)
			return
		}
		s.triggerRecount()
		s.respond(tx, req, sip.StatusOK, "OK")
		return
	}
	if over, err := s.tooManyContacts(aor, contacts); err != nil {
		s.stateDown(tx, req, "bindings", err)
		return
	} else if over {
		s.respond(tx, req, sip.StatusForbidden, "Too Many Contacts")
		return
	}
	if err := s.applyContacts(req, dev, aor, contacts); err != nil {
		s.stateDown(tx, req, "put_binding", err)
		return
	}
	ctx, cancel := s.stateCtx()
	current, err := s.deps.State.Bindings(ctx, aor)
	cancel()
	if err != nil {
		s.stateDown(tx, req, "bindings", err)
		return
	}
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
	for _, b := range current {
		var u sip.Uri
		if err := sip.ParseUri(b.ContactURI, &u); err != nil {
			continue
		}
		left := max(int(time.Until(b.Expires).Round(time.Second)/time.Second), 1)
		res.AppendHeader(&sip.ContactHeader{Address: u, Params: sip.HeaderParams{{K: "expires", V: strconv.Itoa(left)}}})
	}
	res.AppendHeader(sip.NewHeader("Date", time.Now().UTC().Format(time.RFC1123)))
	s.send(tx, res)
	if len(contacts) > 0 {
		s.triggerRecount()
	}
}

// parseContacts reads the Contact headers and their expiry, clamped to the
// configured maximum. A non-zero code is the error response to send (400 or
// 423); wildcard reports a valid "Contact: *" with "Expires: 0".
func (s *Server) parseContacts(req *sip.Request) (contacts []contactReq, wildcard bool, code int, reason string) {
	reqExpires := -1
	if h := req.GetHeader("Expires"); h != nil {
		n, err := strconv.Atoi(strings.TrimSpace(h.Value()))
		if err != nil || n < 0 {
			return nil, false, sip.StatusBadRequest, "Bad Expires"
		}
		reqExpires = n
	}
	for _, h := range req.GetHeaders("Contact") {
		c, ok := h.(*sip.ContactHeader)
		if !ok {
			return nil, false, sip.StatusBadRequest, "Bad Contact"
		}
		if c.Address.Wildcard {
			wildcard = true
			continue
		}
		exp := reqExpires
		if v, ok := c.Params.Get("expires"); ok {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return nil, false, sip.StatusBadRequest, "Bad Contact expires"
			}
			exp = n
		}
		if exp < 0 {
			exp = defaultExpires
		}
		contacts = append(contacts, contactReq{uri: c.Address.String(), expires: exp})
	}
	if wildcard && (len(contacts) > 0 || reqExpires != 0) {
		return nil, false, sip.StatusBadRequest, "Contact * requires Expires: 0 and no other Contact"
	}
	minExp, maxExp := int(s.cfg.MinExpires/time.Second), int(s.cfg.MaxExpires/time.Second)
	for i, c := range contacts {
		if c.expires > 0 && c.expires < minExp {
			return nil, false, sip.StatusIntervalToBrief, "Interval Too Brief"
		}
		contacts[i].expires = min(c.expires, maxExp)
	}
	return contacts, wildcard, 0, ""
}

// maxContacts caps the bindings of one AOR, so one device cannot fan a
// call out to an unbounded number of forks.
const maxContacts = 10

// tooManyContacts reports whether applying contacts would leave aor with
// more than maxContacts bindings.
func (s *Server) tooManyContacts(aor string, contacts []contactReq) (bool, error) {
	ctx, cancel := s.stateCtx()
	existing, err := s.deps.State.Bindings(ctx, aor)
	cancel()
	if err != nil {
		return false, err
	}
	set := make(map[string]bool, len(existing)+len(contacts))
	for _, b := range existing {
		set[b.ContactURI] = true
	}
	for _, c := range contacts {
		if c.expires == 0 {
			delete(set, c.uri)
		} else {
			set[c.uri] = true
		}
	}
	return len(set) > maxContacts, nil
}

// applyContacts stores or removes each contact's binding.
func (s *Server) applyContacts(req *sip.Request, dev snapshot.Device, aor string, contacts []contactReq) error {
	now := time.Now()
	userAgent := ""
	if h := req.GetHeader("User-Agent"); h != nil {
		userAgent = h.Value()
	}
	// Path is ours alone: another node reaches this phone through us, over
	// the flow it registered on (RFC 3327, RFC 5626 flow token). A Path the
	// client sent is dropped; phones register directly, and honouring one
	// would let a client choose where other nodes send its calls.
	for _, c := range contacts {
		ctx, cancel := s.stateCtx()
		var err error
		if c.expires == 0 {
			err = s.deps.State.DeleteBinding(ctx, aor, c.uri)
		} else {
			exp := now.Add(time.Duration(c.expires) * time.Second)
			err = s.deps.State.PutBinding(ctx, livestate.Binding{
				AOR:          aor,
				Extension:    dev.Extension,
				Device:       dev.Username,
				ContactURI:   c.uri,
				Source:       req.Source(), // the packet's source: received/rport
				Transport:    "udp",
				UserAgent:    userAgent,
				Path:         []string{s.pathURI(s.flowToken(req.Source(), "udp", exp))},
				ReceivedNode: s.cfg.NodeID,
				Expires:      exp,
				UpdatedAt:    now,
			})
		}
		cancel()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) stateDown(tx sip.ServerTransaction, req *sip.Request, op string, err error) {
	s.log.Warn("live state unavailable", "op", op, "error", err)
	s.unavailable(tx, req)
}
