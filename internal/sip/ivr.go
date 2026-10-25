// IVR / auto-attendant (migration 00031). A menu answers the call through
// the media relay, plays its greeting (an announcement) with barge-in,
// collects DTMF digits (RFC 4733 through the relay, SIP INFO through the
// dialog) and sends the call to the destination of the chosen option. The
// caller's dialog is the ordinary answered B2BUA dialog and the relay's
// caller leg already points at the phone, so a destination that rings a
// phone only has to fork a leg whose offer is the relay's callee port: no
// re-INVITE of the caller is needed.
package sip

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// Limits that keep a misconfigured menu from holding a call for ever.
const (
	// ivrMaxEntries is how many times one call may enter a menu.
	ivrMaxEntries = 32
	// ivrMaxDigits bounds a direct-dialled number.
	ivrMaxDigits = 16
	// ivrMaxForward bounds the forwarding chain followed from a dial-out.
	ivrMaxForward = 5
)

// ivrState is a call's IVR session: where its digits arrive and the prompts
// already fetched.
type ivrState struct {
	digits chan byte
	// active is true while the menu collects digits; after the call is
	// bridged the caller's digits belong to the feature-code buffer again.
	active atomic.Bool

	mu    sync.Mutex
	cache map[string]ivrAudio // announcement name -> PCM
	done  sync.Once           // closes the call's setupDone
}

func newIVRState() *ivrState {
	st := &ivrState{digits: make(chan byte, 32), cache: map[string]ivrAudio{}}
	st.active.Store(true)
	return st
}

// ivrDigit hands a caller digit to the menu; true when the menu took it.
func (c *call) ivrDigit(leg string, d byte) bool {
	c.mu.Lock()
	st := c.ivr
	c.mu.Unlock()
	if st == nil || !st.active.Load() || leg != legCaller {
		return false
	}
	select {
	case st.digits <- d:
	default: // a flood of digits loses the surplus, never blocks RTP
	}
	return true
}

// stopIVR ends the IVR session with the call: it cancels the flow's context
// and releases the legs' event reports.
func (c *call) stopIVR() {
	c.mu.Lock()
	cancel, st := c.ivrCancel, c.ivr
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if st != nil {
		c.ivrFinishSetup()
	}
}

// ivrFinishSetup closes the menu's setupDone, so legs that report late never
// block on the event queue.
func (c *call) ivrFinishSetup() {
	c.mu.Lock()
	st, done := c.ivr, c.setupDone
	c.mu.Unlock()
	if st != nil {
		st.done.Do(func() { close(done) })
	}
}

// --- entry -----------------------------------------------------------------

// startIVR answers the call and runs the menu. tx is the INVITE's server
// transaction while the call has not begun; it is nil when the call already
// began (a ring group's failure destination, the call is still ringing for
// the caller).
func (c *call) startIVR(tx sip.ServerTransaction, snap *snapshot.Snapshot, m snapshot.IVRMenu) {
	if !m.Enabled {
		c.ivrReject(tx, sip.StatusNotFound, "IVR menu "+m.Name+" is disabled", ResultNotFound)
		return
	}
	if tx != nil {
		c.considerAnchor(snap, EndpointInfo{})
	} else if !c.isAnchored() {
		c.startAnchor(snap, AnchorPolicy)
	}
	if !c.isAnchored() {
		c.ivrReject(tx, sip.StatusNotAcceptableHere, "IVR needs a G.711 SDP offer and a media port", ResultFailed)
		return
	}
	if tx != nil && !c.begin(c.inv, tx) {
		return
	}
	st := newIVRState()
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.ivr, c.ivrCancel = st, cancel
	// The setup that ran before the menu (a ring group's) closed its own
	// channel; the menu's legs report to a fresh one.
	c.setupDone = make(chan struct{})
	c.mu.Unlock()
	c.addTrace("IVR menu " + m.Name)
	if !c.ivrAnswer(m) {
		cancel()
		return
	}
	go func() {
		defer contain(c.s.log, "ivr flow")
		c.ivrRun(ctx, m)
	}()
}

// ivrReject answers a call the menu cannot take.
func (c *call) ivrReject(tx sip.ServerTransaction, code int, why, result string) {
	if tx != nil {
		c.s.respond(tx, c.inv, code, statusText(code))
		c.record(code, cdr.SideSystem, why, result)
		return
	}
	c.respondA(code, statusText(code))
	c.end(code, cdr.SideSystem, why, result)
}

// ivrAnswer sends the 200 with the relay's caller-leg SDP and starts the
// call's replication as a one-legged call.
func (c *call) ivrAnswer(m snapshot.IVRMenu) bool {
	return c.soloAnswer(haPhaseIVR, haIVRDetailPrefix+m.Name, "IVR")
}

