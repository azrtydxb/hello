package prov

import (
	"strings"
	"testing"
)

// TestHardeningTemplate fails if any S-22 key is missing for a Grandstream WP
// model, if a non-WP Grandstream or another vendor gets the WP keys, if
// TR-069 is not off on a WP and on Yealink, or if a key whose use Hello
// cannot yet support is switched on (Authenticate Incoming INVITE would fail
// every call to the phone because Hello does not answer the challenge).
func TestHardeningTemplate(t *testing.T) {
	want := []string{
		"<P1409>0</P1409>",   // TR-069 off
		"<P4503></P4503>",    // no ACS URL
		"<P276>1</P276>",     // SSH disabled
		"<P8469>0</P8469>",   // user web access disabled
		"<P1357>1</P1357>",   // keypad menu: basic settings only
		"<P1310>1</P1310>",   // direct IP calls off
		"<P2347>1</P2347>",   // accept SIP from the proxy only (S-32)
		"<P26015>0</P26015>", // allow SIP reset off
	}
	for _, model := range []string{"WP816", "WP826", "WP836", "WP856", "WP810", "WP820", "WP822", "WP825"} {
		got := renderDevice(t, Grandstream, gsFamilyData(model))
		for _, k := range want {
			if !strings.Contains(got, k) {
				t.Errorf("%s lacks %s", model, k)
			}
		}
		for _, k := range []string{"<P2346>", "<P60082>", "<P84988>", "<P1>", "<P240>", "<P1359>"} {
			if strings.Contains(got, k) {
				t.Errorf("%s writes %s, which is left out until it can be confirmed", model, k)
			}
		}
	}
	for _, model := range []string{"GRP2614", "HT813", "GXP2170"} {
		got := renderDevice(t, Grandstream, gsFamilyData(model))
		for _, k := range []string{"<P1409>", "<P4503>", "<P276>", "<P8469>", "<P1357>", "<P1310>", "<P2347>", "<P26015>"} {
			if strings.Contains(got, k) {
				t.Errorf("%s (not a WP) gets %s", model, k)
			}
		}
	}

	yl := SampleData(true)
	yl.Phone.Vendor, yl.Phone.Model = Yealink, "T54W"
	if got := renderDevice(t, Yealink, yl); !strings.Contains(got, "static.managementserver.enable = 0\n") {
		t.Error("Yealink does not switch TR-069 off")
	}

	// A group can still opt back in, through the setting overrides.
	d := gsFamilyData("WP836")
	d.Overrides = []Override{{Key: "P2347", Value: "0"}}
	if got := renderDevice(t, Grandstream, d); !strings.Contains(got, "<P2347>0</P2347>") || strings.Contains(got, "<P2347>1</P2347>") {
		t.Error("an override does not replace a hardening key")
	}
}
