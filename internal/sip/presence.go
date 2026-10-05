// Presence: per-device state published to the Phase 3 livestate keyspace and
// dialog (BLF) subscriptions served with SUBSCRIBE/NOTIFY (spec S-10,
// contract 3). Publishing fans out from goroutines, never from the SIP
// transaction path (spec failure mode "presence flood").
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

// Device presence states (contract 3).
const (
	StateIdle    = "idle"
	StateRinging = "ringing"
	StateOnCall  = "on-call"
	StateDND     = "dnd"
)

// subscription is one phone's dialog (BLF) subscription to one extension.
// It lives in this node's memory only: a subscription never survives the
// node that accepted it, and a phone re-SUBSCRIBEs after a node restart.
type subscription struct {
	callID    string
	device    string
	extension string
	localTag  string
	remoteTag string
	target    sip.Uri
	cseq      uint32
	version   uint64
	expires   time.Time
	lastState string
}

// presenceFanout bounds one state change's fan-out; it runs on its own
// goroutine, so a slow Valkey or a slow phone never delays the call.
const presenceFanout = 5 * time.Second

// DeviceStatePublisher is satisfied by *livestate.Store (contract 3); the
// Server publishes presence through the same Valkey keyspace that
// hello-control serves, so the two never disagree.
type DeviceStatePublisher interface {
	SetDeviceState(ctx context.Context, sds livestate.DeviceState, ttl time.Duration) error
}

// publishDeviceState stores state for each device and NOTIFYs every live
// subscription of the extension. It fans out from a goroutine, so a slow
// Valkey or a slow phone never delays the call that caused the change.
func (s *Server) publishDeviceState(devices []string, extension, state string) {
	if len(devices) == 0 {
		return
	}
	s.goBG(func() {
		defer contain(s.log, "presence publish")
		ctx, cancel := context.WithTimeout(context.Background(), presenceFanout)
		defer cancel()
		for _, d := range devices {
			sds := livestate.DeviceState{Device: d, Extension: extension, State: state, UpdatedAt: time.Now()}
			if err := s.deps.State.SetDeviceState(ctx, sds, s.cfg.PresenceTTL); err != nil {
				s.log.Warn("could not publish device state", "device", d, "state", state, "error", err)
			}
		}
		s.notifyExtension(extension, state)
	})
}

// devicesOf lists an extension's devices, the owners of its presence.
func (s *Server) devicesOf(snap *snapshot.Snapshot, ext string) []string {
	var out []string
	for _, d := range snap.DevicesForExtension(ext) {
		out = append(out, d.Username)
	}
	return out
}

// handleSubscribe serves a dialog (BLF) SUBSCRIBE: digest-authenticated,
// Event: dialog only, expiry clamped to the registration limits. The 200
// carries our To tag, which every NOTIFY of this dialog reuses.
func (s *Server) handleSubscribe(req *sip.Request, tx sip.ServerTransaction) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		s.unavailable(tx, req)
		return
	}
	if _, ok := s.verify(req, tx, snap); !ok {
		return
	}
	if ev := req.GetHeader("Event"); ev == nil || !strings.EqualFold(strings.TrimSpace(ev.Value()), "dialog") {
		s.respond(tx, req, sip.StatusBadRequest, "Bad Event")
		return
	}
	ext := req.Recipient.User
	if !snap.HasExtension(ext) {
		s.respond(tx, req, sip.StatusNotFound, "Not Found")
		return
	}
	exp := s.subscribeExpires(req)
	if exp == 0 { // an unsubscribe
		s.dropSub(req.CallID().Value(), true)
		s.respond(tx, req, sip.StatusOK, "OK", sip.NewHeader("Expires", "0"))
		return
	}
	sub, fresh := s.upsertSub(req, ext, exp)
	if sub == nil {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	tag := sip.HeaderParams{}
	if sub.localTag != "" {
		tag = sip.HeaderParams{{K: "tag", V: sub.localTag}}
	}
	s.respond(tx, req, sip.StatusOK, "OK",
		sip.NewHeader("Expires", strconv.Itoa(int(exp/time.Second))),
		sip.NewHeader("Event", "dialog"),
		&sip.ToHeader{Address: *req.To().Address.Clone(), Params: tag})
	s.m.PresenceSubscriptions.Set(float64(s.subCount()))
	if fresh {
		init := StateIdle
		if e, ok := snap.Extension(ext); ok && e.DND {
			init = StateDND
		}
		go s.sendStateNotify(sub, init, int(exp/time.Second), "")
	}
}

