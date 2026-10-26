package sip

import (
	"context"
	"testing"
	"time"
)

// TestIVRHuntAnswerAtMemberTimeout: a hunt member that answers just as its
// turn ends has won the call. The menu must wait for that answer and bridge
// it; it fails if the turn's timer gives up on the member (the answered
// dialog would stay up with nobody on it while the caller hears the menu).
func TestIVRHuntAnswerAtMemberTimeout(t *testing.T) {
	s := &Server{dialogs: map[string]dialogRef{}, calls: map[*call]struct{}{}}
	c := &call{s: s, events: make(chan legEvent, 16), setupDone: make(chan struct{})}
	prev := ivrLaunch
	t.Cleanup(func() { ivrLaunch = prev })
	ivrLaunch = func(l *leg) {
		// The member's 200 is claimed now; its report is delivered only after
		// the member's turn (20 ms) has run out.
		if !c.claim(l) {
			t.Error("the member could not claim the call")
		}
		go func() {
			time.Sleep(150 * time.Millisecond)
			c.report(legEvent{leg: l, kind: evAnswered})
		}()
	}
	var forked *leg
	forks := []ivrFork{{per: 20 * time.Millisecond, mk: func(stage int) *leg {
		forked = c.addLeg(&leg{stage: stage})
		return forked
	}}}
	w, _ := c.ivrRing(context.Background(), forks, true, 2*time.Second)
	if w == nil || w != forked {
		t.Fatalf("winner = %v; want the member that answered", w)
	}
}
