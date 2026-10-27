// RFC 4028 session timers and dead-media teardown (phone-feature-parity S-9).
//
// A B2BUA has two dialogs, and each runs its own session timer: Hello's
// Session-Expires and refresher choice on the caller's dialog are the 2xx's,
// and on the callee's dialog they are the INVITE's request and the phone's
// answer. Whoever is the refresher sends an UPDATE (or a re-INVITE with the
// last SDP of the leg when the phone does not list UPDATE) at half the
// interval; when the refresher is the phone, Hello tears the call down if no
// refresh has arrived shortly before the interval ends. Either way a call
// whose far end vanished (a handset that left the Wi-Fi) is cleared within
// one interval instead of at MaxCallDuration.
//
// Hello is the refresher on every leg whose phone does not take the role, so
// a phone without RFC 4028 support is covered too. A leg to a URI target gets
// no request from Hello. A trunk's own setting decides its calls (trunkTimers):
// "on" requests Session-Expires on the legs to it and applies Hello's interval
// to a call from it even when the carrier sent none, "off" does neither, and
// "default" follows Config.SessionTimersTrunks; a trunk call left unforced is
// covered only when the carrier sends Session-Expires itself.
//
// A parked, retrieved, picked-up, returned or transferred call moves to other
// dialogs: the party a parked call keeps stays on its timer, the retriever,
// the picker, the parker a returned call rings, and the parties of a
// transferred or bridged call each get one of their own (see the link helpers
// below).
//
// Dead media: an anchored, bridged call that carried no RTP in either
// direction for Config.MediaDeadAfter is ended unless it is on hold.
package sip

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/emiago/sipgo/sip"
	"github.com/prometheus/client_golang/prometheus"
)

// The two dialogs of a bridged call.
const (
	sessCaller = 0
	sessCallee = 1
)

const (
	// sessionMaxRetries bounds the refresh retries after a 422 or a 491.
	sessionMaxRetries = 2
	// sessionGlareRetry is the wait after a 491 (both ends sent a request).
	sessionGlareRetry = 2500 * time.Millisecond
	// sessionExpiryMargin is how long before the interval ends a remote
	// refresher is declared silent (RFC 4028 section 10: the smaller of 32 s
	// and a third of the interval).
	sessionExpiryMargin = 32 * time.Second
	// defaultRefreshTimeout bounds one refresh transaction (a phone off the
	// Wi-Fi never answers; the call ends when this runs out).
	defaultRefreshTimeout = 8 * time.Second
)

// sessLeg is one dialog's negotiated session timer.
type sessLeg struct {
	interval time.Duration
	local    bool // Hello refreshes this dialog; false: the phone does
	update   bool // the phone lists UPDATE in Allow
	timer    *time.Timer
}

// sessionState is a call's session timers, the SDP last sent on each dialog
// (the body of a re-INVITE refresh) and the dead-media watch. mu is its own:
// the relay's callbacks and the timers reach it without holding the call's.
type sessionState struct {
	mu      sync.Mutex
	leg     [2]*sessLeg
	sdp     [2][]byte
	stopped bool

	// addr is each dialog's media address as its SDP last named it.
	addr [2]string

	// bridge is the SDP an attended transfer's bridge last sent on each
	// dialog of the call it re-pointed, for the bridged call's refreshes.
	bridge [2][]byte

	mediaTimer *time.Timer
	mediaTouch time.Time // the last moment media was not expected (hold, start)
}

// SessionMetrics counts the calls Hello tears down for a dead far end.
type SessionMetrics struct {
	// Teardowns counts calls ended by session timers or dead media, by reason:
	// refresh_failed, refresh_missing or media_dead.
	Teardowns *prometheus.CounterVec
	// Refreshes counts the refreshes Hello sent, by method and result.
	Refreshes *prometheus.CounterVec
}

