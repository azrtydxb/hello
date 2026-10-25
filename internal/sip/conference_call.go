// The call side of the server conference rooms (spec S-34): joining a room
// from a phone, a DID route or another node, the PIN and wait-for-leader
// steps, the participant's DTMF controls, and recovery when a node dies.
package sip

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

const (
	// confForwardHeader marks an INVITE one SIP node sends another to join a
	// room the receiver owns; its value authenticates the sender (HMAC under
	// the cluster's shared nonce secret). confNodeHeader names the sender.
	confForwardHeader = "X-Hello-Conf-Forward"
	confNodeHeader    = "X-Hello-Conf-Node"
	confTokenSkew     = time.Minute
	// confHADetail prefixes a conference call's replicated state detail.
	confHADetail = "conf:"

	// confIdleLimit drops a participant whose phone sent no audio for this
	// long (it lost power or its network), so a seat is not held for the
	// four-hour call limit.
	confIdleLimit  = 5 * time.Minute
	confPINPrompt  = 25 * time.Second
	confKeyWindow  = 3 * time.Second
	confDenyLinger = 1500 * time.Millisecond
	confResolveTry = 3
)

// confFwd is a forwarded call's state on the node the caller reached.
type confFwd struct {
	room        string
	owner       string
	incarnation string
}

// --- node-to-node trust ---------------------------------------------------------

func (s *Server) confMAC(callID, room, node string, ts int64) string {
	m := hmac.New(sha256.New, s.cfg.NonceSecret)
	fmt.Fprintf(m, "hello-conference-forward|%s|%s|%s|%d", callID, room, node, ts)
	return hex.EncodeToString(m.Sum(nil))
}

// confForwardToken is the header value a forwarded INVITE carries.
func (s *Server) confForwardToken(callID, room string) string {
	ts := time.Now().Unix()
	return strconv.FormatInt(ts, 10) + "." + s.confMAC(callID, room, s.cfg.NodeID, ts)
}

// confForwardFrom checks a forwarded INVITE's token and returns the sending
// node. The request's Call-ID, the dialled room and the claimed node are all
// under the MAC, so a captured value works for nothing else.
func (s *Server) confForwardFrom(req *sip.Request) (string, bool) {
	h := req.GetHeader(confForwardHeader)
	n := req.GetHeader(confNodeHeader)
	if h == nil || n == nil {
		return "", false
	}
	tsStr, mac, ok := strings.Cut(strings.TrimSpace(h.Value()), ".")
	if !ok {
		return "", false
	}
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil || time.Since(time.Unix(ts, 0)).Abs() > confTokenSkew {
		return "", false
	}
	node := strings.TrimSpace(n.Value())
	want := s.confMAC(req.CallID().Value(), req.Recipient.User, node, ts)
	if !hmac.Equal([]byte(want), []byte(mac)) {
		return "", false
	}
	return node, true
}

// confForwardHeaders are the headers of the INVITE a node sends to a room's
// owner.
func (c *call) confForwardHeaders(callID string) []sip.Header {
	f := c.confFwd
	if f == nil {
		return nil
	}
	return []sip.Header{
		sip.NewHeader(confForwardHeader, c.s.confForwardToken(callID, f.room)),
		sip.NewHeader(confNodeHeader, c.s.cfg.NodeID),
	}
}

// --- ownership ------------------------------------------------------------------

// confOwnership is where a room is mixed.
type confOwnership struct {
	self    bool
	claimed bool           // this call just took the lease (nothing hosts the room yet)
	owner   cluster.Member // the owning node when !self
}

var errConfNoOwner = errors.New("conference: no node owns the room")

