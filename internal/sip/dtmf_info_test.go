package sip

import (
	"testing"
)

// TestDTMFInfoBodies checks that every SIP INFO DTMF flavour yields the
// same ASCII digit an RFC 2833 event does.
func TestDTMFInfoBodies(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
		want byte
		ok   bool
	}{
		{"relay digit", "Signal=5\r\nDuration=100\r\n", '5', true},
		{"relay star", "Signal=*\r\nDuration=100\r\n", '*', true},
		{"relay hash colon", "Signal: #\r\n", '#', true},
		{"relay star code", "Signal=10\r\nDuration=160\r\n", '*', true},
		{"relay hash code", "Signal=11\r\n", '#', true},
		{"relay letter", "Signal=C\r\n", 'C', true},
		{"relay letter code", "Signal=14\r\n", 'C', true},
		{"relay duration first", "Duration=100\r\nSignal=7\r\n", '7', true},
		{"bare star", "*", '*', true},
		{"bare hash crlf", "#\r\n", '#', true},
		{"bare digit", "3", '3', true},
		{"relay out of range", "Signal=99\r\n", 0, false},
		{"relay flash", "Signal=16\r\n", 0, false},
		{"empty", "", 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := dtmfDigit([]byte(tc.body))
			if ok != tc.ok || got != tc.want {
				t.Fatalf("dtmfDigit(%q) = %q,%v want %q,%v", tc.body, got, ok, tc.want, tc.ok)
			}
		})
	}
	for ct, want := range map[string]bool{
		"application/dtmf-relay": true, "application/dtmf": true,
		"Application/DTMF-Relay; charset=x": true, "application/sdp": false, "text/plain": false,
	} {
		if got := isDTMFInfo(ct); got != want {
			t.Errorf("isDTMFInfo(%q) = %v want %v", ct, got, want)
		}
	}
}
