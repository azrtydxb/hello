package sip

import (
	"sync"
	"testing"
)

// TestHomedCallCalleeHomedWhileReleased homes the callee leg while the
// call is released, as an IVR dial-out and a caller hang-up do. Under
// -race it fails if the legs are read without the homedCall lock.
func TestHomedCallCalleeHomedWhileReleased(t *testing.T) {
	s := &Server{dialogs: map[string]dialogRef{}, calls: map[*call]struct{}{}}
	a := &haLeg{callID: "caller"}
	hom := &homedCall{legs: [2]*haLeg{a, nil}}
	c := &call{s: s, homedCall: hom}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); hom.setCallee(&haLeg{callID: "callee"}) }()
	go func() { defer wg.Done(); c.release() }()
	wg.Wait()
	if got := hom.leg(true); got == nil || got.callID != "callee" {
		t.Fatalf("callee leg = %v; want the homed callee", got)
	}
}
