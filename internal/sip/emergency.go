package sip

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/ringtag"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
)

// Emergency calls (spec phone-feature-parity S-28). A call over an
// emergency-flagged route already ignores the trunk call limit and a trunk
// known to be down (acquireSlot) and is never diverted: it goes to a trunk,
// not to an extension, so the caller's DND and forwarding do not apply to
// it. What this file adds:
//
//   - a mark on the calling extension for EmergencyCallbackWindow, so a call
//     back to it from a trunk rings with the ring tag emergency-callback and
//     ignores its DND (f);
//   - the notify ring group, which hears a spoken notice of every emergency
//     call (g).

// EmergencyCallbackWindow is how long after its emergency call a call back to
// an extension is treated as the callback.
const EmergencyCallbackWindow = 30 * time.Minute

// EmergencyMarks is the shared record of which extensions placed an emergency
// call lately (Valkey); *livestate.Store satisfies it. Nil keeps the marks on
// this node alone, which is right for a single node.
type EmergencyMarks interface {
	MarkEmergency(ctx context.Context, extension string, ttl time.Duration) error
	EmergencyRecent(ctx context.Context, extension string) (bool, error)
}

// emergencyMarks is the node-local record, used with or without the shared
// one, so a callback on the node that took the call needs no round trip.
type emergencyMarks struct {
	mu sync.Mutex
	m  map[string]time.Time
}

func (e *emergencyMarks) set(ext string, until time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.m == nil {
		e.m = map[string]time.Time{}
	}
	e.m[ext] = until
}

func (e *emergencyMarks) recent(ext string, now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	until, ok := e.m[ext]
	if ok && !now.Before(until) {
		delete(e.m, ext)
		return false
	}
	return ok
}

// markEmergency records that extension placed an emergency call.
func (s *Server) markEmergency(ext string) {
	s.emMarks.set(ext, time.Now().Add(EmergencyCallbackWindow))
	m := s.deps.Emergency
	if m == nil {
		return
	}
	s.goBG(func() {
		defer contain(s.log, "emergency mark")
		ctx, cancel := s.stateCtx()
		defer cancel()
		if err := m.MarkEmergency(ctx, ext, EmergencyCallbackWindow); err != nil {
			s.log.Warn("could not record an emergency call", "extension", ext, "error", err)
		}
	})
}

// emergencyRecent reports whether extension placed an emergency call within
// the window, here or on another node. A shared-state failure counts as no.
func (s *Server) emergencyRecent(ext string) bool {
	if s.emMarks.recent(ext, time.Now()) {
		return true
	}
	m := s.deps.Emergency
	if m == nil {
		return false
	}
	ctx, cancel := s.stateCtx()
	defer cancel()
	ok, err := m.EmergencyRecent(ctx, ext)
	if err != nil {
		s.log.Debug("emergency mark unavailable", "error", err)
		return false
	}
	return ok
}

// dndIgnored reports that this call rings its destination whatever the DND.
func (c *call) dndIgnored() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.ignoreDND
}

// noteEmergencyCallback marks a call from a trunk to an extension that placed
// an emergency call lately: it rings with the emergency-callback tag and
// ignores DND.
func (c *call) noteEmergencyCallback(ext string) {
	c.mu.Lock()
	inbound := c.direction == cdr.DirectionInbound
	c.mu.Unlock()
	if !inbound || !c.s.emergencyRecent(ext) {
		return
	}
	c.mu.Lock()
	c.ringKind, c.ignoreDND = ringtag.KindEmergencyCallback, true
	c.mu.Unlock()
	c.addTrace(fmt.Sprintf("Callback to extension %s, which placed an emergency call within %d minutes: ring tag %s, DND ignored",
		ext, int(EmergencyCallbackWindow/time.Minute), ringtag.EmergencyCallback))
	c.s.m.Emergency.WithLabelValues("callback", "rung").Inc()
}

// noteEmergency records that the call is routed over an emergency route: the
// caller is marked for the callback window and the notify group is told.
func (c *call) noteEmergency(snap *snapshot.Snapshot) {
	s := c.s
	c.mu.Lock()
	c.emergency = true
	ext, name := c.callerNum, c.callerName
	c.mu.Unlock()
	c.addTrace("Emergency route: the call limit and a down trunk do not stop it")
	s.m.Emergency.WithLabelValues("emergency", "routed").Inc()
	if ext != "" {
		s.markEmergency(ext)
	}
	if group := snap.EmergencyNotifyGroup(); group != "" && ext != "" {
		s.goBG(func() {
			defer contain(s.log, "emergency notify")
			s.notifyEmergency(snap, group, ext, name)
		})
	}
}

