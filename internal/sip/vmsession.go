package sip

// The guided voicemail session behind *97: a PIN by voice, the counts, each
// message introduced by who it is from and when, a spoken menu after it, and
// a mailbox-options menu that records greetings by voice. This file is the
// menu state machine only. It speaks through vmUI and reads and writes the
// box through vmData, so it runs the same against a phone (vmcall.go) and
// against a script of key presses (the tests). Every sentence comes from the
// voiceprompt catalogue; nothing here is a string literal a caller hears.
//
// Key layout (docs/voicemail.md keeps the same table):
//
//	PIN prompt   digits, then #
//	main menu    1 new messages, 2 saved messages, 0 mailbox options
//	after a message
//	             1 replay        2 next (keeps it as saved)   3 or 7 delete
//	             9 save          * previous                    # skip (leave as is)
//	             0 mailbox options
//	options      1 record unavailable greeting   2 record busy greeting
//	             3 record your name              4 change PIN
//	             5 back to the default greetings * back
//	recording    # finishes; then 1 keep, 2 re-record, 3 cancel
//
// A message that was played to the end counts as saved (heard), as it always
// did; # skips without touching it. Silence after a prompt repeats it once,
// then the session says goodbye.

import (
	"context"
	"errors"
	"time"

	"github.com/azrtydxb/hello/internal/voiceprompt"
)

// utt is one thing to say: a catalogue key (for logs and tests) and the text
// it rendered to.
type utt struct {
	Key  voiceprompt.Key // "" for text built from several parts
	Text string
}

// playStatus says how a message's audio went.
type playStatus int

const (
	playFull    playStatus = iota // played to the end
	playCut                       // the caller pressed a key before the end
	playMissing                   // the audio could not be loaded
)

// vmUI is the session's voice: what it can say, hear and record.
type vmUI interface {
	// Prefetch starts preparing u in the background (the next header).
	Prefetch(u utt)
	// PrefetchMessage starts loading the audio of m in the background.
	PrefetchMessage(m VoicemailMessage)
	// Say speaks u. With barge a key pressed during it stops it and is
	// returned; without, keys are ignored.
	Say(ctx context.Context, u utt, barge bool) (byte, error)
	// PlayMessage plays m's recording; a key pressed during it stops it.
	PlayMessage(ctx context.Context, m VoicemailMessage) (byte, playStatus, error)
	// Digit waits up to wait for one key; 0 means none came.
	Digit(ctx context.Context, wait time.Duration) (byte, error)
	// Digits collects keys until #, starting with first when it is not 0.
	// wait bounds the gap between keys.
	Digits(ctx context.Context, first byte, wait time.Duration) (string, error)
	// Beep plays the tone that starts a recording.
	Beep(ctx context.Context) error
	// Record records until # or max and returns a WAV.
	Record(ctx context.Context, max time.Duration) (wav []byte, dur time.Duration, err error)
	// PlayClip plays a recorded WAV; a key pressed during it stops it.
	PlayClip(ctx context.Context, wav []byte) (byte, error)
}

// vmData is the box the session works on.
type vmData interface {
	CheckPIN(ctx context.Context, pin string) (bool, error)
	Messages(ctx context.Context) ([]VoicemailMessage, error)
	MarkHeard(ctx context.Context, id int64) error
	Delete(ctx context.Context, m VoicemailMessage) error
	SetPIN(ctx context.Context, pin string) error
	// SetGreeting stores a recorded WAV in slot: "greeting", "busy" or "name".
	SetGreeting(ctx context.Context, slot string, wav []byte) error
	// ResetGreetings reverts the greeting, busy and unreachable greetings to
	// the spoken defaults.
	ResetGreetings(ctx context.Context) error
	// Caller names the sender of m.
	Caller(ctx context.Context, m VoicemailMessage) voiceprompt.Caller
}

// errEnd unwinds the session after goodbye (or a locked PIN).
var errEnd = errors.New("voicemail session over")

// Session tunables.
const (
	vmMenuWait      = 6 * time.Second
	vmPINAttempts   = 3
	vmMinClip       = time.Second
	vmGreetingMax   = 60 * time.Second
	vmNameMax       = 15 * time.Second
	vmRecordTakes   = 3
	vmMinPINLength  = 4
	vmMaxPINLength  = 32
	vmMenuIdleTries = 2
)

type folder int

const (
	folderNew folder = iota
	folderSaved
)

type vmMachine struct {
	lang voiceprompt.Lang
	loc  *time.Location
	now  func() time.Time
	ui   vmUI
	data vmData
	// wait is how long the session waits for a key (vmMenuWait; tests shrink it).
	wait time.Duration

	msgs     []VoicemailMessage
	longSaid bool
	headers  map[int64]string
}