func newSessionMetrics(reg prometheus.Registerer) *SessionMetrics {
	m := &SessionMetrics{
		Teardowns: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_sip_session_teardowns_total",
			Help: "Calls ended because the far end went silent, by reason (refresh_failed, refresh_missing, media_dead).",
		}, []string{"reason"}),
		Refreshes: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "hello_sip_session_refreshes_total",
			Help: "RFC 4028 session refreshes Hello sent, by method (UPDATE, INVITE) and result (ok, failed).",
		}, []string{"method", "result"}),
	}
	reg.MustRegister(m.Teardowns, m.Refreshes)
	return m
}

// ---- header parsing -------------------------------------------------------

// sessionExpires reads a Session-Expires header (long or compact form).
func sessionExpires(get func(string) sip.Header) (secs int, refresher string, ok bool) {
	h := get("Session-Expires")
	if h == nil {
		h = get("x")
	}
	if h == nil {
		return 0, "", false
	}
	parts := strings.Split(h.Value(), ";")
	n, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || n <= 0 {
		return 0, "", false
	}
	for _, p := range parts[1:] {
		k, v, _ := strings.Cut(strings.TrimSpace(p), "=")
		if strings.EqualFold(k, "refresher") {
			refresher = strings.ToLower(strings.TrimSpace(v))
		}
	}
	return n, refresher, true
}

// minSEOf reads a Min-SE header in seconds.
func minSEOf(get func(string) sip.Header) (int, bool) {
	h := get("Min-SE")
	if h == nil {
		return 0, false
	}
	n, err := strconv.Atoi(strings.TrimSpace(strings.Split(h.Value(), ";")[0]))
	return n, err == nil && n > 0
}

// listHas reports whether any header in hs lists the token (comma separated,
// case-insensitive).
func listHas(hs []sip.Header, token string) bool {
	for _, h := range hs {
		for _, t := range strings.Split(h.Value(), ",") {
			if strings.EqualFold(strings.TrimSpace(t), token) {
				return true
			}
		}
	}
	return false
}

func secondsOf(d time.Duration) string { return strconv.Itoa(int(d / time.Second)) }

// ---- negotiation ----------------------------------------------------------

// sessionTooSmall answers an initial INVITE whose Session-Expires is below
// Hello's minimum with 422 and Min-SE (RFC 4028 section 6), and reports
// whether it did.
func (s *Server) sessionTooSmall(req *sip.Request, tx sip.ServerTransaction) bool {
	if s.cfg.SessionExpires <= 0 {
		return false
	}
	secs, _, ok := sessionExpires(req.GetHeader)
	if !ok || time.Duration(secs)*time.Second >= s.cfg.SessionMinSE {
		return false
	}
	s.logInviteReject(req, "", 422, "session interval too small")
	s.respond(tx, req, 422, "Session Interval Too Small", sip.NewHeader("Min-SE", secondsOf(s.cfg.SessionMinSE)))
	return true
}

// timerPolicy is how a trunk's setting reads for a dialog with it.
type timerPolicy int

const (
	timersPassive timerPolicy = iota // timers only when the far end asks for them
	timersForced                     // Hello requests and applies its interval
	timersOff                        // no timer at all
)

// trunkTimers resolves a trunk's session-timers setting against Hello's own:
// a global interval of 0 turns timers off everywhere, "on" and "off" are the
// trunk's own word, and "default" (or an unset value) is
// Config.SessionTimersTrunks.
func (s *Server) trunkTimers(t *routing.Trunk) timerPolicy {
	if s.cfg.SessionExpires <= 0 {
		return timersOff
	}
	switch t.SessionTimers {
	case routing.SessionTimersOn:
		return timersForced
	case routing.SessionTimersOff:
		return timersOff
	}
	if s.cfg.SessionTimersTrunks {
		return timersForced
	}
	return timersPassive
}

// callerLeg negotiates the caller's dialog of this call: a phone's call and
// a call from a trunk forced on get Hello's interval when the caller named
// none, a call from a trunk set off gets no timer, and any other trunk call
// gets one only when the carrier sent Session-Expires.
func (c *call) callerLeg() *sessLeg {
	s := c.s
	if t := c.inTrunk; t != nil {
		switch s.trunkTimers(t) {
		case timersOff:
			return nil
		case timersForced:
			return s.callerSession(c.inv, true)
		}
		return s.callerSession(c.inv, false)
	}
	return s.callerSession(c.inv, c.callerDevice != "")
}

