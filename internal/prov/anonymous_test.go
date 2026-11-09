package prov

import (
	"context"
	"strings"
	"testing"
)

// TestAnonymousKeyNotProvisioned pins the item 26 decision: the WP "Send
// Anonymous" setting (P65, account 1; P421 and P521 for accounts 2 and 3 in
// wp8x6_config_1.0.3.39, wp810_822_825_config_1.0.11.83, wp820_config_1.0.7.90
// and wp856_config_1.0.3.16) is left to a per-phone override. Hello applies
// the extension's anonymousOutbound flag on trunk legs, and a phone that sends
// an anonymous From on every call would blank internal caller ID too. 100rel
// stays at the phone default (P272 = 0 is the key; the templates agree).
func TestAnonymousKeyNotProvisioned(t *testing.T) {
	for _, model := range []string{"WP816", "WP836", "WP856", "WP820", "WP822"} {
		d := gsFamilyData(model)
		out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		got := string(out)
		for _, k := range []string{"<P65>", "<P421>", "<P521>", "<P272>1</P272>"} {
			if strings.Contains(got, k) {
				t.Errorf("%s writes %s, which is left to a per-phone override", model, k)
			}
		}
	}
}
