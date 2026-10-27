// Boss and secretary (pbx-gaps S-18, S-19). A call to a boss rings the boss's
// devices and the assistant's together (or the assistant's first), the first
// to answer wins, and the CDR names the assistant when the assistant
// answered. With screening on, the assistant who answers hears a prompt
// naming the boss before anyone is connected: 1 puts the caller through to
// the boss, 2 sends the caller to the boss's voicemail. The *64 code toggles
// the divert, which silences the boss's own devices. Everything here reads
// the snapshot only: the database is never on this path.
package sip

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
	"github.com/emiago/sipgo/sip"
)

// Boss timings. bossAssistantLead is how long an assistant_first link rings
// the assistant alone; the screening prompt is repeated once, and each round
// waits screenWait for a key after the prompt.
const (
	screenRounds = 2
	screenWait   = 8 * time.Second
)

// bossAssistantLead is how long an assistant_first link rings the assistant
// alone (a variable so a test need not wait for it).
var bossAssistantLead = 10 * time.Second

// Results of hello_boss_divert_total.
const (
	bossDivertOn      = "on"
	bossDivertOff     = "off"
	bossDivertNoLink  = "no_link"
	bossDivertFailed  = "failed"
	bossScreenThrough = "through"
	bossScreenMessage = "message"
	bossScreenNone    = "none"
)

// bossCall is the boss-link context of one ringing call.
type bossCall struct {
	boss, assistant string
	bossName        string
	// release starts the boss's own legs, once. It runs at once for a link
	// that rings both, after bossAssistantLead for assistant_first, and when
	// a screened call is put through.
	release func()

	// A screened call is played to one assistant device at a time: busy is
	// held while a leg is screened, cur is that leg (guarded by call.mu),
	// and digits carries its keys while active.
	busy   atomic.Bool
	active atomic.Bool
	digits chan byte
	cur    *leg
}

// bindingsOrFail is the live bindings of ext's devices; ok is false when the
// live state is down, after the call has been answered 503 and recorded.
func (c *call) bindingsOrFail(tx sip.ServerTransaction, req *sip.Request, snap *snapshot.Snapshot, ext string) (bs []livestate.Binding, ok bool) {
	bs, err := c.bindingsFor(snap, ext)
	if err != nil {
		if tx != nil {
			c.s.stateDown(tx, req, "bindings", err)
		}
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "live state unavailable", ResultFailed)
		return nil, false
	}
	return bs, true
}

// ringBoss rings a boss's call through its link: the boss's devices (unless
// DND, the divert or a busy line without call waiting silences them) and the
// assistant's. handled is false when the link does not apply and the call
// goes on as an ordinary call to the extension: the caller is the boss or
// the assistant, the assistant does not exist or has no registered device.
// The boss's forwarding always has been decided by the caller of ringBoss.
func (c *call) ringBoss(req *sip.Request, tx sip.ServerTransaction, snap *snapshot.Snapshot, boss string, e snapshot.Extension, link snapshot.BossLink) (handled bool) {
	s := c.s
	if c.callerNum == link.Assistant || c.callerNum == boss {
		return false
	}
	if !snap.HasExtension(link.Assistant) {
		return false
	}
	aTargets, ok := c.bindingsOrFail(tx, req, snap, link.Assistant)
	if !ok {
		return true
	}
	if len(aTargets) == 0 {
		c.addTrace(fmt.Sprintf("Boss link: assistant %s has no registered device", link.Assistant))
		return false
	}
	var bTargets []livestate.Binding
	switch {
	case link.DivertOn:
		c.addTrace(fmt.Sprintf("Boss %s has the divert on: only assistant %s rings", boss, link.Assistant))
	case e.DND:
		c.addTrace(fmt.Sprintf("Boss %s is DND: only assistant %s rings", boss, link.Assistant))
	case e.NoCallWaiting && s.busyExtensions()[boss]:
		c.addTrace(fmt.Sprintf("Boss %s is busy and has call waiting off: only assistant %s rings", boss, link.Assistant))
	default:
		if bTargets, ok = c.bindingsOrFail(tx, req, snap, boss); !ok {
			return true
		}
	}
	c.considerAnchor(snap, bindingEndpoint(aTargets[0], link.Assistant))
	if !c.begin(req, tx) {
		return true
	}
	b := &bossCall{boss: boss, assistant: link.Assistant, bossName: e.Name, digits: make(chan byte, 8)}
	c.mu.Lock()
	b.release = c.bossRelease(b, bTargets, c.stageSeq)
	c.boss = b
	stage := c.stageSeq
	var legs []*leg
	for _, t := range aTargets {
		legs = append(legs, c.addLeg(&leg{binding: t, stage: stage, screen: link.Screening}))
	}
	c.mu.Unlock()
	mode := "both"
	if link.RingMode == snapshot.BossRingAssistantFirst && len(bTargets) > 0 {
		mode = "assistant first"
		time.AfterFunc(bossAssistantLead, b.release)
	}
	c.addTrace(fmt.Sprintf("Boss %s rings with assistant %s (%s)", boss, link.Assistant, mode))
	for _, l := range legs {
		go l.run()
	}
	if mode == "both" {
		b.release()
	}
	c.setup(len(aTargets) + len(bTargets))
	return true
}