// callerSession negotiates the caller's dialog, where Hello is the UAS. A
// Session-Expires the caller sent is honoured, with the refresher it names
// (uac: the caller refreshes) or else Hello. Without one, a call from a phone
// still gets Hello's interval with Hello as refresher; a trunk's call gets
// none. nil means no timer on this dialog.
func (s *Server) callerSession(inv *sip.Request, phone bool) *sessLeg {
	if s.cfg.SessionExpires <= 0 || inv == nil {
		return nil
	}
	st := &sessLeg{update: listHas(inv.GetHeaders("Allow"), "UPDATE")}
	if secs, refresher, ok := sessionExpires(inv.GetHeader); ok {
		st.interval = time.Duration(secs) * time.Second
		st.local = refresher != "uac"
		return st
	}
	if !phone {
		return nil
	}
	st.interval, st.local = s.cfg.SessionExpires, true
	return st
}

// callerSessionHeaders are the headers of the 2xx that confirm st.
func callerSessionHeaders(inv *sip.Request, st *sessLeg) []sip.Header {
	if st == nil {
		return nil
	}
	refresher := "uac"
	if st.local {
		refresher = "uas"
	}
	hdrs := []sip.Header{
		sip.NewHeader("Session-Expires", secondsOf(st.interval)+";refresher="+refresher),
		sip.NewHeader("Supported", "timer"),
	}
	if !st.local && listHas(inv.GetHeaders("Supported"), "timer") {
		hdrs = append(hdrs, sip.NewHeader("Require", "timer"))
	}
	return hdrs
}

// wantsSessionTimer reports whether the INVITE to this fork asks for session
// timers: legs to a phone binding of a vendor whose phones implement RFC 4028
// (prov.SupportsSessionTimers), and legs to a trunk whose setting forces them.
func (l *leg) wantsSessionTimer() bool {
	if l == nil || l.uri != nil || l.pl != nil || l.c.s.cfg.SessionExpires <= 0 {
		return false
	}
	if l.trunk != nil {
		return l.c.s.trunkTimers(l.trunk) == timersForced
	}
	v, model, _ := prov.ParseUserAgent(l.binding.UserAgent, "")
	return prov.SupportsSessionTimers(v, model)
}

// addSessionHeaders adds the request for session timers to the fork's INVITE:
// Hello wants to be the refresher (uac).
func (l *leg) addSessionHeaders(req *sip.Request) {
	if !l.wantsSessionTimer() {
		return
	}
	s := l.c.s
	req.AppendHeader(sip.NewHeader("Supported", "timer"))
	req.AppendHeader(sip.NewHeader("Session-Expires", secondsOf(s.cfg.SessionExpires)+";refresher=uac"))
	req.AppendHeader(sip.NewHeader("Min-SE", secondsOf(s.cfg.SessionMinSE)))
}

// calleeSession reads the phone's answer to addSessionHeaders. A phone that
// answers with Session-Expires names the interval and the refresher; one that
// does not know timers leaves Hello refreshing at the requested interval.
func (s *Server) calleeSession(w *leg, bres *sip.Response) *sessLeg {
	if !w.wantsSessionTimer() || bres == nil {
		return nil
	}
	st := &sessLeg{interval: s.cfg.SessionExpires, local: true, update: listHas(bres.GetHeaders("Allow"), "UPDATE")}
	if secs, refresher, ok := sessionExpires(bres.GetHeader); ok {
		if iv := time.Duration(secs) * time.Second; iv >= s.cfg.SessionMinSE {
			st.interval = iv
		}
		st.local = refresher != "uas"
	}
	return st
}