// confResolve decides whether this node mixes the room or another does. The
// lease in Valkey is the single truth: the first node to take it owns the
// room while it has participants. An owner that is gone or no longer READY
// (crashed, draining) is replaced atomically.
func (s *Server) confResolve(number string) (confOwnership, error) {
	if s.conf.get(number) != nil {
		return confOwnership{self: true}, nil
	}
	cs := s.deps.Conferences
	if cs == nil {
		return confOwnership{self: true}, nil
	}
	key := livestate.ConfOwnerKey(number)
	for range confResolveTry {
		ctx, cancel := context.WithTimeout(context.Background(), 3*s.cfg.StateTimeout)
		ok, err := cs.AcquireLease(ctx, key, s.cfg.NodeID, livestate.ConfOwnerTTL)
		if err != nil {
			cancel()
			return confOwnership{}, err
		}
		if ok {
			cancel()
			s.m.Conf.Owner.WithLabelValues("claimed").Inc()
			return confOwnership{self: true, claimed: true}, nil
		}
		owner, err := cs.ConfOwner(ctx, number)
		if err != nil {
			cancel()
			return confOwnership{}, err
		}
		if owner == "" || owner == s.cfg.NodeID {
			cancel()
			continue // the lease lapsed between the two calls
		}
		m, found := s.confMember(ctx, owner)
		if s.deps.Membership == nil {
			cancel()
			return confOwnership{}, errConfNoOwner // cannot tell a live owner from a dead one
		}
		if found && m.State == cluster.Ready && m.SIPAddr != "" {
			cancel()
			return confOwnership{owner: m}, nil
		}
		taken, err := cs.TakeConfOwner(ctx, number, owner, s.cfg.NodeID, livestate.ConfOwnerTTL)
		cancel()
		if err != nil {
			return confOwnership{}, err
		}
		if taken {
			s.m.Conf.Owner.WithLabelValues("taken_over").Inc()
			return confOwnership{self: true, claimed: true}, nil
		}
	}
	return confOwnership{}, errConfNoOwner
}

// confYieldLease gives a lease taken for a join that did not go ahead back,
// when nothing hosts the room here.
func (s *Server) confYieldLease(number string) {
	cs := s.deps.Conferences
	if cs == nil || s.conf.get(number) != nil {
		return
	}
	ctx, cancel := s.stateCtx()
	defer cancel()
	_ = cs.ReleaseLease(ctx, livestate.ConfOwnerKey(number), s.cfg.NodeID) // best effort: it expires anyway
}

// --- joining --------------------------------------------------------------------

// joinConference connects the caller to a room: here when this node owns it
// (or the INVITE came from the node that reached the caller), else through
// the owner. It runs on the INVITE handler's goroutine and returns once the
// call is answered or refused; the participant's flow continues in its own
// goroutine.
func (c *call) joinConference(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, cfg snapshot.Conference, fromNode string) {
	s := c.s
	c.kind = cdr.KindConference
	c.rewritten = cfg.Number
	c.confPeer = fromNode != ""
	c.confPeerNode = fromNode
	c.addTrace(fmt.Sprintf("Conference room %s (%s)", cfg.Number, cfg.Name))
	if fromNode != "" {
		c.addTrace("Joined through node " + fromNode)
	}
	refuse := func(code int, result, joinResult, why string) {
		s.m.Conf.Joins.WithLabelValues(joinResult).Inc()
		s.respond(tx, req, code, statusText(code))
		c.record(code, cdr.SideSystem, why, result)
	}
	switch {
	case !cfg.Enabled:
		refuse(sip.StatusTemporarilyUnavailable, ResultUnavailable, "disabled", "conference room disabled")
		return
	case cfg.Misconfigured:
		refuse(sip.StatusTemporarilyUnavailable, ResultUnavailable, "failed", "conference PIN cannot be opened")
		return
	}
	offer, err := media.ParseAudioSDP(req.Body())
	if err != nil {
		refuse(sip.StatusNotAcceptableHere, ResultFailed, "failed", "no usable audio offer")
		return
	}
	if _, ok := media.PartyCodecFor(offer.PayloadType); !ok {
		refuse(sip.StatusNotAcceptableHere, ResultFailed, "failed", "no common conference codec")
		return
	}
	own, err := s.confResolve(cfg.Number)
	if err != nil {
		s.log.Warn("conference ownership unavailable", "room", cfg.Number, "error", err)
		s.m.Conf.Joins.WithLabelValues("unavailable").Inc()
		s.unavailable(tx, req)
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "conference ownership unavailable", ResultUnavailable)
		return
	}
	if !own.self {
		if fromNode != "" {
			// A peer reached us as the owner but the lease says otherwise:
			// the room moved. The peer's caller is told to retry.
			s.m.Conf.Joins.WithLabelValues("unavailable").Inc()
			s.unavailable(tx, req)
			c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "conference room moved", ResultUnavailable)
			return
		}
		c.joinForward(req, tx, snap, cfg, own.owner)
		return
	}
	room, ok := s.confReserve(cfg)
	if !ok {
		if own.claimed {
			s.confYieldLease(cfg.Number)
		}
		refuse(sip.StatusBusyHere, ResultBusy, "full", "conference room full")
		return
	}
	room.mu.Lock()
	room.cfg = cfg
	room.mu.Unlock()
	answer, party, err := s.confParty(req.Body())
	if err != nil {
		s.log.Warn("conference media unavailable", "room", cfg.Number, "error", err)
		s.confRelease(room)
		refuse(sip.StatusServiceUnavailable, ResultUnavailable, "failed", "conference media unavailable")
		return
	}
	if !c.begin(req, tx) {
		party.Close()
		s.confRelease(room)
		return
	}
	p := c.confAttach(room, party, answer)
	if !c.answerSelf(answer) {
		c.confLeave()
		return
	}
	c.mu.Lock()
	c.mediaMode = "anchored"
	c.mu.Unlock()
	if s.deps.HAState != nil && !c.confPeer {
		// A local participant replicates like any call Hello answers
		// itself: a taker re-INVITEs the phone onto its own mixer and the
		// participant rejoins there.
		c.haSoloRefresh(string(answer))
		c.mu.Lock()
		c.haPhase, c.haDetail = haPhaseConference, confHADetail+cfg.Number
		c.mu.Unlock()
		go c.haLoop()
	}
	go c.confFlow(room, p)
}

