// Voicemail (S-8): the anchored-media application behind busy, DND and
// no-answer calls, message storage, and MWI. Everything that touches
// PostgreSQL or MinIO runs in the call's media goroutine (or an MWI
// goroutine), each operation bounded by the StateTimeout discipline, so the
// SIP transaction path never waits on either.
package sip

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/voiceprompt"
	"github.com/emiago/sipgo/sip"
)

// VoicemailBox is the store's view of one box (migration 00004).
type VoicemailBox struct {
	ID                int64
	Extension         string
	Email             string
	GreetingObject    string
	UnreachableObject string
	// BusyObject is the custom greeting for a busy extension and NameObject
	// the owner's recorded name (migration 00036). An empty one means the
	// default spoken prompt.
	BusyObject string
	NameObject string
}

// VoicemailStore is the voicemail metadata store (PostgreSQL, implemented by
// internal/store in the control plane). Every method is called from the
// voicemail media goroutine only — never on the SIP transaction path.
type VoicemailStore interface {
	// Box returns the extension's box (ok=false without one).
	Box(ctx context.Context, extension string) (VoicemailBox, bool, error)
	// PasswordOK compares a retrieval password (hashing lives here).
	PasswordOK(ctx context.Context, boxID int64, password string) (bool, error)
	// InsertMessage stores one message and returns its id. callerName is the
	// caller-ID display name the call carried ("" for none).
	InsertMessage(ctx context.Context, boxID int64, object, caller, callerName string, durationMs int64) (int64, error)
	// MessageCounts returns the unread and total message counts.
	MessageCounts(ctx context.Context, boxID int64) (unread, total int, err error)
	// ListMessages returns the box's messages, newest first.
	ListMessages(ctx context.Context, boxID int64) ([]VoicemailMessage, error)
	// MarkHeard marks a message heard.
	MarkHeard(ctx context.Context, id int64) error

	// The guided session's writes (*97 menus). Each acts on the caller's own
	// box only.

	// DeleteMessage removes a message of the box and returns its object key.
	DeleteMessage(ctx context.Context, boxID, id int64) (object string, err error)
	// SetPIN changes the box's retrieval PIN.
	SetPIN(ctx context.Context, boxID int64, pin string) error
	// SetGreeting points a greeting slot ("greeting", "busy", "unreachable"
	// or "name") at object and returns the object it replaced. An empty
	// object reverts the slot to the spoken default.
	SetGreeting(ctx context.Context, boxID int64, slot, object string) (replaced string, err error)
	// ResetGreetings reverts the greeting, busy and unreachable slots and
	// returns the objects dropped.
	ResetGreetings(ctx context.Context, boxID int64) ([]string, error)
	// ContactName names number from the address books ("" when unknown).
	// callee is the mailbox owner's extension (its phones' books apply).
	ContactName(ctx context.Context, number, callee string) (string, error)
}

// VoicemailMessage is one stored message.
type VoicemailMessage struct {
	ID          int64
	BoxID       int64
	MinioObject string
	Caller      string
	// CallerName is the caller-ID display name the call carried.
	CallerName string
	DurationMs int64
	Heard      bool
	CreatedAt  time.Time
}

// ObjectStore is the call-plane audio store (MinIO, buckets hello-voicemail,
// hello-recordings and hello-announcements, contracts 4 and 6; implemented
// by internal/media). Called from the media goroutine only.
type ObjectStore interface {
	Put(ctx context.Context, key string, data []byte) error
	Get(ctx context.Context, key string) ([]byte, error)
	PutRecording(ctx context.Context, key string, data []byte) error
	GetAnnouncement(ctx context.Context, key string) ([]byte, error)
}

// Voicemail application modes.
const (
	vmLeave = iota
	vmRetrieve
)

// voicemailOwnBox is the *97 path: the caller's own box, password-gated. (*97
// with an extension after it leaves a message instead, see voicemailTarget.)
func (c *call) voicemailOwnBox(string) {
	c.mu.Lock()
	c.vmTarget = c.callerNum
	c.mu.Unlock()
	c.startVoicemail(vmRetrieve, "")
}

