package sip

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/voiceprompt"
)

// scriptUI is the session's voice for tests: it records what is said and
// answers key waits from a script. A key script entry of 0 is a silence. When
// the script runs out every wait is a silence, so a session always ends.
type scriptUI struct {
	said     []string // rendered texts, in order
	keys     []string // catalogue keys of said, "" for composed text
	script   []byte   // answers to Digit
	digitSet []string // answers to Digits, one entry per call: digits then '#'
	barge    map[string]byte
	prefetch []string
	played   []int64 // message ids played
	cut      map[int64]byte
	missing  map[int64]bool
	beeps    int
	takes    []takeScript
	clipsOut [][]byte
	// goneAfter ends the call after that many Say calls (0 = never).
	goneAfter int
}

type takeScript struct {
	dur time.Duration
	wav string
}

func (u *scriptUI) Prefetch(x utt) { u.prefetch = append(u.prefetch, x.Text) }
func (u *scriptUI) PrefetchMessage(m VoicemailMessage) {
	u.prefetch = append(u.prefetch, fmt.Sprintf("msg:%d", m.ID))
}

func (u *scriptUI) Say(_ context.Context, x utt, barge bool) (byte, error) {
	if u.goneAfter > 0 && len(u.said) >= u.goneAfter {
		return 0, context.Canceled
	}
	u.said = append(u.said, x.Text)
	u.keys = append(u.keys, string(x.Key))
	if barge {
		if d, ok := u.barge[string(x.Key)]; ok {
			delete(u.barge, string(x.Key))
			return d, nil
		}
	}
	return 0, nil
}

func (u *scriptUI) PlayMessage(_ context.Context, m VoicemailMessage) (byte, playStatus, error) {
	u.played = append(u.played, m.ID)
	if u.missing[m.ID] {
		return 0, playMissing, nil
	}
	if d, ok := u.cut[m.ID]; ok {
		delete(u.cut, m.ID)
		return d, playCut, nil
	}
	return 0, playFull, nil
}

func (u *scriptUI) Digit(_ context.Context, _ time.Duration) (byte, error) {
	if len(u.script) == 0 {
		return 0, nil
	}
	d := u.script[0]
	u.script = u.script[1:]
	return d, nil
}

func (u *scriptUI) Digits(_ context.Context, first byte, _ time.Duration) (string, error) {
	var out string
	if len(u.digitSet) > 0 {
		out = u.digitSet[0]
		u.digitSet = u.digitSet[1:]
	}
	if first != 0 && first != '#' {
		out = string(first) + out
	}
	return strings.TrimSuffix(out, "#"), nil
}

func (u *scriptUI) Beep(context.Context) error { u.beeps++; return nil }

func (u *scriptUI) Record(_ context.Context, _ time.Duration) ([]byte, time.Duration, error) {
	if len(u.takes) == 0 {
		return nil, 0, nil
	}
	t := u.takes[0]
	u.takes = u.takes[1:]
	return []byte(t.wav), t.dur, nil
}

func (u *scriptUI) PlayClip(_ context.Context, wav []byte) (byte, error) {
	u.clipsOut = append(u.clipsOut, wav)
	return 0, nil
}

// has reports whether the exact text was said.
func (u *scriptUI) has(text string) bool {
	for _, s := range u.said {
		if s == text {
			return true
		}
	}
	return false
}

func (u *scriptUI) hasKey(k voiceprompt.Key) bool {
	for _, s := range u.keys {
		if s == string(k) {
			return true
		}
	}
	return false
}

func (u *scriptUI) count(k voiceprompt.Key) int {
	n := 0
	for _, s := range u.keys {
		if s == string(k) {
			n++
		}
	}
	return n
}

// fakeData is the box behind a session.
type fakeData struct {
	pin      string
	msgs     []VoicemailMessage
	heard    []int64
	deleted  []int64
	newPIN   string
	greets   map[string][]byte
	resets   int
	failSet  bool
	callers  map[string]voiceprompt.Caller
	checks   []string
	downList bool
}