// bossRelease returns the function that starts the boss's own legs on stage,
// once, unless the setup has closed, a fork has won or the call has moved to
// another stage by then.
func (c *call) bossRelease(b *bossCall, targets []livestate.Binding, stage int) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			c.mu.Lock()
			if c.setupClosed || c.winner != nil || c.stageSeq != stage {
				c.mu.Unlock()
				return
			}
			legs := make([]*leg, 0, len(targets))
			for _, t := range targets {
				legs = append(legs, c.addLeg(&leg{binding: t, stage: stage}))
			}
			c.mu.Unlock()
			for _, l := range legs {
				go l.run()
			}
		})
	}
}

// noteBossAnswer records that the assistant answered a call to the boss: the
// CDR's answered_by and the trace.
func (c *call) noteBossAnswer(w *leg) {
	c.mu.Lock()
	b := c.boss
	c.mu.Unlock()
	if b == nil || w.binding.Extension != b.assistant {
		return
	}
	c.mu.Lock()
	c.answeredBy = b.assistant
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Answered by assistant %s for %s", b.assistant, b.boss))
}

// outboundCallerIDExt is the extension whose outbound caller-ID rule a call
// from ext uses: the boss of an assistant whose link presents the boss's
// identity (S-19), else "".
func outboundCallerIDExt(snap *snapshot.Snapshot, ext string) string {
	if boss, ok := snap.OutboundBossOf(ext); ok {
		return boss
	}
	return ""
}

// --- screening -------------------------------------------------------------

// screeningLeg is the assistant leg being screened now, nil when none.
func (c *call) screeningLeg() *leg {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.boss == nil {
		return nil
	}
	return c.boss.cur
}

// screenDigit hands a key of the assistant being screened to the prompt; true
// when the prompt took it.
func (c *call) screenDigit(d byte) bool {
	c.mu.Lock()
	b := c.boss
	c.mu.Unlock()
	if b == nil || !b.active.Load() {
		return false
	}
	select {
	case b.digits <- d:
	default: // a flood of keys loses the surplus, never blocks RTP
	}
	return true
}

// screenBye ends the screening when the assistant hangs up during it; true
// when the BYE was the screened leg's.
func (c *call) screenBye(l *leg, req *sip.Request, tx sip.ServerTransaction) bool {
	if l == nil || l != c.screeningLeg() {
		return false
	}
	if err := l.session().ReadBye(req, tx); err != nil {
		c.s.respond(tx, req, sip.StatusBadRequest, "Bad Request")
		return true
	}
	c.s.m.response(sip.StatusOK)
	l.cancel() // the prompt stops, the leg reports itself gone
	return true
}

// screenLeg takes the answer of an assistant's leg on a screened call: the
// leg never wins the call, it is asked what to do with it. It reports false
// when the call cannot be screened (no media anchor, a late offer), and the
// answer then proceeds as an ordinary one.
func (c *call) screenLeg(l *leg, report func(legEvent)) bool {
	c.mu.Lock()
	b, relay := c.boss, c.relay
	c.mu.Unlock()
	var res *sip.Response
	if dcs := l.session(); dcs != nil {
		res = dcs.InviteResponse
	}
	if b == nil || relay == nil || res == nil || len(res.Body()) == 0 || c.inv == nil || len(c.inv.Body()) == 0 {
		c.addTrace("Boss screening skipped: the call's media is not anchored")
		return false
	}
	ans, err := media.ParseAudioSDP(res.Body())
	if err != nil {
		return false
	}
	if !b.busy.CompareAndSwap(false, true) {
		// Another device of the assistant is being screened: this one is
		// not needed.
		l.bye()
		report(legEvent{leg: l, kind: evFailed, code: sip.StatusTemporarilyUnavailable})
		return true
	}
	defer b.busy.Store(false)
	c.screenRun(l, b, relay, ans, res.Source(), report)
	return true
}

