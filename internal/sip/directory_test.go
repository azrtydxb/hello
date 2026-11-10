package sip

import (
	"context"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
	"github.com/emiago/sipgo/sip"
)

// spoke reports whether the speaker was asked for text.
func (f *fakeSpeaker) spoke(text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[text] > 0
}

func dirText(k voiceprompt.Key, v voiceprompt.Vars) string {
	return voiceprompt.Text(voiceprompt.Default, k, v)
}

func waitSpoke(t *testing.T, sp *fakeSpeaker, text string) {
	t.Helper()
	eventually(t, "the directory speaks "+text, func() bool { return sp.spoke(text) })
}

func dirResult(pbx *testPBX, t *testing.T, result string) float64 {
	t.Helper()
	return pbx.metric(t, "hello_directory_search_total", map[string]string{"result": result})
}

// dirPBX starts a PBX whose menu "main" (number 800) has the directory on key
// 5 and sends an invalid choice to extension 300. The directory holds Alice
// Adams (100), Bob Smith (200), Carol Jones (300), Cathy Jonas (210) and the
// hidden Zed Zebra (599).
func dirPBX(t *testing.T, m func(*snapshot.IVRMenu), codes ...snapshot.FeatureCode) (*testPBX, *fakeSpeaker) {
	t.Helper()
	sp := &fakeSpeaker{}
	menu := ivrMenu("main", "800", map[string]snapshot.IVRDest{"5": {Kind: snapshot.IVRDirectory}})
	menu.Invalid = ext("300")
	if m != nil {
		m(&menu)
	}
	objs := &fakeObjects{}
	_ = objs.Put(context.Background(), "ann/greet.wav", media.EncodeWAV(make([]byte, 16000), 8000))
	feats := func(s *snapshot.Snapshot) {
		s.WithAnnouncements(map[string]string{"greet": "ann/greet.wav"})
		s.WithIVRMenus([]snapshot.IVRMenu{menu})
		s.WithFeatureCodes(codes)
		s.WithExtensionData([]snapshot.Extension{
			{Number: "100", Name: "Alice Adams"}, {Number: "200", Name: "Bob Smith"},
			{Number: "300", Name: "Carol Jones"}, {Number: "210", Name: "Cathy Jonas"},
			{Number: "599", Name: "Zed Zebra", DirectoryHidden: true},
		})
	}
	withSpeech := func(_ *Config, d *Deps) { d.Speech = sp }
	pbx := startPBX(t, threeDevices(), shortRing(2*time.Second), withMedia(false),
		withFeatures(feats), withVoicemail(newVMStore(), objs), withSpeech)
	return pbx, sp
}

// enterDirectory dials the menu and presses 5, then waits for the prompt.
func enterDirectory(t *testing.T, pbx *testPBX, sp *fakeSpeaker, a *phone) (callResult, *uint32) {
	t.Helper()
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatalf("call to the menu: %v", r.err)
	}
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "5", &seq)
	waitSpoke(t, sp, dirText(voiceprompt.DirectoryEnter, nil))
	return r, &seq
}

// TestDirectorySearch: a caller who presses the directory key and spells
// "Smi" (764) is told whom the call goes to and is connected to the one
// extension that matches. It fails if the directory kind is not carried out
// (the menu hangs up on an unknown kind), the keypad letters do not match, the
// name is not spoken or the extension is not rung.
func TestDirectorySearch(t *testing.T) {
	pbx, sp := dirPBX(t, nil)
	a, b, _ := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	r, seq := enterDirectory(t, pbx, sp, a)
	sendDigits(t, a, r.dcs, "764#", seq)
	waitReq(t, b.invites, "INVITE to Bob Smith's extension")
	if !sp.spoke("Bob Smith") || !sp.spoke(dirText(voiceprompt.DirectoryConnecting, nil)) {
		t.Fatal("the name and the hand-over were not spoken")
	}
	eventually(t, "connected", func() bool { return dirResult(pbx, t, dirConnected) == 1 })
	if dirResult(pbx, t, dirOne) != 1 {
		t.Fatal("hello_directory_search_total{one} not counted")
	}
	_ = r.dcs.Bye(t.Context())
}

// TestDirectoryTwoMatches: two names are read in last-name order with a digit
// each and the digit chosen connects that extension. "Jon" (566) matches Cathy
// Jonas (210) and Carol Jones (300); 2 is Jones.
func TestDirectoryTwoMatches(t *testing.T) {
	pbx, sp := dirPBX(t, nil)
	a, b, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	r, seq := enterDirectory(t, pbx, sp, a)
	sendDigits(t, a, r.dcs, "566#", seq)
	waitSpoke(t, sp, dirText(voiceprompt.DirectoryOne, voiceprompt.Vars{"name": "Cathy Jonas", "digit": "1"}))
	waitSpoke(t, sp, dirText(voiceprompt.DirectoryOne, voiceprompt.Vars{"name": "Carol Jones", "digit": "2"}))
	noReq(t, b.invites, 100*time.Millisecond, "a ring before a choice")
	sendDigits(t, a, r.dcs, "2", seq)
	waitReq(t, c.invites, "INVITE to Carol Jones's extension")
	if dirResult(pbx, t, dirSeveral) != 1 || dirResult(pbx, t, dirChosen) != 1 {
		t.Fatal("several/chosen not counted")
	}
	_ = r.dcs.Bye(t.Context())
}