// soloAnswer is ivrAnswer for every application that answers the call
// itself and keeps the caller on the relay (IVR menus, call queues): phase
// and detail are what a taker restarts, what names the application in
// refusals.
func (c *call) soloAnswer(phase, detail, what string) bool {
	s := c.s
	off, err := media.ParseAudioSDP(c.inv.Body())
	if err != nil {
		c.ivrReject(nil, sip.StatusNotAcceptableHere, what+" needs an audio SDP offer", ResultFailed)
		return false
	}
	// No callee: the anchor answers with the best codec it shares with the
	// caller, as an announcement destination does.
	body := c.anchoredAnswerA(off, media.AudioSDP{})
	c.mu.Lock()
	cancelled := c.isCancelled
	aborted := closed(c.aborted)
	c.connected = !cancelled && !aborted
	if c.connected {
		c.answerTime = time.Now()
		c.ivrAnswered = c.answerTime
		c.setupClosed = true // no fork may win before the menu sends one
	}
	c.mu.Unlock()
	if cancelled {
		c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
		return false
	}
	if aborted {
		c.abortCaller()
		return false
	}
	if h := s.answerHook.Load(); h != nil {
		(*h)()
	}
	c.addTrace("Call established")
	c.publish()
	if c.callerDevice != "" {
		s.publishDeviceState([]string{c.callerDevice}, c.callerNum, StateOnCall)
	}
	res := sip.NewResponseFromRequest(c.dss.InviteRequest, sip.StatusOK, "OK", body)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	res.AppendHeader(sip.NewHeader("Allow", allow))
	s.m.response(sip.StatusOK)
	if err := c.dss.WriteResponse(res); err != nil {
		if c.dss.LoadState() == sip.DialogStateEnded {
			// The caller's BYE overtook its ACK: handleBye has accepted it
			// and ends the call as the caller's hangup.
			c.addTrace("Caller hung up before acknowledging the answer")
			return false
		}
		c.mu.Lock()
		cancelled = c.isCancelled
		c.connected = false
		c.mu.Unlock()
		if cancelled {
			c.end(sip.StatusRequestTerminated, cdr.SideCaller, "cancelled by caller", ResultCancelled)
			return false
		}
		s.log.Info("caller did not acknowledge answer", "correlation_id", c.id, "error", err)
		c.byeA()
		c.end(sip.StatusOK, cdr.SideSystem, "no ACK from caller", ResultFailed)
		return false
	}
	c.mu.Lock()
	if !c.ended {
		c.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, c.expire)
	}
	if res := c.dss.InviteResponse; res != nil {
		c.vmTag = tagParam(res.To())
	}
	c.mu.Unlock()
	if s.deps.HAState != nil {
		// Replicated like a voicemail call: a taker re-INVITEs the caller
		// onto its own relay and restarts the menu from its greeting.
		c.haSoloRefresh(string(body))
		c.mu.Lock()
		c.haPhase, c.haDetail = phase, detail
		c.mu.Unlock()
		go c.haLoop()
	}
	return true
}

// --- the menu --------------------------------------------------------------

// ivrExit is how a menu run ended: the destination and why. auto marks an
// exit nobody chose (timeout, invalid choice).
type ivrExit struct {
	dest snapshot.IVRDest
	why  string
	auto bool
}

// ivrRun runs menus (nested ones too) until a destination takes the call or
// the call ends.
func (c *call) ivrRun(ctx context.Context, first snapshot.IVRMenu) {
	cur := first
	autoSeen := map[string]bool{}
	replay := false
	for entries := 1; ctx.Err() == nil; entries++ {
		if entries > ivrMaxEntries {
			c.ivrLog("ivr loop stopped", cur.Name, "entries", entries)
			c.addTrace("IVR stopped: too many menu entries")
			c.ivrHangup()
			return
		}
		ex, ok := c.ivrMenu(ctx, cur, replay)
		if !ok {
			return
		}
		replay = false
		snap := c.s.deps.Snapshots.Current()
		if snap == nil {
			c.ivrHangup()
			return
		}
		next, done := c.ivrLeave(ctx, snap, cur, ex)
		switch {
		case done:
			return
		case next != "":
			nm, ok := snap.IVRMenu(next)
			if !ok || !nm.Enabled {
				c.addTrace(fmt.Sprintf("IVR menu %s is unavailable", next))
				c.ivrHangup()
				return
			}
			if ex.auto {
				// Timeout and invalid exits chain without a key press: a
				// menu seen twice on such a chain is a loop.
				autoSeen[cur.Name] = true
				if autoSeen[nm.Name] {
					c.addTrace("IVR loop through automatic exits stopped")
					c.ivrHangup()
					return
				}
			} else {
				clear(autoSeen)
			}
			cur = nm
		case ex.auto:
			c.ivrHangup() // the exit destination could not take the call
			return
		case c.takeIVRUnrecoverable() && !ex.auto:
			// The destination needs what a takeover could not recover: the
			// menu's invalid-choice exit takes the call instead of a replay.
			c.addTrace("IVR: using the menu's invalid-choice exit")
			fx := ivrExit{dest: cur.Invalid, why: "destination not recoverable after a takeover", auto: true}
			if cur.Invalid.Kind == snapshot.IVRMenuKind {
				nm, ok := snap.IVRMenu(cur.Invalid.Target)
				if !ok || !nm.Enabled {
					c.ivrHangup()
					return
				}
				autoSeen[cur.Name] = true
				cur = nm
				continue
			}
			next, done := c.ivrLeave(ctx, snap, cur, fx)
			if !done && next == "" {
				c.ivrHangup()
				return
			}
			if done {
				return
			}
			nm, ok := snap.IVRMenu(next)
			if !ok || !nm.Enabled {
				c.ivrHangup()
				return
			}
			cur = nm
		default:
			replay = true // the chosen destination failed: the menu says so and repeats
			c.ivrFlush()
		}
	}
}

// ivrLeave carries out the exit's destination. done means the call is
// finished or connected; next names a menu to continue in; neither means
// the destination could not take the call.
func (c *call) ivrLeave(ctx context.Context, snap *snapshot.Snapshot, m snapshot.IVRMenu, ex ivrExit) (next string, done bool) {
	d := ex.dest
	c.ivrLog("ivr exit", m.Name, "reason", ex.why, "kind", d.Kind, "target", d.Target)
	c.addTrace(fmt.Sprintf("IVR %s: %s -> %s %s", m.Name, ex.why, d.Kind, d.Target))
	c.s.m.IVRTotal.WithLabelValues("exit_" + d.Kind).Inc()
	switch d.Kind {
	case snapshot.IVRMenuKind:
		return d.Target, false
	case snapshot.IVRAnnouncement:
		c.ivrAnnounce(ctx, snap, d.Target)
		c.ivrHangup()
		return "", true
	case snapshot.IVRVoicemail:
		return c.ivrVoicemailExit(ctx, snap, d.Target, "ivr")
	case snapshot.IVRExtension, snapshot.IVRRingGroup, snapshot.IVRExternal:
		res := c.ivrDial(ctx, snap, m, d)
		return res.menu, res.kind == ivrConnected || res.kind == ivrFinished
	case snapshot.IVRPaging:
		return "", c.ivrPage(ctx, snap, d.Target)
	case snapshot.IVRQueue:
		q, ok := snap.Queue(d.Target)
		if !ok || !q.Enabled {
			c.addTrace(fmt.Sprintf("IVR: queue %s is unavailable", d.Target))
			return "", false
		}
		c.queueSession(ctx, snap, q, 0)
		return "", true
	}
	c.ivrHangup() // hangup, or a kind this node does not know
	return "", true
}