// screenRun plays the prompt to the assistant on the relay's callee leg while
// the caller still hears ringback, and reports what the assistant chose. The
// legs are muted against each other until then, so the caller is never
// heard by the assistant (nor the assistant by the caller).
func (c *call) screenRun(l *leg, b *bossCall, relay *media.Relay, ans media.AudioSDP, source string, report func(legEvent)) {
	l.ack(nil, "")
	// The relay's callee leg is shared by every fork: aim it at the assistant
	// only while nobody has won. Checked under mu, a fork that wins after this
	// runs its own answer (and re-aims the leg) later.
	c.mu.Lock()
	if c.winner != nil || c.setupClosed {
		c.mu.Unlock()
		l.bye()
		report(legEvent{leg: l, kind: evFailed, code: sip.StatusTemporarilyUnavailable})
		return
	}
	relay.SetCodecs(legCallee, ans.Codecs, ans.DTMFPayloadType)
	relay.SetTarget(legCallee, udpAddr(ans.Address, ans.Port))
	allowSignalSource(relay, legCallee, source)
	relay.Mute(legCaller, true)
	relay.Mute(legCallee, true)
	b.cur = l
	c.mu.Unlock()
	for len(b.digits) > 0 {
		<-b.digits
	}
	b.active.Store(true)
	choice := c.screenAsk(l.ctx, relay, b)
	b.active.Store(false)
	c.mu.Lock()
	b.cur = nil
	c.mu.Unlock()
	relay.Mute(legCaller, false)
	relay.Mute(legCallee, false)
	l.bye()
	s := c.s
	switch choice {
	case '1':
		c.addTrace(fmt.Sprintf("Boss screening: assistant %s puts the call through to %s", b.assistant, b.boss))
		s.log.Info("boss screening", "boss", b.boss, "assistant", b.assistant, "result", bossScreenThrough)
		b.release()
		report(legEvent{leg: l, kind: evFailed, code: sip.StatusTemporarilyUnavailable})
	case '2':
		c.mu.Lock()
		st := c.stages
		c.mu.Unlock()
		if st != nil && c.voicemailConfigured(st.feats) {
			c.addTrace(fmt.Sprintf("Boss screening: assistant %s sends the call to the voicemail of %s", b.assistant, b.boss))
			s.log.Info("boss screening", "boss", b.boss, "assistant", b.assistant, "result", bossScreenMessage)
			report(legEvent{leg: l, kind: evScreenMessage})
			return
		}
		c.addTrace(fmt.Sprintf("Boss screening: %s has no voicemail box", b.boss))
		report(legEvent{leg: l, kind: evFailed, code: sip.StatusTemporarilyUnavailable})
	default:
		c.addTrace(fmt.Sprintf("Boss screening: assistant %s made no choice", b.assistant))
		s.log.Info("boss screening", "boss", b.boss, "assistant", b.assistant, "result", bossScreenNone)
		report(legEvent{leg: l, kind: evFailed, code: sip.StatusTemporarilyUnavailable})
	}
}

// screenAsk plays the prompt and waits for 1 or 2, repeating the prompt once.
// It returns the key, or 0 when the assistant chose nothing or the screening
// was cancelled.
func (c *call) screenAsk(ctx context.Context, relay *media.Relay, b *bossCall) byte {
	pcm, rate := c.screenAudio(ctx, b)
	valid := func(d byte) bool { return d == '1' || d == '2' }
	play := func() (byte, bool) {
		pctx, cancel := context.WithCancel(ctx)
		defer cancel()
		done := make(chan struct{})
		go func() {
			defer close(done)
			_ = relay.PlayPCMTo(pctx, legCallee, pcm, rate)
		}()
		for {
			select {
			case d := <-b.digits:
				if valid(d) {
					cancel()
					<-done
					return d, true
				}
			case <-done:
				return 0, false
			case <-ctx.Done():
				<-done
				return 0, false
			}
		}
	}
	wait := func() (byte, bool) {
		t := time.NewTimer(screenWait)
		defer t.Stop()
		for {
			select {
			case d := <-b.digits:
				if valid(d) {
					return d, true
				}
			case <-t.C:
				return 0, false
			case <-ctx.Done():
				return 0, false
			}
		}
	}
	for round := 0; round < screenRounds; round++ {
		if d, ok := play(); ok {
			return d
		}
		if ctx.Err() != nil {
			return 0
		}
		if d, ok := wait(); ok {
			return d
		}
		if ctx.Err() != nil {
			return 0
		}
	}
	return 0
}