func (d *fakeData) CheckPIN(_ context.Context, pin string) (bool, error) {
	d.checks = append(d.checks, pin)
	return pin == d.pin, nil
}

func (d *fakeData) Messages(context.Context) ([]VoicemailMessage, error) {
	if d.downList {
		return nil, errors.New("db down")
	}
	return append([]VoicemailMessage(nil), d.msgs...), nil
}

func (d *fakeData) MarkHeard(_ context.Context, id int64) error {
	d.heard = append(d.heard, id)
	return nil
}

func (d *fakeData) Delete(_ context.Context, m VoicemailMessage) error {
	d.deleted = append(d.deleted, m.ID)
	return nil
}

func (d *fakeData) SetPIN(_ context.Context, pin string) error {
	if d.failSet {
		return errors.New("db down")
	}
	d.newPIN = pin
	return nil
}

func (d *fakeData) SetGreeting(_ context.Context, slot string, wav []byte) error {
	if d.failSet {
		return errors.New("store down")
	}
	if d.greets == nil {
		d.greets = map[string][]byte{}
	}
	d.greets[slot] = wav
	return nil
}

func (d *fakeData) ResetGreetings(context.Context) error { d.resets++; return nil }

func (d *fakeData) Caller(_ context.Context, m VoicemailMessage) voiceprompt.Caller {
	if c, ok := d.callers[m.Caller]; ok {
		return c
	}
	return voiceprompt.Caller{Number: m.Caller, CallerID: m.CallerName}
}

var vmNow = time.Date(2026, 10, 25, 20, 0, 0, 0, time.UTC)

// at is a message time on the given day offset (0 today, 1 yesterday).
func at(daysAgo, hour, min int) time.Time {
	return time.Date(2026, 10, 25-daysAgo, hour, min, 0, 0, time.UTC)
}

// threeMessages: two new (ids 1, 2, oldest first) and one saved (id 3).
func threeMessages() []VoicemailMessage {
	return []VoicemailMessage{
		{ID: 3, Caller: "0471234567", Heard: true, CreatedAt: at(5, 9, 0)},
		{ID: 1, Caller: "201", CreatedAt: at(0, 14, 32)},
		{ID: 2, Caller: "0499888777", CallerName: "Acme Ltd", CreatedAt: at(1, 8, 5)},
	}
}

func runSession(t *testing.T, ui *scriptUI, d *fakeData) {
	t.Helper()
	m := newVMMachine(voiceprompt.English, time.UTC, ui, d)
	m.now = func() time.Time { return vmNow }
	m.wait = time.Millisecond
	if err := m.run(t.Context()); err != nil {
		t.Fatalf("session: %v", err)
	}
}

func newData() *fakeData {
	return &fakeData{pin: "1234", msgs: threeMessages(),
		callers: map[string]voiceprompt.Caller{"201": {Number: "201", Extension: "Jan Peeters"}}}
}

