package prov

import (
	"bytes"
	"context"
	"testing"
)

// TestMWISubscriptionProvisioned fails if a vendor template that has a known
// MWI subscribe setting does not write it (spec S-1): Grandstream P99 = 1
// with P33 defaulting to *97, Yealink account.1.subscribe_mwi, Poly
// msg.mwi.1.subscribe. Snom and Fanvil have no confirmed key (verify) and
// must still carry the voicemail number.
func TestMWISubscriptionProvisioned(t *testing.T) {
	d := renderSample(false)
	d.Line.VoicemailCode = ""
	for _, tc := range []struct {
		v     Vendor
		file  string
		wants []string
	}{
		{Grandstream, "cfg" + d.Phone.MAC + ".xml", []string{"<P99>1</P99>", "<P33>*97</P33>"}},
		{Yealink, d.Phone.MAC + ".cfg", []string{"account.1.subscribe_mwi = 1"}},
		{Poly, d.Phone.MAC + "-hello.cfg", []string{`msg.mwi.1.subscribe="` + d.Line.Username + `"`}},
	} {
		out, err := Render(context.Background(), builtinFor(t, tc.v), tc.file, d)
		if err != nil {
			t.Fatalf("%s: %v", tc.v, err)
		}
		for _, w := range tc.wants {
			if !bytes.Contains(out, []byte(w)) {
				t.Errorf("%s: %q missing from\n%s", tc.v, w, out)
			}
		}
	}
	d.Line.VoicemailCode = "*55"
	for _, tc := range []struct {
		v    Vendor
		file string
		want string
	}{
		{Snom, d.Phone.MACUpper, ">*55<"},
		{Fanvil, d.Phone.MAC + ".cfg", "SIP1 MWI Num       :*55"},
	} {
		out, err := Render(context.Background(), builtinFor(t, tc.v), tc.file, d)
		if err != nil {
			t.Fatalf("%s: %v", tc.v, err)
		}
		if !bytes.Contains(out, []byte(tc.want)) {
			t.Errorf("%s: %q missing", tc.v, tc.want)
		}
	}
}