// subscribeExpires reads the subscription's expiry: the Expires header or
// the Contact's expires parameter, clamped to the registration limits;
// 0 means unsubscribe, -1 uses the default.
func (s *Server) subscribeExpires(req *sip.Request) time.Duration {
	sec := -1
	if h := req.GetHeader("Expires"); h != nil {
		n, err := strconv.Atoi(strings.TrimSpace(h.Value()))
		if err != nil || n < 0 {
			return -time.Second // malformed: the caller answers 400
		}
		sec = n
	}
	for _, h := range req.GetHeaders("Contact") {
		if c, ok := h.(*sip.ContactHeader); ok && !c.Address.Wildcard {
			if v, ok := c.Params.Get("expires"); ok {
				n, err := strconv.Atoi(v)
				if err != nil || n < 0 {
					return -time.Second
				}
				sec = n
			}
		}
	}
	if sec < 0 {
		sec = defaultExpires
	}
	if sec == 0 {
		return 0
	}
	// Unlike a registration, a dialog subscription may be arbitrarily
	// short (BLF keys renew at their own pace); only the maximum is ours.
	hi := int(s.cfg.MaxExpires / time.Second)
	return time.Duration(min(sec, hi)) * time.Second
}

// upsertSub creates or refreshes the subscription of req's dialog. It
// returns the subscription (nil when an in-dialog SUBSCRIBE matches no
// dialog: 481) and whether it was created now.
func (s *Server) upsertSub(req *sip.Request, ext string, exp time.Duration) (*subscription, bool) {
	id := req.CallID().Value()
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	if isInDialog(req) {
		sub, ok := s.subs[id]
		if !ok {
			return nil, false
		}
		sub.expires = time.Now().Add(exp)
		return sub, false
	}
	target := req.From().Address
	if ct := req.Contact(); ct != nil {
		target = ct.Address
	}
	sub := &subscription{
		callID: id, device: req.From().Address.User, extension: ext,
		localTag: sip.GenerateTagN(8), remoteTag: tagOf(req.From()),
		target: *target.Clone(), expires: time.Now().Add(exp),
	}
	s.subs[id] = sub
	if s.byExt[ext] == nil {
		s.byExt[ext] = map[string]bool{}
	}
	s.byExt[ext][id] = true
	return sub, true
}

// dropSub forgets a subscription; when notify it sends the final
// Subscription-State: terminated NOTIFY the subscriber expects.
func (s *Server) dropSub(id string, notify bool) {
	s.subsMu.Lock()
	sub, ok := s.subs[id]
	if ok {
		delete(s.subs, id)
		delete(s.byExt[sub.extension], id)
	}
	s.subsMu.Unlock()
	if ok && notify {
		go s.sendStateNotify(sub, StateIdle, 0, "deactivated")
	}
	if ok {
		s.m.PresenceSubscriptions.Set(float64(s.subCount()))
	}
}

func (s *Server) subCount() int {
	s.subsMu.Lock()
	defer s.subsMu.Unlock()
	return len(s.subs)
}

// tagOf reads a From/To header's tag parameter.
func tagOf(h *sip.FromHeader) string {
	if v, ok := h.Params.Get("tag"); ok {
		return v
	}
	return ""
}

// notifyExtension fans the extension's new state out to its subscribers,
// one goroutine per subscription (spec failure mode "presence flood").
func (s *Server) notifyExtension(ext, state string) {
	s.subsMu.Lock()
	var targets []*subscription
	for id := range s.byExt[ext] {
		if sub, ok := s.subs[id]; ok {
			sub.lastState = state
			cp := *sub
			targets = append(targets, &cp)
		}
	}
	s.subsMu.Unlock()
	for _, sub := range targets {
		go s.sendStateNotify(sub, state, int(time.Until(sub.expires)/time.Second), "")
	}
}