// confParty binds the participant's media socket on this node and builds the
// answer.
func (s *Server) confParty(offer []byte) ([]byte, *media.Party, error) {
	a := s.voicemailAnchor(offer)
	if a == nil {
		return nil, nil, errors.New("no media address to advertise")
	}
	return media.NewParty(a.AdvertisedHost(), s.cfg.RTPPortMin, s.cfg.RTPPortMax, offer)
}

// confAttach ties a participant to the call.
func (c *call) confAttach(room *confRoom, party *media.Party, answer []byte) *confPart {
	c.mu.Lock()
	defer c.mu.Unlock()
	p := &confPart{
		id: c.id, c: c, party: party, number: c.callerNum, name: c.callerName,
		admit: make(chan struct{}),
	}
	c.conf, c.confRoom, c.confAnswer = p, room, answer
	c.confDone = make(chan struct{})
	return p
}

// confLeave detaches the participant when the call ends (it is called from
// the call's single completion point): the media socket closes at once, the
// room bookkeeping and a possible teardown run off the call's path.
func (c *call) confLeave() {
	c.mu.Lock()
	p, room, done := c.conf, c.confRoom, c.confDone
	c.mu.Unlock()
	if p == nil || done == nil {
		return
	}
	c.confOnce.Do(func() {
		close(done)
		p.party.Close()
		s := c.s
		s.goBG(func() {
			defer contain(s.log, "conference leave")
			wasAdmitted, remaining := room.remove(p)
			if wasAdmitted && remaining > 0 {
				room.announce(false)
			}
			s.confRelease(room)
		})
	})
}

// confHangup ends a participant's call from this side (a kick, a denied
// entry, the end of a room).
func (c *call) confHangup() {
	c.announcementEnd()
}

// confDeny refuses an admitted-to-the-call participant entry: a tone, then
// the call ends.
func (c *call) confDeny(room *confRoom, p *confPart, result string) {
	c.s.m.Conf.Joins.WithLabelValues(result).Inc()
	c.addTrace("Conference entry refused: " + result)
	room.mix.SayTo(p.id, media.ConfToneSamples(media.ToneDenied))
	t := time.NewTimer(confDenyLinger)
	select {
	case <-t.C:
	case <-c.confDone:
		t.Stop()
	}
	c.confHangup()
}

