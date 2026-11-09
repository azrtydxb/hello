package prov

import (
	"context"
	"strings"
	"testing"
)

// TestGrandstreamRTCPXRRendered fails if the WP8xx quality-report collector
// keys (P26093, P60087, P26094, P26095, from the WP810/822/825 release
// notes) are written when HELLO_PROV_RTCPXR is off, are missing when it is
// on, or reach a model whose release notes name no such keys.
func TestGrandstreamRTCPXRRendered(t *testing.T) {
	render := func(model string, on bool) string {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = Grandstream, model
		d.Phone.Family = GrandstreamFamily(model)
		d.Prov.RTCPXR = on
		out, err := Render(context.Background(), builtinFor(t, Grandstream), ExpandPattern("cfg{mac}.xml", d.Phone), d)
		if err != nil {
			t.Fatalf("%s: %v", model, err)
		}
		return string(out)
	}
	keys := []string{"<P26093>", "<P60087>1</P60087>", "<P26094></P26094>", "<P26095>"}
	on := render("WP822", true)
	for _, k := range keys {
		if !strings.Contains(on, k) {
			t.Errorf("WP822 with RTCP-XR on: %q missing", k)
		}
	}
	for _, c := range []struct {
		model string
		on    bool
	}{{"WP822", false}, {"WP836", true}, {"WP856", true}, {"WP836", false}} {
		out := render(c.model, c.on)
		for _, k := range []string{"P26093", "P60087", "P26094", "P26095"} {
			if strings.Contains(out, k) {
				t.Errorf("%s (RTCP-XR %v): %s must not be written", c.model, c.on, k)
			}
		}
	}
}