// TestVoicemailSessionWalk fails if the session does not ask for the PIN,
// announce the counts, introduce each message with its number, sender and
// time, and follow the menu keys: 2 next, 9 save, # skip.
func TestVoicemailSessionWalk(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'2', '#'}}
	d := newData()
	runSession(t, ui, d)

	if ui.keys[0] != "pin.enter" {
		t.Fatalf("first prompt = %q, want the PIN prompt", ui.keys[0])
	}
	if len(d.checks) != 1 || d.checks[0] != "1234" {
		t.Fatalf("PIN checks = %v", d.checks)
	}
	if ui.said[1] != "You have 2 new messages and 1 saved message." {
		t.Fatalf("counts = %q", ui.said[1])
	}
	// Messages play oldest first; the example from the requirement.
	if ui.said[2] != "Message 1, from Jan Peeters, today at 14:32." {
		t.Fatalf("header 1 = %q", ui.said[2])
	}
	// Message 1 is second: yesterday, named by its caller ID.
	if !ui.has("Message 2, from Acme Ltd, yesterday at 08:05.") {
		t.Fatalf("header 2 missing: %q", ui.said)
	}
	// Hmm: order is by the list given (store lists newest first, callData
	// reverses; here the fake is already in the order to play).
	if got := ui.played; len(got) < 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("played = %v, want 1 then 2", got)
	}
	// 2 saved message 1 (it was already heard by playing it); '#' skipped 2
	// after it had played to the end, which is also heard.
	if len(d.heard) != 2 || d.heard[0] != 1 || d.heard[1] != 2 {
		t.Fatalf("heard = %v", d.heard)
	}
	if !ui.hasKey(voiceprompt.MsgMenu) || ui.count(voiceprompt.MsgMenu) != 1 {
		t.Fatalf("the full menu must be spoken once: %v", ui.keys)
	}
	if !ui.hasKey(voiceprompt.MsgMenuShort) {
		t.Fatalf("later menus must be the short one: %v", ui.keys)
	}
	if !ui.hasKey(voiceprompt.MsgEnd) {
		t.Fatalf("no end-of-messages prompt: %v", ui.keys)
	}
	// Then the main menu, silence twice, goodbye.
	if ui.keys[len(ui.keys)-1] != "goodbye" {
		t.Fatalf("last prompt = %q, want goodbye", ui.keys[len(ui.keys)-1])
	}
	if ui.count(voiceprompt.MainMenu) != 2 {
		t.Fatalf("main menu repeated %d times, want 2", ui.count(voiceprompt.MainMenu))
	}
}

// TestVoicemailSessionDeleteSaveReplayPrev covers 3, 9, 1 and *.
func TestVoicemailSessionDeleteSaveReplayPrev(t *testing.T) {
	// msg 1: replay (1), delete (3). msg 2: previous at the first (none);
	// then the next one after delete is msg 2: * (prev, nothing before it now
	// is deleted... it is the first) -> "no previous", then 9 save.
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'1', '3', '*', '9'}}
	d := newData()
	runSession(t, ui, d)
	if len(d.deleted) != 1 || d.deleted[0] != 1 {
		t.Fatalf("deleted = %v, want [1]", d.deleted)
	}
	if !ui.hasKey(voiceprompt.MsgDeleted) || !ui.hasKey(voiceprompt.MsgNoPrev) || !ui.hasKey(voiceprompt.MsgSaved) {
		t.Fatalf("prompts = %v", ui.keys)
	}
	// Replay plays message 1 twice but its header once.
	n1 := 0
	for _, id := range ui.played {
		if id == 1 {
			n1++
		}
	}
	if n1 != 2 {
		t.Fatalf("message 1 played %d times, want 2 (play + replay)", n1)
	}
	hdr := 0
	for _, s := range ui.said {
		if strings.HasPrefix(s, "Message 1, from Jan Peeters") {
			hdr++
		}
	}
	if hdr != 1 {
		t.Fatalf("header of message 1 said %d times, want 1", hdr)
	}
	// After the delete, the remaining new message is number 1 of the folder.
	if !ui.has("Message 1, from Acme Ltd, yesterday at 08:05.") {
		t.Fatalf("renumbered header missing: %q", ui.said)
	}
}

// TestVoicemailSessionPreviousAndSkip: * goes back one message; # skips
// without marking an unfinished message.
func TestVoicemailSessionPreviousAndSkip(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'2', '#', '#'}, cut: map[int64]byte{2: '*'}}
	d := newData()
	runSession(t, ui, d)
	// msg1 plays (heard) and 2 moves on; msg2 is cut by '*' (not heard) which
	// goes back to msg1; '#' skips to msg2 again, which plays through
	// (heard); '#' ends the folder.
	hdr1 := 0
	for _, s := range ui.said {
		if strings.HasPrefix(s, "Message 1,") {
			hdr1++
		}
	}
	if hdr1 != 2 {
		t.Fatalf("message 1 introduced %d times, want 2 (once, and again after *): %q", hdr1, ui.said)
	}
	// Message 2 interrupted the first time: heard only after its full play.
	if len(d.heard) != 2 || d.heard[0] != 1 || d.heard[1] != 2 {
		t.Fatalf("heard = %v, want [1 2]", d.heard)
	}
}

