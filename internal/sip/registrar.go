package sip

import (
	"context"
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
	uri      string
	expires  int
	instance string // the Contact's +sip.instance, "" when it names none
}

// handleRegister authenticates the device, applies its contacts to the AOR
// and answers with every current binding (RFC 3261 §10.3).
func (s *Server) handleRegister(req *sip.Request, tx sip.ServerTransaction) {
	if rec, ok := s.deps.State.(registerAttemptRecorder); ok {
		rtx := &recordingTx{ServerTransaction: tx}
		defer s.recordRegisterAttempt(rec, req, rtx)
		tx = rtx
	}
	if s.refuseIfNotReady(req, tx) {
		return
	}
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
	if over, err := s.tooManyContacts(aor, contacts, req.CallID().Value()); err != nil {
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
		// Messages stored while the extension had no device go out now.
		s.kickMessages(dev.Extension)
	}
	// A registration refreshes the device's presence: idle, or dnd while the
	// extension's DND is on (contract 3, "DND from the registrar state").
	if snap2 := s.deps.Snapshots.Current(); snap2 != nil {
		state := StateIdle
		if e, ok := snap2.Extension(dev.Extension); ok && e.DND {
			state = StateDND
		}
		s.publishDeviceState([]string{dev.Username}, dev.Extension, state)
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
		inst, _ := c.Params.Get("+sip.instance")
		contacts = append(contacts, contactReq{uri: c.Address.String(), expires: exp, instance: strings.Trim(inst, `"<> `)})
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
// more than maxContacts bindings. A binding the request replaces (a roamed
// phone's old contact) does not count.
func (s *Server) tooManyContacts(aor string, contacts []contactReq, callID string) (bool, error) {
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
	for _, uri := range replacedBindings(existing, contacts, callID) {
		delete(set, uri)
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

// replacedBindings lists the contact URIs of existing bindings a REGISTER
// replaces (phone-feature-parity S-9): a phone that roamed to another IP
// registers a new Contact URI, and its old one must not keep ringing a dead
// address until it expires. A binding is replaced when a refreshed or new
// contact of the request names the same +sip.instance, or the request has the
// binding's Call-ID (a phone registers with one Call-ID for as long as it
// runs), and the binding's own URI is not one the request registers or
// removes.
func replacedBindings(existing []livestate.Binding, contacts []contactReq, callID string) []string {
	keep := make(map[string]bool, len(contacts))
	instances := map[string]bool{}
	registering := false
	for _, c := range contacts {
		keep[c.uri] = true
		if c.expires > 0 {
			registering = true
			if c.instance != "" {
				instances[c.instance] = true
			}
		}
	}
	if !registering {
		return nil
	}
	var out []string
	for _, b := range existing {
		if keep[b.ContactURI] {
			continue
		}
		if (b.Instance != "" && instances[b.Instance]) || (callID != "" && b.CallID == callID) {
			out = append(out, b.ContactURI)
		}
	}
	return out
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
	callID := req.CallID().Value()
	ctx0, cancel0 := s.stateCtx()
	existing, err := s.deps.State.Bindings(ctx0, aor)
	cancel0()
	if err != nil {
		return err
	}
	for _, uri := range replacedBindings(existing, contacts, callID) {
		ctx, cancel := s.stateCtx()
		err := s.deps.State.DeleteBinding(ctx, aor, uri)
		cancel()
		if err != nil {
			return err
		}
		s.log.Info("registration replaced by a new address", "device", dev.Username, "old_contact", uri)
	}
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
				Source:       s.clientSource(req), // received/rport, or the trusted proxy's X-Hello-Client
				Transport:    "udp",
				UserAgent:    userAgent,
				Path:         s.bindingPath(req, exp),
				ReceivedNode: s.cfg.NodeID,
				Instance:     c.instance,
				CallID:       callID,
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

// bindingPath is a binding's Path. Through a trusted edge proxy it is the
// Path the proxy added (calls reach the phone through it, from any node);
// otherwise it is this node's own flow-token Path (direct mode), and any
// client-supplied Path is dropped.
func (s *Server) bindingPath(req *sip.Request, exp time.Time) []string {
	if s.fromTrustedProxy(req) {
		var path []string
		for _, h := range req.GetHeaders("Path") {
			path = append(path, h.Value())
		}
		if len(path) > 0 {
			return path
		}
	}
	return []string{s.pathURI(s.flowToken(req.Source(), "udp", exp))}
}

// registerAttemptRecorder keeps each enabled device's recent REGISTER
// outcomes for the Diagnostics view; *livestate.Store implements it. A
// State without it records nothing.
type registerAttemptRecorder interface {
	RecordRegisterAttempt(ctx context.Context, a livestate.RegisterAttempt) error
}

// recordingTx notes the final response sent through it.
type recordingTx struct {
	sip.ServerTransaction
	res *sip.Response
	at  time.Time // when the final response was handed to the transport
}

func (t *recordingTx) Respond(res *sip.Response) error {
	if res.StatusCode >= 200 {
		t.res = res
		t.at = time.Now().UTC()
	}
	return t.ServerTransaction.Respond(res)
}

// recordRegisterAttempt stores the REGISTER's final response, after it was
// sent, for the AOR's device. The record is stamped with the moment the
// response was sent, not the moment this goroutine got to write it: the
// next REGISTER is handled concurrently and may be stored first, so the
// timestamp is what orders the attempts. Only enabled devices of the snapshot are
// recorded, so the keyspace is bounded by configuration, not by what
// clients send; a failed write is logged and never affects the response.
func (s *Server) recordRegisterAttempt(rec registerAttemptRecorder, req *sip.Request, tx *recordingTx) {
	snap := s.deps.Snapshots.Current()
	if tx.res == nil || snap == nil || req.To() == nil {
		return
	}
	user := req.To().Address.User
	if _, ok := snap.DeviceByUsername(user); !ok {
		return
	}
	stale := false
	for _, h := range tx.res.GetHeaders("WWW-Authenticate") {
		stale = stale || strings.Contains(strings.ToLower(h.Value()), "stale=true")
	}
	ua := ""
	if h := req.GetHeader("User-Agent"); h != nil {
		ua = h.Value()
	}
	ctx, cancel := s.stateCtx()
	defer cancel()
	err := rec.RecordRegisterAttempt(ctx, livestate.RegisterAttempt{
		At: tx.at, Device: user, Source: s.clientSource(req), IP: s.clientIP(req), Node: s.cfg.NodeID, UserAgent: ua,
		Credentials: req.GetHeader("Authorization") != nil, Code: tx.res.StatusCode, Reason: tx.res.Reason, Stale: stale,
	})
	if err != nil {
		s.log.Debug("register attempt not recorded", "error", err)
	}
}