// ivrHangup ends the call from the PBX side.
func (c *call) ivrHangup() {
	c.mu.Lock()
	done := c.hungUp || c.ended
	c.mu.Unlock()
	if !done {
		c.hangup(cdr.SideCallee)
	}
}

func (c *call) ivrLog(msg, menu string, kv ...any) {
	args := append([]any{"component", "sip", "menu", menu, "correlation_id", c.id, "call_id", c.callID, "caller", c.callerNum}, kv...)
	c.s.log.Info(msg, args...)
}

// takeIVRUnrecoverable reads and clears the unrecoverable-destination flag.
func (c *call) takeIVRUnrecoverable() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	v := c.ivrUnrecoverable
	c.ivrUnrecoverable = false
	return v
}

func (c *call) ivrFlush() {
	c.mu.Lock()
	st := c.ivr
	c.mu.Unlock()
	if st == nil {
		return
	}
	for {
		select {
		case <-st.digits:
		default:
			return
		}
	}
}

// ivrInput is what one prompt-and-collect round produced.
type ivrInput struct {
	kind  int
	value string // the option digit, or the dialled extension
}

const (
	ivrGone = iota
	ivrOption
	ivrDirect
	ivrTimeout
	ivrInvalid
)

// ivrMenu runs one menu: prompt, collect, retry. replay opens with the
// invalid-choice prompt (a destination failed).
func (c *call) ivrMenu(ctx context.Context, m snapshot.IVRMenu, replay bool) (ivrExit, bool) {
	snap := c.s.deps.Snapshots.Current()
	if snap == nil {
		c.ivrHangup()
		return ivrExit{}, false
	}
	c.ivrLog("ivr menu entered", m.Name)
	c.addTrace("IVR menu " + m.Name + " entered")
	c.s.m.IVRTotal.WithLabelValues("entered").Inc()
	c.mu.Lock()
	if c.haPhase == haPhaseIVR {
		c.haDetail = haIVRDetailPrefix + m.Name
	}
	c.mu.Unlock()
	prompts := ivrPrompts(m.InvalidPrompt, m.Greeting)
	if !replay {
		prompts = ivrPrompts("", m.Greeting)
	}
	retries := 0
	for {
		in := c.ivrCollect(ctx, snap, m, prompts)
		switch in.kind {
		case ivrGone:
			return ivrExit{}, false
		case ivrOption:
			c.ivrLog("ivr option chosen", m.Name, "digit", in.value)
			c.s.m.IVRTotal.WithLabelValues("option").Inc()
			return ivrExit{dest: m.Options[in.value], why: "option " + in.value}, true
		case ivrDirect:
			c.ivrLog("ivr direct dial", m.Name, "extension", in.value)
			c.s.m.IVRTotal.WithLabelValues("direct_dial").Inc()
			return ivrExit{dest: snapshot.IVRDest{Kind: snapshot.IVRExtension, Target: in.value}, why: "direct dial " + in.value}, true
		case ivrTimeout, ivrInvalid:
			event, special, dest := "timeout", m.TimeoutPrompt, m.Timeout
			if in.kind == ivrInvalid {
				event, special, dest = "invalid", m.InvalidPrompt, m.Invalid
			}
			retries++
			c.ivrLog("ivr "+event, m.Name, "retry", retries, "max_retries", m.Retries, "input", in.value)
			c.s.m.IVRTotal.WithLabelValues(event).Inc()
			if retries > m.Retries {
				return ivrExit{dest: dest, why: event + " after " + fmt.Sprint(retries-1) + " retries", auto: true}, true
			}
			prompts = ivrPrompts(special, m.Greeting)
		}
	}
}