func TestVoicemailSessionWrongPIN(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1#", "22#", "333#"}}
	d := newData()
	runSession(t, ui, d)
	if len(d.checks) != 3 {
		t.Fatalf("PIN checks = %v, want 3", d.checks)
	}
	if ui.count(voiceprompt.PINInvalid) != 2 || !ui.hasKey(voiceprompt.PINLocked) {
		t.Fatalf("prompts = %v", ui.keys)
	}
	if ui.hasKey(voiceprompt.Counts) || len(ui.played) > 0 {
		t.Fatal("a wrong PIN reached the messages")
	}
}

// TestVoicemailSessionPINTypedDuringPrompt: a digit pressed while the
// prompt plays starts the PIN.
func TestVoicemailSessionPINTypedDuringPrompt(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"234#"}, barge: map[string]byte{"pin.enter": '1'}}
	d := newData()
	runSession(t, ui, d)
	if len(d.checks) != 1 || d.checks[0] != "1234" {
		t.Fatalf("checks = %v, want 1234", d.checks)
	}
}

func TestVoicemailSessionEmptyAndMissing(t *testing.T) {
	// No messages at all: counts, then the main menu.
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'1', '2', '5'}}
	d := newData()
	d.msgs = nil
	runSession(t, ui, d)
	if ui.said[1] != "You have no new messages and no saved messages." {
		t.Fatalf("counts = %q", ui.said[1])
	}
	if !ui.hasKey(voiceprompt.NoNew) || !ui.hasKey(voiceprompt.NoSaved) || !ui.hasKey(voiceprompt.InvalidKey) {
		t.Fatalf("prompts = %v", ui.keys)
	}

	// An unplayable message is announced as such and the menu still works.
	ui = &scriptUI{digitSet: []string{"1234#"}, script: []byte{'3'}, missing: map[int64]bool{1: true}}
	d = newData()
	runSession(t, ui, d)
	if !ui.hasKey(voiceprompt.MsgUnavail) || len(d.deleted) != 1 || len(d.heard) > 0 && d.heard[0] == 1 {
		t.Fatalf("missing message handling: keys=%v deleted=%v heard=%v", ui.keys, d.deleted, d.heard)
	}
}

func TestVoicemailSessionSavedFolder(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'2', '2', '2', '2', '2'}}
	d := newData()
	d.msgs = []VoicemailMessage{{ID: 3, Caller: "0471234567", Heard: true, CreatedAt: at(5, 9, 0)}}
	runSession(t, ui, d)
	if ui.said[1] != "You have no new messages and 1 saved message." {
		t.Fatalf("counts = %q", ui.said[1])
	}
	// The saved message is read as digits, dated by weekday (5 days ago = Tuesday... 20 Oct).
	if !ui.has("Message 1, from 0, 4, 7, 1, 2, 3, 4, 5, 6, 7, on Tuesday at 09:00.") {
		t.Fatalf("saved header missing: %q", ui.said)
	}
}

// TestVoicemailSessionIdleSaysGoodbye: silence at the message menu repeats it
// once and ends the call politely.
func TestVoicemailSessionIdleSaysGoodbye(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}}
	runSession(t, ui, newData())
	if ui.count(voiceprompt.MsgMenu)+ui.count(voiceprompt.MsgMenuShort) != 2 {
		t.Fatalf("menu said %d times: %v", ui.count(voiceprompt.MsgMenu)+ui.count(voiceprompt.MsgMenuShort), ui.keys)
	}
	if ui.keys[len(ui.keys)-1] != "goodbye" {
		t.Fatalf("keys = %v", ui.keys)
	}
}

func TestVoicemailSessionCallGone(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, goneAfter: 3}
	m := newVMMachine(voiceprompt.English, time.UTC, ui, newData())
	m.wait = time.Millisecond
	if err := m.run(t.Context()); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the call going away to surface", err)
	}
}