// sessionAnswer records the session timers of a call being answered and
// returns the headers of the caller's 2xx. callerBody is the SDP the caller
// is answered with.
func (c *call) sessionAnswer(w *leg, bres *sip.Response, callerBody []byte) []sip.Header {
	c.ses.mu.Lock()
	defer c.ses.mu.Unlock()
	c.ses.sdp[sessCaller] = append([]byte(nil), callerBody...)
	if dcs := w.session(); dcs != nil && dcs.InviteRequest != nil {
		c.ses.sdp[sessCallee] = append([]byte(nil), dcs.InviteRequest.Body()...)
	}
	if off, err := media.ParseAudioSDP(c.inv.Body()); err == nil {
		c.ses.addr[sessCaller] = sdpAddr(off)
	}
	if bres != nil {
		if ans, err := media.ParseAudioSDP(bres.Body()); err == nil {
			c.ses.addr[sessCallee] = sdpAddr(ans)
		}
	}
	s := c.s
	c.ses.leg[sessCaller] = c.callerLeg()
	c.ses.leg[sessCallee] = s.calleeSession(w, bres)
	return callerSessionHeaders(c.inv, c.ses.leg[sessCaller])
}

// noteSDP records the SDP last sent on a dialog, for a re-INVITE refresh.
func (c *call) noteSDP(idx int, body []byte) {
	if len(body) == 0 {
		return
	}
	c.ses.mu.Lock()
	c.ses.sdp[idx] = append([]byte(nil), body...)
	c.ses.mu.Unlock()
}

// sdpAddr is the media address an SDP names, "" for none or a hold address.
func sdpAddr(a media.AudioSDP) string {
	if ip := net.ParseIP(a.Address); a.Port == 0 || ip == nil || ip.IsUnspecified() {
		return ""
	}
	return net.JoinHostPort(a.Address, strconv.Itoa(a.Port))
}

// relearnMedia points the anchor's leg at the media address a mid-call
// offer names when it differs from the one before (an IP change after a
// Wi-Fi roam): the relay's source policy starts over from the new address,
// vouched by the signalling source of the request that carried it, and the
// old address stops being accepted. A hold's zero address and an unchanged
// address leave the leg alone.
func (c *call) relearnMedia(fromCaller bool, source string, off media.AudioSDP) {
	addr := sdpAddr(off)
	if addr == "" {
		return
	}
	idx, leg := sessCallee, legCallee
	if fromCaller {
		idx, leg = sessCaller, legCaller
	}
	c.ses.mu.Lock()
	prev := c.ses.addr[idx]
	c.ses.addr[idx] = addr
	c.ses.mu.Unlock()
	if prev == "" || prev == addr {
		return
	}
	relay := c.anchorRelay()
	if relay == nil {
		return
	}
	handOverLeg(relay, leg, udpAddr(off.Address, off.Port), source)
	c.addTrace(fmt.Sprintf("Media address of the %s changed %s -> %s; the anchor relearned it", leg, prev, addr))
	c.s.log.Info("media address changed mid-call", "correlation_id", c.id, "leg", leg)
}

// ---- timers ---------------------------------------------------------------

// firstDelay is when st's timer next fires after a refresh: half the interval
// for the refresher, shortly before the end for a phone that must refresh.
func (st *sessLeg) firstDelay() time.Duration {
	if st.local {
		return st.interval / 2
	}
	return st.interval - min(sessionExpiryMargin, st.interval/3)
}

// sessionArm starts the timers once the call is established.
func (c *call) sessionArm() {
	c.ses.mu.Lock()
	defer c.ses.mu.Unlock()
	if c.ses.stopped {
		return
	}
	for i, st := range c.ses.leg {
		if st != nil && st.timer == nil {
			idx := i
			st.timer = time.AfterFunc(st.firstDelay(), func() { c.sessionTick(idx) })
		}
	}
}

// sessionStop stops every timer of the call, for good.
func (c *call) sessionStop() {
	c.ses.mu.Lock()
	defer c.ses.mu.Unlock()
	c.ses.stopped = true
	for _, st := range c.ses.leg {
		if st != nil && st.timer != nil {
			st.timer.Stop()
		}
	}
	if c.ses.mediaTimer != nil {
		c.ses.mediaTimer.Stop()
	}
}