// voicemailTarget points the call at extension ext's voicemail box, for the
// voicemail feature code dialled with an extension (*97<ext>, which is also
// what a phone's transfer-to-voicemail key dials). It reports false when ext
// is no extension of this PBX or its box cannot take a message here.
func (c *call) voicemailTarget(ext string) bool {
	snap := c.s.deps.Snapshots.Current()
	if snap == nil {
		return false
	}
	e, ok := snap.Extension(ext)
	if !ok || !c.voicemailConfigured(e) {
		return false
	}
	c.mu.Lock()
	c.dialled = ext
	c.mu.Unlock()
	return true
}

// mailboxExt is the extension whose box the application works on: the
// dialled extension for a message being left, the caller's own for retrieval.
// (The dialled string of a *97 call is the feature code, not a mailbox.)
func (c *call) mailboxExt(mode int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if mode == vmRetrieve {
		if c.vmTarget != "" {
			return c.vmTarget
		}
		return c.callerNum
	}
	if c.vmExt != "" {
		return c.vmExt // an IVR menu handed the call to this box
	}
	return c.dialled
}

// startVoicemail answers the caller through the media anchor and runs the
// application in the media goroutine. reason is why the call is here
// ("no-answer", "busy", "dnd", "unreachable" or "" for *97). The box id
// comes from the snapshot, so no database call precedes the 200.
func (c *call) startVoicemail(mode int, reason string) {
	s := c.s
	answer, sess, err := s.answerAnchored(c.inv.Body())
	if err != nil {
		// No anchor: the call still completes, without recording (spec
		// failure mode); the flow notes it in the CDR.
		c.addTrace("Media anchor unavailable: voicemail without recording")
		answer, sess = c.inv.Body(), nil
	}
	c.mu.Lock()
	c.mediaMode = "anchored"
	c.vmSession = sess
	c.mu.Unlock()
	// The answer goes out before the INVITE handler returns (sipgo
	// terminates the transaction then); only the application runs on after
	// it, in its own goroutine.
	if !c.answerSelf(answer) {
		return
	}
	if sess != nil {
		c.logTerminatedCodec("answer", "voicemail", answer)
	}
	if sess != nil && s.deps.HAState != nil {
		// The voicemail call replicates like any (incall-ha S-5): a taker
		// re-INVITEs the caller onto its own anchor and restarts the
		// application from the greeting.
		c.haSoloRefresh(string(answer))
		c.mu.Lock()
		c.haPhase, c.haDetail = haPhaseVoicemail, voicemailDetail(mode, reason, c.mailboxExtLocked(mode))
		c.mu.Unlock()
		go c.haLoop()
	}
	go c.voicemailFlow(mode, reason, sess)
}

// mailboxExtLocked is mailboxExt for a caller that holds c.mu.
func (c *call) mailboxExtLocked(mode int) string {
	if mode == vmRetrieve {
		if c.vmTarget != "" {
			return c.vmTarget
		}
		return c.callerNum
	}
	if c.vmExt != "" {
		return c.vmExt // an IVR menu handed the call to this box
	}
	return c.dialled
}

// answerAnchored binds the media anchor for this call and builds the answer
// SDP from the caller's offer.
func (s *Server) answerAnchored(offer []byte) ([]byte, media.Session, error) {
	anchor := s.voicemailAnchor(offer)
	if s.deps.Objects == nil || anchor == nil {
		return nil, nil, errors.New("voicemail media disabled")
	}
	return anchor.Answer(offer)
}

// voicemailAnchor is the node's media anchor; without an advertised anchor
// host (HELLO_MEDIA_ANCHOR_HOST unset, as in the lab) it advertises the
// offer's own address, the way the relay mirrors it (anchorHostOr), so
// voicemail still records. nil when neither gives an address.
func (s *Server) voicemailAnchor(offer []byte) *media.Anchor {
	if a := s.anchor.Load(); a != nil {
		return a
	}
	off, err := media.ParseAudioSDP(offer)
	if err != nil {
		return nil
	}
	if ip := net.ParseIP(off.Address); ip == nil || ip.To4() == nil {
		return nil
	}
	return media.NewAnchor(off.Address, s.log)
}

