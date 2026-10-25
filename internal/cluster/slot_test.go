package cluster

import "testing"

// TestSlot fails if a Deployment pod's generated suffix stays in its slot
// (a replaced node would not be recognised) or a stable ID is altered.
func TestSlot(t *testing.T) {
	for id, want := range map[string]string{
		"hello-sip-1-d4f7cfdc8-2ccf7":    "hello-sip-1",
		"hello-sip-2-b97d69c-xxfcw":      "hello-sip-2",
		"hello-control-576dfb9d77-5kr5f": "hello-control",
		"hello-sip-0":                    "hello-sip-0", // StatefulSet pod
		"sip-1":                          "sip-1",       // compose service
		"hello-sip-1":                    "hello-sip-1",
		"node-a":                         "node-a",
	} {
		if got := Slot(id); got != want {
			t.Errorf("Slot(%q) = %q, want %q", id, got, want)
		}
	}
}