// confFlow is the participant's life in the room: PIN, wait for a leader,
// admission, then the DTMF controls until the call ends.
func (c *call) confFlow(room *confRoom, p *confPart) {
	s := c.s
	defer contain(s.log, "conference participant")
	done := c.confDone
	room.mix.Add(p.id, p.party, true, true)
	cfg := room.config()
	if c.confPeerNode != "" {
		go c.confWatchPeer(done)
	}

	leader, ok := c.confAuthenticate(room, p, cfg, done)
	if !ok {
		return
	}
	room.mu.Lock()
	p.leader = leader
	locked := room.locked
	room.mu.Unlock()
	if locked && !leader {
		c.confDeny(room, p, "locked")
		return
	}
	room.add(p)
	if cfg.WaitForLeader && !leader {
		room.mu.Lock()
		waiting := !room.leaderPresentLocked()
		p.waiting = waiting
		room.mu.Unlock()
		if waiting {
			room.mix.SetHold(p.id, room.holdSamples())
			c.addTrace("Waiting for a leader")
			room.publish()
			select {
			case <-p.admit:
			case <-done:
				return
			}
		}
	}
	released := room.admitPart(p)
	for _, w := range released {
		close(w.admit)
		room.mix.SayTo(w.id, media.ConfToneSamples(media.ToneAdmit))
	}
	room.announce(true)
	if cfg.Record {
		room.startRecording()
	}
	s.m.Conf.Joins.WithLabelValues("joined").Inc()
	c.addTrace("Joined the conference")
	room.publish()
	c.confControl(room, p, done)
}

// confAuthenticate asks for the PIN when the room has one. It returns the
// participant's role: a leader PIN makes a leader, the user PIN (or no PIN
// at all when the room has none for users) a plain participant.
func (c *call) confAuthenticate(room *confRoom, p *confPart, cfg snapshot.Conference, done <-chan struct{}) (leader, ok bool) {
	if cfg.UserPIN == "" && cfg.LeaderPIN == "" {
		return false, true
	}
	for attempt := 0; attempt < confPINAttempts; attempt++ {
		if room.pinGuarded() {
			c.confDeny(room, p, "bad_pin")
			return false, false
		}
		room.mix.SayTo(p.id, media.ConfToneSamples(media.TonePIN))
		pin, got := c.confReadPIN(p, done)
		if !got {
			select {
			case <-done:
				return false, false
			default:
			}
		}
		switch {
		case got && cfg.LeaderPIN != "" && pinEqual(pin, cfg.LeaderPIN):
			return true, true
		case got && cfg.UserPIN != "" && pinEqual(pin, cfg.UserPIN):
			return false, true
		case got && cfg.UserPIN == "" && pin == "":
			return false, true // a room with only a leader PIN lets everyone else in with '#'
		}
		room.pinFailed()
		room.mix.SayTo(p.id, media.ConfToneSamples(media.ToneDenied))
		select {
		case <-time.After(confDenyLinger):
		case <-done:
			return false, false
		}
	}
	c.confDeny(room, p, "bad_pin")
	return false, false
}

func pinEqual(a, b string) bool { return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1 }

// confReadPIN collects digits up to '#'. '*' clears what was typed.
func (c *call) confReadPIN(p *confPart, done <-chan struct{}) (string, bool) {
	var b []byte
	timeout := time.NewTimer(confPINPrompt)
	defer timeout.Stop()
	for {
		select {
		case d := <-p.party.DTMF():
			switch {
			case d == '#':
				return string(b), true
			case d == '*':
				b = b[:0]
			case d >= '0' && d <= '9':
				if len(b) >= 16 {
					return "", false
				}
				b = append(b, d)
			}
		case <-timeout.C:
			return "", false
		case <-done:
			return "", false
		}
	}
}

