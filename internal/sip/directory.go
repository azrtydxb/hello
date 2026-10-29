package sip

// The dial-by-name directory (pbx-gaps S-13). The caller spells the first
// letters of a name on the keypad and ends with #; the names that match come
// from the snapshot's in-memory index (hidden extensions are not in it), so
// the search never reads PostgreSQL. One match is connected after its name
// is spoken; two or three are read out with a digit each; more are refused
// with a request for more letters. Three entries that lead nowhere end the
// directory: an IVR menu then takes its invalid-choice destination, a call
// that came straight to the directory is hung up.
//
// Names are spoken through the Speaker (text-to-speech behind go-ai-sdk) in
// the configured prompt language; the one name that is connected is played
// from the extension's recorded voicemail name when it has one. Without a
// Speaker a prompt degrades to a tone, as in the voicemail application.

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/media"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/azrtydxb/hello/internal/voiceprompt"
	"github.com/emiago/sipgo/sip"
)

// Directory limits and replicated phase.
const (
	// directoryAttempts is how many entries a caller gets.
	directoryAttempts = 3
	// directoryMaxShown is how many names are read out; more matches ask for
	// more letters.
	directoryMaxShown = 3
	// directoryDigitWait and directoryInterDigit time a call that came
	// straight to the directory; a call from a menu uses the menu's own.
	directoryDigitWait  = 6 * time.Second
	directoryInterDigit = 4 * time.Second
	// haPhaseDirectory is the replicated phase of a call that came straight
	// to the directory; a taker restarts it from the first prompt.
	haPhaseDirectory  = "directory"
	haDirectoryDetail = "directory"
)

// Results of DirectoryTotal.
const (
	dirOne           = "one"
	dirSeveral       = "several"
	dirTooMany       = "too_many"
	dirNone          = "none"
	dirTooLong       = "too_long"
	dirTimeout       = "timeout"
	dirChosen        = "chosen"
	dirInvalidChoice = "invalid_choice"
	dirConnected     = "connected"
	dirFailed        = "failed"
)

// directoryTimes are the key waits of one run.
type directoryTimes struct{ first, between time.Duration }

func menuDirectoryTimes(m snapshot.IVRMenu) directoryTimes {
	t := directoryTimes{first: directoryDigitWait, between: directoryInterDigit}
	if m.DigitTimeout > 0 {
		t.first = time.Duration(m.DigitTimeout) * time.Second
	}
	if m.InterDigitTimeout > 0 {
		t.between = time.Duration(m.InterDigitTimeout) * time.Second
	}
	return t
}

// startDirectory answers a call that came straight to the directory (an
// inbound route, a time condition, a ring group failure or the feature code)
// and runs it. tx is the INVITE's transaction while the call has not begun,
// nil when it already did (a ring group's failure destination).
func (c *call) startDirectory(tx sip.ServerTransaction, snap *snapshot.Snapshot) {
	if tx != nil {
		c.considerAnchor(snap, EndpointInfo{})
	} else if !c.isAnchored() {
		c.startAnchor(snap, AnchorPolicy)
	}
	if !c.isAnchored() {
		c.ivrReject(tx, sip.StatusNotAcceptableHere, "the directory needs a G.711 SDP offer and a media port", ResultFailed)
		return
	}
	if tx != nil && !c.begin(c.inv, tx) {
		return
	}
	st := newIVRState()
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.ivr, c.ivrCancel = st, cancel
	c.setupDone = make(chan struct{})
	c.mu.Unlock()
	c.addTrace("Dial-by-name directory")
	if !c.soloAnswer(haPhaseDirectory, haDirectoryDetail, "directory") {
		cancel()
		return
	}
	go func() {
		defer contain(c.s.log, "directory flow")
		c.directoryStandalone(ctx)
	}()
}

