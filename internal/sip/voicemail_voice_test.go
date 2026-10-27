package sip

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
)

func TestCustomGreetingChoice(t *testing.T) {
	box := VoicemailBox{GreetingObject: "g", UnreachableObject: "u", BusyObject: "b"}
	for _, tc := range []struct {
		reason string
		box    VoicemailBox
		want   string
	}{
		{"no-answer", box, "g"},
		{"dnd", box, "g"},
		{"", box, "g"},
		{"busy", box, "b"},
		{"unreachable", box, "u"},
		// A variant without its own recording uses the general one, as the
		// box always did, before the spoken default.
		{"busy", VoicemailBox{GreetingObject: "g"}, "g"},
		{"unreachable", VoicemailBox{GreetingObject: "g"}, "g"},
		// Nothing recorded: the spoken default applies.
		{"busy", VoicemailBox{}, ""},
		{"no-answer", VoicemailBox{BusyObject: "b", UnreachableObject: "u"}, ""},
	} {
		if got := customGreeting(tc.box, tc.reason); got != tc.want {
			t.Errorf("customGreeting(%q) = %q, want %q", tc.reason, got, tc.want)
		}
	}
	if greetingVariant("busy") != voiceprompt.Busy || greetingVariant("unreachable") != voiceprompt.Unreachable ||
		greetingVariant("no-answer") != voiceprompt.Unavailable || greetingVariant("dnd") != voiceprompt.Unavailable {
		t.Fatal("greetingVariant")
	}
}

func greetingCall(sp Speaker, objs ObjectStore) *call {
	snap := snapshot.New(1, testDomain, nil).WithExtensionData([]snapshot.Extension{
		{Number: "200", Name: "Jan Peeters"}, {Number: "300"}})
	c := testCall(objs, snap, newVMStore(), discard)
	c.s.deps.Speech = sp
	c.dialled = "200"
	return c
}

func tone(n int) []byte { return media.EncodeWAV(make([]byte, n), 8000) }

// TestDefaultGreetingIsSpoken fails if a box without a recording does not
// speak "<name> is not available..." in its variant, with the extension read
// out when it has no name.
func TestDefaultGreetingIsSpoken(t *testing.T) {
	for _, tc := range []struct {
		reason, ext, want string
	}{
		{"no-answer", "200", "Jan Peeters is not available. Please leave a message after the tone."},
		{"dnd", "200", "Jan Peeters is not available. Please leave a message after the tone."},
		{"busy", "200", "Jan Peeters is busy at the moment. Please leave a message after the tone."},
		{"unreachable", "200", "Jan Peeters cannot be reached at the moment. Please leave a message after the tone."},
		{"no-answer", "300", "extension 3, 0, 0 is not available. Please leave a message after the tone."},
	} {
		sp := &fakeSpeaker{}
		c := greetingCall(sp, &fakeObjects{})
		c.dialled = tc.ext
		sess := newFakeSess(time.Millisecond)
		c.playGreeting(t.Context(), sess, VoicemailBox{ID: 5}, tc.reason)
		if sp.n(tc.want) != 1 || sess.playCount() != 1 {
			t.Errorf("%s/%s: spoke %v, plays %d; want %q", tc.reason, tc.ext, sp.texts(), sess.playCount(), tc.want)
		}
	}
}

// TestCustomGreetingOverridesSpokenDefault fails if a recorded or uploaded
// greeting is not played in place of the spoken one, per variant.
func TestCustomGreetingOverridesSpokenDefault(t *testing.T) {
	objs := &fakeObjects{objs: map[string][]byte{"box/5/greeting.wav": tone(3200), "box/5/busy.wav": tone(1600)}}
	sp := &fakeSpeaker{}
	c := greetingCall(sp, objs)
	box := VoicemailBox{ID: 5, GreetingObject: "box/5/greeting.wav", BusyObject: "box/5/busy.wav"}

	sess := newFakeSess(time.Millisecond)
	c.playGreeting(t.Context(), sess, box, "no-answer")
	if len(sp.texts()) != 0 || sess.playCount() != 1 || len(sess.plays[0]) != 3200 {
		t.Fatalf("no-answer: spoke %v, plays=%d", sp.texts(), sess.playCount())
	}
	sess = newFakeSess(time.Millisecond)
	c.playGreeting(t.Context(), sess, box, "busy")
	if len(sp.texts()) != 0 || sess.playCount() != 1 || len(sess.plays[0]) != 1600 {
		t.Fatalf("busy: spoke %v, plays=%d, len=%d", sp.texts(), sess.playCount(), len(sess.plays[0]))
	}
	// The variant with no recording is still spoken, because the general
	// recording is "unavailable" only for variants without their own... it
	// is the fallback, so it plays for unreachable too.
	sess = newFakeSess(time.Millisecond)
	c.playGreeting(t.Context(), sess, box, "unreachable")
	if len(sp.texts()) != 0 || len(sess.plays[0]) != 3200 {
		t.Fatalf("unreachable: spoke %v", sp.texts())
	}
	// A custom greeting that cannot be fetched falls back to the spoken one.
	sess = newFakeSess(time.Millisecond)
	c.playGreeting(t.Context(), sess, VoicemailBox{ID: 5, GreetingObject: "box/5/missing.wav"}, "no-answer")
	if sp.n("Jan Peeters is not available. Please leave a message after the tone.") != 1 {
		t.Fatalf("missing greeting did not fall back: %v", sp.texts())
	}
}

