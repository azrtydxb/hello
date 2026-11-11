package prov

import (
	"context"
	"strings"
	"testing"
)

// TestSendAnonymousKey pins the operator decision on the WP "Send Anonymous"
// setting (P65, account 1; P421 and P521 for accounts 2 and 3 in
// wp8x6_config_1.0.3.39, wp810_822_825_config_1.0.11.83, wp820_config_1.0.7.90
// and wp856_config_1.0.3.16). It is off by default and then nothing is
// written; only the extension's phoneSendAnonymous setting writes P65=1.
// Hello provisions account 1 only, so P421 and P521 are never written.
// 100rel stays at the phone default (P272 = 0 is the key; the templates agree).
func TestSendAnonymousKey(t *testing.T) {
	for _, model := range []string{"WP816", "WP836", "WP856", "WP820", "WP822"} {
		render := func(on bool) string {
			d := gsFamilyData(model)
			d.Line.SendAnonymous = on
			out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
			if err != nil {
				t.Fatalf("%s: %v", model, err)
			}
			return string(out)
		}
		off := render(false)
		for _, k := range []string{"<P65>", "<P421>", "<P521>", "<P272>1</P272>"} {
			if strings.Contains(off, k) {
				t.Errorf("%s with the setting off writes %s", model, k)
			}
		}
		on := render(true)
		if !strings.Contains(on, "<P65>1</P65>") {
			t.Errorf("%s with the setting on does not write P65=1", model)
		}
		for _, k := range []string{"<P421>", "<P521>", "<P272>1</P272>"} {
			if strings.Contains(on, k) {
				t.Errorf("%s with the setting on writes %s", model, k)
			}
		}
	}
}