// directoryResume restarts the directory on a call a survivor took over.
func (c *call) directoryResume() {
	relay := c.anchorRelay()
	if relay == nil {
		c.addTrace("Directory: no media after the takeover; ending the call")
		c.ivrHangup()
		return
	}
	st := newIVRState()
	ctx, cancel := context.WithCancel(context.Background())
	c.mu.Lock()
	c.ivr, c.ivrCancel = st, cancel
	c.events = make(chan legEvent, 16)
	c.setupDone = make(chan struct{})
	c.mu.Unlock()
	relay.OnDTMF(func(leg string, digit byte) { c.relayDTMF(leg, digit) })
	relay.OnPacket(c.recTap)
	c.addTrace("Directory restarts from its first prompt after the takeover")
	go func() {
		defer contain(c.s.log, "directory flow after takeover")
		c.directoryStandalone(ctx)
	}()
}

// directoryStandalone runs the directory for a call with no menu behind it:
// the call ends when no entry leads anywhere.
func (c *call) directoryStandalone(ctx context.Context) {
	snap := c.s.deps.Snapshots.Current()
	if snap == nil {
		c.ivrHangup()
		return
	}
	m := snapshot.IVRMenu{Name: "directory"}
	if next, done := c.ivrDirectory(ctx, snap, m); !done && next == "" {
		c.addTrace("Directory: no name was chosen; hanging up")
		c.ivrHangup()
	}
}

// ivrDirectoryExit is the directory as the destination of a menu key, a
// timeout, an invalid choice or a queue exit. When no entry leads anywhere
// the menu's invalid-choice destination takes the call; a menu whose invalid
// choice is the directory itself hangs up instead of looping.
func (c *call) ivrDirectoryExit(ctx context.Context, snap *snapshot.Snapshot, m snapshot.IVRMenu) (next string, done bool) {
	next, done = c.ivrDirectory(ctx, snap, m)
	if done || next != "" || ctx.Err() != nil {
		return next, done
	}
	if k := m.Invalid.Kind; k == "" || k == snapshot.IVRDirectory {
		c.ivrHangup()
		return "", true
	}
	c.addTrace("Directory: no name was chosen; using the menu's invalid-choice destination")
	return c.ivrLeave(ctx, snap, m, ivrExit{dest: m.Invalid, why: "directory: no name chosen", auto: true})
}

// ivrDirectory runs the directory. done means the call is connected or over;
// next names a menu to continue in; neither means no name was chosen.
func (c *call) ivrDirectory(ctx context.Context, snap *snapshot.Snapshot, m snapshot.IVRMenu) (next string, done bool) {
	lang := voiceprompt.Resolve(m.PromptLanguage, c.s.cfg.PromptLanguage)
	times := menuDirectoryTimes(m)
	for attempt := 0; attempt < directoryAttempts; attempt++ {
		keys, res := c.directoryEntry(ctx, lang, times)
		switch res {
		case dirEntryGone:
			return "", true
		case dirEntryTimeout:
			c.s.m.DirectoryTotal.WithLabelValues(dirTimeout).Inc()
			continue
		case dirEntryTooLong:
			c.s.m.DirectoryTotal.WithLabelValues(dirTooLong).Inc()
			if !c.directorySay(ctx, lang, voiceprompt.DirectoryTooLong, nil) {
				return "", true
			}
			continue
		}
		matches := snap.DirectorySearch(keys)
		c.addTrace(fmt.Sprintf("Directory: keys %s match %d extension(s)", keys, len(matches)))
		c.ivrLog("directory search", m.Name, "matches", len(matches))
		switch {
		case len(matches) == 0:
			c.s.m.DirectoryTotal.WithLabelValues(dirNone).Inc()
			if !c.directorySay(ctx, lang, voiceprompt.DirectoryNone, nil) {
				return "", true
			}
		case len(matches) > directoryMaxShown:
			c.s.m.DirectoryTotal.WithLabelValues(dirTooMany).Inc()
			if !c.directorySay(ctx, lang, voiceprompt.DirectoryMore, nil) {
				return "", true
			}
		case len(matches) == 1:
			c.s.m.DirectoryTotal.WithLabelValues(dirOne).Inc()
			return c.directoryConnect(ctx, snap, m, lang, matches[0])
		default:
			c.s.m.DirectoryTotal.WithLabelValues(dirSeveral).Inc()
			picked, ok, alive := c.directoryChoose(ctx, lang, times, matches)
			switch {
			case !alive:
				return "", true
			case ok:
				c.s.m.DirectoryTotal.WithLabelValues(dirChosen).Inc()
				return c.directoryConnect(ctx, snap, m, lang, picked)
			}
			c.s.m.DirectoryTotal.WithLabelValues(dirInvalidChoice).Inc()
		}
	}
	return "", false
}