// TestDirectoryHiddenAndLong: a hidden extension is not found, and the search
// says nothing about it; an eighth key is refused with a request for fewer.
func TestDirectoryHiddenAndLong(t *testing.T) {
	pbx, sp := dirPBX(t, nil)
	a, _, _ := registerABC(t, pbx)
	r, seq := enterDirectory(t, pbx, sp, a)
	sendDigits(t, a, r.dcs, "932#", seq) // "Zeb": Zed Zebra is hidden
	waitSpoke(t, sp, dirText(voiceprompt.DirectoryNone, nil))
	if sp.spoke("Zed Zebra") {
		t.Fatal("a hidden extension was named")
	}
	eventually(t, "the second entry is prompted", func() bool { return dirResult(pbx, t, dirNone) == 1 })
	time.Sleep(500 * time.Millisecond) // the "no match" prompt ends, the next entry starts
	sendDigits(t, a, r.dcs, "76476476", seq)
	waitSpoke(t, sp, dirText(voiceprompt.DirectoryTooLong, nil))
	if dirResult(pbx, t, dirTooLong) != 1 {
		t.Fatal("too_long not counted")
	}
	_ = r.dcs.Bye(t.Context())
}

// TestDirectoryFailedEntriesUseInvalidDestination: three entries that match
// nothing send the call to the menu's invalid-choice destination.
func TestDirectoryFailedEntriesUseInvalidDestination(t *testing.T) {
	pbx, sp := dirPBX(t, nil)
	a, _, c := registerABC(t, pbx)
	c.setCallee(answerAfter(nil))
	r, seq := enterDirectory(t, pbx, sp, a)
	deadline := time.After(15 * time.Second)
	for sent := false; !sent; {
		sendDigits(t, a, r.dcs, "999#", seq) // every entry, whenever it is read, matches nothing
		select {
		case <-c.invites:
			sent = true
		case <-deadline:
			t.Fatal("the invalid-choice destination did not take the call")
		case <-time.After(300 * time.Millisecond):
		}
	}
	if n := dirResult(pbx, t, dirNone); n < 3 {
		t.Fatalf("%v entries failed, want 3", n)
	}
	_ = r.dcs.Bye(t.Context())
}

// TestDirectoryFeatureCode: the directory feature code answers the call
// itself and connects a name; with no usable entry it hangs the caller up.
func TestDirectoryFeatureCode(t *testing.T) {
	pbx, sp := dirPBX(t, nil, snapshot.FeatureCode{Code: "*42", Action: ActionDirectory})
	a, b, _ := registerABC(t, pbx)
	b.setCallee(answerAfter(nil))
	r := waitCall(t, dial(t.Context(), a, "*42"))
	if r.err != nil {
		t.Fatalf("the directory code did not answer: %v", r.err)
	}
	if res := r.dcs.InviteResponse; res == nil || res.StatusCode != sip.StatusOK {
		t.Fatal("not answered")
	}
	waitSpoke(t, sp, dirText(voiceprompt.DirectoryEnter, nil))
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "764#", &seq)
	waitReq(t, b.invites, "INVITE to Bob Smith's extension")
	_ = r.dcs.Bye(t.Context())

	// A caller who never finds a name is hung up on.
	a2 := newPhone(t, pbx, "d1", "pd")
	a2.register(t)
	r2 := waitCall(t, dial(t.Context(), a2, "*42"))
	if r2.err != nil {
		t.Fatal(r2.err)
	}
	seq = r2.dcs.InviteRequest.CSeq().SeqNo + 10
	deadline := time.After(20 * time.Second)
	for {
		sendDigits(t, a2, r2.dcs, "999#", &seq)
		select {
		case <-a2.byes:
			return
		case <-deadline:
			t.Fatal("the caller was not hung up after three failed entries")
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// TestDirectoryPromptLanguage: a menu's promptLanguage chooses the language
// of the directory prompts. It fails if the English text is spoken.
func TestDirectoryPromptLanguage(t *testing.T) {
	pbx, sp := dirPBX(t, func(m *snapshot.IVRMenu) { m.PromptLanguage = "nl" })
	a, _, _ := registerABC(t, pbx)
	r := waitCall(t, dial(t.Context(), a, "800"))
	if r.err != nil {
		t.Fatal(r.err)
	}
	seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
	sendDigits(t, a, r.dcs, "5", &seq)
	waitSpoke(t, sp, voiceprompt.Text(voiceprompt.Dutch, voiceprompt.DirectoryEnter, nil))
	if sp.spoke(dirText(voiceprompt.DirectoryEnter, nil)) {
		t.Fatal("the English prompt was spoken")
	}
	_ = r.dcs.Bye(t.Context())
}
