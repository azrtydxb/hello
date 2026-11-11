package prov

import (
	"context"
	"strings"
	"testing"
)

// TestGrandstreamRTCPXRRendered fails if the quality-report collector keys
// are written when HELLO_PROV_RTCPXR is off, are missing or wrong when it is
// on, or reach a model whose official template has no such keys. The keys are
// from wp8x6_config_1.0.3.39 (P26093, P60087, P26094, P26095, P8489),
// wp810_822_825_config_1.0.11.83 (the same five) and wp856_config_1.0.3.16
// (P26093, P26094, P26095 only); wp820_config_1.0.7.90 has none.
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
	all := []string{"P26093", "P60087", "P26094", "P26095", "P8489"}
	auto := []string{"<P26093>", "<P60087>1</P60087>", "<P26094></P26094>", "<P26095>", "<P8489>1</P8489>"}
	for _, model := range []string{"WP822", "WP836"} {
		got := render(model, true)
		for _, k := range auto {
			if !strings.Contains(got, k) {
				t.Errorf("%s with RTCP-XR on: %q missing", model, k)
			}
		}
	}
	// The WP856 template has no Address Selection and no Session Report key:
	// the address is spelled out and neither of those is written.
	wp856 := render("WP856", true)
	for _, k := range []string{"<P26093>", "<P26094>", "<P26095>"} {
		if !strings.Contains(wp856, k) {
			t.Errorf("WP856 with RTCP-XR on: %q missing", k)
		}
	}
	if strings.Contains(wp856, "<P26094></P26094>") {
		t.Error("WP856 has no Auto address selection, so its collector address must be set")
	}
	for _, k := range []string{"P60087", "P8489"} {
		if strings.Contains(wp856, k) {
			t.Errorf("WP856: %s is in no WP856 template and must not be written", k)
		}
	}
	for _, c := range []struct {
		model string
		on    bool
	}{{"WP822", false}, {"WP836", false}, {"WP856", false}, {"WP820", true}, {"WP820", false}, {"GRP2614", true}} {
		out := render(c.model, c.on)
		for _, k := range all {
			if strings.Contains(out, k) {
				t.Errorf("%s (RTCP-XR %v): %s must not be written", c.model, c.on, k)
			}
		}
	}
}
