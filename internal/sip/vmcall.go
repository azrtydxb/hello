package sip

// The phone side of the guided voicemail session: callUI speaks, listens and
// records through the call's media session, and callData reads and writes the
// box. Spoken text comes from the Speaker (a cache over text-to-speech); when
// it cannot speak, a prompt degrades to a short tone and the session keeps
// working on the key layout alone.

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/voiceprompt"
)

// Speaker turns prompt text into 8 kHz WAV audio (ttscache.Cache in
// hello-sip). Any error means the caller plays its built-in audio instead.
type Speaker interface {
	Speak(ctx context.Context, text string) ([]byte, error)
}

// Timeouts of the guided session's own work. They are looser than the SIP
// path's StateTimeout (200 ms): this runs in the call's media goroutine,
// where waiting a second for MinIO or PostgreSQL is harmless.
const (
	vmOpTimeout   = 3 * time.Second
	vmSpeakWait   = 8 * time.Second
	vmDigitGap    = 8 * time.Second
	vmPumpSlice   = 10 * time.Minute
	vmDigitBuffer = 64
)

// pendingAudio is a prompt or message being prepared in the background.
type pendingAudio struct {
	done chan struct{}
	pcm  []byte
	err  error
}

type callUI struct {
	c     *call
	sess  media.Session
	log   func(msg string, args ...any)
	speak Speaker

	digits chan byte

	pumpMu     sync.Mutex
	pumpCancel context.CancelFunc
	pumpDone   chan struct{}

	mu       sync.Mutex
	prompts  map[string]*pendingAudio
	messages map[int64]*pendingAudio
	warned   bool
	rootCtx  context.Context
}

func newCallUI(ctx context.Context, c *call, sess media.Session, sp Speaker) *callUI {
	u := &callUI{
		c: c, sess: sess, speak: sp, rootCtx: ctx,
		digits:   make(chan byte, vmDigitBuffer),
		prompts:  map[string]*pendingAudio{},
		messages: map[int64]*pendingAudio{},
		log:      func(msg string, args ...any) { c.s.log.Warn(msg, args...) },
	}
	u.startPump()
	return u
}

// close stops the key listener.
func (u *callUI) close() { u.stopPump() }

// startPump listens for keys for the whole session. The session only
// reports keys while Record runs, so a Record in a loop is the listener; it
// also drains the caller's audio, which nobody else reads while we talk.
func (u *callUI) startPump() {
	u.pumpMu.Lock()
	defer u.pumpMu.Unlock()
	if u.pumpCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(u.rootCtx)
	done := make(chan struct{})
	u.pumpCancel, u.pumpDone = cancel, done
	go func() {
		defer close(done)
		for ctx.Err() == nil {
			if _, err := u.sess.Record(ctx, vmPumpSlice, u.digits); err != nil {
				return // cancelled, or the session closed
			}
		}
	}()
}

func (u *callUI) stopPump() {
	u.pumpMu.Lock()
	cancel, done := u.pumpCancel, u.pumpDone
	u.pumpCancel, u.pumpDone = nil, nil
	u.pumpMu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

// drain discards keys typed ahead.
func (u *callUI) drain() {
	for {
		select {
		case <-u.digits:
		default:
			return
		}
	}
}

// pcmFor prepares text as PCM at the session rate: from the Speaker, bounded
// by vmSpeakWait.
func (u *callUI) pcmFor(ctx context.Context, text string) ([]byte, error) {
	if u.speak == nil {
		return nil, errors.New("no speaker")
	}
	sctx, cancel := context.WithTimeout(ctx, vmSpeakWait)
	defer cancel()
	wav, err := u.speak.Speak(sctx, text)
	if err != nil {
		return nil, err
	}
	return media.WAVToRate(wav, u.sess.SampleRate())
}

// prepare returns the (possibly already running) preparation of text.
func (u *callUI) prepare(text string) *pendingAudio {
	u.mu.Lock()
	defer u.mu.Unlock()
	if p, ok := u.prompts[text]; ok {
		return p
	}
	p := &pendingAudio{done: make(chan struct{})}
	u.prompts[text] = p
	go func() {
		defer close(p.done)
		defer func() {
			if r := recover(); r != nil {
				p.err = errors.New("prefetch panicked")
			}
		}()
		p.pcm, p.err = u.pcmFor(u.rootCtx, text)
	}()
	return p
}

func (u *callUI) Prefetch(x utt) {
	if u.speak != nil && x.Text != "" {
		u.prepare(x.Text)
	}
}

func (u *callUI) PrefetchMessage(m VoicemailMessage) {
	u.mu.Lock()
	if _, ok := u.messages[m.ID]; ok {
		u.mu.Unlock()
		return
	}
	p := &pendingAudio{done: make(chan struct{})}
	u.messages[m.ID] = p
	u.mu.Unlock()
	go func() {
		defer close(p.done)
		p.pcm, p.err = u.loadMessage(u.rootCtx, m)
	}()
}

func (u *callUI) loadMessage(ctx context.Context, m VoicemailMessage) ([]byte, error) {
	octx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	raw, err := u.c.s.deps.Objects.Get(octx, m.MinioObject)
	if err != nil {
		return nil, err
	}
	return media.WAVToRate(raw, u.sess.SampleRate())
}

// playBarge plays pcm and stops at the first key, which it returns. A key
// typed before the prompt began (type-ahead) skips it at once.
func (u *callUI) playBarge(ctx context.Context, pcm []byte) (byte, error) {
	select {
	case d := <-u.digits:
		return d, nil
	default:
	}
	pctx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- u.sess.Play(pctx, pcm) }()
	select {
	case d := <-u.digits:
		cancel()
		<-done //nolint:errcheck // the interrupted Play reports its own cancellation; nothing to add
		return d, nil
	case err := <-done:
		if err != nil && ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, err
	}
}

