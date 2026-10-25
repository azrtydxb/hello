// Server paging and 1-to-1 intercom (phonebooks-ptt spec P-12, P-13, P-16).
//
// A page forks one INVITE to every registered contact of every eligible
// member of a paging group, each carrying the vendor's auto-answer headers
// and a sendonly offer; the caller is answered when the first member
// answers, and the caller's audio reaches all answered members through a
// one-way media fan-out (media.Fanout). Hello never forwards what a member
// sends back. The page ends when the caller hangs up, at the page maximum,
// or when every member has gone.
//
// An intercom call is an ordinary two-way call to one extension whose
// legs carry the auto-answer headers. Auto-answer headers are added only on
// the legs of a page or an intercom call that Hello itself originates: the
// legs of every other call (forwarded, transferred, relayed) are built from
// scratch and never copy a header from a request Hello received, so nobody
// can ask for auto-answer on another phone (spec P-16).
package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// Page leg results (the CDR trace and hello_paging_legs_total).
const (
	PageLegAnswered       = "answered"
	PageLegBusy           = "busy"
	PageLegDND            = "dnd"
	PageLegNoAnswer       = "no_answer"
	PageLegNoRegistration = "no_registration"
)

// pageResult is one member's outcome that never became a leg.
type pageResult struct{ ext, result string }

// pageLeg is a page member leg's own state (guarded by pageState.mu).
type pageLeg struct {
	ext      string
	l        *leg
	offer    []byte
	result   string // "" while the leg may still answer
	answered bool
	ended    bool // the member hung up
}

type pageEvent struct{ answered bool }

// pageState is a running page.
type pageState struct {
	group snapshot.PagingGroup
	fan   *media.Fanout
	host  string
	pt    uint8
	// codec is the page's one codec (G.722 when the caller offered it,
	// else its first G.711): the caller's answer, the fan-out and the
	// members' offer all speak it. A G.722 page offers members PCMU as
	// well, and a member that picks it hears a G.711 rendition.
	codec  media.AudioSDP
	events chan pageEvent

	// The tone the caller hears until the first member answers (early
	// media); toneStop ends it. callerAddr is where the caller's SDP wants
	// it.
	callerAddr *net.UDPAddr
	toneStop   context.CancelFunc
	toneDone   chan struct{}

	mu      sync.Mutex
	legs    []*pageLeg
	skipped []pageResult
	closed  bool
	// report is every member's outcome, one row per leg or skipped member,
	// set once by pageStop for the CDR.
	report []cdr.PageLeg
}

// results returns the per-member results for the CDR (nil for a page that
// never started).
func (ps *pageState) results() []cdr.PageLeg {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return slices.Clone(ps.report)
}

// autoAnswerHeaders are the headers that make a phone answer by itself
// (spec P-12 vendor table). The default set, which Grandstream honours
// ("Allow Auto Answer by Call-Info/Alert-Info"), is Call-Info answer-after=0
// with Alert-Info auto-answer. The other vendors' Alert-Info values are the
// ones their administrator guides name and are still to be confirmed on
// hardware (spec: verify), except Snom's, which the Snom Service Hub
// ("Intercom - Paging feature") gives in full:
// Alert-Info: <http://www.notused.com>;info=alert-autoanswer;delay=0. The vendor is read from the User-Agent of the
// contact's registration.
func autoAnswerHeaders(domain, userAgent string) []sip.Header {
	alert := "auto-answer"
	ua := strings.ToLower(userAgent)
	switch {
	case strings.Contains(ua, "yealink"):
		alert = "Ring Answer"
	case strings.Contains(ua, "polycom"), strings.HasPrefix(ua, "poly"):
		alert = "Auto Answer"
	case strings.Contains(ua, "snom"):
		alert = "<http://www.notused.com>;info=alert-autoanswer;delay=0"
	case strings.Contains(ua, "fanvil"):
		alert = "info=Auto Answer"
	}
	return []sip.Header{
		sip.NewHeader("Call-Info", "<sip:"+domain+">;answer-after=0"),
		sip.NewHeader("Alert-Info", alert),
	}
}