// speakPCM synthesizes text and returns 8 kHz PCM, or ok false when speech is
// unavailable (the caller then plays a tone).
func (s *Server) speakPCM(text string) (pcm []byte, ok bool) {
	sp := s.deps.Speech
	if sp == nil || text == "" {
		return nil, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), vmSpeakWait)
	defer cancel()
	wav, err := sp.Speak(ctx, text)
	if err != nil {
		s.log.Debug("announcement speech unavailable", "error", err)
		return nil, false
	}
	pcm, rate, err := media.DecodeWAV(wav)
	if err != nil {
		return nil, false
	}
	if rate != 8000 {
		if pcm, err = media.Resample(pcm, rate, 8000); err != nil {
			return nil, false
		}
	}
	return pcm, true
}

// notifyEmergency tells the members of the notify ring group, one way, that
// ext placed an emergency call: each device is paged like a test page (the
// vendor's auto-answer headers, a sendonly offer) and hears the notice, which
// names the extension, before Hello hangs up. A member in DND is paged
// regardless: this is a notice, not a call. Nothing here can fail the
// emergency call itself.
func (s *Server) notifyEmergency(snap *snapshot.Snapshot, group, ext, name string) {
	_, members, ok := snap.RingGroup(group)
	if !ok {
		s.m.Emergency.WithLabelValues("notify", "no_group").Inc()
		s.log.Warn("emergency notify group does not exist", "group", group)
		return
	}
	var cs []*testContact
	for _, m := range members {
		if m.Extension == ext {
			continue
		}
		for _, d := range snap.DevicesForExtension(m.Extension) {
			sctx, cancel := s.stateCtx()
			bs, err := s.deps.State.Bindings(sctx, s.aor(d.Username))
			cancel()
			if err != nil {
				s.log.Warn("emergency notify: live state unavailable", "error", err)
				s.m.Emergency.WithLabelValues("notify", "failed").Inc()
				return
			}
			for _, b := range bs {
				cs = append(cs, &testContact{b: b, ext: m.Extension, name: "Emergency call", num: ext,
					res: livestate.PageTestContact{Device: d.Username, UserAgent: b.UserAgent, Headers: []string{}}})
			}
		}
	}
	if len(cs) == 0 {
		s.m.Emergency.WithLabelValues("notify", "no_phones").Inc()
		return
	}
	lang := voiceprompt.Parse(s.cfg.PromptLanguage)
	who := name
	if who == "" {
		who = ext
	}
	text := voiceprompt.Text(lang, voiceprompt.EmergencyAnnounce, voiceprompt.Vars{"who": who, "ext": voiceprompt.SpellDigits(lang, ext)})
	pcm, spoken := s.speakPCM(text)
	if !spoken {
		pcm = media.PageTonePCM()
	}
	fan, err := media.NewFanout(s.cfg.RTPPortMin, s.cfg.RTPPortMax, 0)
	if err != nil {
		s.log.Warn("emergency notify: no media port", "error", err)
		s.m.Emergency.WithLabelValues("notify", "failed").Inc()
		return
	}
	defer fan.Close()
	offer := media.BuildAudioSDPDir(s.anchorHostOr(s.advHost), fan.MemberPort(), 0, 0, 0, "sendonly")
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.PageAnswerTimeout+time.Minute)
	defer cancel()
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Go(func() {
			defer contain(s.log, "emergency notify contact")
			s.testContact(ctx, c, c.ext, offer, fan)
		})
	}
	wg.Wait()
	var answered []*testContact
	for _, c := range cs {
		if c.res.Result == PageLegAnswered {
			answered = append(answered, c)
		}
	}
	if len(answered) == 0 {
		s.m.Emergency.WithLabelValues("notify", "unanswered").Inc()
		return
	}
	pctx, pcancel := context.WithTimeout(ctx, 30*time.Second)
	_ = fan.PlayMembers(pctx, pcm)
	pcancel()
	var bw sync.WaitGroup
	for _, c := range answered {
		bw.Go(func() {
			defer contain(s.log, "emergency notify BYE")
			c.l.bye()
		})
	}
	bw.Wait()
	s.m.Emergency.WithLabelValues("notify", "delivered").Inc()
}