// ivrPrompts lists the announcements to play in order, without blanks.
func ivrPrompts(names ...string) []string {
	var out []string
	for _, n := range names {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

// ivrCollect plays the prompts (a key press stops them) and reads the
// caller's choice.
func (c *call) ivrCollect(ctx context.Context, snap *snapshot.Snapshot, m snapshot.IVRMenu, prompts []string) ivrInput {
	var buf []byte
	for _, name := range prompts {
		d, hit, alive := c.ivrPlay(ctx, snap, name, true)
		if !alive {
			return ivrInput{kind: ivrGone}
		}
		if hit {
			buf = append(buf, d)
			c.addTrace("IVR prompt interrupted by a key press")
			break
		}
	}
	timeout := time.Duration(m.DigitTimeout) * time.Second
	for {
		if len(buf) > 0 {
			v, val := ivrClassify(snap, m, string(buf), false)
			if v != ivrMore {
				return ivrVerdictInput(v, val)
			}
			timeout = time.Duration(m.InterDigitTimeout) * time.Second
		}
		d, ok, alive := c.ivrNextDigit(ctx, timeout)
		switch {
		case !alive:
			return ivrInput{kind: ivrGone}
		case !ok: // silence
			if len(buf) == 0 {
				return ivrInput{kind: ivrTimeout}
			}
			v, val := ivrClassify(snap, m, string(buf), true)
			return ivrVerdictInput(v, val)
		case d == '#' && len(buf) > 0:
			v, val := ivrClassify(snap, m, string(buf), true)
			return ivrVerdictInput(v, val)
		}
		if len(buf) >= ivrMaxDigits {
			return ivrInput{kind: ivrInvalid, value: string(buf)}
		}
		buf = append(buf, d)
		timeout = time.Duration(m.InterDigitTimeout) * time.Second
	}
}

func ivrVerdictInput(v int, val string) ivrInput {
	switch v {
	case ivrVerdictOption:
		return ivrInput{kind: ivrOption, value: val}
	case ivrVerdictDirect:
		return ivrInput{kind: ivrDirect, value: val}
	}
	return ivrInput{kind: ivrInvalid, value: val}
}

// ivrNextDigit waits for a key press; ok false is the timeout, alive false
// the end of the call.
func (c *call) ivrNextDigit(ctx context.Context, wait time.Duration) (d byte, ok, alive bool) {
	c.mu.Lock()
	st := c.ivr
	c.mu.Unlock()
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case d := <-st.digits:
		return d, true, true
	case <-t.C:
		return 0, false, ctx.Err() == nil
	case <-ctx.Done():
		return 0, false, false
	}
}

// Verdicts of ivrClassify.
const (
	ivrVerdictOption = iota
	ivrVerdictDirect
	ivrVerdictInvalid
	ivrMore
)

// ivrClassify judges the digits collected so far. terminated means no more
// are coming (a # or the inter-digit timeout). A single digit with an option
// fires at once unless direct dialling is on and an extension number starts
// with it, in which case the menu waits for more.
func ivrClassify(snap *snapshot.Snapshot, m snapshot.IVRMenu, digits string, terminated bool) (verdict int, value string) {
	_, isOpt := m.Options[digits]
	isOpt = isOpt && len(digits) == 1
	var exact, longer bool
	if m.AllowDirectDial && strings.Trim(digits, "0123456789") == "" {
		exact, longer = snap.ExtensionPrefix(digits)
	}
	switch {
	case terminated && isOpt:
		return ivrVerdictOption, digits
	case terminated && exact:
		return ivrVerdictDirect, digits
	case terminated:
		return ivrVerdictInvalid, digits
	case isOpt && !longer:
		return ivrVerdictOption, digits
	case exact && !longer:
		return ivrVerdictDirect, digits
	case longer:
		return ivrMore, digits
	}
	return ivrVerdictInvalid, digits
}

// --- prompts ---------------------------------------------------------------

// ivrAudio is a decoded prompt: PCM at the WAV's own rate (8 or 16 kHz).
type ivrAudio struct {
	pcm  []byte
	rate int
}

// ivrPrompt fetches an announcement as PCM; empty when it is unavailable (the
// step is skipped with a trace, like every announcement).
func (c *call) ivrPrompt(ctx context.Context, snap *snapshot.Snapshot, name string) ivrAudio {
	c.mu.Lock()
	st := c.ivr
	c.mu.Unlock()
	st.mu.Lock()
	a, ok := st.cache[name]
	st.mu.Unlock()
	if ok {
		return a
	}
	obj, found := snap.Announcement(name)
	objs := c.s.deps.Objects
	switch {
	case !found:
		c.addTrace(fmt.Sprintf("IVR prompt %q is missing: skipped", name))
	case objs == nil:
		c.addTrace(fmt.Sprintf("IVR prompt %q unavailable: no audio store", name))
	default:
		octx, cancel := context.WithTimeout(ctx, c.s.cfg.StateTimeout)
		raw, err := objs.GetAnnouncement(octx, obj)
		cancel()
		if err != nil {
			c.addTrace(fmt.Sprintf("IVR prompt %q unavailable: skipped", name))
			c.s.log.Warn("ivr prompt unavailable", "announcement", name, "error", err)
			break
		}
		if a.pcm, a.rate, err = media.DecodeWAV(raw); err != nil {
			a = ivrAudio{}
			c.addTrace(fmt.Sprintf("IVR prompt %q is not a WAV: skipped", name))
		}
	}
	if ctx.Err() == nil {
		st.mu.Lock()
		st.cache[name] = a
		st.mu.Unlock()
	}
	return a
}

// ivrPlay plays one announcement to the caller. With barge-in a key press
// stops it and is returned. alive is false once the call has ended.
func (c *call) ivrPlay(ctx context.Context, snap *snapshot.Snapshot, name string, barge bool) (digit byte, hit, alive bool) {
	a := c.ivrPrompt(ctx, snap, name)
	if len(a.pcm) == 0 {
		return 0, false, ctx.Err() == nil
	}
	relay := c.anchorRelay()
	if relay == nil {
		return 0, false, false
	}
	c.mu.Lock()
	st := c.ivr
	c.mu.Unlock()
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan struct{})
	var playErr error
	go func() {
		defer close(done)
		// The relay resamples to the caller's codec rate.
		playErr = relay.PlayPCMTo(pctx, legCaller, a.pcm, a.rate)
	}()
	var keys <-chan byte
	if barge {
		keys = st.digits
	}
	select {
	case d := <-keys:
		cancel()
		<-done
		return d, true, ctx.Err() == nil
	case <-done:
		if playErr != nil && ctx.Err() == nil {
			c.s.log.Debug("ivr prompt playback failed", "announcement", name, "error", playErr)
		}
		return 0, false, ctx.Err() == nil
	case <-ctx.Done():
		cancel()
		<-done
		return 0, false, false
	}
}

// ivrAnnounce plays an announcement destination to the end.
func (c *call) ivrAnnounce(ctx context.Context, snap *snapshot.Snapshot, name string) {
	c.ivrPlay(ctx, snap, name, false)
}

// --- voicemail -------------------------------------------------------------

// relaySession gives the voicemail application the IVR call's media: the
// relay's caller leg. Record reads the caller's audio from the relay's packet
// tap and the digits the menu received.
type relaySession struct {
	c     *call
	relay *media.Relay
}

// callerCodec is the codec the relay speaks on the caller leg: the first
// supported one it negotiated, G.711 µ-law when none was set.
func callerCodec(r *media.Relay) media.Codec {
	for _, c := range r.Codecs(legCaller) {
		if c.Supported() {
			return c
		}
	}
	c, _ := media.StaticCodec(0)
	return c
}

// SampleRate is the caller leg's codec rate: 16 kHz for a wideband caller,
// so a message is recorded and played back without losing the band.
func (r *relaySession) SampleRate() int { return callerCodec(r.relay).SampleRate() }

func (r *relaySession) Play(ctx context.Context, pcm []byte) error {
	return r.relay.PlayPCMTo(ctx, legCaller, pcm, r.SampleRate())
}

func (r *relaySession) Close() error { return nil }