// playPlain plays pcm to the end, ignoring keys typed meanwhile.
func (u *callUI) playPlain(ctx context.Context, pcm []byte) error {
	err := u.sess.Play(ctx, pcm)
	u.drain()
	return err
}

func (u *callUI) Say(ctx context.Context, x utt, barge bool) (byte, error) {
	var pcm []byte
	if u.speak != nil {
		p := u.prepare(x.Text)
		select {
		case <-p.done:
		case <-ctx.Done():
			return 0, ctx.Err()
		}
		if p.err == nil {
			pcm = p.pcm
		} else {
			u.noteFailure(x, p.err)
			// A failed preparation is retried on the next use.
			u.mu.Lock()
			if u.prompts[x.Text] == p {
				delete(u.prompts, x.Text)
			}
			u.mu.Unlock()
		}
	}
	if pcm == nil {
		pcm = media.BeepPCMRate(u.sess.SampleRate()) // the built-in audio
	}
	if barge {
		return u.playBarge(ctx, pcm)
	}
	return 0, u.playPlain(ctx, pcm)
}

func (u *callUI) noteFailure(x utt, err error) {
	u.mu.Lock()
	warned := u.warned
	u.warned = true
	u.mu.Unlock()
	if !warned {
		// The error never carries the provider key.
		u.log("voicemail: spoken prompt unavailable; playing a tone instead", "prompt", string(x.Key), "error", err)
	}
}

func (u *callUI) PlayMessage(ctx context.Context, m VoicemailMessage) (byte, playStatus, error) {
	u.PrefetchMessage(m)
	u.mu.Lock()
	p := u.messages[m.ID]
	u.mu.Unlock()
	select {
	case <-p.done:
	case <-ctx.Done():
		return 0, playMissing, ctx.Err()
	}
	if p.err != nil || len(p.pcm) == 0 {
		u.c.s.log.Warn("voicemail message unavailable", "error", p.err)
		u.mu.Lock()
		delete(u.messages, m.ID)
		u.mu.Unlock()
		return 0, playMissing, nil
	}
	d, err := u.playBarge(ctx, p.pcm)
	if err != nil {
		return 0, playMissing, err
	}
	if d != 0 {
		return d, playCut, nil
	}
	return 0, playFull, nil
}