// screenAudio is the prompt as PCM: spoken in the configured voice, or a
// tone when speech is not available (the choice still works on the keys).
func (c *call) screenAudio(ctx context.Context, b *bossCall) (pcm []byte, rate int) {
	who := b.bossName
	if who == "" {
		who = b.boss
	}
	text := voiceprompt.Text(voiceprompt.Default, voiceprompt.BossScreen, voiceprompt.Vars{"who": who})
	if sp := c.s.deps.Speech; sp != nil {
		sctx, cancel := context.WithTimeout(ctx, vmSpeakWait)
		defer cancel()
		wav, err := sp.Speak(sctx, text)
		if err == nil {
			if p, r, derr := media.DecodeWAV(wav); derr == nil && len(p) > 0 {
				return p, r
			}
		}
		c.s.log.Warn("boss screening: spoken prompt unavailable; playing a tone instead", "error", err)
	}
	return media.BeepPCMRate(8000), 8000
}

// screenMessage is the setup loop's answer to the assistant's choice of the
// boss's voicemail: the forks stop and the caller leaves a message in the
// boss's box. It reports false when a fork has won in the meantime.
func (c *call) screenMessage() bool {
	c.mu.Lock()
	b := c.boss
	c.mu.Unlock()
	if b == nil || !c.closeSetup() {
		return false
	}
	c.cancelForks(nil)
	c.mu.Lock()
	c.vmExt = b.boss
	c.mu.Unlock()
	c.s.m.Forwarded.WithLabelValues(ForwardBossScreen).Inc()
	c.startVoicemail(vmLeave, "no-answer")
	return true
}

// --- the divert code -------------------------------------------------------

// bossDivertUpdate is the queued change of *64 for ext: it flips the divert
// of the link whose boss is ext. ok is false when ext is no boss.
func bossDivertUpdate(snap *snapshot.Snapshot, ext string) (upd ExtUpdate, ok bool) {
	link, ok := snap.BossLinkOf(ext)
	if !ok {
		return ExtUpdate{}, false
	}
	on := !link.DivertOn
	return ExtUpdate{Extension: ext, BossDivert: &on}, true
}

// applyBossDivert queues the toggle and counts it. ok is false when the
// extension is no boss or the change was not accepted.
func (s *Server) applyBossDivert(snap *snapshot.Snapshot, ext string) (upd ExtUpdate, result string, ok bool) {
	upd, found := bossDivertUpdate(snap, ext)
	switch {
	case !found:
		result = bossDivertNoLink
	case s.deps.Settings == nil || !s.deps.Settings.Apply(upd):
		result = bossDivertFailed
	case *upd.BossDivert:
		result, ok = bossDivertOn, true
	default:
		result, ok = bossDivertOff, true
	}
	s.m.BossDivert.WithLabelValues(result).Inc()
	return upd, result, ok
}

// handleBossDivert serves *64 dialled as a call from the boss's own phone:
// the confirmation is the 200 the phone hears, as for DND.
func (c *call) handleBossDivert(tx sip.ServerTransaction) {
	s := c.s
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		s.unavailable(tx, c.inv)
		return
	}
	_, result, ok := s.applyBossDivert(snap, c.callerNum)
	switch {
	case ok:
		if !c.begin(c.inv, tx) {
			return
		}
		c.addTrace("Boss divert " + result)
		c.answerSelf(c.inv.Body())
	case result == bossDivertNoLink:
		s.respond(tx, c.inv, sip.StatusNotFound, "Not Found")
		c.record(sip.StatusNotFound, cdr.SideSystem, "the extension is not a boss", ResultNotFound)
	default:
		s.respond(tx, c.inv, sip.StatusServiceUnavailable, "Service Unavailable")
		c.record(sip.StatusServiceUnavailable, cdr.SideSystem, "configuration store unavailable", ResultUnavailable)
	}
}

// ivrAssistant is the assistant's live bindings for a menu's call to ext when
// ext is a boss whose link applies, and whether the boss's divert is on. It
// is nil when no link applies or the assistant has no registered phone.
func (c *call) ivrAssistant(snap *snapshot.Snapshot, ext string) (bs []livestate.Binding, divert bool) {
	link, ok := snap.BossLinkOf(ext)
	if !ok || link.Assistant == ext || !snap.HasExtension(link.Assistant) {
		return nil, false
	}
	c.mu.Lock()
	caller := c.callerNum
	c.mu.Unlock()
	if caller == link.Assistant || caller == ext {
		return nil, false
	}
	bs, err := c.extensionBindings(snap, link.Assistant)
	if err != nil || len(bs) == 0 {
		return nil, false
	}
	c.mu.Lock()
	c.boss = &bossCall{boss: ext, assistant: link.Assistant}
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Boss %s rings with assistant %s", ext, link.Assistant))
	return bs, link.DivertOn
}
