package sip

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
)

// fakeSess is a media.Session whose Play takes real time and can be
// interrupted, and whose Record forwards the digits the test "presses".
type fakeSess struct {
	rate    int
	playFor time.Duration
	keys    chan byte

	mu     sync.Mutex
	plays  [][]byte
	closed bool
	recs   atomic.Int64
}

func newFakeSess(playFor time.Duration) *fakeSess {
	return &fakeSess{rate: 8000, playFor: playFor, keys: make(chan byte, 64)}
}

func (f *fakeSess) SampleRate() int { return f.rate }

func (f *fakeSess) Play(ctx context.Context, audio []byte) error {
	f.mu.Lock()
	f.plays = append(f.plays, audio)
	f.mu.Unlock()
	select {
	case <-time.After(f.playFor):
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (f *fakeSess) Record(ctx context.Context, maxDur time.Duration, dtmf chan<- byte) ([]byte, error) {
	f.recs.Add(1)
	deadline := time.After(maxDur)
	var out []byte
	for {
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		case <-deadline:
			return out, nil
		case d := <-f.keys:
			select {
			case dtmf <- d:
			default:
			}
			if d == '#' {
				return out, nil
			}
		case <-time.After(10 * time.Millisecond):
			out = append(out, make([]byte, 160)...)
		}
	}
}

func (f *fakeSess) Close() error { f.closed = true; return nil }

func (f *fakeSess) playCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.plays)
}

// fakeSpeaker is the text-to-speech stand-in: no network and no key.
type fakeSpeaker struct {
	mu    sync.Mutex
	calls map[string]int
	err   error
	delay time.Duration
}

func (f *fakeSpeaker) Speak(ctx context.Context, text string) ([]byte, error) {
	f.mu.Lock()
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	f.calls[text]++
	err, delay := f.err, f.delay
	f.mu.Unlock()
	if delay > 0 {
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if err != nil {
		return nil, err
	}
	return media.EncodeWAV(make([]byte, 1600), 8000), nil // 100 ms
}

func (f *fakeSpeaker) n(text string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[text]
}

func (f *fakeSpeaker) texts() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for t := range f.calls {
		out = append(out, t)
	}
	return out
}

func testCall(objs ObjectStore, snap *snapshot.Snapshot, vm VoicemailStore, log *slog.Logger) *call {
	fs := &fakeSnaps{}
	if snap != nil {
		fs.p.Store(snap)
	}
	return &call{s: &Server{log: log, deps: Deps{Objects: objs, Snapshots: fs, Voicemails: vm}}}
}

func TestCallUIBargeIn(t *testing.T) {
	sess := newFakeSess(5 * time.Second)
	sp := &fakeSpeaker{}
	u := newCallUI(t.Context(), testCall(&fakeObjects{}, nil, nil, discard), sess, sp)
	defer u.close()
	go func() {
		time.Sleep(100 * time.Millisecond)
		sess.keys <- '7'
	}()
	start := time.Now()
	d, err := u.Say(t.Context(), utt{Text: "Press a key"}, true)
	if err != nil || d != '7' {
		t.Fatalf("Say = %q, %v, want 7", d, err)
	}
	if time.Since(start) > 3*time.Second {
		t.Fatal("the key did not interrupt the prompt")
	}
}

func TestCallUITypeAhead(t *testing.T) {
	sess := newFakeSess(5 * time.Second)
	u := newCallUI(t.Context(), testCall(&fakeObjects{}, nil, nil, discard), sess, &fakeSpeaker{})
	defer u.close()
	sess.keys <- '4'
	time.Sleep(100 * time.Millisecond) // the listener has it
	start := time.Now()
	d, err := u.Say(t.Context(), utt{Text: "skipped"}, true)
	if err != nil || d != '4' || time.Since(start) > time.Second {
		t.Fatalf("Say = %q, %v after %v: a key typed ahead must skip the prompt", d, err, time.Since(start))
	}
	if sess.playCount() != 0 {
		t.Fatal("the prompt played though a key was already pressed")
	}
}

// TestCallUINoBargeIgnoresKeys: a prompt that must be heard (the recording
// instruction) plays to the end and forgets keys pressed over it.
func TestCallUINoBargeIgnoresKeys(t *testing.T) {
	sess := newFakeSess(200 * time.Millisecond)
	u := newCallUI(t.Context(), testCall(&fakeObjects{}, nil, nil, discard), sess, &fakeSpeaker{})
	defer u.close()
	go func() {
		time.Sleep(50 * time.Millisecond)
		sess.keys <- '5'
	}()
	if d, err := u.Say(t.Context(), utt{Text: "listen"}, false); err != nil || d != 0 {
		t.Fatalf("Say = %q, %v", d, err)
	}
	if d, _ := u.Digit(t.Context(), 100*time.Millisecond); d != 0 {
		t.Fatalf("a key pressed over a non-interruptible prompt leaked: %q", d)
	}
}