// setVMTap installs the caller's-RTP tap of a voicemail recording and returns
// the call's IVR session.
func (c *call) setVMTap(f func(leg string, pkt *media.RTPPacket)) *ivrState {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.vmTap = f
	return c.ivr
}

// Record captures the caller's audio for at most maxDur, stopping early on
// '#' (every digit is forwarded on dtmf). Silence the phone did not send is
// padded, so the length tracks the time recorded like the voicemail
// session's.
func (r *relaySession) Record(ctx context.Context, maxDur time.Duration, dtmf chan<- byte) ([]byte, error) {
	codec := callerCodec(r.relay)
	rate := codec.SampleRate()
	decoder, err := media.NewDecoder(codec)
	if err != nil {
		return nil, fmt.Errorf("voicemail recording: %w", err)
	}
	defer decoder.Close()
	var (
		mu  sync.Mutex
		pcm []byte
	)
	tap := func(leg string, pkt *media.RTPPacket) {
		if leg != legCaller || pkt.PayloadType != codec.PT {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if decoder != nil && len(pcm) < 2*rate*int(maxDur/time.Second+1) {
			pcm = append(pcm, decoder.Decode(pkt.Payload)...)
		}
	}
	defer func() {
		// Runs after the tap is uninstalled (deferred later, so run
		// earlier) and before the decoder closes: a tap call still in
		// flight then sees no decoder.
		mu.Lock()
		decoder = nil
		mu.Unlock()
	}()
	st := r.c.setVMTap(tap)
	defer r.c.setVMTap(nil)
	start := time.Now()
	t := time.NewTimer(maxDur)
	defer t.Stop()
	finish := func(err error) ([]byte, error) {
		mu.Lock()
		out := append([]byte(nil), pcm...)
		mu.Unlock()
		if want := int(time.Since(start).Seconds() * float64(2*rate)); len(out) < want {
			out = append(out, make([]byte, (want-len(out))&^1)...)
		}
		return out, err
	}
	for {
		select {
		case d := <-st.digits:
			select {
			case dtmf <- d:
			default:
			}
			if d == '#' {
				return finish(nil)
			}
		case <-t.C:
			return finish(nil)
		case <-ctx.Done():
			return finish(ctx.Err())
		}
	}
}

// ivrVoicemailExit takes a message for the box of extension ext. next and
// done follow ivrLeave's meaning.
func (c *call) ivrVoicemailExit(ctx context.Context, snap *snapshot.Snapshot, ext, reason string) (next string, done bool) {
	e, ok := snap.Extension(ext)
	relay := c.anchorRelay()
	if !ok || relay == nil || !c.voicemailConfigured(e) {
		c.addTrace(fmt.Sprintf("IVR: no voicemail box for extension %s", ext))
		return "", false
	}
	c.addTrace(fmt.Sprintf("IVR: to voicemail of %s", ext))
	c.mu.Lock()
	c.vmExt = ext
	c.haPhase, c.haDetail = "", "" // a voicemail call after the menu is not replayed by a taker
	c.mu.Unlock()
	box := c.boxDetails()
	c.vmLeave(ctx, &relaySession{c: c, relay: relay}, box, reason)
	c.ivrHangup()
	return "", true
}

// --- dialling out ----------------------------------------------------------

// Results of a dial-out.
const (
	ivrFailed    = iota // nothing answered
	ivrConnected        // the caller is talking to a phone or trunk
	ivrFinished         // voicemail or an announcement took the call and it ends
	ivrToMenu           // continue in menu
)

type ivrResult struct {
	kind int
	menu string
	why  string
}

// ivrDial rings the destination (extension, ring group or external number)
// and bridges the caller to whoever answers.
func (c *call) ivrDial(ctx context.Context, snap *snapshot.Snapshot, m snapshot.IVRMenu, d snapshot.IVRDest) ivrResult {
	if len(c.offerSDP()) == 0 {
		// Nothing to offer the new leg: the takeover state lacked the
		// caller's SDP. The menu falls back to its exit destination.
		c.addTrace("IVR: the caller's media offer is not recoverable: dialling out is not possible")
		c.setIVRUnrecoverable()
		return ivrResult{kind: ivrFailed}
	}
	c.s.m.IVRTotal.WithLabelValues("dial").Inc()
	var res ivrResult
	switch d.Kind {
	case snapshot.IVRRingGroup:
		g, members, ok := snap.RingGroup(d.Target)
		if !ok {
			c.addTrace(fmt.Sprintf("IVR: ring group %s does not exist", d.Target))
			return ivrResult{kind: ivrFailed}
		}
		res = c.ivrRingGroup(ctx, snap, g, members, nil)
	case snapshot.IVRExtension:
		res = c.ivrRingExtension(ctx, snap, d.Target, nil)
	default:
		from := m.Number
		if from == "" {
			from = m.Name
		}
		res = c.ivrExternal(ctx, snap, d.Target, from)
	}
	if res.kind == ivrFailed && ctx.Err() == nil {
		c.addTrace("IVR: " + orDefault(res.why, "the destination did not answer"))
		c.ivrLog("ivr dial failed", m.Name, "kind", d.Kind, "target", d.Target, "reason", orDefault(res.why, "no answer"))
		c.s.m.IVRTotal.WithLabelValues("dial_failed").Inc()
	}
	return res
}

// ivrLaunch starts a fork's INVITE; tests replace it to script a fork's
// outcome.
var ivrLaunch = func(l *leg) { go l.run() }

// ivrFork is one leg to try.
type ivrFork struct {
	mk    func(stage int) *leg // called with c.mu held
	delay time.Duration        // ring-all, sequential: when the leg starts
	per   time.Duration        // hunt: how long the member rings
}

// ivrRing starts the forks and waits for the first to answer. hunt rings
// one member at a time. busyAll reports that every leg failed busy.
func (c *call) ivrRing(ctx context.Context, forks []ivrFork, hunt bool, total time.Duration) (w *leg, busyAll bool) {
	if len(forks) == 0 {
		return nil, false
	}
	c.mu.Lock()
	c.setupClosed, c.winner = false, nil
	c.stageSeq++
	stage := c.stageSeq
	c.mu.Unlock()
	for drained := false; !drained; { // events of an earlier attempt
		select {
		case <-c.events:
		default:
			drained = true
		}
	}
	deadline := time.NewTimer(total)
	defer deadline.Stop()
	var timers []*time.Timer
	defer func() {
		for _, t := range timers {
			t.Stop()
		}
	}()
	start := func(f ivrFork) *leg {
		c.mu.Lock()
		l := f.mk(stage)
		c.mu.Unlock()
		ivrLaunch(l)
		return l
	}
	var (
		failed, busy, next int
		cur                *leg
		memberT            <-chan time.Time
	)
	startHunt := func() {
		f := forks[next]
		next++
		cur = start(f)
		per := f.per
		if per <= 0 || per > total {
			per = total
		}
		t := time.NewTimer(per)
		timers = append(timers, t)
		memberT = t.C
	}
	if hunt {
		startHunt()
	} else {
		for _, f := range forks {
			if f.delay <= 0 {
				start(f)
				continue
			}
			f := f
			timers = append(timers, time.AfterFunc(f.delay, func() {
				c.mu.Lock()
				stop := c.setupClosed || c.winner != nil || c.stageSeq != stage
				var l *leg
				if !stop {
					l = f.mk(stage)
				}
				c.mu.Unlock()
				if l != nil {
					ivrLaunch(l)
				}
			}))
		}
	}
	giveUp := func() {
		c.closeSetup()
		c.cancelForks(nil)
	}
	legFailed := func() bool { // true when the stage is exhausted
		failed++
		if hunt && next < len(forks) {
			startHunt()
			return false
		}
		return failed >= len(forks)
	}
	for {
		select {
		case <-ctx.Done():
			giveUp()
			return nil, false
		case ev := <-c.events:
			if ev.leg == nil || ev.leg.stage != stage {
				continue
			}
			switch ev.kind {
			case evAnswered:
				return ev.leg, false
			case evFailed:
				if isBusy(ev.code) {
					busy++
				}
				if hunt && ev.leg != cur {
					continue
				}
				if legFailed() {
					giveUp()
					return nil, busy == failed
				}
			}
		case <-memberT:
			memberT = nil
			if cur != nil && !c.abandon(cur) {
				// The member answered just as its turn ended: it has won,
				// and its answer is on the way. Moving on would leave that
				// answered dialog up with nobody on it.
				continue
			}
			if legFailed() {
				giveUp()
				return nil, false
			}
		case <-deadline.C:
			if c.closeSetup() {
				c.cancelForks(nil)
				return nil, false
			}
			// a fork won just now: its answer is on the way
		}
	}
}

// ivrConnect bridges the caller to the answering leg. The caller's dialog
// is already up on the relay's caller leg; the callee's answer aims the
// relay's callee leg.
func (c *call) ivrConnect(w *leg, ext string) ivrResult {
	c.cancelForks(w)
	dcs := w.session()
	bres := dcs.InviteResponse
	w.ack(nil, "")
	if hom := c.homed(); hom != nil && !c.ivrHomeCallee(hom, w) {
		w.bye()
		c.addTrace("IVR: the new leg could not be taken into the recovered call")
		c.setIVRUnrecoverable()
		return ivrResult{kind: ivrFailed}
	}
	if relay := c.anchorRelay(); relay != nil && bres != nil && len(bres.Body()) > 0 {
		if ans, err := media.ParseAudioSDP(bres.Body()); err == nil {
			relay.SetTarget(legCallee, udpAddr(ans.Address, ans.Port))
		}
	}
	c.mu.Lock()
	answered := c.ivrAnswered
	if !answered.IsZero() {
		c.answerTime = answered // billing runs from the menu's answer
	}
	if ext != "" {
		c.rewritten = ext
		c.bridgedExt = ext
	}
	st := c.ivr
	c.haSolo, c.haPhase, c.haDetail = false, "", ""
	c.mu.Unlock()
	st.active.Store(false)
	c.ivrFinishSetup()
	c.addTrace("IVR: caller connected to " + orDefault(ext, "the destination"))
	c.s.m.IVRTotal.WithLabelValues("connected").Inc()
	c.publish()
	if snap := c.s.deps.Snapshots.Current(); snap != nil && ext != "" {
		c.s.publishDeviceState(c.s.devicesOf(snap, ext), ext, StateOnCall)
		if e, ok := snap.Extension(ext); ok && e.RecordDefault {
			go c.startRecordingFlow("default")
		}
	}
	c.haRefresh()
	c.noteHAState("", "")
	return ivrResult{kind: ivrConnected}
}

// ivrRingExtension rings an extension the way a call to it would: forward
// always, DND to voicemail, no phone registered, then busy or no answer
// forwards and voicemail.
func (c *call) ivrRingExtension(ctx context.Context, snap *snapshot.Snapshot, ext string, visited []string) ivrResult {
	if slices.Contains(visited, ext) || len(visited) >= ivrMaxForward {
		return ivrResult{kind: ivrFailed, why: "forward loop at extension " + ext}
	}
	visited = append(slices.Clone(visited), ext)
	if !snap.HasExtension(ext) {
		return ivrResult{kind: ivrFailed, why: "extension " + ext + " does not exist"}
	}
	e, _ := snap.Extension(ext)
	if e.ForwardAlways != "" {
		c.addTrace(fmt.Sprintf("Extension %s forwards always to %s", ext, e.ForwardAlways))
		return c.ivrFollow(ctx, snap, ext, e.ForwardAlways, visited)
	}
	voicemail := func(reason string) ivrResult {
		if next, done := c.ivrVoicemailExit(ctx, snap, ext, reason); done {
			return ivrResult{kind: ivrFinished}
		} else if next != "" {
			return ivrResult{kind: ivrToMenu, menu: next}
		}
		return ivrResult{kind: ivrFailed, why: "extension " + ext + " is " + reason}
	}
	if e.DND {
		c.addTrace(fmt.Sprintf("Extension %s is DND", ext))
		if c.voicemailConfigured(e) {
			return voicemail("dnd")
		}
		return ivrResult{kind: ivrFailed, why: "extension " + ext + " is DND"}
	}
	targets, err := c.extensionBindings(snap, ext)
	if err != nil {
		return ivrResult{kind: ivrFailed, why: "live state unavailable"}
	}
	if len(targets) == 0 {
		switch {
		case e.ForwardNoAnswer != "":
			return c.ivrFollow(ctx, snap, ext, e.ForwardNoAnswer, visited)
		case c.voicemailConfigured(e):
			return voicemail("unreachable")
		}
		return ivrResult{kind: ivrFailed, why: "no registered device for extension " + ext}
	}
	forks := make([]ivrFork, len(targets))
	for i, b := range targets {
		b := b
		forks[i] = ivrFork{mk: func(stage int) *leg { return c.addLeg(&leg{binding: b, stage: stage}) }}
	}
	c.addTrace("IVR: ringing extension " + ext)
	w, busyAll := c.ivrRing(ctx, forks, false, c.s.cfg.RingTimeout)
	if ctx.Err() != nil {
		return ivrResult{kind: ivrFinished}
	}
	if w != nil {
		return c.ivrConnect(w, ext)
	}
	switch {
	case busyAll && e.ForwardBusy != "":
		return c.ivrFollow(ctx, snap, ext, e.ForwardBusy, visited)
	case !busyAll && e.ForwardNoAnswer != "":
		return c.ivrFollow(ctx, snap, ext, e.ForwardNoAnswer, visited)
	case c.voicemailConfigured(e):
		if busyAll {
			return voicemail("busy")
		}
		return voicemail("no-answer")
	}
	return ivrResult{kind: ivrFailed, why: "extension " + ext + " did not answer"}
}

// ivrFollow follows a forwarding target: a ring group, an extension or an
// external number.
func (c *call) ivrFollow(ctx context.Context, snap *snapshot.Snapshot, from, target string, visited []string) ivrResult {
	if g, members, ok := snap.RingGroup(target); ok {
		return c.ivrRingGroup(ctx, snap, g, members, visited)
	}
	if snap.HasExtension(target) {
		return c.ivrRingExtension(ctx, snap, target, visited)
	}
	return c.ivrExternal(ctx, snap, target, from)
}

// ivrRingGroup rings a ring or hunt group and applies its failure
// destination.
func (c *call) ivrRingGroup(ctx context.Context, snap *snapshot.Snapshot, g snapshot.RingGroup, members []snapshot.RingGroupMember, visited []string) ivrResult {
	s := c.s
	var ms []groupMember
	for _, m := range members {
		e, _ := snap.Extension(m.Extension)
		ms = append(ms, groupMember{ext: m.Extension, weight: m.Weight, delay: m.Delay, dnd: e.DND, lastEnd: s.lastEndOf(m.Extension)})
	}
	ordered := orderGroup(g.Strategy, ms, g.IgnoreDND, s.rotation(g.ID), groupSeed(g.Name, c.id))
	c.addTrace(fmt.Sprintf("IVR: group %s (%s): members %v", g.Name, g.Strategy, extsOf(ordered)))
	var forks []ivrFork
	for _, m := range ordered {
		bs, err := c.extensionBindings(snap, m.ext)
		if err != nil || len(bs) == 0 {
			continue
		}
		b := bs[0]
		f := ivrFork{mk: func(stage int) *leg { return c.addLeg(&leg{binding: b, stage: stage}) }}
		per := time.Duration(m.delay) * time.Second
		if per == 0 {
			per = time.Duration(g.MemberDelay) * time.Second
		}
		f.per = per
		switch {
		case g.Hunt:
		case g.Strategy == "sequential":
			f.delay = time.Duration(len(forks)*g.MemberDelay) * time.Second
		default:
			f.delay = time.Duration(m.delay) * time.Second
		}
		forks = append(forks, f)
	}
	if len(forks) > 0 {
		w, _ := c.ivrRing(ctx, forks, g.Hunt, time.Duration(g.RingTimeout)*time.Second)
		if ctx.Err() != nil {
			return ivrResult{kind: ivrFinished}
		}
		if w != nil {
			return c.ivrConnect(w, "")
		}
	}
	switch g.FailureKind {
	case "voicemail":
		if next, done := c.ivrVoicemailExit(ctx, snap, g.FailureTarget, "no-answer"); done {
			return ivrResult{kind: ivrFinished}
		} else if next != "" {
			return ivrResult{kind: ivrToMenu, menu: next}
		}
	case "announcement":
		c.ivrAnnounce(ctx, snap, g.FailureTarget)
		c.ivrHangup()
		return ivrResult{kind: ivrFinished}
	case "external":
		return c.ivrExternal(ctx, snap, g.FailureTarget, g.Name)
	case "ivr":
		return ivrResult{kind: ivrToMenu, menu: g.FailureTarget}
	}
	return ivrResult{kind: ivrFailed, why: "group " + g.Name + " did not answer"}
}

// ivrExternal routes a number through the outbound routes as if the menu
// (fromExt: its number or name) had dialled it, and tries the first
// candidate trunk that has a free slot.
func (c *call) ivrExternal(ctx context.Context, snap *snapshot.Snapshot, number, fromExt string) ivrResult {
	s := c.s
	dec := s.decide(snap.Routing(), routing.Call{
		FromExtension: fromExt, Number: number, CallerID: c.callerNum, At: time.Now(),
	})
	c.mu.Lock()
	c.trace = append(c.trace, dec.Trace...)
	c.mu.Unlock()
	if dec.Kind != routing.KindOutbound || len(dec.Candidates) == 0 {
		return ivrResult{kind: ivrFailed, why: "external number " + number + " has no outbound route"}
	}
	for _, cand := range dec.Candidates {
		t := cand.Trunk
		if t == nil {
			continue
		}
		if ok, why := c.acquireSlot(t, dec.Emergency, c.id); !ok {
			c.addTrace(fmt.Sprintf("Trunk %s skipped: %s", t.Name, why))
			continue
		}
		var forks []ivrFork
		for _, d := range cand.Destinations {
			if s.trunks.destinationDown(t.ID, d) && !dec.Emergency {
				continue
			}
			d := d
			forks = append(forks, ivrFork{mk: func(stage int) *leg {
				return c.addLeg(&leg{trunk: t, dest: d, addr: s.trunks.destAddr(d), number: dec.Number, callerID: dec.CallerID, stage: stage})
			}})
		}
		// The destinations of one trunk are alternatives: ring them one
		// after another.
		for i := range forks {
			forks[i].per = s.cfg.TrunkAttemptTimeout
		}
		c.mu.Lock()
		c.trunkName = t.Name
		c.route, c.direction = dec.Route, cdr.DirectionOutbound
		c.mu.Unlock()
		c.addTrace("IVR: calling " + number + " through trunk " + t.Name)
		w, _ := c.ivrRing(ctx, forks, true, s.cfg.RingTimeout)
		if ctx.Err() != nil {
			return ivrResult{kind: ivrFinished}
		}
		if w != nil {
			c.mu.Lock()
			c.rewritten = dec.Number
			c.mu.Unlock()
			return c.ivrConnect(w, "")
		}
		c.releaseSlot(t.ID, c.id)
	}
	return ivrResult{kind: ivrFailed, why: "external number " + number + " did not answer"}
}

// ivrPage hands the call to a paging group.
func (c *call) ivrPage(ctx context.Context, snap *snapshot.Snapshot, code string) bool {
	if c.homed() != nil {
		// A page's member dialogs are not part of the replicated state, so a
		// survivor could not hang them up: it uses the menu's exit instead.
		c.addTrace("IVR: paging is not possible after a takeover")
		c.setIVRUnrecoverable()
		return false
	}
	return c.ivrPageGroup(ctx, snap, code)
}

// ivrResume restarts the menu on a call a survivor took over: the taker's
// relay already carries the caller, the menu starts from its greeting and
// reads the caller's RFC 4733 digits (a SIP INFO is not recovered).
func (c *call) ivrResume(menu string) {
	relay := c.anchorRelay()
	snap := c.s.deps.Snapshots.Current()
	var m snapshot.IVRMenu
	ok := false
	if snap != nil {
		m, ok = snap.IVRMenu(menu)
	}
	if !ok || relay == nil {
		c.addTrace("IVR menu " + menu + " is gone: ending the call")
		c.ivrHangup()
		return
	}
	st := newIVRState()
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.ivr, c.ivrCancel = st, cancel
	// A taken-over call has no setup of its own: the menu's legs report here.
	c.events = make(chan legEvent, 16)
	c.setupDone = make(chan struct{})
	c.mu.Unlock()
	relay.OnDTMF(func(leg string, digit byte) { c.relayDTMF(leg, digit) })
	relay.OnPacket(c.recTap)
	c.addTrace("IVR menu restarts from its greeting after the takeover")
	go func() {
		defer contain(c.s.log, "ivr flow after takeover")
		c.ivrRun(ctx, m)
	}()
}

// offerSDP is the caller's SDP offer: the INVITE's, or on a call a survivor
// took over the one replicated with the caller's dialog.
func (c *call) offerSDP() []byte {
	if c.inv != nil {
		return c.inv.Body()
	}
	if hom := c.homed(); hom != nil && hom.legs[0] != nil {
		hom.legs[0].mu.Lock()
		defer hom.legs[0].mu.Unlock()
		return []byte(hom.legs[0].remoteSDP)
	}
	return nil
}

// isCallerContact reports whether b is the phone the call came from, which
// is never rung for its own call. A call a survivor took over has no caller
// device name, so the caller's replicated contact stands in for it.
func (c *call) isCallerContact(b livestate.Binding) bool {
	hom := c.homed()
	if hom == nil || hom.legs[0] == nil {
		return false
	}
	hom.legs[0].mu.Lock()
	target := trimAngle(hom.legs[0].remoteTarget)
	hom.legs[0].mu.Unlock()
	return target != "" && trimAngle(b.ContactURI) == target
}

// setIVRUnrecoverable notes that the destination just tried needs something
// a takeover could not bring back; ivrRun then applies the menu's exit.
func (c *call) setIVRUnrecoverable() {
	c.mu.Lock()
	c.ivrUnrecoverable = true
	c.mu.Unlock()
}

// ivrHomeCallee makes the leg that answered a menu's dial-out the callee
// dialog of a call a survivor took over, so the call is an ordinary taken
// over two-leg call from here on: its hang-ups, re-INVITEs and replication
// use the replicated dialog forms. It reports false when the dialog cannot
// be described.
func (c *call) ivrHomeCallee(hom *homedCall, w *leg) bool {
	dcs := w.session()
	relay := c.anchorRelay()
	if dcs == nil || dcs.InviteRequest == nil || dcs.InviteResponse == nil || relay == nil {
		return false
	}
	off, err := media.ParseAudioSDP(c.offerSDP())
	if err != nil {
		return false
	}
	s := c.s
	req, res := dcs.InviteRequest, dcs.InviteResponse
	c.mu.Lock()
	host := c.anchorHost
	c.mu.Unlock()
	l := haLegFrom(livestate.DialogLeg{
		CallID: w.callID, LocalTag: tagParam(req.From()), RemoteTag: tagParam(res.To()),
		LocalCSeq: s.nextCSeq(w.callID, req.CSeq().SeqNo, dcs.CSEQ()), RemoteCSeq: req.CSeq().SeqNo,
		RouteSet:      reversed(headerValues(res, "Record-Route")),
		LocalIdentity: uriString(*req.From().Address.Clone()), RemoteIdentity: uriString(*req.To().Address.Clone()),
		RemoteTarget: uriString(w.target()), Endpoint: w.binding.AOR, Source: res.Source(),
		SDP: relaySDPDir(host, relay, legCallee, off, "sendrecv"), RemoteSDP: string(res.Body()),
	}, false)
	if l == nil {
		return false
	}
	c.mu.Lock()
	hom.legs[1] = l
	c.mu.Unlock()
	return true
}