// sessionRearm re-arms leg idx's timer for d; nothing once the call is over.
func (c *call) sessionRearm(idx int, d time.Duration) {
	c.ses.mu.Lock()
	defer c.ses.mu.Unlock()
	st := c.ses.leg[idx]
	if st == nil || c.ses.stopped {
		return
	}
	if st.timer != nil {
		st.timer.Stop()
	}
	st.timer = time.AfterFunc(d, func() { c.sessionTick(idx) })
}

// sessionTick is a timer firing: a refresher refreshes, a waiting side that
// heard no refresh gives up on the call.
func (c *call) sessionTick(idx int) {
	defer contain(c.s.log, "session timer")
	c.ses.mu.Lock()
	st := c.ses.leg[idx]
	stopped := c.ses.stopped
	local := st != nil && st.local
	c.ses.mu.Unlock()
	if st == nil || stopped {
		return
	}
	c.mu.Lock()
	over := c.ended || c.hungUp
	c.mu.Unlock()
	if over {
		return
	}
	if c.isLinked() {
		// A parked or linked call keeps one dialog, its own party's; the
		// other was released with the parker (or belongs to the peer call)
		// and its timer ends here. The party's dialog goes on as before.
		party, ok := c.linkedPartyIdx()
		if !ok {
			c.sessionRearm(idx, st.firstDelay())
			return
		}
		if idx != party {
			return
		}
	}
	if !local {
		c.sessionTeardown("refresh_missing", idx, "no session refresh arrived before the session expired")
		return
	}
	c.sessionRefresh(idx)
}

// sessionRefresh sends one refresh on dialog idx and handles the answer: 2xx
// re-arms the timer, 422 raises the interval, 491 retries after a pause, 405
// or 501 to an UPDATE falls back to a re-INVITE, and anything else (or no
// answer) ends the call.
func (c *call) sessionRefresh(idx int) {
	if !c.beginInDialog() {
		return
	}
	defer c.endInDialog()
	for attempt := 0; ; attempt++ {
		c.ses.mu.Lock()
		st := c.ses.leg[idx]
		if st == nil || c.ses.stopped {
			c.ses.mu.Unlock()
			return
		}
		interval, useUpdate, sdp := st.interval, st.update, c.ses.sdp[idx]
		c.ses.mu.Unlock()
		if len(sdp) == 0 {
			useUpdate = true // no SDP to repeat: only UPDATE can refresh without one
		}
		method := sip.INVITE
		if useUpdate {
			method = sip.UPDATE
		}
		res, err := c.sendRefresh(idx, method, interval, sdp)
		result := "ok"
		if err != nil || res == nil || !res.IsSuccess() {
			result = "failed"
		}
		if m := c.s.m.Session; m != nil {
			m.Refreshes.WithLabelValues(string(method), result).Inc()
		}
		switch {
		case err != nil || res == nil:
			c.sessionTeardown("refresh_failed", idx, fmt.Sprintf("session refresh got no answer: %v", err))
			return
		case c.carrierAlive(idx, res.StatusCode):
			// The carrier answered, so the call is alive; it just does not
			// take the refresh. Try again after another interval.
			c.ses.mu.Lock()
			next := st.firstDelay()
			c.ses.mu.Unlock()
			c.sessionRearm(idx, next)
			return
		case res.IsSuccess():
			c.ses.mu.Lock()
			if secs, _, ok := sessionExpires(res.GetHeader); ok && time.Duration(secs)*time.Second >= c.s.cfg.SessionMinSE {
				st.interval = max(st.interval, time.Duration(secs)*time.Second)
			}
			next := st.firstDelay()
			c.ses.mu.Unlock()
			c.sessionRearm(idx, next)
			return
		case res.StatusCode == 422 && attempt < sessionMaxRetries:
			if m, ok := minSEOf(res.GetHeader); ok {
				c.ses.mu.Lock()
				st.interval = max(st.interval, time.Duration(m)*time.Second)
				c.ses.mu.Unlock()
				continue
			}
		case res.StatusCode == sip.StatusRequestPending && attempt < sessionMaxRetries:
			time.Sleep(sessionGlareRetry)
			continue
		case (res.StatusCode == sip.StatusMethodNotAllowed || res.StatusCode == sip.StatusNotImplemented) && useUpdate && len(sdp) > 0:
			c.ses.mu.Lock()
			st.update = false
			c.ses.mu.Unlock()
			continue
		}
		c.sessionTeardown("refresh_failed", idx, fmt.Sprintf("session refresh answered %d %s", res.StatusCode, res.Reason))
		return
	}
}

