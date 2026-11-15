package sip

import "sync"

// inviteGate serializes the INVITE transactions Hello starts on one sipgo
// dialog session. RFC 3261 14.1: a UAC must not start a re-INVITE while
// another INVITE transaction on the dialog is in progress; a second one
// draws a 491 (glare) and, before this gate, raced on the session's stored
// INVITE (sipgo parses its headers lazily, and two senders wrote the same
// cached header) and on its CSeq counter (sipgo loads, increments and stores
// it non-atomically, so two senders could send one CSeq twice).
//
// The key is the session itself (a *sipgo.DialogClientSession or
// *sipgo.DialogServerSession): it is what the senders share, and it is per
// node, so the dialogs of one test process's several nodes never wait on
// each other. A homed leg (incall-ha) has its own gate (haLeg.inviteMu).
var inviteGate = newDialogGate()

// dialogGate is a set of mutexes keyed by dialog, each alive only while
// someone holds or waits for it.
type dialogGate struct {
	mu sync.Mutex
	m  map[any]*gateSlot
}

type gateSlot struct {
	mu   sync.Mutex
	refs int // holders and waiters
}

func newDialogGate() *dialogGate { return &dialogGate{m: map[any]*gateSlot{}} }

// lock takes the gate of key, waiting while another INVITE transaction on
// the dialog is in progress, and returns its release. A nil key is not
// gated.
func (g *dialogGate) lock(key any) (unlock func()) {
	if key == nil {
		return func() {}
	}
	g.mu.Lock()
	sl := g.m[key]
	if sl == nil {
		sl = &gateSlot{}
		g.m[key] = sl
	}
	sl.refs++
	g.mu.Unlock()
	sl.mu.Lock()
	return func() {
		sl.mu.Unlock()
		g.mu.Lock()
		if sl.refs--; sl.refs == 0 {
			delete(g.m, key)
		}
		g.mu.Unlock()
	}
}

// size is the number of dialogs with a holder or waiter (for tests).
func (g *dialogGate) size() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	return len(g.m)
}