// TestRecordedNameInDefaultGreeting: the owner's recorded name plays, then the
// spoken tail, instead of the spoken display name.
func TestRecordedNameInDefaultGreeting(t *testing.T) {
	objs := &fakeObjects{objs: map[string][]byte{"box/5/name.wav": tone(2400)}}
	sp := &fakeSpeaker{}
	c := greetingCall(sp, objs)
	sess := newFakeSess(time.Millisecond)
	c.playGreeting(t.Context(), sess, VoicemailBox{ID: 5, NameObject: "box/5/name.wav"}, "busy")
	if sess.playCount() != 2 || len(sess.plays[0]) != 2400 {
		t.Fatalf("plays = %d, want the name then the tail", sess.playCount())
	}
	if want := "is busy at the moment. Please leave a message after the tone."; sp.n(want) != 1 || len(sp.texts()) != 1 {
		t.Fatalf("spoke %v, want only the tail", sp.texts())
	}
	// An unfetchable name falls back to the spoken display name.
	sp2 := &fakeSpeaker{}
	c2 := greetingCall(sp2, &fakeObjects{})
	c2.playGreeting(t.Context(), newFakeSess(time.Millisecond), VoicemailBox{ID: 5, NameObject: "box/5/gone.wav"}, "no-answer")
	if sp2.n("Jan Peeters is not available. Please leave a message after the tone.") != 1 {
		t.Fatalf("spoke %v", sp2.texts())
	}
}

// TestGreetingFallsBackToNothing: with speech failing or not wired, the
// greeting is silent (as before) and the caller still reaches the tone.
func TestGreetingFallsBackToNothing(t *testing.T) {
	sess := newFakeSess(time.Millisecond)
	greetingCall(&fakeSpeaker{err: errors.New("down")}, &fakeObjects{}).playGreeting(t.Context(), sess, VoicemailBox{ID: 5}, "busy")
	greetingCall(nil, &fakeObjects{}).playGreeting(t.Context(), sess, VoicemailBox{ID: 5}, "busy")
	if sess.playCount() != 0 {
		t.Fatalf("played %d things with no voice", sess.playCount())
	}
}

// guidedPBX wires a PBX where extension 100 (device a1) owns box 3, PIN 1234
// and two messages from 200, 300.
func guidedPBX(t *testing.T, sp Speaker) (*testPBX, *fakeVMStore, *fakeObjects) {
	t.Helper()
	vm := newVMStore()
	vm.boxes["100"] = VoicemailBox{ID: 3, Extension: "100"}
	vm.pwOK[3] = "1234"
	vm.nextID = 0
	objs := &fakeObjects{objs: map[string][]byte{"box/3/1.wav": tone(1600), "box/3/2.wav": tone(1600)}}
	vm.msgs = []VoicemailMessage{
		{ID: 2, BoxID: 3, MinioObject: "box/3/2.wav", Caller: "300", CreatedAt: time.Now().Add(-time.Hour)},
		{ID: 1, BoxID: 3, MinioObject: "box/3/1.wav", Caller: "200", CreatedAt: time.Now().Add(-2 * time.Hour)},
	}
	feats := func(s *snapshot.Snapshot) {
		featureSnapshot(s)
		s.WithExtensionData([]snapshot.Extension{
			{Number: "100", Name: "Alice", VoicemailEnabled: true, VoicemailBoxID: 3},
			{Number: "200", Name: "Front desk"}})
	}
	pbx := startPBX(t, threeDevices(), withFeatures(feats), withVoicemail(vm, objs),
		func(c *Config, d *Deps) {
			if sp != nil {
				d.Speech = sp
			}
			c.Timezone = "UTC"
		})
	anchored(pbx)
	return pbx, vm, objs
}

func (f *fakeVMStore) heardIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []int64
	for _, m := range f.msgs {
		if m.Heard {
			out = append(out, m.ID)
		}
	}
	return out
}

func (f *fakeVMStore) deletedIDs() []int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]int64(nil), f.deleted...)
}