// sendStateNotify sends one in-dialog NOTIFY carrying the extension's dialog
// state. seq and version advance under the subscription lock, so NOTIFYs
// never repeat a CSeq or go backwards in version.
func (s *Server) sendStateNotify(sub *subscription, state string, expires int, reason string) {
	defer contain(s.log, "presence notify")
	s.subsMu.Lock()
	sub.cseq++
	sub.version++
	seq, ver := sub.cseq, sub.version
	s.subsMu.Unlock()
	body := dialogXML(sub.extension, s.cfg.Domain, ver, state)
	req := sip.NewRequest(sip.NOTIFY, *sub.target.Clone())
	from := &sip.FromHeader{
		Address: sip.Uri{Scheme: "sip", User: sub.extension, Host: s.cfg.Domain},
		Params:  sip.HeaderParams{{K: "tag", V: sub.localTag}},
	}
	to := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: sub.device, Host: s.cfg.Domain}, Params: sip.HeaderParams{{K: "tag", V: sub.remoteTag}}}
	callID := sip.CallIDHeader(sub.callID)
	req.AppendHeader(from)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: seq, MethodName: sip.NOTIFY})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Event", "dialog"))
	if reason != "" {
		req.AppendHeader(sip.NewHeader("Subscription-State", "terminated;reason="+reason))
	} else {
		req.AppendHeader(sip.NewHeader("Subscription-State", "active;expires="+strconv.Itoa(expires)))
	}
	req.AppendHeader(sip.NewHeader("Content-Type", "application/dialog-info+xml"))
	req.SetBody(body)
	ctx, cancel := context.WithTimeout(context.Background(), byeTimeout)
	defer cancel()
	res, err := s.client.Do(ctx, req)
	if err != nil {
		s.log.Debug("presence NOTIFY failed", "device", sub.device, "error", err)
		return
	}
	s.m.response(res.StatusCode)
}

// dialogXML renders the dialog-info document (RFC 4235) for one extension's
// state. The mapping is the one BLF lamps expect: ringing is "early", a
// connected or DND extension is "busy", idle is "terminated".
func dialogXML(ext, domain string, version uint64, state string) []byte {
	dlg := "confirmed"
	switch state {
	case StateIdle:
		dlg = "terminated"
	case StateRinging:
		dlg = "early"
	case StateOnCall, StateDND:
		dlg = "confirmed"
	}
	if state == StateDND {
		dlg = "busy"
	}
	return []byte(`<?xml version="1.0"?>` + "\r\n" +
		`<dialog-info xmlns="urn:ietf:params:xml:ns:dialog-info" version="` +
		strconv.FormatUint(version, 10) + `" state="full" entity="sip:` + ext + `@` + domain + `">` + "\r\n" +
		`<dialog id="` + ext + `"><state>` + dlg + `</state></dialog>` + "\r\n" +
		`</dialog-info>` + "\r\n")
}

// subExpireLoop ends subscriptions whose expiry passed: the subscriber gets
// a final terminated NOTIFY and must re-SUBSCRIBE (spec S-10).
func (s *Server) subExpireLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case <-t.C:
		}
		var expired []*subscription
		s.subsMu.Lock()
		now := time.Now()
		for id, sub := range s.subs {
			if now.After(sub.expires) {
				expired = append(expired, sub)
				delete(s.subs, id)
				delete(s.byExt[sub.extension], id)
			}
		}
		s.subsMu.Unlock()
		if len(expired) > 0 {
			s.m.PresenceSubscriptions.Set(float64(s.subCount()))
			for _, sub := range expired {
				//nolint:gosec // detached on purpose: the fan-out must not
				// stall the sweeper (spec failure mode "presence flood").
				go s.sendStateNotify(sub, sub.lastState, 0, "timeout")
			}
		}
	}
}

// restorePresence puts a finished call's participants back to idle, unless
// their extension is DND (which keeps the dnd state published at REGISTER).
func (s *Server) restorePresenceOf(callerNum, callerDevice, calleeExt string) {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return
	}
	state := func(ext string) string {
		if e, ok := snap.Extension(ext); ok && e.DND {
			return StateDND
		}
		return StateIdle
	}
	if callerDevice != "" {
		s.publishDeviceState([]string{callerDevice}, callerNum, state(callerNum))
	}
	s.publishDeviceState(s.devicesOf(snap, calleeExt), calleeExt, state(calleeExt))
}