func newVMMachine(lang voiceprompt.Lang, loc *time.Location, ui vmUI, data vmData) *vmMachine {
	if loc == nil {
		loc = time.UTC
	}
	return &vmMachine{lang: lang, loc: loc, now: time.Now, ui: ui, data: data, wait: vmMenuWait, headers: map[int64]string{}}
}

func (m *vmMachine) u(k voiceprompt.Key) utt {
	return utt{Key: k, Text: voiceprompt.Text(m.lang, k, nil)}
}

func (m *vmMachine) say(ctx context.Context, k voiceprompt.Key) error {
	_, err := m.ui.Say(ctx, m.u(k), false)
	return err
}

// goodbye says goodbye and ends the session.
func (m *vmMachine) goodbye(ctx context.Context) error {
	if err := m.say(ctx, voiceprompt.Goodbye); err != nil {
		return err
	}
	return errEnd
}

// run is the whole session. It returns nil when the session ended by its own
// design (goodbye, locked PIN) and an error when the call went away.
func (m *vmMachine) run(ctx context.Context) error {
	if err := m.session(ctx); err != nil && !errors.Is(err, errEnd) {
		return err
	}
	return nil
}

func (m *vmMachine) session(ctx context.Context) error {
	if err := m.login(ctx); err != nil {
		return err
	}
	msgs, err := m.data.Messages(ctx)
	if err != nil {
		return err
	}
	m.msgs = msgs
	// Everything the caller will hear first is requested now, together: the
	// counts and the first message's header and audio synthesize while the
	// counts play.
	counts := m.countsUtt()
	m.ui.Prefetch(counts)
	if first := m.pick(folderNew); len(first) > 0 {
		m.prefetchMessage(ctx, first[0], 1)
	}
	if _, err := m.ui.Say(ctx, counts, false); err != nil {
		return err
	}
	next, haveNext := folderNew, len(m.pick(folderNew)) > 0
	for idle := 0; ; {
		if haveNext {
			if err := m.folder(ctx, next); err != nil {
				return err
			}
			haveNext = false
		}
		if err := m.say(ctx, voiceprompt.MainMenu); err != nil {
			return err
		}
		k, err := m.ui.Digit(ctx, m.wait)
		if err != nil {
			return err
		}
		switch k {
		case 0:
			if idle++; idle >= vmMenuIdleTries {
				return m.goodbye(ctx)
			}
		case '1':
			idle, next, haveNext = 0, folderNew, true
		case '2':
			idle, next, haveNext = 0, folderSaved, true
		case '0':
			idle = 0
			if err := m.options(ctx); err != nil {
				return err
			}
		default:
			idle = 0
			if err := m.say(ctx, voiceprompt.InvalidKey); err != nil {
				return err
			}
		}
	}
}

func (m *vmMachine) login(ctx context.Context) error {
	for attempt := 0; attempt < vmPINAttempts; attempt++ {
		first, err := m.ui.Say(ctx, m.u(voiceprompt.PINEnter), true)
		if err != nil {
			return err
		}
		pin, err := m.ui.Digits(ctx, first, m.wait)
		if err != nil {
			return err
		}
		ok := false
		if pin != "" {
			if ok, err = m.data.CheckPIN(ctx, pin); err != nil {
				return err
			}
		}
		if ok {
			return nil
		}
		if attempt < vmPINAttempts-1 {
			if err := m.say(ctx, voiceprompt.PINInvalid); err != nil {
				return err
			}
		}
	}
	if err := m.say(ctx, voiceprompt.PINLocked); err != nil {
		return err
	}
	return errEnd
}

// pick returns the messages of a folder: unheard ones are new, heard ones
// are saved. The slice is a copy; the session tracks changes in m.msgs.
func (m *vmMachine) pick(f folder) []VoicemailMessage {
	var out []VoicemailMessage
	for _, x := range m.msgs {
		if x.Heard == (f == folderSaved) {
			out = append(out, x)
		}
	}
	return out
}

func (m *vmMachine) countsUtt() utt {
	newN, savedN := len(m.pick(folderNew)), len(m.pick(folderSaved))
	t := voiceprompt.Text(m.lang, voiceprompt.Counts, voiceprompt.Vars{
		"new":   voiceprompt.Count(m.lang, voiceprompt.CountNew, newN, nil),
		"saved": voiceprompt.Count(m.lang, voiceprompt.CountSaved, savedN, nil),
	})
	return utt{Key: voiceprompt.Counts, Text: t}
}