// Results of directoryEntry besides keys.
const (
	dirEntryOK = iota
	dirEntryGone
	dirEntryTimeout
	dirEntryTooLong
)

// directoryEntry plays the prompt (a key press stops it) and collects the
// letters up to #. Keys without letters (0 and 1) are ignored and * starts
// the entry again. More than DirectoryMaxKeys letters are refused as soon as
// the extra one arrives.
func (c *call) directoryEntry(ctx context.Context, lang voiceprompt.Lang, t directoryTimes) (string, int) {
	c.ivrFlush()
	var buf []byte
	take := func(d byte) (full, end bool) {
		switch {
		case d == '#':
			return false, len(buf) > 0
		case d == '*':
			buf = buf[:0]
		case d >= '2' && d <= '9':
			buf = append(buf, d)
			return len(buf) > snapshot.DirectoryMaxKeys, false
		}
		return false, false
	}
	d, hit, alive := c.directorySpeak(ctx, lang, voiceprompt.DirectoryEnter, nil, true)
	if !alive {
		return "", dirEntryGone
	}
	wait := t.first
	if hit {
		if full, end := take(d); full {
			return "", dirEntryTooLong
		} else if end {
			return string(buf), dirEntryOK
		}
		wait = t.between
	}
	for {
		d, ok, alive := c.ivrNextDigit(ctx, wait)
		switch {
		case !alive:
			return "", dirEntryGone
		case !ok:
			if len(buf) == 0 {
				return "", dirEntryTimeout
			}
			return string(buf), dirEntryOK // a pause ends the entry too
		}
		full, end := take(d)
		switch {
		case full:
			return "", dirEntryTooLong
		case end:
			return string(buf), dirEntryOK
		}
		wait = t.between
	}
}

// directoryChoose reads two or three names with a digit each and waits for
// the caller's choice. alive is false once the call is over.
func (c *call) directoryChoose(ctx context.Context, lang voiceprompt.Lang, t directoryTimes, matches []snapshot.DirectoryEntry) (picked snapshot.DirectoryEntry, ok, alive bool) {
	if _, _, alive := c.directorySpeak(ctx, lang, voiceprompt.DirectoryChoose, nil, false); !alive {
		return picked, false, false
	}
	choice := func(d byte) (snapshot.DirectoryEntry, bool) {
		if i := int(d - '1'); d >= '1' && i < len(matches) {
			return matches[i], true
		}
		return snapshot.DirectoryEntry{}, false
	}
	for i, e := range matches {
		vars := voiceprompt.Vars{"name": e.Name, "digit": fmt.Sprint(i + 1)}
		d, hit, alive := c.directorySpeak(ctx, lang, voiceprompt.DirectoryOne, vars, true)
		if !alive {
			return picked, false, false
		}
		if hit {
			p, ok := choice(d)
			return p, ok, true
		}
	}
	d, got, alive := c.ivrNextDigit(ctx, t.first)
	if !alive {
		return picked, false, false
	}
	if !got {
		return picked, false, true
	}
	p, ok := choice(d)
	return p, ok, true
}

