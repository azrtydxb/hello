package prov

import (
	"context"
	"strings"
	"testing"
)

// TestProvisionedPagingKeys fails if a built-in template that enables
// auto-answer for server paging and intercom (phonebooks-ptt spec P-12, P-13)
// omits its key, or if Grandstream's template turns on the plain account
// Auto Answer (P90), which would answer every call.
func TestProvisionedPagingKeys(t *testing.T) {
	cases := []struct {
		vendor  Vendor
		model   string
		file    string
		want    []string
		notWant []string
	}{
		{Grandstream, "WP822", "cfg805ec0aabbcc.xml", []string{"<P298>1</P298>"}, []string{"<P90>"}},
		{Yealink, "T54W", "805ec0aabbcc.cfg", []string{"features.intercom.allow = 1", "features.intercom.tone = 1"}, nil},
		{Poly, "VVX 450", "805ec0aabbcc-hello.cfg", []string{`voIpProt.SIP.alertInfo.1.class="ringAutoAnswer"`}, nil},
		{Fanvil, "X5U", "805ec0aabbcc.cfg", []string{"P1 Enable Intercom :1", "P1 Intercom Tone   :1", "P1 Intercom Mute   :0"}, []string{"SIP1 Auto Answer"}},
	}
	for _, c := range cases {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = c.vendor, c.model
		b, err := Render(context.Background(), builtinFor(t, c.vendor), c.file, d)
		if err != nil {
			t.Fatalf("%s: %v", c.vendor, err)
		}
		for _, w := range c.want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%s: rendering lacks %q", c.vendor, w)
			}
		}
		for _, w := range c.notWant {
			if strings.Contains(string(b), w) {
				t.Errorf("%s: rendering carries %q", c.vendor, w)
			}
		}
	}
}