func (u *callUI) Digit(ctx context.Context, wait time.Duration) (byte, error) {
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case d := <-u.digits:
		return d, nil
	case <-t.C:
		return 0, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (u *callUI) Digits(ctx context.Context, first byte, wait time.Duration) (string, error) {
	var out []byte
	add := func(d byte) bool { // true when the entry is complete
		switch {
		case d == '#':
			return true
		case d >= '0' && d <= '9' && len(out) < vmMaxPINLength:
			out = append(out, d)
		}
		return false
	}
	if first != 0 && add(first) {
		return string(out), nil
	}
	for {
		d, err := u.Digit(ctx, wait)
		if err != nil {
			return "", err
		}
		if d == 0 || add(d) { // a pause ends the entry too
			return string(out), nil
		}
	}
}

func (u *callUI) Beep(ctx context.Context) error {
	return u.playPlain(ctx, media.BeepPCMRate(u.sess.SampleRate()))
}

func (u *callUI) Record(ctx context.Context, max time.Duration) ([]byte, time.Duration, error) {
	u.stopPump()
	defer u.startPump()
	u.drain()
	pcm, err := u.sess.Record(ctx, max, make(chan byte, 16))
	if err != nil && ctx.Err() != nil {
		return nil, 0, ctx.Err()
	}
	rate := u.sess.SampleRate()
	dur := time.Duration(len(pcm)/2) * time.Second / time.Duration(rate)
	return media.EncodeWAV(pcm, rate), dur, nil
}

func (u *callUI) PlayClip(ctx context.Context, wav []byte) (byte, error) {
	pcm, err := media.WAVToRate(wav, u.sess.SampleRate())
	if err != nil {
		return 0, nil //nolint:nilerr // our own recording; nothing to play, carry on to the menu
	}
	return u.playBarge(ctx, pcm)
}

// callData is the box behind a retrieval session.
type callData struct {
	c       *call
	box     VoicemailBox
	mailbox string // the extension number
	mu      sync.Mutex
	names   map[string]voiceprompt.Caller
}

func (d *callData) vm() VoicemailStore { return d.c.s.deps.Voicemails }

func (d *callData) CheckPIN(ctx context.Context, pin string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	return d.vm().PasswordOK(ctx, d.box.ID, pin)
}

func (d *callData) Messages(ctx context.Context) ([]VoicemailMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	msgs, err := d.vm().ListMessages(ctx, d.box.ID)
	if err != nil {
		return nil, err
	}
	// Played in the order they arrived within a folder: oldest first, the
	// way a voicemail box is heard. The store lists newest first.
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (d *callData) MarkHeard(ctx context.Context, id int64) error {
	ctx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	if err := d.vm().MarkHeard(ctx, id); err != nil {
		d.c.s.log.Warn("voicemail mark-heard failed", "error", err)
		return err
	}
	d.c.sendMWI(d.mailbox)
	return nil
}

func (d *callData) Delete(ctx context.Context, m VoicemailMessage) error {
	ctx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	object, err := d.vm().DeleteMessage(ctx, d.box.ID, m.ID)
	if err != nil {
		d.c.s.log.Warn("voicemail delete failed", "error", err)
		return err
	}
	d.c.removeObject(ctx, object)
	d.c.sendMWI(d.mailbox)
	return nil
}

func (d *callData) SetPIN(ctx context.Context, pin string) error {
	ctx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	if err := d.vm().SetPIN(ctx, d.box.ID, pin); err != nil {
		d.c.s.log.Warn("voicemail PIN change failed", "error", err)
		return err
	}
	return nil
}

func (d *callData) SetGreeting(ctx context.Context, slot string, wav []byte) error {
	if d.c.s.deps.Objects == nil {
		return errors.New("no object store")
	}
	key, err := greetingKey(d.box.ID, slot, time.Now())
	if err != nil {
		return err
	}
	if !d.c.putAudio(ctx, key, wav) {
		return errors.New("greeting upload failed")
	}
	sctx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	prev, err := d.vm().SetGreeting(sctx, d.box.ID, slot, key)
	if err != nil {
		d.c.s.log.Warn("voicemail greeting save failed", "error", err)
		d.c.removeObject(ctx, key)
		return err
	}
	d.c.removeObject(ctx, prev)
	return nil
}

func (d *callData) ResetGreetings(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, vmOpTimeout)
	defer cancel()
	dropped, err := d.vm().ResetGreetings(ctx, d.box.ID)
	if err != nil {
		d.c.s.log.Warn("voicemail greeting reset failed", "error", err)
		return err
	}
	for _, o := range dropped {
		d.c.removeObject(ctx, o)
	}
	return nil
}

func (d *callData) Caller(ctx context.Context, m VoicemailMessage) voiceprompt.Caller {
	key := m.Caller + "\x00" + m.CallerName
	d.mu.Lock()
	if c, ok := d.names[key]; ok {
		d.mu.Unlock()
		return c
	}
	d.mu.Unlock()
	who := voiceprompt.Caller{Number: m.Caller, CallerID: m.CallerName}
	if snap := d.c.s.deps.Snapshots.Current(); snap != nil {
		if e, ok := snap.Extension(m.Caller); ok {
			who.Extension = e.Name
		}
	}
	if who.Extension == "" && m.Caller != "" {
		cctx, cancel := context.WithTimeout(ctx, vmOpTimeout)
		name, err := d.vm().ContactName(cctx, m.Caller, d.mailbox)
		cancel()
		if err != nil {
			d.c.s.log.Warn("voicemail caller lookup failed", "error", err)
		}
		who.Contact = name
	}
	d.mu.Lock()
	if d.names == nil {
		d.names = map[string]voiceprompt.Caller{}
	}
	d.names[key] = who
	d.mu.Unlock()
	return who
}