// header is the spoken introduction of message n (1-based) of its folder.
func (m *vmMachine) header(ctx context.Context, msg VoicemailMessage, n int) utt {
	t := voiceprompt.Header(m.lang, n, m.data.Caller(ctx, msg), m.loc, m.now(), msg.CreatedAt)
	return utt{Key: voiceprompt.MsgHeader, Text: t}
}

func (m *vmMachine) prefetchMessage(ctx context.Context, msg VoicemailMessage, n int) {
	m.ui.Prefetch(m.header(ctx, msg, n))
	m.ui.PrefetchMessage(msg)
}

func (m *vmMachine) markHeard(ctx context.Context, list []VoicemailMessage, i int) error {
	if list[i].Heard {
		return nil
	}
	if err := m.data.MarkHeard(ctx, list[i].ID); err != nil {
		return nil //nolint:nilerr // a failed mark leaves the message new; the session carries on
	}
	list[i].Heard = true
	for j := range m.msgs {
		if m.msgs[j].ID == list[i].ID {
			m.msgs[j].Heard = true
		}
	}
	return nil
}

func (m *vmMachine) forget(id int64) {
	for j := range m.msgs {
		if m.msgs[j].ID == id {
			m.msgs = append(m.msgs[:j], m.msgs[j+1:]...)
			return
		}
	}
}

// folder plays one folder from its first message.
func (m *vmMachine) folder(ctx context.Context, f folder) error {
	list := m.pick(f)
	if len(list) == 0 {
		if f == folderNew {
			return m.say(ctx, voiceprompt.NoNew)
		}
		return m.say(ctx, voiceprompt.NoSaved)
	}
	idx, showHeader := 0, true
	for idx < len(list) {
		msg := list[idx]
		if idx+1 < len(list) {
			m.prefetchMessage(ctx, list[idx+1], idx+2)
		}
		var pending byte
		var err error
		if showHeader {
			if pending, err = m.ui.Say(ctx, m.header(ctx, msg, idx+1), true); err != nil {
				return err
			}
		}
		showHeader = true
		if pending == 0 {
			var st playStatus
			pending, st, err = m.ui.PlayMessage(ctx, msg)
			if err != nil {
				return err
			}
			switch st {
			case playFull:
				if err := m.markHeard(ctx, list, idx); err != nil {
					return err
				}
			case playMissing:
				if err := m.say(ctx, voiceprompt.MsgUnavail); err != nil {
					return err
				}
			case playCut:
			}
		}
		// The menu after the message. pending is a key already pressed.
	menu:
		for {
			if pending == 0 {
				k, err := m.menuKey(ctx)
				if err != nil {
					return err
				}
				if k == 0 {
					return m.goodbye(ctx)
				}
				pending = k
			}
			k := pending
			pending = 0
			switch k {
			case '1':
				showHeader = false
				break menu
			case '2':
				if err := m.markHeard(ctx, list, idx); err != nil {
					return err
				}
				idx++
				break menu
			case '3', '7':
				if err := m.data.Delete(ctx, msg); err != nil {
					if err := m.say(ctx, voiceprompt.MsgUnavail); err != nil {
						return err
					}
					continue
				}
				m.forget(msg.ID)
				list = append(list[:idx], list[idx+1:]...)
				if err := m.say(ctx, voiceprompt.MsgDeleted); err != nil {
					return err
				}
				break menu
			case '9':
				if err := m.markHeard(ctx, list, idx); err != nil {
					return err
				}
				if err := m.say(ctx, voiceprompt.MsgSaved); err != nil {
					return err
				}
				idx++
				break menu
			case '*':
				if idx == 0 {
					if err := m.say(ctx, voiceprompt.MsgNoPrev); err != nil {
						return err
					}
					continue
				}
				idx--
				break menu
			case '#':
				idx++
				break menu
			case '0':
				if err := m.options(ctx); err != nil {
					return err
				}
			default:
				if err := m.say(ctx, voiceprompt.InvalidKey); err != nil {
					return err
				}
			}
		}
	}
	return m.say(ctx, voiceprompt.MsgEnd)
}

// menuKey speaks the message menu (in full the first time, short after) and
// waits for a key; silence repeats it once. 0 means the caller is idle.
func (m *vmMachine) menuKey(ctx context.Context) (byte, error) {
	for try := 0; try < vmMenuIdleTries; try++ {
		k := voiceprompt.MsgMenu
		if m.longSaid {
			k = voiceprompt.MsgMenuShort
		}
		m.longSaid = true
		d, err := m.ui.Say(ctx, m.u(k), true)
		if err != nil || d != 0 {
			return d, err
		}
		if d, err = m.ui.Digit(ctx, m.wait); err != nil || d != 0 {
			return d, err
		}
	}
	return 0, nil
}

