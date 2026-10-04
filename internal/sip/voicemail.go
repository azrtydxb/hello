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
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/media"
	"github.com/emiago/sipgo/sip"
)

// VoicemailBox is the store's view of one box (migration 00004).
type VoicemailBox struct {
	ID                int64
	Extension         string
	Email             string
	GreetingObject    string
	UnreachableObject string
}

// VoicemailStore is the voicemail metadata store (PostgreSQL, implemented by
// internal/store in the control plane). Every method is called from the
// voicemail media goroutine only — never on the SIP transaction path.
type VoicemailStore interface {
	// Box returns the extension's box (ok=false without one).
	Box(ctx context.Context, extension string) (VoicemailBox, bool, error)
	// PasswordOK compares a retrieval password (hashing lives here).
	PasswordOK(ctx context.Context, boxID int64, password string) (bool, error)
	// InsertMessage stores one message and returns its id.
	InsertMessage(ctx context.Context, boxID int64, object, caller string, durationMs int64) (int64, error)
	// MessageCounts returns the unread and total message counts.
	MessageCounts(ctx context.Context, boxID int64) (unread, total int, err error)
	// ListMessages returns the box's messages, newest first.
	ListMessages(ctx context.Context, boxID int64) ([]VoicemailMessage, error)
	// MarkHeard marks a message heard.
	MarkHeard(ctx context.Context, id int64) error
}