// addPagingHeaders applies a page or intercom leg's specifics to its
// INVITE: a page leg's own sendonly offer, the auto-answer headers, and the
// caller's identity in P-Asserted-Identity.
func (l *leg) addPagingHeaders(req *sip.Request) {
	c, s := l.c, l.c.s
	if l.pl != nil {
		req.RemoveHeader("Content-Type")
		req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		req.SetBody(l.pl.offer)
	}
	c.mu.Lock()
	auto := c.autoAnswer
	c.mu.Unlock()
	if l.pl == nil && !auto {
		return
	}
	for _, h := range pagingHeaders(s.cfg.Domain, l.binding.UserAgent, c.callerName, c.callerNum) {
		req.AppendHeader(h)
	}
}

// pagingHeaders are the headers a page or intercom INVITE carries beyond a
// plain call's: the vendor's auto-answer headers and the caller's identity
// in P-Asserted-Identity.
func pagingHeaders(domain, userAgent, callerName, callerNum string) []sip.Header {
	name := strings.NewReplacer(`"`, ``, `\`, ``, "\r", ``, "\n", ``).Replace(callerName)
	return append(autoAnswerHeaders(domain, userAgent),
		sip.NewHeader("P-Asserted-Identity", fmt.Sprintf(`"%s" <sip:%s@%s>`, name, callerNum, domain)))
}

// isPageOrIntercomCode reports whether dialled is a paging group's code or
// an intercom prefix call (a trunk dialling one is refused, spec P-16).
func isPageOrIntercomCode(snap *snapshot.Snapshot, dialled string) bool {
	if _, ok := snap.PagingGroup(dialled); ok {
		return true
	}
	fc, _, ok := matchFeatureCode(snap, dialled)
	return ok && fc.Action == ActionIntercom
}

// busyExtensions is the set of extensions in a connected call: the cluster's
// live calls (every node publishes its own) plus this node's, which may not
// have been published yet. A state failure leaves the set smaller, never
// failing the call.
func (s *Server) busyExtensions() map[string]bool {
	busy := map[string]bool{}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls, err := s.deps.State.Calls(ctx)
	if err != nil {
		s.log.Debug("busy check: live calls unavailable", "error", err)
	}
	for _, lc := range calls {
		if lc.State == "connected" {
			busy[lc.From], busy[lc.To] = true, true
			if lc.Callee != "" {
				busy[lc.Callee] = true
			}
		}
	}
	s.mu.Lock()
	local := make([]*call, 0, len(s.calls))
	for c := range s.calls {
		local = append(local, c)
	}
	s.mu.Unlock()
	for _, c := range local {
		c.mu.Lock()
		if c.connected && !c.ended {
			busy[c.callerNum], busy[c.dialled], busy[c.rewritten] = true, true, true
			if c.winner != nil && c.winner.binding.Extension != "" {
				busy[c.winner.binding.Extension] = true
			}
		}
		c.mu.Unlock()
	}
	return busy
}

// extensionBindings are the live bindings of an extension's devices except
// the calling device. A live-state failure is an error, never "no devices".
func (c *call) extensionBindings(snap *snapshot.Snapshot, ext string) ([]livestate.Binding, error) {
	s := c.s
	var out []livestate.Binding
	for _, d := range snap.DevicesForExtension(ext) {
		if d.Username == c.callerDevice {
			continue
		}
		ctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
		cancel()
		if err != nil {
			return nil, err
		}
		for _, b := range bs {
			if c.isCallerContact(b) {
				continue
			}
			out = append(out, b)
		}
	}
	return out, nil
}

// handleIntercom places a 1-to-1 intercom call (spec P-13): the dialled
// code is the intercom prefix and arg the extension. It respects DND, never
// cuts into a call (a busy callee gets a normal ring) and rings normally
// when the extension does not allow intercom. It does not follow the
// extension's forwarding: the call is for that phone.
func (c *call) handleIntercom(tx sip.ServerTransaction, arg string) {
	s, req := c.s, c.inv
	c.mu.Lock()
	c.kind = cdr.KindIntercom
	c.mu.Unlock()
	snap := s.deps.Snapshots.Current()
	deny := func(code int, why, result string) {
		s.m.IntercomTotal.WithLabelValues("denied").Inc()
		s.respond(tx, req, code, statusText(code))
		c.record(code, cdr.SideSystem, why, result)
	}
	e, ok := snap.Extension(arg)
	switch {
	case arg == "" || !ok:
		deny(sip.StatusNotFound, "intercom target is not an extension", ResultNotFound)
		return
	case arg == c.callerNum:
		deny(sip.StatusBusyHere, "intercom to the caller's own extension", ResultBusy)
		return
	case e.DND:
		deny(sip.StatusGlobalDecline, "DND", ResultBusy)
		return
	}
	c.mu.Lock()
	c.rewritten = arg
	c.mu.Unlock()
	targets, err := c.extensionBindings(snap, arg)
	if err != nil {
		s.stateDown(tx, req, "bindings", err)
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "live state unavailable", ResultFailed)
		return
	}
	if len(targets) == 0 {
		deny(sip.StatusTemporarilyUnavailable, "no registered device", ResultUnavailable)
		return
	}
	auto := e.AllowIntercom && !s.busyExtensions()[arg]
	c.mu.Lock()
	c.autoAnswer = auto
	c.mu.Unlock()
	switch {
	case auto:
		c.addTrace(fmt.Sprintf("Intercom to extension %s: auto-answer", arg))
		s.m.IntercomTotal.WithLabelValues("auto_answer").Inc()
	case !e.AllowIntercom:
		c.addTrace(fmt.Sprintf("Intercom to extension %s: not allowed, rings normally", arg))
		s.m.IntercomTotal.WithLabelValues("ring").Inc()
	default:
		c.addTrace(fmt.Sprintf("Intercom to extension %s: in a call, rings normally", arg))
		s.m.IntercomTotal.WithLabelValues("ring").Inc()
	}
	c.considerAnchor(snap, bindingEndpoint(targets[0], arg))
	if !c.begin(req, tx) {
		return
	}
	c.forkBindings(targets)
	c.setup(len(targets))
}

// pageBinding is one member contact to page.
type pageBinding struct {
	ext string
	b   livestate.Binding
}

// handlePage pages a group (spec P-12).
func (c *call) handlePage(tx sip.ServerTransaction, snap *snapshot.Snapshot, g snapshot.PagingGroup) {
	s, req := c.s, c.inv
	c.mu.Lock()
	c.kind = cdr.KindPage
	c.rewritten = g.Name
	c.mu.Unlock()
	deny := func(code int, why, metric, result string) {
		s.m.PagingTotal.WithLabelValues(metric).Inc()
		s.respond(tx, req, code, statusText(code))
		c.record(code, cdr.SideSystem, why, result)
	}
	switch {
	case !g.Enabled:
		deny(sip.StatusForbidden, "paging group "+g.Name+" is disabled", "denied", ResultFailed)
		return
	case len(g.Callers) > 0 && !slices.Contains(g.Callers, c.callerNum):
		deny(sip.StatusForbidden, "extension "+c.callerNum+" may not page "+g.Name, "denied", ResultFailed)
		return
	}
	full, err := media.ParseAudioSDP(req.Body())
	if err != nil {
		deny(sip.StatusNotAcceptableHere, "page needs a G.711 or G.722 offer: "+err.Error(), "failed", ResultFailed)
		return
	}
	off, ok := full.Pick("g722", "g711")
	if !ok {
		deny(sip.StatusNotAcceptableHere, "page needs a G.711 or G.722 offer", "failed", ResultFailed)
		return
	}
	off.Events = nil // a page carries no DTMF
	off.Codecs = off.Codecs[:1]
	off.DTMFPayloadType, off.DTMFRate = 0, 0
	emergency := g.Priority == snapshot.PagingEmergency
	busy := s.busyExtensions()
	ps := &pageState{group: g, host: s.anchorHostOr(off.Address), pt: off.PayloadType, codec: off, callerAddr: udpAddr(off.Address, off.Port)}
	var targets []pageBinding
	for _, ext := range g.Members {
		if ext == c.callerNum {
			continue // the caller does not page itself
		}
		e, _ := snap.Extension(ext)
		switch {
		case e.DND && !emergency:
			ps.skipped = append(ps.skipped, pageResult{ext, PageLegDND})
			continue
		case busy[ext]:
			ps.skipped = append(ps.skipped, pageResult{ext, PageLegBusy})
			continue
		}
		bs, err := c.extensionBindings(snap, ext)
		if err != nil {
			s.stateDown(tx, req, "bindings", err)
			c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "live state unavailable", ResultFailed)
			return
		}
		if len(bs) == 0 {
			ps.skipped = append(ps.skipped, pageResult{ext, PageLegNoRegistration})
			continue
		}
		for _, b := range bs {
			targets = append(targets, pageBinding{ext, b})
		}
	}
	for _, sk := range ps.skipped {
		c.addTrace(fmt.Sprintf("Page member %s skipped: %s", sk.ext, sk.result))
		s.m.PagingLegs.WithLabelValues(sk.result).Inc()
	}
	if len(targets) == 0 && g.Multicast == nil {
		deny(sip.StatusBusyHere, "no reachable page member", "no_answer", ResultBusy)
		return
	}
	if len(targets) > s.cfg.PageFanout {
		deny(sip.StatusBusyHere, fmt.Sprintf("page would need %d legs, the limit is %d", len(targets), s.cfg.PageFanout), "capacity", ResultBusy)
		return
	}
	c.mu.Lock()
	c.page = ps // before admission: another page reads the owner's group
	c.mediaMode = "anchored"
	c.mu.Unlock()
	preempted, ok := s.pageAdmit(c, g)
	if !ok {
		deny(sip.StatusBusyHere, "page capacity or group "+g.Name+" is busy", "capacity", ResultBusy)
		return
	}
	if preempted != nil {
		preempted.terminate("preempted by an emergency page")
	}
	if !c.begin(req, tx) {
		s.pageRelease(c)
		return
	}
	fan, err := media.NewFanoutCodec(s.cfg.RTPPortMin, s.cfg.RTPPortMax, off.Codec)
	if err != nil {
		c.addTrace("Page media failed: " + err.Error())
		c.respondA(sip.StatusServiceUnavailable, "Service Unavailable")
		c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "no media port for the page", ResultFailed)
		return
	}
	ps.fan = fan
	if mc := g.Multicast; mc != nil {
		err := fan.EnableMulticast(udpAddr(mc.Address, mc.Port), mc.PT, s.cfg.PageMulticastIface, s.cfg.PageMulticastTTL)
		if err != nil {
			fan.Close()
			ps.fan = nil
			c.addTrace("Page multicast failed: " + err.Error())
			c.respondA(sip.StatusServiceUnavailable, "Service Unavailable")
			c.end(sip.StatusServiceUnavailable, cdr.SideSystem, "multicast paging is unavailable", ResultFailed)
			return
		}
		c.addTrace(fmt.Sprintf("Page also sent to multicast %s:%d", mc.Address, mc.Port))
	}
	ps.events = make(chan pageEvent, len(targets)+1)
	offer := pageMemberOffer(ps, fan.MemberPort())
	c.addTrace(fmt.Sprintf("Paging group %s (%s): %d legs", g.Name, g.Priority, len(targets)))
	if g.Multicast == nil {
		c.pageEarlyMedia(ps)
	}
	c.mu.Lock()
	for _, t := range targets {
		pl := &pageLeg{ext: t.ext, offer: offer}
		pl.l = c.addLeg(&leg{binding: t.b, pl: pl})
		ps.mu.Lock()
		ps.legs = append(ps.legs, pl)
		ps.mu.Unlock()
	}
	legs := slices.Clone(ps.legs)
	c.mu.Unlock()
	for _, pl := range legs {
		go pl.l.runPage()
	}
	if g.Multicast != nil {
		// Multicast has no answer to wait for: the caller is connected at
		// once and the member legs, if any, join as they answer.
		defer close(c.setupDone)
		c.pageAnswerCaller()
		return
	}
	c.pageSetup(len(legs))
}

// pageEarlyMedia answers the caller's INVITE with 183 Session Progress and
// starts the page tone, which the caller hears instead of ringback until the
// first member answers. The SDP is sendrecv, the same as the final answer,
// so the phone's media does not change when the page connects; Hello only
// ever sends the tone and never sends a member's audio back. A phone that
// ignores early media just hears nothing until the answer.
func (c *call) pageEarlyMedia(ps *pageState) {
	body := ps.codec.Build(ps.host, ps.fan.CallerPort(), "sendrecv")
	c.s.m.response(sip.StatusSessionInProgress)
	err := c.dss.Respond(sip.StatusSessionInProgress, "Session Progress", body, sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		c.s.log.Debug("page early media failed", "correlation_id", c.id, "error", err)
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	ps.toneStop, ps.toneDone = cancel, make(chan struct{})
	go func() {
		defer close(ps.toneDone)
		defer contain(c.s.log, "page tone")
		_ = ps.fan.PlayCaller(ctx, media.PageTonePCM(), ps.callerAddr, true)
	}()
	c.addTrace("Page tone to the caller")
}

// pageToneStop ends the page tone and waits for it, so it never overlaps
// the answer or outlives the media. Safe to call any number of times.
func (ps *pageState) pageToneStop() {
	if ps.toneStop != nil {
		ps.toneStop()
		<-ps.toneDone
	}
}

// pageAdmit takes the group's single active-page slot and one of the node's
// page slots. An emergency page preempts a running normal page of the
// group (returned for the caller to end); anything else gets busy.
func (s *Server) pageAdmit(c *call, g snapshot.PagingGroup) (preempted *call, ok bool) {
	s.pageMu.Lock()
	defer s.pageMu.Unlock()
	if cur := s.pages[g.ID]; cur != nil {
		if g.Priority != snapshot.PagingEmergency || cur.page.group.Priority != snapshot.PagingNormal {
			return nil, false
		}
		preempted = cur
	} else if len(s.pages) >= s.cfg.PageConcurrent {
		return nil, false
	}
	s.pages[g.ID] = c
	return preempted, true
}

// pageRelease frees the call's page slots (only if it still owns them).
func (s *Server) pageRelease(c *call) {
	s.pageMu.Lock()
	defer s.pageMu.Unlock()
	for id, cur := range s.pages {
		if cur == c {
			delete(s.pages, id)
		}
	}
}

// pageSetup waits for the first member to answer, every leg to fail, the
// answer timeout or the caller to cancel, and answers the caller.
func (c *call) pageSetup(pending int) {
	defer close(c.setupDone)
	ps := c.page
	timer := time.NewTimer(c.s.cfg.PageAnswerTimeout)
	defer timer.Stop()
	for {
		select {
		case ev := <-ps.events:
			if ev.answered {
				c.pageAnswerCaller()
				return
			}
			pending--
			if pending > 0 {
				continue
			}
			if c.closeSetup() {
				c.pageFailCaller("no member answered", ResultBusy)
				return
			}
			if c.setupAbort() {
				return
			}
		case <-c.canceled:
			if c.closeSetup() {
				c.cancelForks(nil)
				c.s.m.PagingTotal.WithLabelValues("no_answer").Inc()
				c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
				return
			}
			if c.setupAbort() {
				return
			}
		case <-c.aborted:
			if c.setupAbort() {
				return
			}
		case <-timer.C:
			if c.closeSetup() {
				c.pageFailCaller("no member answered in time", ResultNoAnswer)
				return
			}
			if c.setupAbort() {
				return
			}
		}
	}
}

// pageFailCaller answers a page nobody took: the caller hears busy.
func (c *call) pageFailCaller(reason, result string) {
	c.cancelForks(nil)
	c.s.m.PagingTotal.WithLabelValues("no_answer").Inc()
	c.respondA(sip.StatusBusyHere, "Busy Here")
	c.end(sip.StatusBusyHere, cdr.SideCallee, reason, result)
}

// pageAnswerCaller answers the caller once a member answered. The answer
// is sendrecv like the early media's (the phone's media does not change),
// but Hello takes the caller's audio and sends nothing back.
func (c *call) pageAnswerCaller() {
	ps := c.page
	ps.pageToneStop()
	c.mu.Lock()
	cancelled := c.isCancelled
	aborted := closed(c.aborted)
	c.connected = !cancelled && !aborted
	c.setupClosed = true
	if c.connected {
		c.answerTime = time.Now()
	}
	c.mu.Unlock()
	if cancelled {
		c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
		go c.pageByeMembers()
		return
	}
	if aborted {
		go c.pageByeMembers()
		c.abortCaller()
		return
	}
	c.addTrace("Page established")
	c.s.m.PagingTotal.WithLabelValues("answered").Inc()
	c.publish()
	body := ps.codec.Build(ps.host, ps.fan.CallerPort(), "sendrecv")
	res := sip.NewResponseFromRequest(c.dss.InviteRequest, sip.StatusOK, "OK", body)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res.AppendHeader(sip.NewHeader("Allow", allow))
	c.s.m.response(sip.StatusOK)
	if err := c.dss.WriteResponse(res); err == nil {
		c.mu.Lock()
		if !c.ended {
			// A page ends at the page maximum, never the call maximum.
			c.maxTimer = time.AfterFunc(c.s.cfg.PageMaxDuration, c.expire)
		}
		c.mu.Unlock()
		return
	}
	c.mu.Lock()
	cancelled = c.isCancelled
	c.connected = false
	c.mu.Unlock()
	go c.pageByeMembers()
	if cancelled {
		c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
		return
	}
	c.s.log.Info("page caller did not acknowledge the answer", "correlation_id", c.id)
	c.byeA()
	c.end(sip.StatusOK, cdr.SideSystem, "no ACK from caller", ResultFailed)
}

// runPage sends a page leg's INVITE and reports its outcome once.
func (l *leg) runPage() {
	c, pl := l.c, l.pl
	defer func() {
		if r := recover(); r != nil {
			c.s.log.Error("recovered panic", "in", "page leg", "panic", fmt.Sprint(r))
			c.pageLegFailed(pl, PageLegNoAnswer)
		}
	}()
	req, err := l.invite()
	if err != nil {
		c.s.log.Warn("bad binding contact", "contact", l.binding.ContactURI, "error", err)
		c.pageLegFailed(pl, PageLegNoRegistration)
		return
	}
	dcs, err := c.s.uac.WriteInvite(context.Background(), req)
	if err != nil {
		c.s.log.Debug("page INVITE failed", "contact", l.binding.ContactURI, "error", err)
		c.pageLegFailed(pl, PageLegNoAnswer)
		return
	}
	l.mu.Lock()
	l.dcs = dcs
	l.mu.Unlock()
	err = dcs.WaitAnswer(l.ctx, sipgo.AnswerOptions{})
	if res := dcs.InviteResponse; err == nil || (res != nil && res.IsSuccess()) {
		c.pageLegAnswered(l)
		return
	}
	result := PageLegNoAnswer
	var de *sipgo.ErrDialogResponse
	if errors.As(err, &de) && isBusy(de.Res.StatusCode) {
		result = PageLegBusy
	}
	c.pageLegFailed(pl, result)
}

// pageLegAnswered takes an answered member into the fan-out: it must
// answer with the caller's codec and a media address; anything else (or a
// member answering after the page ended) is acknowledged and hung up.
func (c *call) pageLegAnswered(l *leg) {
	ps, pl := c.page, l.pl
	ans, err := media.ParseAudioSDP(l.session().InviteResponse.Body())
	addr := udpAddr(ans.Address, ans.Port)
	ps.mu.Lock()
	closedNow := ps.closed
	// A member answers with the page's codec or, on a G.722 page, PCMU.
	native := err == nil && ans.PayloadType == ps.pt
	g711 := err == nil && ps.pt == 9 && ans.PayloadType == 0
	usable := addr != nil && (native || g711)
	if !closedNow && usable {
		pl.answered, pl.result = true, PageLegAnswered
	}
	ps.mu.Unlock()
	if closedNow || !usable {
		l.bye()
		if !closedNow {
			c.pageLegFailed(pl, PageLegNoAnswer)
		}
		return
	}
	l.ack(nil, "")
	if g711 {
		ps.fan.AddFallbackTarget(l.callID, addr)
	} else {
		ps.fan.AddTarget(l.callID, addr)
	}
	select {
	case ps.events <- pageEvent{answered: true}:
	default:
	}
}

// pageLegFailed records a member leg that will never answer.
func (c *call) pageLegFailed(pl *pageLeg, result string) {
	ps := c.page
	ps.mu.Lock()
	if pl.result == "" {
		pl.result = result
	}
	ps.mu.Unlock()
	select {
	case ps.events <- pageEvent{}:
	default:
	}
}

// pageStop ends the page's media and pending legs and records every
// member's result in the call trace. It runs once, from end(); answered
// legs are hung up by pageByeMembers.
func (c *call) pageStop() {
	ps := c.page
	if ps == nil {
		return
	}
	ps.mu.Lock()
	if ps.closed {
		ps.mu.Unlock()
		return
	}
	ps.closed = true
	legs := slices.Clone(ps.legs)
	for _, pl := range legs {
		if !pl.answered && pl.result == "" {
			pl.result = PageLegNoAnswer
		}
		c.s.m.PagingLegs.WithLabelValues(pl.result).Inc()
	}
	ps.report = ps.report[:0]
	for _, sk := range ps.skipped {
		ps.report = append(ps.report, cdr.PageLeg{Extension: sk.ext, Result: sk.result})
	}
	for _, pl := range legs {
		ps.report = append(ps.report, cdr.PageLeg{Extension: pl.ext, Device: pl.l.binding.Device, Result: pl.result})
	}
	ps.mu.Unlock()
	ps.pageToneStop()
	for _, pl := range legs {
		if !pl.answered {
			pl.l.cancel()
		}
		c.addTrace(fmt.Sprintf("Page leg %s (%s): %s", pl.ext, pl.l.binding.Device, pl.result))
	}
	if ps.fan != nil {
		st := ps.fan.Stats()
		c.addTrace(fmt.Sprintf("Page media: %d packets fanned out, %d datagrams from members dropped", st.Packets, st.Inbound))
		ps.fan.Close()
	}
	c.s.pageRelease(c)
}

// pageByeMembers hangs up every member leg that answered and has not
// already hung up, in parallel.
func (c *call) pageByeMembers() {
	defer contain(c.s.log, "page member BYE")
	ps := c.page
	ps.mu.Lock()
	var legs []*pageLeg
	for _, pl := range ps.legs {
		if pl.answered && !pl.ended {
			pl.ended = true
			legs = append(legs, pl)
		}
	}
	ps.mu.Unlock()
	var wg sync.WaitGroup
	for _, pl := range legs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer contain(c.s.log, "page member BYE")
			pl.l.bye()
		}()
	}
	wg.Wait()
}

// pageHangup ends a connected page because the caller hung up: every
// answered member leg gets a BYE.
func (c *call) pageHangup(side string) {
	c.mu.Lock()
	if c.hungUp {
		c.mu.Unlock()
		return
	}
	c.hungUp = true
	c.mu.Unlock()
	c.end(sip.StatusOK, side, "", ResultAnswered)
	go func() {
		defer c.release()
		defer contain(c.s.log, "page hangup")
		c.pageByeMembers()
	}()
}

// pageEndBoth ends a connected page for a system reason (page maximum,
// drain, preemption, every member gone): BYE to the caller and the members.
func (c *call) pageEndBoth(reason string) {
	c.mu.Lock()
	if c.hungUp || c.ended {
		c.mu.Unlock()
		return
	}
	c.hungUp = true
	c.mu.Unlock()
	c.end(sip.StatusOK, cdr.SideSystem, reason, ResultAnswered)
	go func() {
		defer c.release()
		defer contain(c.s.log, "page end")
		done := make(chan struct{})
		go func() { defer close(done); c.pageByeMembers() }()
		c.byeA()
		<-done
	}()
}

// matchPageLeg accepts an in-dialog request from an answered member leg:
// the dialog's tags from the address its 2xx came from.
func (s *Server) matchPageLeg(ref dialogRef, req *sip.Request) (dialogRef, bool) {
	l := ref.leg
	if l == nil || l.pl == nil {
		return dialogRef{}, false
	}
	ps := ref.c.page
	ps.mu.Lock()
	answered := l.pl.answered
	ps.mu.Unlock()
	if !answered {
		return dialogRef{}, false
	}
	res := l.session().InviteResponse
	want, err1 := sip.DialogIDFromResponse(res)
	got, err2 := sip.DialogIDFromRequestUAC(req)
	return ref, err1 == nil && err2 == nil && got == want && req.Source() == res.Source()
}

// pageBye handles a BYE on a page: the caller's ends the page; a member's
// removes only that member, and the page ends when none is left.
func (s *Server) pageBye(c *call, ref dialogRef, req *sip.Request, tx sip.ServerTransaction) {
	c.mu.Lock()
	connected := c.connected
	c.mu.Unlock()
	if !connected {
		s.respond(tx, req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist")
		return
	}
	if ref.leg == nil {
		if err := c.dss.ReadBye(req, tx); err != nil {
			s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
			return
		}
		s.m.response(sip.StatusOK)
		c.hangup(cdr.SideCaller)
		return
	}
	if err := ref.leg.session().ReadBye(req, tx); err != nil {
		s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return
	}
	s.m.response(sip.StatusOK)
	c.pageMemberGone(ref.leg)
}

// pageMemberGone drops a member that hung up from the fan-out and ends the
// page when no member is left and none can still answer.
func (c *call) pageMemberGone(l *leg) {
	ps := c.page
	ps.fan.RemoveTarget(l.callID)
	ps.mu.Lock()
	l.pl.ended = true
	pending := false
	for _, pl := range ps.legs {
		if pl.result == "" && !pl.answered {
			pending = true
		}
		if pl.answered && !pl.ended {
			pending = true
		}
	}
	ps.mu.Unlock()
	c.addTrace(fmt.Sprintf("Page member %s hung up", l.pl.ext))
	if !pending && ps.group.Multicast == nil {
		go c.endBoth("every page member hung up")
	}
}

// pageMemberOffer is the sendonly SDP offered to page members: the page's
// codec and, on a G.722 page, PCMU as the fallback for a phone without
// G.722 (the fan-out renders it from the G.722 stream).
func pageMemberOffer(ps *pageState, port int) []byte {
	codecs := ps.codec.Codecs
	if ps.pt == 9 {
		pcmu, _ := media.StaticCodec(0)
		codecs = append(append([]media.Codec(nil), codecs...), pcmu)
	}
	return media.BuildSDP(ps.host, port, codecs, nil, "sendonly")
}
