package prov

import (
	"bytes"
	"context"
	"testing"
)

// TestCheckSyncProvisioned fails if the Grandstream template does not make
// a plain check-sync re-provision without a reboot (P95030 = 0) or does not
// turn on NOTIFY authentication (P4428 = 0), which hello-sip answers.
func TestCheckSyncProvisioned(t *testing.T) {
	d := renderSample(false)
	out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{"<P95030>0</P95030>", "<P4428>0</P4428>"} {
		if !bytes.Contains(out, []byte(w)) {
			t.Errorf("%q missing from\n%s", w, out)
		}
	}
}