// TestVoicemailSessionPrefetchesNext fails if the next message's header and
// audio are not requested before the current message is spoken.
func TestVoicemailSessionPrefetchesNext(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'2'}}
	runSession(t, ui, newData())
	var iHeader, iMsg = -1, -1
	for i, p := range ui.prefetch {
		if p == "Message 2, from Acme Ltd, yesterday at 08:05." && iHeader < 0 {
			iHeader = i
		}
		if p == "msg:2" && iMsg < 0 {
			iMsg = i
		}
	}
	if iHeader < 0 || iMsg < 0 {
		t.Fatalf("next header/audio not prefetched: %q", ui.prefetch)
	}
	// The counts and the first header are requested up front.
	if ui.prefetch[0] != "You have 2 new messages and 1 saved message." {
		t.Fatalf("first prefetch = %q", ui.prefetch[0])
	}
}

// TestVoicemailOptionsRecordGreeting walks 0, 1 (record), keep.
func TestVoicemailOptionsRecordGreeting(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'0', '1', '1', '*', '3'},
		takes: []takeScript{{dur: 3 * time.Second, wav: "WAV-A"}}}
	d := newData()
	runSession(t, ui, d)
	if string(d.greets["greeting"]) != "WAV-A" {
		t.Fatalf("greetings = %v", d.greets)
	}
	for _, k := range []voiceprompt.Key{voiceprompt.OptMenu, voiceprompt.RecUnavail, voiceprompt.RecReview, voiceprompt.RecConfirm, voiceprompt.RecSaved} {
		if !ui.hasKey(k) {
			t.Errorf("missing prompt %q in %v", k, ui.keys)
		}
	}
	if ui.beeps != 1 || len(ui.clipsOut) != 1 {
		t.Fatalf("beeps=%d clips=%d, want 1/1", ui.beeps, len(ui.clipsOut))
	}
	if !strings.Contains(ui.said[indexOfKey(ui, "rec.unavailable")], "After the tone, record your greeting. Press pound when finished.") {
		t.Fatalf("record prompt wording: %q", ui.said)
	}
	if !strings.Contains(ui.said[indexOfKey(ui, "rec.confirm")], "Press 1 to keep it, 2 to re-record, or 3 to cancel.") {
		t.Fatalf("confirm wording: %q", ui.said)
	}
}

func indexOfKey(u *scriptUI, k string) int {
	for i, s := range u.keys {
		if s == k {
			return i
		}
	}
	return -1
}

// TestVoicemailOptionsRerecordCancelTooShort covers 2 (re-record), 3 (cancel)
// and a take under a second.
func TestVoicemailOptionsRerecordCancelTooShort(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"},
		// options, busy greeting: take 1 too short, take 2 re-record, take 3 keep.
		script: []byte{'0', '2', '2', '1', '3', '3', '*'},
		takes: []takeScript{{dur: 200 * time.Millisecond, wav: "short"}, {dur: 2 * time.Second, wav: "B1"}, {dur: 4 * time.Second, wav: "B2"},
			{dur: 2 * time.Second, wav: "N1"}}}
	d := newData()
	runSession(t, ui, d)
	if string(d.greets["busy"]) != "B2" {
		t.Fatalf("busy greeting = %q, want the third take", d.greets["busy"])
	}
	if ui.count(voiceprompt.RecTooShort) != 1 {
		t.Fatalf("too-short prompt %d times", ui.count(voiceprompt.RecTooShort))
	}
	// The name recording was cancelled with 3: nothing stored.
	if _, ok := d.greets["name"]; ok {
		t.Fatalf("a cancelled recording was stored: %v", d.greets)
	}
	if !ui.hasKey(voiceprompt.RecCancelled) {
		t.Fatalf("no cancel prompt: %v", ui.keys)
	}
}