// options is the mailbox-options menu.
func (m *vmMachine) options(ctx context.Context) error {
	for {
		d, err := m.ui.Say(ctx, m.u(voiceprompt.OptMenu), true)
		if err != nil {
			return err
		}
		if d == 0 {
			if d, err = m.ui.Digit(ctx, m.wait); err != nil {
				return err
			}
		}
		switch d {
		case '1':
			err = m.record(ctx, "greeting", voiceprompt.RecUnavail, vmGreetingMax)
		case '2':
			err = m.record(ctx, "busy", voiceprompt.RecBusy, vmGreetingMax)
		case '3':
			err = m.record(ctx, "name", voiceprompt.RecName, vmNameMax)
		case '4':
			err = m.changePIN(ctx)
		case '5':
			if rerr := m.data.ResetGreetings(ctx); rerr != nil {
				err = m.say(ctx, voiceprompt.RecFailed)
			} else {
				err = m.say(ctx, voiceprompt.RevertDone)
			}
		case '*', 0:
			return nil
		default:
			err = m.say(ctx, voiceprompt.InvalidKey)
		}
		if err != nil {
			return err
		}
	}
}

// record guides one greeting recording: prompt, tone, recording, playback,
// then keep, re-record or cancel.
func (m *vmMachine) record(ctx context.Context, slot string, prompt voiceprompt.Key, max time.Duration) error {
	for take := 0; take < vmRecordTakes; take++ {
		if err := m.say(ctx, prompt); err != nil {
			return err
		}
		if err := m.ui.Beep(ctx); err != nil {
			return err
		}
		wav, dur, err := m.ui.Record(ctx, max)
		if err != nil {
			return err
		}
		if dur < vmMinClip {
			if err := m.say(ctx, voiceprompt.RecTooShort); err != nil {
				return err
			}
			continue
		}
		again, err := m.review(ctx, slot, wav)
		if err != nil || !again {
			return err
		}
	}
	return m.say(ctx, voiceprompt.RecCancelled)
}

// review plays a take back and asks what to do with it; again is true for a
// re-record.
func (m *vmMachine) review(ctx context.Context, slot string, wav []byte) (again bool, err error) {
	for try := 0; try < vmMenuIdleTries+1; try++ {
		if err := m.say(ctx, voiceprompt.RecReview); err != nil {
			return false, err
		}
		d, err := m.ui.PlayClip(ctx, wav)
		if err != nil {
			return false, err
		}
		if d == 0 {
			if d, err = m.ui.Say(ctx, m.u(voiceprompt.RecConfirm), true); err != nil {
				return false, err
			}
		}
		if d == 0 {
			if d, err = m.ui.Digit(ctx, m.wait); err != nil {
				return false, err
			}
		}
		switch d {
		case '1':
			if serr := m.data.SetGreeting(ctx, slot, wav); serr != nil {
				return false, m.say(ctx, voiceprompt.RecFailed)
			}
			return false, m.say(ctx, voiceprompt.RecSaved)
		case '2':
			return true, nil
		case '3':
			return false, m.say(ctx, voiceprompt.RecCancelled)
		case 0:
			return false, m.say(ctx, voiceprompt.RecCancelled)
		default:
			if err := m.say(ctx, voiceprompt.InvalidKey); err != nil {
				return false, err
			}
		}
	}
	return false, m.say(ctx, voiceprompt.RecCancelled)
}

// changePIN asks for the new PIN twice.
func (m *vmMachine) changePIN(ctx context.Context) error {
	read := func(k voiceprompt.Key) (string, error) {
		first, err := m.ui.Say(ctx, m.u(k), true)
		if err != nil {
			return "", err
		}
		return m.ui.Digits(ctx, first, m.wait)
	}
	a, err := read(voiceprompt.PINNew)
	if err != nil {
		return err
	}
	if len(a) < vmMinPINLength {
		return m.say(ctx, voiceprompt.PINTooShort)
	}
	b, err := read(voiceprompt.PINConfirm)
	if err != nil {
		return err
	}
	if a != b {
		return m.say(ctx, voiceprompt.PINMismatch)
	}
	if err := m.data.SetPIN(ctx, a); err != nil {
		return m.say(ctx, voiceprompt.PINChangeFail)
	}
	return m.say(ctx, voiceprompt.PINChanged)
}