// TestGuidedRetrievalOverRTP drives *97 end to end through the SIP and RTP
// layers: the PIN is asked by voice, the counts and the message header are
// spoken (a fake voice records what it was asked to say), and key 3 during
// the session deletes the first message.
func TestGuidedRetrievalOverRTP(t *testing.T) {
	sp := &fakeSpeaker{}
	pbx, vm, _ := guidedPBX(t, sp)
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	r := waitCall(t, dial(t.Context(), a, "*97"))
	if r.err != nil {
		t.Fatalf("*97: %v", r.err)
	}
	feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
	eventually(t, "the PIN prompt is spoken", func() bool {
		return sp.n("Please enter your PIN, followed by the pound key.") > 0
	})
	for _, d := range []byte("1234#") {
		feed.digit(d)
	}
	eventually(t, "counts and the first header are spoken", func() bool {
		return sp.n("You have 2 new messages and no saved messages.") > 0 &&
			sp.n("Message 1, from Front desk, 2 hours ago") == 0 && hasPrefixText(sp, "Message 1, from Front desk, today at ")
	})
	// The next header was prepared while the first message plays.
	eventually(t, "the next header is prefetched", func() bool { return hasPrefixText(sp, "Message 2, from 3, 0, 0, today at ") })
	// Press 3 until the delete lands (the key must hit a point where the
	// session listens; the prompts are short in this test).
	eventually(t, "message 1 deleted", func() bool {
		feed.digit('3')
		time.Sleep(150 * time.Millisecond)
		return len(vm.deletedIDs()) > 0
	})
	if got := vm.deletedIDs(); got[0] != 1 {
		t.Fatalf("deleted = %v, want message 1 first", got)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

func hasPrefixText(sp *fakeSpeaker, prefix string) bool {
	for _, t := range sp.texts() {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// TestRetrievalFallsBackWithoutVoice fails if *97 does not still work, as the
// plain beep, PIN and playback, when no voice is configured or the voice
// fails: voicemail must never break because of text to speech.
func TestRetrievalFallsBackWithoutVoice(t *testing.T) {
	for name, sp := range map[string]Speaker{
		"no speaker":      nil,
		"speaker failing": &fakeSpeaker{err: errors.New("provider down")},
	} {
		t.Run(name, func(t *testing.T) {
			pbx, vm, objs := guidedPBX(t, sp)
			a := newPhone(t, pbx, "a1", "pa")
			a.register(t)
			r := waitCall(t, dial(t.Context(), a, "*97"))
			if r.err != nil {
				t.Fatalf("*97: %v", r.err)
			}
			feed := startRTPFeed(t, r.dcs.InviteResponse.Body())
			time.Sleep(700 * time.Millisecond) // past the beep
			for _, d := range []byte("1234#") {
				feed.digit(d)
			}
			eventually(t, "both messages heard", func() bool { return len(vm.heardIDs()) == 2 })
			if len(objs.snapshotGets()) < 2 {
				t.Fatalf("messages were not fetched: %v", objs.snapshotGets())
			}
			// The application ends the call once the messages are played.
			pbx.nextCDR(t)
		})
	}
}

// TestPromptLanguage fails if a box does not speak in its own language: a Dutch
// box gets the Dutch default greeting, an unset box inherits the deployment
// language, a box set to English stays English under a Dutch deployment, and
// English is the default (pbx-gaps S-20).
func TestPromptLanguage(t *testing.T) {
	en := voiceprompt.GreetingText(voiceprompt.English, voiceprompt.Unavailable, "Jan Peeters")
	nl := voiceprompt.GreetingText(voiceprompt.Dutch, voiceprompt.Unavailable, "Jan Peeters")
	if en == nl {
		t.Fatal("the English and Dutch greetings are the same text")
	}
	for _, tc := range []struct {
		name, global, box, want string
	}{
		{"default is english", "", "", en},
		{"dutch box", "", "nl", nl},
		{"dutch box over english deployment", "en", "nl", nl},
		{"unset box inherits dutch deployment", "nl", "", nl},
		{"english box over dutch deployment", "nl", "en", en},
		{"unknown box language is english", "nl", "xx", en},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sp := &fakeSpeaker{}
			c := greetingCall(sp, &fakeObjects{})
			c.s.cfg.PromptLanguage = tc.global
			c.playGreeting(t.Context(), newFakeSess(time.Millisecond), VoicemailBox{ID: 5, PromptLanguage: tc.box}, "no-answer")
			if sp.n(tc.want) != 1 || len(sp.texts()) != 1 {
				t.Errorf("spoke %v, want only %q", sp.texts(), tc.want)
			}
		})
	}
}

// onlyEnglish is a speaker that can voice English but not Dutch, like a
// provider with no clip stored for a language.
type onlyEnglish struct{ fakeSpeaker }

func (o *onlyEnglish) Speak(ctx context.Context, text string) ([]byte, error) {
	if text == voiceprompt.GreetingText(voiceprompt.Dutch, voiceprompt.Unavailable, "Jan Peeters") {
		return nil, errors.New("no Dutch voice")
	}
	return o.fakeSpeaker.Speak(ctx, text)
}

// TestPromptLanguageFallsBackToEnglish fails if a Dutch greeting that cannot be
// spoken leaves the caller in silence instead of hearing English (S-21).
func TestPromptLanguageFallsBackToEnglish(t *testing.T) {
	sp := &onlyEnglish{}
	c := greetingCall(sp, &fakeObjects{})
	sess := newFakeSess(time.Millisecond)
	c.playGreeting(t.Context(), sess, VoicemailBox{ID: 5, PromptLanguage: "nl"}, "no-answer")
	want := voiceprompt.GreetingText(voiceprompt.English, voiceprompt.Unavailable, "Jan Peeters")
	if sp.n(want) != 1 || sess.playCount() != 1 {
		t.Fatalf("spoke %v, plays %d; want the English greeting", sp.texts(), sess.playCount())
	}
}