func TestVoicemailOptionsRecordFailureAndName(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'0', '3', '1', '*'},
		takes: []takeScript{{dur: 2 * time.Second, wav: "NAME"}}}
	d := newData()
	d.failSet = true
	runSession(t, ui, d)
	if !ui.hasKey(voiceprompt.RecName) || !ui.hasKey(voiceprompt.RecFailed) || ui.hasKey(voiceprompt.RecSaved) {
		t.Fatalf("prompts = %v", ui.keys)
	}
}

func TestVoicemailOptionsChangePIN(t *testing.T) {
	// 0, 4: new PIN 5678 twice; then 4 again with a mismatch, then too short.
	ui := &scriptUI{digitSet: []string{"1234#", "5678#", "5678#", "9999#", "1111#", "12#"}, script: []byte{'0', '4', '4', '4', '*'}}
	d := newData()
	runSession(t, ui, d)
	if d.newPIN != "5678" {
		t.Fatalf("new PIN = %q", d.newPIN)
	}
	if !ui.hasKey(voiceprompt.PINChanged) || !ui.hasKey(voiceprompt.PINMismatch) || !ui.hasKey(voiceprompt.PINTooShort) {
		t.Fatalf("prompts = %v", ui.keys)
	}
}

func TestVoicemailOptionsRevert(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'0', '5', '*'}}
	d := newData()
	runSession(t, ui, d)
	if d.resets != 1 || !ui.hasKey(voiceprompt.RevertDone) {
		t.Fatalf("resets=%d keys=%v", d.resets, ui.keys)
	}
}

// TestVoicemailMenuFromMessageThenBack: 0 at the message menu goes to the
// options and * returns to the same message's menu.
func TestVoicemailMenuFromMessageThenBack(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'0', '*', '9'}}
	d := newData()
	runSession(t, ui, d)
	if !ui.hasKey(voiceprompt.OptMenu) || !ui.hasKey(voiceprompt.MsgSaved) {
		t.Fatalf("keys = %v", ui.keys)
	}
	if len(d.heard) == 0 || d.heard[0] != 1 {
		t.Fatalf("heard = %v", d.heard)
	}
}

// TestVoicemailSessionBargeInOnMessage: a key during the message goes straight
// to its action.
func TestVoicemailSessionBargeInOnMessage(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, cut: map[int64]byte{1: '3'}, script: []byte{'9'}}
	d := newData()
	runSession(t, ui, d)
	if len(d.deleted) != 1 || d.deleted[0] != 1 {
		t.Fatalf("deleted = %v", d.deleted)
	}
	// 3 during playback means no full listen: not marked heard before the delete.
	for _, id := range d.heard {
		if id == 1 {
			t.Fatalf("message 1 marked heard though cut and deleted: %v", d.heard)
		}
	}
	// The menu was never needed for message 1: no menu prompt before the delete.
	if i := indexOfKey(ui, "msg.deleted"); i < 0 || indexOfKey(ui, "msg.menu") > 0 && indexOfKey(ui, "msg.menu") < i {
		t.Fatalf("menu spoken before the barge-in delete: %v", ui.keys)
	}
}

func TestSessionListFailure(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}}
	d := newData()
	d.downList = true
	m := newVMMachine(voiceprompt.English, time.UTC, ui, d)
	m.wait = time.Millisecond
	if err := m.run(t.Context()); err == nil {
		t.Fatal("a failing store must surface")
	}
}

// TestDutchSession runs the walk in Dutch to show a second language needs
// only its table.
func TestDutchSession(t *testing.T) {
	ui := &scriptUI{digitSet: []string{"1234#"}, script: []byte{'2'}}
	d := newData()
	m := newVMMachine(voiceprompt.Dutch, time.UTC, ui, d)
	m.now = func() time.Time { return vmNow }
	m.wait = time.Millisecond
	if err := m.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ui.said[1] != "U heeft 2 nieuwe berichten en 1 bewaard bericht." {
		t.Fatalf("counts = %q", ui.said[1])
	}
	if ui.said[2] != "Bericht 1, van Jan Peeters, vandaag om 14:32." {
		t.Fatalf("header = %q", ui.said[2])
	}
}
