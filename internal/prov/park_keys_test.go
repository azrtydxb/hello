package prov

import (
	"context"
	"strings"
	"testing"
)

// TestProvisionedParkPickupKeys fails if the Grandstream WP8x6 template does
// not write the Park and Park Retrieve codes the softkeys use (P60069,
// P2336), writes them for a phone without those softkeys or without codes
// configured, or if the Yealink and Poly templates omit the pickup codes.
func TestProvisionedParkPickupKeys(t *testing.T) {
	render := func(v Vendor, model, file string, edit func(*RenderData)) string {
		t.Helper()
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = v, model
		d.Phone.PTTCapable = SupportsPTT(v, model)
		d.Line.ParkCode, d.Line.ParkRetrieveCode = "*74", "*75"
		d.Line.PickupGroupCode, d.Line.PickupDirectedCode = "*40", "*41"
		if edit != nil {
			edit(&d)
		}
		b, err := Render(context.Background(), builtinFor(t, v), file, d)
		if err != nil {
			t.Fatalf("%s %s: %v", v, model, err)
		}
		return string(b)
	}
	has := func(what, out string, want ...string) {
		t.Helper()
		for _, w := range want {
			if !strings.Contains(out, w) {
				t.Errorf("%s lacks %q", what, w)
			}
		}
	}
	lacks := func(what, out string, not ...string) {
		t.Helper()
		for _, w := range not {
			if strings.Contains(out, w) {
				t.Errorf("%s carries %q", what, w)
			}
		}
	}

	wp836 := render(Grandstream, "WP836", "cfg805ec0aabbcc.xml", nil)
	has("WP836", wp836, "<P60069>*74</P60069>", "<P2336>*75</P2336>")
	lacks("WP822 (no PTT softkeys)", render(Grandstream, "WP822", "cfg805ec0aabbcc.xml", nil), "<P60069>", "<P2336>")
	lacks("WP836 without codes", render(Grandstream, "WP836", "cfg805ec0aabbcc.xml", func(d *RenderData) {
		d.Line.ParkCode, d.Line.ParkRetrieveCode = "", ""
	}), "<P60069>", "<P2336>")

	has("Yealink", render(Yealink, "T54W", "805ec0aabbcc.cfg", nil),
		"features.pickup.group_pickup_code = *40", "features.pickup.direct_pickup_code = *41")
	lacks("Yealink without codes", render(Yealink, "T54W", "805ec0aabbcc.cfg", func(d *RenderData) {
		d.Line.PickupGroupCode, d.Line.PickupDirectedCode = "", ""
	}), "features.pickup")
	has("Poly", render(Poly, "VVX 450", "805ec0aabbcc-hello.cfg", nil),
		`call.directedCallPickupString="*41"`, `call.groupCallPickupString="*40"`)
}