// directoryConnect says whom the call goes to and dials the extension. The
// extension's own forwarding, do-not-disturb and voicemail apply, as for a
// menu key. A call the extension did not take is a failed entry.
func (c *call) directoryConnect(ctx context.Context, snap *snapshot.Snapshot, m snapshot.IVRMenu, lang voiceprompt.Lang, e snapshot.DirectoryEntry) (next string, done bool) {
	c.addTrace(fmt.Sprintf("Directory: connecting to %s (%s)", e.Name, e.Number))
	c.ivrLog("directory connect", m.Name, "extension", e.Number)
	if _, _, alive := c.directorySpeak(ctx, lang, voiceprompt.DirectoryConnecting, nil, false); !alive {
		return "", true
	}
	if !c.directoryPlayName(ctx, e) {
		return "", true
	}
	res := c.ivrDial(ctx, snap, m, snapshot.IVRDest{Kind: snapshot.IVRExtension, Target: e.Number})
	switch {
	case res.kind == ivrConnected || res.kind == ivrFinished:
		c.s.m.DirectoryTotal.WithLabelValues(dirConnected).Inc()
		return "", true
	case res.menu != "":
		return res.menu, false
	}
	c.s.m.DirectoryTotal.WithLabelValues(dirFailed).Inc()
	return "", false
}

// directoryPlayName plays the callee's name: the recording made in its
// voicemail box when there is one, else the name spoken. false means the
// call is over.
func (c *call) directoryPlayName(ctx context.Context, e snapshot.DirectoryEntry) bool {
	if objs := c.s.deps.Objects; objs != nil {
		if box := c.boxDetails(e.Number); box.NameObject != "" {
			octx, cancel := context.WithTimeout(ctx, c.s.cfg.StateTimeout)
			raw, err := objs.Get(octx, box.NameObject)
			cancel()
			if err == nil {
				if pcm, rate, err := media.DecodeWAV(raw); err == nil && len(pcm) > 0 {
					_, _, alive := c.ivrPlayAudio(ctx, ivrAudio{pcm: pcm, rate: rate}, "recorded name", false)
					return alive
				}
			}
		}
	}
	_, _, alive := c.ivrPlayAudio(ctx, c.directoryAudio(ctx, e.Name), "name", false)
	return alive
}

// directorySay speaks a prompt without barge-in; false means the call is
// over.
func (c *call) directorySay(ctx context.Context, lang voiceprompt.Lang, k voiceprompt.Key, v voiceprompt.Vars) bool {
	_, _, alive := c.directorySpeak(ctx, lang, k, v, false)
	return alive
}

// directorySpeak speaks a catalogue prompt; a key press stops it when barge
// is set and is returned.
func (c *call) directorySpeak(ctx context.Context, lang voiceprompt.Lang, k voiceprompt.Key, v voiceprompt.Vars, barge bool) (digit byte, hit, alive bool) {
	return c.ivrPlayAudio(ctx, c.directoryAudio(ctx, voiceprompt.Text(lang, k, v)), string(k), barge)
}

// directoryAudio turns text into audio through the Speaker, remembering it
// for the call. When speech is unavailable the caller hears a tone, so the
// keys still work.
func (c *call) directoryAudio(ctx context.Context, text string) ivrAudio {
	c.mu.Lock()
	st := c.ivr
	c.mu.Unlock()
	key := "tts:" + text
	st.mu.Lock()
	a, ok := st.cache[key]
	st.mu.Unlock()
	if ok {
		return a
	}
	if sp := c.s.deps.Speech; sp != nil && strings.TrimSpace(text) != "" {
		sctx, cancel := context.WithTimeout(ctx, vmSpeakWait)
		wav, err := sp.Speak(sctx, text)
		cancel()
		if err == nil {
			a.pcm, a.rate, err = media.DecodeWAV(wav)
		}
		if err != nil {
			a = ivrAudio{}
			// The error never carries the provider key.
			c.s.log.Warn("directory: spoken prompt unavailable; playing a tone instead", "component", "sip", "error", err)
		}
	}
	if len(a.pcm) == 0 {
		a = ivrAudio{pcm: media.BeepPCMRate(8000), rate: 8000}
	}
	if ctx.Err() == nil {
		st.mu.Lock()
		st.cache[key] = a
		st.mu.Unlock()
	}
	return a
}