// TestCallUIFallsBackToATone fails if a prompt that cannot be spoken stops the
// session instead of playing the built-in tone, or if the warning names a key.
func TestCallUIFallsBackToATone(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewTextHandler(&logs, nil))
	sess := newFakeSess(10 * time.Millisecond)
	sp := &fakeSpeaker{err: errors.New("provider down")}
	u := newCallUI(t.Context(), testCall(&fakeObjects{}, nil, nil, log), sess, sp)
	defer u.close()
	for range 2 {
		if _, err := u.Say(t.Context(), utt{Key: voiceprompt.Goodbye, Text: "Goodbye."}, false); err != nil {
			t.Fatal(err)
		}
	}
	if sess.playCount() != 2 {
		t.Fatalf("plays = %d, want 2 tones", sess.playCount())
	}
	if !bytes.Equal(sess.plays[0], media.BeepPCMRate(8000)) {
		t.Fatal("the fallback is not the built-in beep")
	}
	if n := strings.Count(logs.String(), "spoken prompt unavailable"); n != 1 {
		t.Fatalf("warned %d times, want once per session: %s", n, logs.String())
	}
	// A nil speaker (TTS not wired) is the same: tones.
	u2 := newCallUI(t.Context(), testCall(&fakeObjects{}, nil, nil, discard), newFakeSess(time.Millisecond), nil)
	defer u2.close()
	if _, err := u2.Say(t.Context(), utt{Text: "x"}, false); err != nil {
		t.Fatal(err)
	}
}

// TestCallUIPrefetchShares fails if a prefetched prompt is synthesized again
// when it is spoken, or if a failed prefetch is never retried.
func TestCallUIPrefetchShares(t *testing.T) {
	sess := newFakeSess(5 * time.Millisecond)
	sp := &fakeSpeaker{delay: 80 * time.Millisecond}
	u := newCallUI(t.Context(), testCall(&fakeObjects{}, nil, nil, discard), sess, sp)
	defer u.close()
	u.Prefetch(utt{Text: "Message 2"})
	u.Prefetch(utt{Text: "Message 2"})
	if _, err := u.Say(t.Context(), utt{Text: "Message 2"}, false); err != nil {
		t.Fatal(err)
	}
	if sp.n("Message 2") != 1 {
		t.Fatalf("synthesized %d times, want 1", sp.n("Message 2"))
	}
	if sess.playCount() != 1 || bytes.Equal(sess.plays[0], media.BeepPCMRate(8000)) {
		t.Fatal("the prompt did not play its audio")
	}
	// Failure then recovery: the second Say asks again.
	sp.mu.Lock()
	sp.err = errors.New("blip")
	sp.mu.Unlock()
	_, _ = u.Say(t.Context(), utt{Text: "again"}, false)
	sp.mu.Lock()
	sp.err = nil
	sp.mu.Unlock()
	_, _ = u.Say(t.Context(), utt{Text: "again"}, false)
	if sp.n("again") != 2 {
		t.Fatalf("a failed prompt was not retried: %d calls", sp.n("again"))
	}
}

func TestCallUIDigits(t *testing.T) {
	sess := newFakeSess(time.Millisecond)
	u := newCallUI(t.Context(), testCall(&fakeObjects{}, nil, nil, discard), sess, &fakeSpeaker{})
	defer u.close()
	for _, k := range []byte{'2', '3', '*', '4', '#'} {
		sess.keys <- k
	}
	got, err := u.Digits(t.Context(), '1', time.Second)
	if err != nil || got != "1234" {
		t.Fatalf("Digits = %q, %v, want 1234 (the star ignored)", got, err)
	}
	// '#' as the first key is an empty entry.
	if got, _ := u.Digits(t.Context(), '#', time.Second); got != "" {
		t.Fatalf("Digits(#) = %q", got)
	}
	// A pause ends the entry.
	sess.keys <- '9'
	start := time.Now()
	if got, _ := u.Digits(t.Context(), 0, 150*time.Millisecond); got != "9" || time.Since(start) > 2*time.Second {
		t.Fatalf("Digits with a pause = %q", got)
	}
}

