package prov

import (
	"context"
	"strings"
	"testing"
)

// TestCallWaitingRendered fails if a vendor's template does not write the
// phone's own call-waiting switch (and tone) from the extension's flag, so
// the phone and Hello disagree about a second call (spec S-18).
func TestCallWaitingRendered(t *testing.T) {
	cases := []struct {
		v     Vendor
		model string
		file  string
		on    []string
		off   []string
	}{
		{Grandstream, "WP822", "cfg{mac}.xml",
			[]string{"<P26063>1</P26063>", "<P91>0</P91>", "<P186>0</P186>"},
			[]string{"<P26063>2</P26063>", "<P91>1</P91>", "<P186>1</P186>"}},
		{Grandstream, "WP836", "cfg{mac}.xml",
			[]string{"<P26063>1</P26063>", "<P91>0</P91>", "<P186>0</P186>"},
			[]string{"<P26063>2</P26063>", "<P91>1</P91>", "<P186>1</P186>"}},
		{Yealink, "T54W", "{mac}.cfg",
			[]string{"call_waiting.enable = 1\n", "call_waiting.tone = 1\n"},
			[]string{"call_waiting.enable = 0\n", "call_waiting.tone = 0\n"}},
		{Snom, "D785", "{MAC}",
			[]string{`<call_waiting idx="1" perm="R">on</call_waiting>`},
			[]string{`<call_waiting idx="1" perm="R">off</call_waiting>`}},
		{Poly, "VVX 450", "{mac}-hello.cfg",
			[]string{`call.callWaiting.enable="1" call.callWaiting.ring="beep"`},
			[]string{`call.callWaiting.enable="0"`}},
		{Fanvil, "X5U", "{mac}.cfg",
			[]string{"P1 CallWaiting     :1\n", "P1 CWaiting Tone   :1\n"},
			[]string{"P1 CallWaiting     :0\n", "P1 CWaiting Tone   :0\n"}},
	}
	for _, c := range cases {
		for _, on := range []bool{true, false} {
			d := renderSample(false)
			d.Phone.Vendor, d.Phone.Model = c.v, c.model
			d.Line.CallWaiting = on
			out, err := Render(context.Background(), builtinFor(t, c.v), ExpandPattern(c.file, d.Phone), d)
			if err != nil {
				t.Fatalf("%s %s: %v", c.v, c.model, err)
			}
			want, other := c.on, c.off
			if !on {
				want, other = c.off, c.on
			}
			for _, w := range want {
				if !strings.Contains(string(out), w) {
					t.Errorf("%s %s call waiting %v: %q missing from\n%s", c.v, c.model, on, w, out)
				}
			}
			for _, w := range other {
				if strings.Contains(string(out), w) {
					t.Errorf("%s %s call waiting %v: %q must not be written", c.v, c.model, on, w)
				}
			}
		}
	}
}