// confControl reads the participant's DTMF for the rest of the call. A
// control is '*' and a digit:
//
//	*6  mute or unmute yourself (not when a leader muted you)
//	*5  leader: mute or unmute everyone but the leaders
//	*7  leader: lock or unlock the room
//	*8  leader: remove the participant who joined last
func (c *call) confControl(room *confRoom, p *confPart, done <-chan struct{}) {
	var starAt time.Time
	idle := time.NewTicker(10 * time.Second)
	defer idle.Stop()
	for {
		select {
		case <-idle.C:
			last := p.party.LastAudio()
			if last.IsZero() {
				last = p.joined
			}
			if time.Since(last) > confIdleLimit {
				c.addTrace("Conference participant sent no audio for " + confIdleLimit.String() + ": removed")
				c.confHangup()
				return
			}
		case d := <-p.party.DTMF():
			if d == '*' {
				starAt = time.Now()
				continue
			}
			if starAt.IsZero() || time.Since(starAt) > confKeyWindow {
				continue
			}
			starAt = time.Time{}
			c.confKey(room, p, d)
		case <-done:
			return
		}
	}
}

func (c *call) confKey(room *confRoom, p *confPart, key byte) {
	actor := confActor(p)
	deny := func() { room.mix.SayTo(p.id, media.ConfToneSamples(media.ToneDenied)) }
	room.mu.Lock()
	leader := p.leader
	room.mu.Unlock()
	switch key {
	case '6':
		muted, denied := room.selfMute(p)
		if denied {
			deny()
			return
		}
		a := confUnmute
		if muted {
			a = confMute
		}
		c.s.m.Conf.Control.WithLabelValues(a, confSrcDTMF).Inc()
	case '5':
		if !leader {
			deny()
			return
		}
		room.mu.Lock()
		any := false
		for _, o := range room.parts {
			if o.admitted && !o.leader && !o.mutedByLeader {
				any = true
			}
		}
		room.mu.Unlock()
		a := confUnmuteAll
		if any {
			a = confMuteAll
		}
		room.control(a, "", actor, confSrcDTMF)
	case '7':
		if !leader {
			deny()
			return
		}
		room.mu.Lock()
		a := confLock
		if room.locked {
			a = confUnlock
		}
		room.mu.Unlock()
		room.control(a, "", actor, confSrcDTMF)
	case '8':
		if !leader {
			deny()
			return
		}
		target, ok := room.kickLast(p.id)
		if !ok {
			deny()
			return
		}
		c.s.m.Conf.Control.WithLabelValues(confKick, confSrcDTMF).Inc()
		room.audit(actor, confKick, target)
		room.publish()
	}
}

// confActor names a participant in the audit log.
func confActor(p *confPart) string {
	if p.number != "" {
		return "ext:" + p.number
	}
	return "caller:" + p.id
}

// confDigit feeds a SIP INFO digit to the participant's DTMF; false when the
// call is not a conference participant here.
func (c *call) confDigit(d byte) bool {
	c.mu.Lock()
	p := c.conf
	c.mu.Unlock()
	if p == nil {
		return false
	}
	p.party.PushDTMF(d)
	return true
}

// confInDialog answers a re-INVITE or UPDATE from a participant (a session
// refresh, a hold, a changed media address) from the participant's own
// media socket.
func (c *call) confInDialog(req *sip.Request, tx sip.ServerTransaction) bool {
	c.mu.Lock()
	p, answer := c.conf, c.confAnswer
	c.mu.Unlock()
	if p == nil || len(answer) == 0 {
		return false
	}
	s := c.s
	body := answer
	if off, err := media.ParseAudioSDP(req.Body()); err == nil {
		p.party.Retarget(off.Address, off.Port)
		dir := "sendrecv"
		switch media.SDPDirection(req.Body()) {
		case "sendonly", "inactive":
			dir = "recvonly"
			if media.SDPDirection(req.Body()) == "inactive" {
				dir = "inactive"
			}
		}
		if sdp, err := media.ParseAudioSDP(answer); err == nil {
			body = media.BuildAudioSDPDir(sdp.Address, sdp.Port, off.PayloadType, off.DTMFPayloadType, off.DTMFRate, dir)
		}
	}
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", body)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res.AppendHeader(sip.HeaderClone(&s.contact))
	s.send(tx, res)
	return true
}

