package prov

import (
	"context"
	"strings"
	"testing"
)

// TestProvisionedDoorSystems fails if a WP8x6 or WP8xx does not get one door
// system entry per station the phone's extension can be called from (type
// GDS, account 1, name and number, five P-values apart), if the access
// password P-value (the door-open code) is ever written, if a name is not
// escaped, if more than ten stations are written, or if a phone that is not
// one of those families, or a WP with no station, gets any (spec S-49).
func TestProvisionedDoorSystems(t *testing.T) {
	stations := []DoorStation{{Number: "900", Name: "Front <door> & gate"}, {Number: "901"}}
	render := func(v Vendor, model, file string, st []DoorStation) string {
		t.Helper()
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = v, model
		d.Phone.PTTCapable = SupportsPTT(v, model)
		d.DoorStations = st
		b, err := Render(context.Background(), builtinFor(t, v), file, d)
		if err != nil {
			t.Fatalf("%s %s: %v", v, model, err)
		}
		return string(b)
	}
	for _, model := range []string{"WP836", "WP822"} {
		out := render(Grandstream, model, "cfg805ec0aabbcc.xml", stations)
		for _, want := range []string{
			"<P32000>0</P32000>", "<P32001>0</P32001>", "<P32002>Front &lt;door&gt; &amp; gate</P32002>", "<P32003>900</P32003>",
			"<P32005>0</P32005>", "<P32006>0</P32006>", "<P32007>901</P32007>", "<P32008>901</P32008>",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("%s lacks %q", model, want)
			}
		}
		for _, never := range []string{"<P32004>", "<P32009>", "<P32010>"} {
			if strings.Contains(out, never) {
				t.Errorf("%s writes %s: the door-open code or a third entry", model, never)
			}
		}
		if out := render(Grandstream, model, "cfg805ec0aabbcc.xml", nil); strings.Contains(out, "P32000") {
			t.Errorf("%s with no station carries door system entries", model)
		}
	}
	many := make([]DoorStation, 0, MaxDoorStations)
	for i := range MaxDoorStations {
		many = append(many, DoorStation{Number: "90" + string(rune('0'+i)), Name: "D"})
	}
	if out := render(Grandstream, "WP836", "cfg805ec0aabbcc.xml", many); !strings.Contains(out, "<P32048>") || strings.Contains(out, "<P32050>") {
		t.Error("ten stations do not fill exactly the ten entries P32000 to P32048")
	}
	for _, c := range []struct {
		v           Vendor
		model, file string
	}{{Grandstream, "WP856", "cfg805ec0aabbcc.xml"}, {Yealink, "T54W", "805ec0aabbcc.cfg"}, {Poly, "VVX 450", "805ec0aabbcc-hello.cfg"}} {
		if out := render(c.v, c.model, c.file, stations); strings.Contains(out, "P32000") || strings.Contains(out, "P32002") {
			t.Errorf("%s %s carries door system entries", c.v, c.model)
		}
	}
}
