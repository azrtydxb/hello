package prov

import (
	"fmt"
	"strings"
	"testing"
)

// TestHardeningTemplate fails if any S-22 key is missing for a Grandstream WP
// model, if a non-WP Grandstream or another vendor gets the WP keys, if
// TR-069 is not off on a WP and on Yealink, if Authenticate Incoming INVITE
// is not on for every account of a WP (Hello answers the phone's challenge,
// internal/sip TestAuthenticatedInviteToPhone), or if a key whose use Hello
// cannot yet support is switched on.
func TestHardeningTemplate(t *testing.T) {
	// Keys every WP template defines (wp8x6_config_1.0.3.39,
	// wp810_822_825_config_1.0.11.83, wp820_config_1.0.7.90,
	// wp856_config_1.0.3.16).
	common := []string{
		"<P1409>0</P1409>",   // TR-069 off
		"<P4503></P4503>",    // no ACS URL
		"<P276>1</P276>",     // SSH off ("Enable SSH 0 Yes, 1 No" or "Disable SSH 1 Yes": 1 is off in all)
		"<P8469>0</P8469>",   // user web access disabled
		"<P2347>1</P2347>",   // accept SIP from the proxy only (S-32)
		"<P2346>1</P2346>",   // authenticate incoming INVITE (Hello answers the 401)
		"<P26015>0</P26015>", // allow SIP reset off
	}
	// Keys only some templates define.
	perModel := map[string]struct{ want, not []string }{
		"WP816": {[]string{"<P1357>1</P1357>", "<P1310>1</P1310>", "<P8375>0</P8375>"}, []string{"<P84988>"}},
		"WP826": {[]string{"<P1357>1</P1357>", "<P1310>1</P1310>", "<P8375>0</P8375>"}, []string{"<P84988>"}},
		"WP836": {[]string{"<P1357>1</P1357>", "<P1310>1</P1310>", "<P8375>0</P8375>"}, []string{"<P84988>"}},
		"WP810": {[]string{"<P1357>1</P1357>", "<P1310>1</P1310>", "<P84988>1</P84988>"}, []string{"<P8375>"}},
		"WP822": {[]string{"<P1357>1</P1357>", "<P1310>1</P1310>", "<P84988>1</P84988>"}, []string{"<P8375>"}},
		"WP825": {[]string{"<P1357>1</P1357>", "<P1310>1</P1310>", "<P84988>1</P84988>"}, []string{"<P8375>"}},
		// The WP820 template has no direct-IP or upgrade-confirmation key.
		"WP820": {[]string{"<P1357>1</P1357>"}, []string{"<P1310>", "<P8375>", "<P84988>"}},
		// The WP856 template has none of the three.
		"WP856": {nil, []string{"<P1357>", "<P1310>", "<P8375>", "<P84988>"}},
	}
	for model, pm := range perModel {
		got := renderDevice(t, Grandstream, gsFamilyData(model))
		for _, k := range append(append([]string{}, common...), pm.want...) {
			if !strings.Contains(got, k) {
				t.Errorf("%s lacks %s", model, k)
			}
		}
		for _, k := range pm.not {
			if strings.Contains(got, k) {
				t.Errorf("%s writes %s, which its template does not define", model, k)
			}
		}
		// Left out on purpose: P60082 (needs P2311 and a certificate domain),
		// P240 and P1359 (the phone would accept only encrypted files), P1 (in
		// no template).
		for _, k := range []string{"<P60082>", "<P240>", "<P1359>", "<P1>"} {
			if strings.Contains(got, k) {
				t.Errorf("%s writes %s, which is left out until Hello can support it", model, k)
			}
		}
	}
	for _, model := range []string{"GRP2614", "HT813", "GXP2170"} {
		got := renderDevice(t, Grandstream, gsFamilyData(model))
		for _, k := range []string{"<P1409>", "<P4503>", "<P276>", "<P8469>", "<P1357>", "<P1310>", "<P2346>", "<P2347>", "<P26015>", "<P8375>", "<P84988>"} {
			if strings.Contains(got, k) {
				t.Errorf("%s (not a WP) gets %s", model, k)
			}
		}
	}

	// Every further account of a WP is hardened too: account N at
	// +100 (N-1), up to the WP856's sixth (P2846, P2847).
	for model, lines := range map[string]int{"WP836": 3, "WP822": 2, "WP856": 6} {
		d := linesSample(Grandstream, model, 2)
		extra := d.Lines[0]
		d.Lines = nil
		for range lines - 1 {
			d.Lines = append(d.Lines, extra)
		}
		out := renderFor(t, Grandstream, linesFile(Grandstream, d), d)
		for n := 2; n <= lines; n++ {
			for _, base := range []int{2346, 2347} {
				k := fmt.Sprintf("<P%d>1</P%d>", base+100*(n-1), base+100*(n-1))
				if !strings.Contains(out, k) {
					t.Errorf("%s account %d lacks %s", model, n, k)
				}
			}
		}
	}
	gsLines := linesSample(Grandstream, "GRP2614", 2)
	if out := renderFor(t, Grandstream, linesFile(Grandstream, gsLines), gsLines); strings.Contains(out, "<P2446>") || strings.Contains(out, "<P2447>") {
		t.Error("a Grandstream that is not a WP gets the per-account hardening keys")
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
