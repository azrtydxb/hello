package prov

import (
	"context"
	"strings"
	"testing"
)

// TestGrandstreamDSCPRendered fails if the Grandstream template does not
// write the SIP (P1558) and audio (P1559) DSCP from the configured values
// (spec S-21).
func TestGrandstreamDSCPRendered(t *testing.T) {
	for _, model := range []string{"WP822", "WP836", "WP856"} {
		for _, v := range []struct {
			sip, rtp int
			want     []string
		}{
			{26, 46, []string{"<P1558>26</P1558>", "<P1559>46</P1559>"}},
			{24, 34, []string{"<P1558>24</P1558>", "<P1559>34</P1559>"}},
		} {
			d := renderSample(false)
			d.Phone.Vendor, d.Phone.Model = Grandstream, model
			d.Prov.DSCPSIP, d.Prov.DSCPRTP = v.sip, v.rtp
			out, err := Render(context.Background(), builtinFor(t, Grandstream), ExpandPattern("cfg{mac}.xml", d.Phone), d)
			if err != nil {
				t.Fatalf("%s: %v", model, err)
			}
			for _, w := range v.want {
				if !strings.Contains(string(out), w) {
					t.Errorf("%s: %q missing from the rendered config", model, w)
				}
			}
		}
	}
}