// answerSelf connects a call with no B leg (voicemail, feature codes):
// the caller dialog is answered with body as its SDP answer.
// answerSelf connects a call with no B leg (voicemail): the caller's INVITE
// is answered with body as its SDP answer, through the raw transaction. The
// answer carries our own To tag, which identifies the dialog from here on —
// sipgo's dialog 2xx path waits out the full ACK retransmission window, and
// the voicemail media must not queue behind that.
func (c *call) answerSelf(body []byte) bool {
	s := c.s
	c.stopRingMOH() // voicemail and announcements replace the waiting music
	c.mu.Lock()
	tx := c.invTx
	c.mu.Unlock()
	if tx == nil {
		return false
	}
	tag := sip.GenerateTagN(8)
	res := sip.NewResponseFromRequest(c.inv, sip.StatusOK, "OK", body)
	res.To().Params = append(res.To().Params, sip.HeaderParams{{K: "tag", V: tag}}...)
	if len(body) > 0 {
		res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	}
	res.AppendHeader(sip.HeaderClone(&s.contact))
	res.AppendHeader(sip.NewHeader("Allow", allow))
	c.mu.Lock()
	c.connected = true
	c.answerTime = time.Now()
	c.vmTag = tag
	c.mu.Unlock()
	if h := s.answerHook.Load(); h != nil {
		(*h)()
	}
	c.addTrace("Call established")
	c.publish()
	s.m.response(sip.StatusOK)
	if err := tx.Respond(res); err != nil {
		c.mu.Lock()
		c.connected = false
		c.mu.Unlock()
		c.byeA()
		c.end(sip.StatusOK, cdr.SideSystem, "answer failed", ResultFailed)
		return false
	}
	c.mu.Lock()
	if !c.ended {
		c.maxTimer = time.AfterFunc(s.cfg.MaxCallDuration, c.expire)
	}
	c.mu.Unlock()
	return true
}

// vmCtx is the media goroutine's context: cancelled when the call ends for
// any reason, so recording never outlives its call.
func (c *call) vmCtx() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		select {
		case <-c.stopHB:
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, cancel
}

// voicemailFlow runs the application after the answer: greeting, beep,
// record, store (with retry), MWI — or the password-gated retrieval. All
// store and object calls happen here, in the media goroutine, each bounded
// by the StateTimeout discipline.
func (c *call) voicemailFlow(mode int, reason string, sess media.Session) {
	defer contain(c.s.log, "voicemail flow")
	if sess != nil {
		// The application owns the session's socket: it closes once the
		// message is stored (or the retrieval is over).
		defer func() { _ = sess.Close() }()
	}
	ctx, cancel := c.vmCtx()
	defer cancel()
	ext := c.mailboxExt(mode)
	box := c.boxDetails(ext)
	switch mode {
	case vmRetrieve:
		c.vmRetrieve(ctx, sess, box, ext)
	default:
		c.vmLeave(ctx, sess, box, reason)
	}
	// The call is over for the caller once the application has run.
	c.mu.Lock()
	done := c.hungUp || c.ended
	c.mu.Unlock()
	if !done {
		side := cdr.SideCallee
		if c.isLinked() {
			side = cdr.SideSystem // a parked call's party is on either side of the dialog
		}
		c.hangup(side)
	}
}

