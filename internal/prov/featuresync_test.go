package prov

import (
	"bytes"
	"context"
	"testing"
)

// TestFeatureKeySyncProvisioned fails if a vendor template that has a
// feature key synchronization setting does not switch it on (spec S-7):
// Grandstream P2325 with Special Feature P198 = 102 (Broadsoft), Yealink
// account.1.feature_key_sync.enable and the dnd and forward switches, Poly
// serverFeatureControl dnd and cf, Fanvil SIP1 Feature Sync.

func TestFeatureKeySyncProvisioned(t *testing.T) {
	d := renderSample(false)
	for _, tc := range []struct {
		v     Vendor
		file  string
		wants []string
	}{
		{Grandstream, "cfg" + d.Phone.MAC + ".xml", []string{"<P2325>1</P2325>", "<P198>102</P198>"}},
		{Yealink, d.Phone.MAC + ".cfg", []string{
			"account.1.feature_key_sync.enable = 1", "account.1.dnd.feature_key_sync.enable = 1",
			"account.1.forward.feature_key_sync.enable = 1", "features.feature_key_sync.enable = 1"}},
		{Poly, d.Phone.MAC + "-hello.cfg", []string{
			`reg.1.serverFeatureControl.dnd="1"`, `reg.1.serverFeatureControl.cf="1"`,
			`voIpProt.SIP.serverFeatureControl.dnd="1"`, `voIpProt.SIP.serverFeatureControl.cf="1"`}},
		{Fanvil, d.Phone.MAC + ".cfg", []string{"SIP1 Feature Sync  :1"}},
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
}

// TestGrandstreamLocalFeatureCodes fails if a Grandstream phone is left
// handling the feature codes itself (P191 = 1), if a model's code strings
// are not Hello's codes where its template has the key, or if a key a model
// does not have is written: WP8x6 has DND, forward and record codes, WP856
// only the DND codes and Record Mode on the server, WP810/822/825 only P191.
func TestGrandstreamLocalFeatureCodes(t *testing.T) {
	codes := FeatureCodes{DNDOn: "*78", DNDOff: "*79", ForwardOn: "*72", ForwardOff: "*73",
		ForwardBusyOn: "*90", ForwardBusyOff: "*91", ForwardNoAnswerOn: "*92", ForwardNoAnswerOff: "*93"}
	for _, tc := range []struct {
		model            string
		ptt, wp856       bool
		wants, wantsNone []string
	}{
		{"WP826", true, false,
			[]string{"<P191>0</P191>", "<P2344>*78</P2344>", "<P2345>*79</P2345>", "<P26013>*72</P26013>", "<P26014>*73</P26014>",
				"<P26009>*90</P26009>", "<P26010>*91</P26010>", "<P26011>*92</P26011>", "<P26012>*93</P26012>",
				"<P26028>*1</P26028>", "<P26029>*1</P26029>"},
			[]string{"P6760"}},
		{"WP856", false, true,
			[]string{"<P191>0</P191>", "<P2344>*78</P2344>", "<P2345>*79</P2345>", "<P6760>2</P6760>"},
			[]string{"P26013", "P26028", "P26009"}},
		{"WP822", false, false,
			[]string{"<P191>0</P191>"},
			[]string{"P2344", "P26013", "P26028", "P6760"}},
	} {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model, d.Phone.PTTCapable = Grandstream, tc.model, tc.ptt
		d.Codes = codes
		out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
		if err != nil {
			t.Fatalf("%s: %v", tc.model, err)
		}
		for _, w := range tc.wants {
			if !bytes.Contains(out, []byte(w)) {
				t.Errorf("%s: %q missing", tc.model, w)
			}
		}
		for _, w := range tc.wantsNone {
			if bytes.Contains(out, []byte(w)) {
				t.Errorf("%s: %q must not be written", tc.model, w)
			}
		}
	}
}
