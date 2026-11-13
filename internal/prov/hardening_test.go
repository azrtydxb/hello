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
	// Keys every WP template defines (wp8x6_config_1.0.3.39,
	// wp810_822_825_config_1.0.11.83, wp820_config_1.0.7.90,
	// wp856_config_1.0.3.16).
	common := []string{
		"<P1409>0</P1409>",   // TR-069 off
		"<P4503></P4503>",    // no ACS URL
		"<P276>1</P276>",     // SSH off ("Enable SSH 0 Yes, 1 No" or "Disable SSH 1 Yes": 1 is off in all)
		"<P8469>0</P8469>",   // user web access disabled
		"<P2347>1</P2347>",   // accept SIP from the proxy only (S-32)
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
		// Left out on purpose: P2346 (Hello cannot answer a 401 on the INVITE it
		// originates), P60082 (needs P2311 and a certificate domain), P240 and
		// P1359 (the phone would accept only encrypted files), P1 (in no template).
		for _, k := range []string{"<P2346>", "<P60082>", "<P240>", "<P1359>", "<P1>"} {
			if strings.Contains(got, k) {
				t.Errorf("%s writes %s, which is left out until Hello can support it", model, k)
			}
		}
	}
	for _, model := range []string{"GRP2614", "HT813", "GXP2170"} {
		got := renderDevice(t, Grandstream, gsFamilyData(model))
		for _, k := range []string{"<P1409>", "<P4503>", "<P276>", "<P8469>", "<P1357>", "<P1310>", "<P2347>", "<P26015>", "<P8375>", "<P84988>"} {
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