// boxDetails loads the box's settings from the store (greeting objects,
// email); failures leave a zero box, which records without a greeting.
func (c *call) boxDetails(ext string) VoicemailBox {
	s := c.s
	if s.deps.Voicemails == nil {
		return VoicemailBox{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	defer cancel()
	box, ok, err := s.deps.Voicemails.Box(ctx, ext)
	if err != nil {
		s.log.Warn("voicemail box lookup failed", "error", err)
	}
	if !ok {
		return VoicemailBox{}
	}
	return box
}

// vmLeave records one message: greeting, beep, record (# finish, * retry),
// then WAV to MinIO with retry and the message row to the store. The store
// work runs detached from the call context, so a caller who hangs up
// mid-recording still leaves the message he recorded (spec edge case: only
// a take under one second is discarded).
func (c *call) vmLeave(ctx context.Context, sess media.Session, box VoicemailBox, reason string) {
	c.playPreRoll(ctx, sess)
	c.playGreeting(ctx, sess, box, reason)
	if sess != nil {
		// The tone that says "now".
		_ = sess.Play(ctx, media.BeepPCMRate(sess.SampleRate()))
	}
	c.mu.Lock()
	vm := c.s.deps.Voicemails
	objs := c.s.deps.Objects
	c.mu.Unlock()
	if sess == nil || vm == nil || objs == nil {
		c.s.m.VoicemailMessages.WithLabelValues(VMFailed).Inc()
		return
	}
	audio, dur, ok := c.recordMessage(ctx, sess)
	ctx = context.WithoutCancel(ctx) // the caller may hang up; the message survives
	if !ok {
		// Caller gone during the beep, or under one second: discarded.
		c.s.m.VoicemailMessages.WithLabelValues(VMDiscord).Inc()
		c.addTrace("Voicemail message discarded (hangup or shorter than 1s)")
		return
	}
	wav := media.EncodeWAV(audio, sess.SampleRate())
	key, err := media.ObjectKey(box.ID, c.callID, time.Now())
	if err != nil {
		c.s.log.Warn("voicemail object key refused", "error", err)
		c.s.m.VoicemailMessages.WithLabelValues(VMFailed).Inc()
		return
	}
	if !c.putAudio(ctx, key, wav) {
		// MinIO stayed down: the message is marked failed, MWI reports the
		// unchanged counts, nothing blocked the SIP path (spec failure mode).
		c.s.m.VoicemailMessages.WithLabelValues(VMFailed).Inc()
		c.addTrace("Voicemail storage failed after 3 attempts")
		return
	}
	idctx, icancel := context.WithTimeout(context.Background(), c.s.cfg.StateTimeout)
	defer icancel()
	if _, err := vm.InsertMessage(idctx, box.ID, key, c.callerNum, c.callerName, dur.Milliseconds()); err != nil {
		c.s.log.Warn("voicemail message row failed", "error", err)
		c.s.m.VoicemailMessages.WithLabelValues(VMFailed).Inc()
		c.addTrace("Voicemail metadata store failed")
		return
	}
	c.mu.Lock()
	n := c.s.vmBytes.Add(int64(len(wav)))
	c.mu.Unlock()
	c.s.m.VoicemailStorage.Set(float64(n))
	c.s.m.VoicemailMessages.WithLabelValues(VMStored).Inc()
	c.addTrace(fmt.Sprintf("Voicemail message %d stored (%d ms)", box.ID, dur.Milliseconds()))
	c.sendMWI(c.mailboxExt(vmLeave))
}

// customGreeting is the object of the box's own greeting for reason: the
// busy greeting for a busy call and the unreachable one for an unreachable
// call, each falling back to the general greeting, which every other reason
// plays. "" means the box has none and the spoken default applies.
func customGreeting(box VoicemailBox, reason string) string {
	var specific string
	switch reason {
	case "busy":
		specific = box.BusyObject
	case "unreachable":
		specific = box.UnreachableObject
	}
	if specific != "" {
		return specific
	}
	return box.GreetingObject
}

// greetingVariant is the default spoken greeting a call reason gets.
func greetingVariant(reason string) voiceprompt.Variant {
	switch reason {
	case "busy":
		return voiceprompt.Busy
	case "unreachable":
		return voiceprompt.Unreachable
	}
	return voiceprompt.Unavailable
}

// playGreeting plays the box's greeting: its own recording or upload when it
// has one for this reason, else the default spoken in the configured voice
// (naming the owner), else nothing. A failed fetch or synthesis is logged and
// skipped: the tone still marks the recording start.
func (c *call) playGreeting(ctx context.Context, sess media.Session, box VoicemailBox, reason string) {
	if sess == nil {
		return
	}
	if key := customGreeting(box, reason); key != "" && c.s.deps.Objects != nil {
		if c.playObject(ctx, sess, key) {
			return
		}
	}
	c.playDefaultGreeting(ctx, sess, box, greetingVariant(reason))
}

// playObject plays a stored WAV and reports whether it could.
func (c *call) playObject(ctx context.Context, sess media.Session, key string) bool {
	octx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	raw, err := c.s.deps.Objects.Get(octx, key)
	if err != nil {
		c.s.log.Warn("voicemail greeting unavailable", "error", err)
		return false
	}
	pcm, err := media.WAVToRate(raw, sess.SampleRate())
	if err != nil {
		c.s.log.Warn("voicemail greeting is not a WAV", "error", err)
		return false
	}
	if err := sess.Play(ctx, pcm); err != nil {
		c.s.log.Debug("greeting playback failed", "error", err)
	}
	return true
}

// speakOnce synthesizes text (or fetches it from the cache) and plays it;
// false when speech is unavailable.
func (c *call) speakOnce(ctx context.Context, sess media.Session, text string) bool {
	sp := c.s.deps.Speech
	if sp == nil || text == "" {
		return false
	}
	sctx, cancel := context.WithTimeout(ctx, vmSpeakWait)
	defer cancel()
	wav, err := sp.Speak(sctx, text)
	if err != nil {
		c.s.log.Debug("voicemail default greeting unavailable", "error", err)
		return false
	}
	pcm, err := media.WAVToRate(wav, sess.SampleRate())
	if err != nil {
		return false
	}
	if err := sess.Play(ctx, pcm); err != nil {
		c.s.log.Debug("greeting playback failed", "error", err)
	}
	return true
}

// playDefaultGreeting speaks "<name> is not available, please leave a
// message after the tone" (or the busy / unreachable wording). With a
// recorded name it plays the recording and speaks only the rest.
func (c *call) playDefaultGreeting(ctx context.Context, sess media.Session, box VoicemailBox, v voiceprompt.Variant) {
	if c.s.deps.Speech == nil {
		return
	}
	lang := c.s.promptLang()
	ext := c.mailboxExt(vmLeave)
	if box.NameObject != "" && c.s.deps.Objects != nil && c.playObject(ctx, sess, box.NameObject) {
		c.speakOnce(ctx, sess, voiceprompt.GreetingTail(lang, v))
		return
	}
	name := ""
	if snap := c.s.deps.Snapshots.Current(); snap != nil {
		if e, ok := snap.Extension(ext); ok {
			name = e.Name
		}
	}
	c.speakOnce(ctx, sess, voiceprompt.GreetingText(lang, v, voiceprompt.Owner(lang, name, ext)))
}

// recordMessage runs the record loop: DTMF '#' finishes, '*' retries (at
// most three takes), the caller hanging up stops everything. Audio shorter
// than one second of recording is discarded. The digit watcher is the only
// reader of the DTMF channel: Record forwards each digit there, and the
// watcher decides — '#' and '*' both cancel the take, and the last digit
// says which.
func (c *call) recordMessage(ctx context.Context, sess media.Session) ([]byte, time.Duration, bool) {
	dtmf := make(chan byte, 16)
	start := time.Now()
	for attempt := 0; attempt < 3; attempt++ {
		rctx, rcancel := context.WithCancel(ctx)
		var lastDigit byte
		watch := make(chan struct{})
		go func() {
			defer close(watch)
			for {
				select {
				case d := <-dtmf:
					lastDigit = d
					if d == '*' {
						rcancel()
						return
					}
				case <-rctx.Done():
					return
				}
			}
		}()
		const maxMessage = time.Minute
		audio, _ := sess.Record(rctx, maxMessage, dtmf)
		rcancel()
		<-watch
		dur := time.Since(start)
		if lastDigit == '#' {
			if len(audio) >= 2*sess.SampleRate() { // one second of 16-bit samples
				return audio, dur, true
			}
			return nil, 0, false
		}
		if lastDigit == '*' {
			continue // the caller retypes the message
		}
		// The caller hung up (or the session closed): keep what was said.
		if len(audio) >= 2*sess.SampleRate() {
			return audio, dur, true
		}
		return nil, 0, false
	}
	return nil, 0, false
}

// putAudio writes the WAV to MinIO, retrying three times with backoff;
// every attempt is bounded by the StateTimeout discipline.
func (c *call) putAudio(ctx context.Context, key string, wav []byte) bool {
	for i := 0; i < 3; i++ {
		octx, cancel := context.WithTimeout(ctx, c.s.cfg.StateTimeout)
		err := c.s.deps.Objects.Put(octx, key, wav)
		cancel()
		if err == nil {
			return true
		}
		c.s.log.Warn("voicemail audio upload failed", "attempt", i+1, "error", err)
		select {
		case <-ctx.Done():
			return false
		case <-time.After(time.Duration(100*(i+1)) * time.Millisecond):
		}
	}
	return false
}

// vmRetrieve is the *97 application. With speech available it is the guided
// session (vmsession.go): PIN by voice, counts, each message introduced,
// menus. Without it - no key configured, or the provider down - it is the
// plain flow it always was: a tone, the PIN, then the unheard messages one
// after the other. Either way three wrong PINs end the call.
func (c *call) vmRetrieve(ctx context.Context, sess media.Session, box VoicemailBox, ext string) {
	s := c.s
	if sess == nil || s.deps.Voicemails == nil || s.deps.Objects == nil {
		return
	}
	if box.ID == 0 {
		// No box: nothing to retrieve (the 200 already went out).
		if s.deps.Speech != nil {
			ui := newCallUI(ctx, c, sess, s.deps.Speech)
			lang := s.promptLang()
			_, _ = ui.Say(ctx, utt{Key: voiceprompt.NoMailbox, Text: voiceprompt.Text(lang, voiceprompt.NoMailbox, nil)}, false)
			ui.close()
		}
		return
	}
	lang := s.promptLang()
	if s.deps.Speech != nil {
		// The first prompt decides: if it cannot be spoken, fall back before
		// the caller has heard anything.
		pinText := voiceprompt.Text(lang, voiceprompt.PINEnter, nil)
		wctx, cancel := context.WithTimeout(ctx, vmSpeakWait)
		_, err := s.deps.Speech.Speak(wctx, pinText)
		cancel()
		if err == nil {
			ui := newCallUI(ctx, c, sess, s.deps.Speech)
			defer ui.close()
			data := &callData{c: c, box: box, mailbox: ext}
			m := newVMMachine(lang, s.timeLocation(), ui, data)
			if err := m.run(ctx); err != nil && ctx.Err() == nil {
				s.log.Debug("voicemail session ended", "error", err)
			}
			c.sendMWI(ext)
			return
		}
		s.log.Warn("voicemail: spoken prompts unavailable; using the plain retrieval flow", "error", err)
	}
	c.vmRetrievePlain(ctx, sess, box, ext)
}

// vmRetrievePlain is the retrieval without speech: prompt (beep), collect the
// password with DTMF, then play the unheard messages one by one, marking each
// heard. Three wrong passwords end the call.
func (c *call) vmRetrievePlain(ctx context.Context, sess media.Session, box VoicemailBox, ext string) {
	s := c.s
	var ok bool
	var err error
	for attempt := 0; attempt < 3 && !ok; attempt++ {
		_ = sess.Play(ctx, media.BeepPCMRate(sess.SampleRate()))
		dtmf := make(chan byte, 16)
		dctx, dcancel := context.WithCancel(ctx)
		digits := collectDigits(dctx, sess, dtmf)
		dcancel()
		pctx, pcancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
		ok, err = s.deps.Voicemails.PasswordOK(pctx, box.ID, string(digits))
		pcancel()
		if err != nil {
			s.log.Warn("voicemail password check failed", "error", err)
			return
		}
	}
	if !ok {
		return
	}
	lctx, lcancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
	msgs, err := s.deps.Voicemails.ListMessages(lctx, box.ID)
	lcancel()
	if err != nil {
		s.log.Warn("voicemail list failed", "error", err)
		return
	}
	for _, m := range msgs {
		if m.Heard {
			continue
		}
		octx, ocancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
		raw, err := s.deps.Objects.Get(octx, m.MinioObject)
		ocancel()
		if err != nil {
			s.log.Warn("voicemail message unavailable", "error", err)
			continue
		}
		pcm, err := media.WAVToRate(raw, sess.SampleRate())
		if err != nil {
			continue
		}
		if err := sess.Play(ctx, pcm); err != nil {
			return
		}
		hctx, hcancel := context.WithTimeout(ctx, s.cfg.StateTimeout)
		err = s.deps.Voicemails.MarkHeard(hctx, m.ID)
		hcancel()
		if err != nil {
			s.log.Warn("voicemail mark-heard failed", "error", err)
		}
	}
	c.sendMWI(ext)
}

// collectDigits reads DTMF until '#'. The session's own '#' handling ends
// Record; here the digits arrive on the channel as the caller dials them.
func collectDigits(ctx context.Context, sess media.Session, dtmf chan byte) []byte {
	var out []byte
	rctx, rcancel := context.WithCancel(ctx)
	go func() {
		_, _ = sess.Record(rctx, 30*time.Second, dtmf) // ends on '#' or hangup
		rcancel()
	}()
	for {
		select {
		case d := <-dtmf:
			if d == '#' {
				return out
			}
			out = append(out, d)
		case <-rctx.Done():
			return out
		}
	}
}

// sendMWI tells the mailbox owner ext the counts changed (S-1).
func (c *call) sendMWI(ext string) {
	c.s.notifyMWI(ext)
}

// greetingKey is the object key of a greeting recorded from the phone.
func greetingKey(boxID int64, slot string, at time.Time) (string, error) {
	switch slot {
	case "greeting", "busy", "unreachable", "name":
	default:
		return "", fmt.Errorf("sip: unknown greeting slot %q", slot)
	}
	return fmt.Sprintf("box/%d/%s-%d.wav", boxID, slot, at.UnixNano()), nil
}

// objectRemover is the optional delete of an object store; without it a
// replaced greeting or deleted message is left behind in storage, harmless.
type objectRemover interface {
	Remove(ctx context.Context, key string) error
}

// removeObject deletes key from the object store, best effort.
func (c *call) removeObject(ctx context.Context, key string) {
	if key == "" {
		return
	}
	if r, ok := c.s.deps.Objects.(objectRemover); ok {
		if err := r.Remove(ctx, key); err != nil {
			c.s.log.Warn("voicemail: remove object", "error", err)
		}
	}
}

// promptLang is the language of the spoken prompts.
func (s *Server) promptLang() voiceprompt.Lang { return voiceprompt.Parse(s.cfg.PromptLanguage) }

// timeLocation is the zone message times are spoken in (UTC when unset or
// unknown).
func (s *Server) timeLocation() *time.Location {
	if s.cfg.Timezone != "" {
		if loc, err := time.LoadLocation(s.cfg.Timezone); err == nil {
			return loc
		}
	}
	return time.UTC
}

// boxIDFor is the snapshot's box id for an extension (0 without one).
func (s *Server) boxIDFor(ext string) int64 {
	if snap := s.deps.Snapshots.Current(); snap != nil {
		if e, ok := snap.Extension(ext); ok {
			return e.VoicemailBoxID
		}
	}
	return 0
}

// bindingsFor lists the live bindings of an extension's devices.
func (s *Server) bindingsFor(ext string) []livestate.Binding {
	snap := s.deps.Snapshots.Current()
	if snap == nil {
		return nil
	}
	var out []livestate.Binding
	for _, d := range snap.DevicesForExtension(ext) {
		ctx, cancel := s.stateCtx()
		bs, err := s.deps.State.Bindings(ctx, s.aor(d.Username))
		cancel()
		if err != nil {
			continue
		}
		out = append(out, bs...)
	}
	return out
}