// confWatchPeer ends a participant forwarded from a node that died: that
// node's caller is gone with it (or was taken over elsewhere and rejoins
// as a new participant), and the stale seat must not linger.
func (c *call) confWatchPeer(done <-chan struct{}) {
	s := c.s
	defer contain(s.log, "conference peer watch")
	if s.deps.Membership == nil {
		return
	}
	t := time.NewTicker(2 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-done:
			return
		}
		ctx, cancel := s.stateCtx()
		ms, err := s.deps.Membership.Members(ctx)
		cancel()
		if err != nil {
			continue
		}
		for _, m := range ms {
			if m.ID == c.confPeerNode && m.Kind == cluster.KindSIP && m.State == cluster.Offline {
				c.addTrace("The node that forwarded this participant is gone: removed")
				c.confHangup()
				return
			}
		}
	}
}

// --- forwarding to the owner ------------------------------------------------------

// joinForward joins the room through the node that owns it: this node keeps
// the caller's dialog and media relay, and a second leg carries the call to
// the owner as an ordinary SIP call that the owner answers with a
// participant. DTMF, hold and hang-up then work as for any bridged call.
func (c *call) joinForward(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, cfg snapshot.Conference, owner cluster.Member) {
	s := c.s
	host, port, err := sip.ParseAddr(owner.SIPAddr)
	if err != nil {
		s.m.Conf.Joins.WithLabelValues("unavailable").Inc()
		s.unavailable(tx, req)
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "conference owner has no usable address", ResultUnavailable)
		return
	}
	u := sip.Uri{Scheme: "sip", User: cfg.Number, Host: host, Port: port}
	c.mu.Lock()
	c.confFwd = &confFwd{room: cfg.Number, owner: owner.ID, incarnation: owner.Incarnation}
	c.haPhase, c.haDetail = haPhaseConference, confHADetail+cfg.Number
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Room %s is mixed on node %s: forwarding the call there", cfg.Number, owner.ID))
	s.m.Conf.Joins.WithLabelValues("forwarded").Inc()
	c.considerAnchor(snap, EndpointInfo{})
	if !c.begin(req, tx) {
		return
	}
	c.mu.Lock()
	l := c.addLeg(&leg{uri: &u})
	c.mu.Unlock()
	go l.run()
	go c.confWatchOwner(cfg)
	c.setup(1)
}

// confFwdActive reports a call that reaches its room through another node.
func (c *call) confFwdActive() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.confFwd != nil && c.conf == nil
}

// confForwardDigit relays a SIP INFO digit to the owner's leg.
func (c *call) confForwardDigit(d byte) {
	c.mu.Lock()
	w := c.winner
	c.mu.Unlock()
	if w == nil || w.session() == nil || w.session().InviteResponse == nil {
		return
	}
	out := sip.NewRequest(sip.INFO, w.target())
	out.AppendHeader(sip.NewHeader("Content-Type", "application/dtmf-relay"))
	out.SetBody([]byte("Signal=" + string(d) + "\r\nDuration=160\r\n"))
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := w.session().Do(ctx, out); err != nil {
		c.s.log.Debug("conference INFO forward failed", "error", err)
	}
}

// confWatchOwner watches the owner of a forwarded call. When the owner is
// gone (OFFLINE, or restarted under the same id) the call keeps its phone
// dialog and relay and moves its far end onto a mixer on this node.
func (c *call) confWatchOwner(cfg snapshot.Conference) {
	s := c.s
	defer contain(s.log, "conference owner watch")
	if s.deps.Membership == nil {
		return
	}
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-t.C:
		case <-s.done:
			return
		}
		c.mu.Lock()
		connected, ended, f := c.connected, c.ended, c.confFwd
		c.mu.Unlock()
		if ended || f == nil {
			return
		}
		if !connected {
			continue
		}
		ctx, cancel := s.stateCtx()
		ms, err := s.deps.Membership.Members(ctx)
		cancel()
		if err != nil {
			continue
		}
		dead := true
		for _, m := range ms {
			if m.ID != f.owner || m.Kind != cluster.KindSIP {
				continue
			}
			dead = m.State == cluster.Offline ||
				(f.incarnation != "" && m.Incarnation != "" && m.Incarnation != f.incarnation)
		}
		if !dead {
			continue
		}
		c.confRehome(cfg, f)
		return
	}
}