func TestCallUIRecord(t *testing.T) {
	sess := newFakeSess(time.Millisecond)
	u := newCallUI(t.Context(), testCall(&fakeObjects{}, nil, nil, discard), sess, &fakeSpeaker{})
	defer u.close()
	go func() {
		time.Sleep(300 * time.Millisecond)
		sess.keys <- '#'
	}()
	wav, dur, err := u.Record(t.Context(), 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if pcm, rate, derr := media.DecodeWAV(wav); derr != nil || rate != 8000 || len(pcm) == 0 {
		t.Fatalf("Record did not return a WAV: %v", derr)
	}
	if dur <= 0 {
		t.Fatalf("duration = %v", dur)
	}
	// The key listener is back afterwards.
	sess.keys <- '8'
	if d, _ := u.Digit(t.Context(), time.Second); d != '8' {
		t.Fatalf("the listener did not resume after recording: %q", d)
	}
}

func TestCallUICallGone(t *testing.T) {
	sess := newFakeSess(5 * time.Second)
	ctx, cancel := context.WithCancel(t.Context())
	u := newCallUI(ctx, testCall(&fakeObjects{}, nil, nil, discard), sess, &fakeSpeaker{})
	defer u.close()
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()
	if _, err := u.Say(ctx, utt{Text: "long"}, true); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestCallUIPlayMessage(t *testing.T) {
	objs := &fakeObjects{objs: map[string][]byte{"box/1/a.wav": media.EncodeWAV(make([]byte, 1600), 8000)}}
	sess := newFakeSess(20 * time.Millisecond)
	u := newCallUI(t.Context(), testCall(objs, nil, nil, discard), sess, &fakeSpeaker{})
	defer u.close()
	d, st, err := u.PlayMessage(t.Context(), VoicemailMessage{ID: 1, MinioObject: "box/1/a.wav"})
	if err != nil || d != 0 || st != playFull {
		t.Fatalf("PlayMessage = %q %v %v", d, st, err)
	}
	if _, st, err = u.PlayMessage(t.Context(), VoicemailMessage{ID: 2, MinioObject: "box/1/gone.wav"}); err != nil || st != playMissing {
		t.Fatalf("missing audio = %v %v, want playMissing", st, err)
	}
	// Prefetched audio is read once.
	before := len(objs.snapshotGets())
	u.PrefetchMessage(VoicemailMessage{ID: 1, MinioObject: "box/1/a.wav"})
	if _, _, err = u.PlayMessage(t.Context(), VoicemailMessage{ID: 1, MinioObject: "box/1/a.wav"}); err != nil {
		t.Fatal(err)
	}
	if got := len(objs.snapshotGets()) - before; got != 0 {
		t.Fatalf("a message already loaded was fetched %d more times", got)
	}
	// A key during the message cuts it.
	objs.objs["box/1/long.wav"] = media.EncodeWAV(make([]byte, 1600), 8000)
	sess.playFor = 5 * time.Second
	go func() {
		time.Sleep(100 * time.Millisecond)
		sess.keys <- '3'
	}()
	if d, st, _ = u.PlayMessage(t.Context(), VoicemailMessage{ID: 5, MinioObject: "box/1/long.wav"}); d != '3' || st != playCut {
		t.Fatalf("cut = %q %v", d, st)
	}
}

// TestCallerNameOrder fails if the sender is named from anything but, in
// order, the extension's display name, the phonebook, the caller-ID name, and
// then the number digit by digit.
func TestCallerNameOrder(t *testing.T) {
	vm := newVMStore()
	vm.contacts = map[string]string{"0471234567": "Jan Peeters (book)", "201": "Book name for an extension"}
	snap := snapshot.New(1, testDomain, nil).WithExtensionData([]snapshot.Extension{{Number: "201", Name: "Front desk"}})
	c := testCall(&fakeObjects{}, snap, vm, discard)
	d := &callData{c: c, box: VoicemailBox{ID: 1}, mailbox: "100"}
	lang := voiceprompt.English
	for _, tc := range []struct {
		name string
		msg  VoicemailMessage
		want string
	}{
		{"extension beats the phonebook and caller id", VoicemailMessage{Caller: "201", CallerName: "Someone"}, "Front desk"},
		{"phonebook beats caller id", VoicemailMessage{Caller: "0471234567", CallerName: "J. Peeters"}, "Jan Peeters (book)"},
		{"caller id when unknown to the books", VoicemailMessage{Caller: "0499888777", CallerName: "Acme Ltd"}, "Acme Ltd"},
		{"digits when nothing names it", VoicemailMessage{Caller: "0499888777"}, "0, 4, 9, 9, 8, 8, 8, 7, 7, 7"},
		{"a caller id that is only a number is not a name", VoicemailMessage{Caller: "0499888777", CallerName: "0499888777"}, "0, 4, 9, 9, 8, 8, 8, 7, 7, 7"},
		{"anonymous", VoicemailMessage{}, "an unknown caller"},
	} {
		who, _ := d.Caller(t.Context(), tc.msg).Who(lang)
		if who != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, who, tc.want)
		}
	}
}

// TestCallerLookupFailureFallsThrough: a broken phonebook lookup must not
// stop the name falling back to the caller ID.
func TestCallerLookupFailureFallsThrough(t *testing.T) {
	c := testCall(&fakeObjects{}, snapshot.New(1, testDomain, nil), failingContacts{newVMStore()}, discard)
	d := &callData{c: c, mailbox: "100"}
	who, _ := d.Caller(t.Context(), VoicemailMessage{Caller: "0499", CallerName: "Acme"}).Who(voiceprompt.English)
	if who != "Acme" {
		t.Fatalf("who = %q", who)
	}
}

type failingContacts struct{ *fakeVMStore }

func (failingContacts) ContactName(context.Context, string, string) (string, error) {
	return "", errors.New("db down")
}