// sendRefresh builds and sends one UPDATE or re-INVITE on dialog idx and,
// for a re-INVITE answered 2xx, its ACK.
func (c *call) sendRefresh(idx int, method sip.RequestMethod, interval time.Duration, sdp []byte) (*sip.Response, error) {
	s := c.s
	build := func(target sip.Uri) *sip.Request {
		req := sip.NewRequest(method, target)
		req.AppendHeader(sip.HeaderClone(&s.contact))
		req.AppendHeader(sip.NewHeader("Supported", "timer"))
		req.AppendHeader(sip.NewHeader("Session-Expires", secondsOf(interval)+";refresher=uac"))
		req.AppendHeader(sip.NewHeader("Min-SE", secondsOf(s.cfg.SessionMinSE)))
		if method == sip.INVITE && len(sdp) > 0 {
			req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			req.SetBody(sdp)
		}
		req.SetTransport("UDP")
		return req
	}
	timeout := s.cfg.SessionRefreshTimeout
	if timeout <= 0 {
		timeout = defaultRefreshTimeout
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	if hom := c.homed(); hom != nil {
		return c.sendHomedRefresh(ctx, hom.leg(idx == sessCallee), method, interval, sdp)
	}
	if idx == sessCaller && c.isRawCaller() {
		return c.sendRawCallerRefresh(ctx, method, interval, sdp)
	}
	if idx == sessCallee {
		w := c.winnerSession()
		if w == nil || w.InviteResponse == nil {
			return nil, fmt.Errorf("no callee dialog")
		}
		target := *w.InviteRequest.Recipient.Clone()
		if ct := w.InviteResponse.Contact(); ct != nil {
			target = *ct.Address.Clone()
		}
		res, err := requireResponse(w.Do(ctx, build(target)))
		if err == nil && method == sip.INVITE && res.IsSuccess() {
			ack := sip.NewRequest(sip.ACK, target)
			if werr := w.WriteRequest(ack); werr != nil {
				s.log.Debug("session refresh ACK to the callee failed", "correlation_id", c.id, "error", werr)
			}
		}
		return res, err
	}
	c.mu.Lock()
	dss := c.dss
	c.mu.Unlock()
	if dss == nil || dss.InviteRequest == nil {
		return nil, fmt.Errorf("no caller dialog")
	}
	target := *dss.InviteRequest.Recipient.Clone()
	if ct := dss.InviteRequest.Contact(); ct != nil {
		target = *ct.Address.Clone()
	}
	res, err := requireResponse(dss.Do(ctx, build(target)))
	if err == nil && method == sip.INVITE && res.IsSuccess() {
		if werr := dss.WriteRequest(sip.NewRequest(sip.ACK, target)); werr != nil {
			s.log.Debug("session refresh ACK to the caller failed", "correlation_id", c.id, "error", werr)
		}
	}
	return res, err
}

// sessionTeardown ends the call because one dialog's far end went silent.
func (c *call) sessionTeardown(reason string, idx int, detail string) {
	side := "caller"
	if idx == sessCallee {
		side = "callee"
	}
	c.s.log.Info("ending a call whose session timer failed", "correlation_id", c.id, "leg", side, "reason", reason, "detail", detail)
	if m := c.s.m.Session; m != nil {
		m.Teardowns.WithLabelValues(reason).Inc()
	}
	c.addTrace(fmt.Sprintf("Session timer: %s (%s leg)", detail, side))
	c.endBoth("session timer: " + strings.ReplaceAll(reason, "_", " "))
}

// ---- refreshes from the phone ---------------------------------------------

// sessionHeaderTx adds the Session-Expires of a negotiated timer to the 2xx
// of a request the phone sent.
type sessionHeaderTx struct {
	sip.ServerTransaction
	hdrs []sip.Header
}

func (t *sessionHeaderTx) Respond(res *sip.Response) error {
	if res.IsSuccess() {
		for _, h := range t.hdrs {
			res.RemoveHeader(h.Name())
			res.AppendHeader(sip.HeaderClone(h))
		}
	}
	return t.ServerTransaction.Respond(res)
}

// sessionIn handles an in-dialog INVITE or UPDATE the phone on dialog idx
// sent. It counts as a refresh whether or not it carries Session-Expires. It
// returns the transaction to answer on (adding the negotiated
// Session-Expires to the 2xx) and handled=true when the request was a
// refresh only (an UPDATE without a body, answered here) or was refused with
// 422 for an interval below Min-SE.
func (c *call) sessionIn(idx int, req *sip.Request, tx sip.ServerTransaction) (out sip.ServerTransaction, handled bool) {
	c.ses.mu.Lock()
	st := c.ses.leg[idx]
	stopped := c.ses.stopped
	c.ses.mu.Unlock()
	if st == nil || stopped {
		return tx, false
	}
	s := c.s
	secs, refresher, has := sessionExpires(req.GetHeader)
	if has && time.Duration(secs)*time.Second < s.cfg.SessionMinSE {
		s.respond(tx, req, 422, "Session Interval Too Small", sip.NewHeader("Min-SE", secondsOf(s.cfg.SessionMinSE)))
		return tx, true
	}
	c.ses.mu.Lock()
	if has {
		st.interval = time.Duration(secs) * time.Second
		// The sender of a refresh is its UAC: "uas" hands the next refresh
		// to Hello, "uac" or no preference leaves it to the phone.
		st.local = refresher == "uas"
	}
	next := st.firstDelay()
	interval, local := st.interval, st.local
	c.ses.mu.Unlock()
	c.sessionRearm(idx, next)
	if !has {
		return tx, false
	}
	role := "uac" // from the request's sender: the phone refreshes
	if local {
		role = "uas"
	}
	out = &sessionHeaderTx{ServerTransaction: tx, hdrs: []sip.Header{
		sip.NewHeader("Session-Expires", secondsOf(interval)+";refresher="+role),
		sip.NewHeader("Supported", "timer"),
	}}
	if req.Method == sip.UPDATE && len(req.Body()) == 0 {
		res := symmetric(req, sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
		res.AppendHeader(sip.HeaderClone(&s.contact))
		s.send(out, res)
		return out, true
	}
	return out, false
}

// ---- dead media -----------------------------------------------------------

// mediaWatched reports whether the call is one whose silence means a dead
// call: connected, bridged between two phones or trunks, on this node's
// relay, and not held, parked or in the middle of an announcement. A
// retrieved, picked-up or returned pair is watched on the call that holds the
// relay (its peer has none), and not while either party holds.
func (c *call) mediaWatched() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.connected || c.ended || c.hungUp || !c.anchored || c.relay == nil || c.held ||
		c.park != nil || c.page != nil || c.ivr != nil || c.conf != nil || c.haSolo {
		return false
	}
	if c.link != nil {
		return c.link.relayOwner == c && !c.linkHeld
	}
	return true
}

// mediaTouched marks now as a moment media was not expected, so the silence
// clock restarts (a call leaving hold, a re-anchor).
func (c *call) mediaTouched() {
	c.ses.mu.Lock()
	c.ses.mediaTouch = time.Now()
	c.ses.mu.Unlock()
}

// startMediaWatch begins the dead-media watch of an answered call; it is
// idempotent, and follows the call's current relay across a re-anchor.
func (c *call) startMediaWatch() {
	d := c.s.cfg.MediaDeadAfter
	if d <= 0 {
		return
	}
	c.ses.mu.Lock()
	defer c.ses.mu.Unlock()
	if c.ses.mediaTimer != nil || c.ses.stopped {
		return
	}
	c.ses.mediaTouch = time.Now()
	c.ses.mediaTimer = time.AfterFunc(d, c.mediaCheck)
}

// mediaCheck is the watch firing: it ends the call when no RTP has arrived
// for MediaDeadAfter, else sleeps until that moment could come.
func (c *call) mediaCheck() {
	defer contain(c.s.log, "media watch")
	d := c.s.cfg.MediaDeadAfter
	watched := c.mediaWatched()
	c.mu.Lock()
	relay := c.relay
	over := c.ended || c.hungUp
	c.mu.Unlock()
	if over {
		return
	}
	c.ses.mu.Lock()
	if c.ses.stopped {
		c.ses.mu.Unlock()
		return
	}
	if !watched || relay == nil {
		c.ses.mediaTouch = time.Now()
	}
	last := c.ses.mediaTouch
	if relay != nil && relay.LastRTP().After(last) {
		last = relay.LastRTP()
	}
	idle := time.Since(last)
	if watched && idle >= d {
		c.ses.mu.Unlock()
		if m := c.s.m.Session; m != nil {
			m.Teardowns.WithLabelValues("media_dead").Inc()
		}
		c.s.log.Info("ending a call with no media", "correlation_id", c.id, "idle", idle.Round(time.Second).String())
		c.addTrace(fmt.Sprintf("No RTP in either direction for %s", idle.Round(time.Second)))
		c.endBoth("no media")
		return
	}
	wait := d - idle
	if !watched {
		wait = d
	}
	c.ses.mediaTimer = time.AfterFunc(max(wait, time.Second), c.mediaCheck)
	c.ses.mu.Unlock()
}

// sendHomedRefresh is sendRefresh on a leg a takeover rebuilt: the dialog is
// the replicated data, not a sipgo session.
func (c *call) sendHomedRefresh(ctx context.Context, l *haLeg, method sip.RequestMethod, interval time.Duration, sdp []byte) (*sip.Response, error) {
	if l == nil {
		return nil, fmt.Errorf("no replicated dialog")
	}
	s := c.s
	if method == sip.INVITE && len(sdp) == 0 {
		l.mu.Lock()
		sdp = []byte(l.sdp)
		l.mu.Unlock()
	}
	var body []byte
	if method == sip.INVITE {
		body = sdp
	}
	req, err := l.request(s, method, body, "application/sdp")
	if err != nil {
		return nil, err
	}
	req.AppendHeader(sip.NewHeader("Supported", "timer"))
	req.AppendHeader(sip.NewHeader("Session-Expires", secondsOf(interval)+";refresher=uac"))
	req.AppendHeader(sip.NewHeader("Min-SE", secondsOf(s.cfg.SessionMinSE)))
	l.useCSeq()
	res, err := s.do(ctx, req)
	if err == nil && method == sip.INVITE {
		s.haAck(l, req, res)
	}
	return res, err
}

// sessionArmHomed starts the session timers and the media watch of a call a
// takeover rebuilt. The negotiated state is not replicated, so every leg
// restarts as Hello-refreshed at Hello's interval, with UPDATE; a phone that
// refreshes on its own is picked up at its first refresh (sessionIn).
func (c *call) sessionArmHomed() {
	if c.s.cfg.SessionExpires > 0 {
		c.ses.mu.Lock()
		for i := range c.ses.leg {
			if c.ses.leg[i] == nil {
				c.ses.leg[i] = &sessLeg{interval: c.s.cfg.SessionExpires, local: true, update: true}
			}
		}
		c.ses.mu.Unlock()
		c.sessionArm()
	}
	c.startMediaWatch()
}