// VoicemailMessage is one stored message.
type VoicemailMessage struct {
	ID          int64
	BoxID       int64
	MinioObject string
	Caller      string
	DurationMs  int64
	Heard       bool
	CreatedAt   time.Time
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

// voicemailOwnBox is the *97 path: the caller's own box, password-gated.
func (c *call) voicemailOwnBox(string) {
	c.startVoicemail(vmRetrieve, "")
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
	go c.voicemailFlow(mode, reason, sess)
}

// answerAnchored binds the media anchor for this call and builds the answer
// SDP from the caller's offer.
func (s *Server) answerAnchored(offer []byte) ([]byte, media.Session, error) {
	anchor := s.anchor.Load()
	if s.deps.Objects == nil || anchor == nil {
		return nil, nil, errors.New("voicemail media disabled")
	}
	return anchor.Answer(offer)
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
	ctx, cancel := c.vmCtx()
	defer cancel()
	box := c.boxDetails()
	switch mode {
	case vmRetrieve:
		c.vmRetrieve(ctx, sess, box)
	default:
		c.vmLeave(ctx, sess, box, reason)
	}
	// The call is over for the caller once the application has run.
	c.mu.Lock()
	done := c.hungUp || c.ended
	c.mu.Unlock()
	if !done {
		c.hangup(cdr.SideCallee)
	}
}

// boxDetails loads the box's settings from the store (greeting objects,
// email); failures leave a zero box, which records without a greeting.
func (c *call) boxDetails() VoicemailBox {
	s := c.s
	if s.deps.Voicemails == nil {
		return VoicemailBox{}
	}
	c.mu.Lock()
	dialled := c.dialled
	c.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
	defer cancel()
	box, ok, err := s.deps.Voicemails.Box(ctx, dialled)
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
	c.playGreeting(ctx, sess, box, reason)
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
	wav := media.EncodeWAV(audio, 8000)
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
	if _, err := vm.InsertMessage(idctx, box.ID, key, c.callerNum, dur.Milliseconds()); err != nil {
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
	c.sendMWI()
}

// playGreeting plays the box's greeting (the unreachable one for an
// unreachable call), or nothing when neither is set. A failed object fetch
// is logged and skipped: the beep still marks the recording start.
func (c *call) playGreeting(ctx context.Context, sess media.Session, box VoicemailBox, reason string) {
	if sess == nil || c.s.deps.Objects == nil {
		return
	}
	key := box.GreetingObject
	if reason == "unreachable" && box.UnreachableObject != "" {
		key = box.UnreachableObject
	}
	if key == "" {
		return
	}
	octx, cancel := context.WithTimeout(ctx, c.s.cfg.StateTimeout)
	defer cancel()
	raw, err := c.s.deps.Objects.Get(octx, key)
	if err != nil {
		c.s.log.Warn("voicemail greeting unavailable", "error", err)
		return
	}
	pcm, _, err := media.DecodeWAV(raw)
	if err != nil {
		c.s.log.Warn("voicemail greeting is not a WAV", "error", err)
		return
	}
	if err := sess.Play(ctx, pcm); err != nil {
		c.s.log.Debug("greeting playback failed", "error", err)
	}
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
			if len(audio) >= 2*8000 { // one second of 16-bit 8 kHz samples
				return audio, dur, true
			}
			return nil, 0, false
		}
		if lastDigit == '*' {
			continue // the caller retypes the message
		}
		// The caller hung up (or the session closed): keep what was said.
		if len(audio) >= 2*8000 {
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

// vmRetrieve is the *97 application: prompt (beep), collect the password
// with DTMF, then play the unheard messages one by one, marking each heard.
// Three wrong passwords end the call.
func (c *call) vmRetrieve(ctx context.Context, sess media.Session, box VoicemailBox) {
	s := c.s
	if sess == nil || s.deps.Voicemails == nil || s.deps.Objects == nil {
		return
	}
	if box.ID == 0 {
		return // no box: nothing to retrieve (the 200 already went out)
	}
	var ok bool
	var err error
	for attempt := 0; attempt < 3 && !ok; attempt++ {
		_ = sess.Play(ctx, media.BeepPCM())
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
		pcm, _, err := media.DecodeWAV(raw)
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
	c.sendMWI()
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

// sendMWI publishes the extension's message counts as a MESSAGE with
// Message-Account (S-8), to every registered device of the box's extension.
func (c *call) sendMWI() {
	c.mu.Lock()
	ext := c.dialled
	c.mu.Unlock()
	c.s.sendMWI(ext)
}

func (s *Server) sendMWI(ext string) {
	if s.deps.Voicemails == nil {
		return
	}
	s.bg.Go(func() {
		defer contain(s.log, "MWI")
		ctx, cancel := context.WithTimeout(context.Background(), 2*s.cfg.StateTimeout)
		defer cancel()
		unread, total, err := s.deps.Voicemails.MessageCounts(ctx, s.boxIDFor(ext))
		if err != nil {
			s.log.Warn("MWI counts unavailable", "error", err)
			return
		}
		body := fmt.Sprintf("Messages-Waiting: %s\r\nMessage-Account: sip:%s@%s\r\nVoice-Message: %d/%d (%d/%d)\r\n",
			yesNo(unread > 0), ext, s.cfg.Domain, unread, total, unread, total)
		for _, b := range s.bindingsFor(ext) {
			mctx, mcancel := context.WithTimeout(context.Background(), s.cfg.StateTimeout)
			err := s.sendMESSAGE(mctx, "sip:"+ext+"@"+s.cfg.Domain, b, body)
			mcancel()
			if err != nil {
				s.log.Debug("MWI MESSAGE failed", "device", b.Device, "error", err)
			}
		}
	})
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

// sendMESSAGE sends one MESSAGE request to a binding's contact.
func (s *Server) sendMESSAGE(ctx context.Context, from string, b livestate.Binding, body string) error {
	var target sip.Uri
	raw := strings.TrimSuffix(strings.TrimPrefix(b.ContactURI, "<"), ">")
	if err := sip.ParseUri(raw, &target); err != nil {
		return fmt.Errorf("binding contact %q: %w", b.ContactURI, err)
	}
	req := sip.NewRequest(sip.MESSAGE, target)
	fromHdr := &sip.FromHeader{Address: sip.Uri{User: "voicemail", Host: s.cfg.Domain}, Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}}
	to := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: b.Extension, Host: s.cfg.Domain}}
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(fromHdr)
	req.AppendHeader(to)
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.MESSAGE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&s.contact))
	req.AppendHeader(sip.NewHeader("Content-Type", "application/simple-message-summary"))
	req.SetBody([]byte(body))
	req.SetTransport("UDP")
	if b.Source != "" {
		req.SetDestination(b.Source)
	}
	res, err := s.client.Do(ctx, req)
	if err != nil {
		return err
	}
	s.m.response(res.StatusCode)
	return nil
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