// confRehome replaces the dead owner's far end of a forwarded call with a
// participant of a room mixed on this node. The phone's dialog and the relay
// stay as they are: the relay's second leg, which spoke to the owner, now
// speaks to this node's own media socket over loopback.
func (c *call) confRehome(cfg snapshot.Conference, f *confFwd) {
	s := c.s
	relay := c.anchorRelay()
	if relay == nil {
		c.endBoth("conference owner lost")
		return
	}
	snap := s.deps.Snapshots.Current()
	if snap != nil {
		if cur, ok := snap.Conference(cfg.Number); ok {
			cfg = cur
		}
	}
	off, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil {
		c.endBoth("conference owner lost")
		return
	}
	loop := media.BuildAudioSDP("127.0.0.1", relay.LegPort(legCallee), off.PayloadType, off.DTMFPayloadType, off.DTMFRate)
	own, err := s.confResolve(cfg.Number)
	if err != nil || !own.self {
		c.addTrace("Conference owner lost and the room could not be moved here")
		c.endBoth("conference owner lost")
		return
	}
	room, ok := s.confReserve(cfg)
	if !ok {
		c.addTrace("Conference room full after the owner was lost")
		c.endBoth("conference room full")
		return
	}
	_, party, err := media.NewParty("127.0.0.1", s.cfg.RTPPortMin, s.cfg.RTPPortMax, loop)
	if err != nil {
		s.confRelease(room)
		c.endBoth("conference media unavailable")
		return
	}
	relay.SetTarget(legCallee, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: party.Port()})
	c.mu.Lock()
	c.confFwd = nil
	c.confRehomed = true
	c.haPhase, c.haDetail = haPhaseConference, confHADetail+cfg.Number
	c.mu.Unlock()
	p := c.confAttach(room, party, nil)
	s.m.Conf.Owner.WithLabelValues("rehomed").Inc()
	c.addTrace(fmt.Sprintf("Conference owner %s lost: the call continues in room %s on this node", f.owner, cfg.Number))
	go c.confFlow(room, p)
}

// --- recovery on this node: taking over a participant ---------------------------

// confTakeoverJoin rejoins a participant whose dialog this node took over
// from a dead or draining node: the phone was re-INVITEd onto party, and the
// participant enters the room again (asking for the PIN again, if there is
// one). This node must be able to mix the room: it takes the lease from the
// dead owner, or already hosts it.
func (c *call) confTakeoverJoin(number string, party *media.Party, answer []byte) {
	s := c.s
	defer contain(s.log, "conference takeover join")
	snap := s.deps.Snapshots.Current()
	var (
		cfg snapshot.Conference
		ok  bool
	)
	if snap != nil {
		cfg, ok = snap.Conference(number)
	}
	if !ok || !cfg.Enabled {
		party.Close()
		c.addTrace("Conference room gone after the takeover")
		c.confHangupHomed()
		return
	}
	var own confOwnership
	var err error
	for range 10 { // the dead owner's lease may still be draining out
		if own, err = s.confResolve(number); err == nil && own.self {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil || !own.self {
		party.Close()
		c.addTrace("Conference room is mixed on another node: the participant cannot rejoin it from here")
		c.confHangupHomed()
		return
	}
	room, ok := s.confReserve(cfg)
	if !ok {
		party.Close()
		c.addTrace("Conference room full after the takeover")
		c.confHangupHomed()
		return
	}
	p := c.confAttach(room, party, answer)
	c.mu.Lock()
	c.rewritten = number
	c.mu.Unlock()
	c.confFlow(room, p)
}

// confHangupHomed ends a taken-over call.
func (c *call) confHangupHomed() { c.announcementEnd() }

// confNewParty builds the participant's media for a taken-over dialog from
// the phone's replicated offer.
func (s *Server) confNewParty(offer string) ([]byte, *media.Party, error) {
	return s.confParty([]byte(offer))
}
