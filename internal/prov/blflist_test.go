package prov

import (
	"bytes"
	"context"
	"testing"
)

// TestBLFListURI fails if a template cannot name the phone's resource-list
// URI (S-35), if the URI is set for a phone without BLF keys, or if a
// built-in WP template switches on PUBLISH (P188) or SUBSCRIBE for
// Registration, which the WP phones gain nothing from [R8x6 §4].
func TestBLFListURI(t *testing.T) {
	tmpl := Template{Vendor: Generic, Name: "list", Files: []TemplateFile{{Pattern: "{mac}.cfg", ContentType: "text/plain", Body: "list={{.Line.BLFListURI}}"}}}
	d := renderSample(false)
	out, err := Render(context.Background(), tmpl, d.Phone.MAC+".cfg", d)
	if err != nil {
		t.Fatal(err)
	}
	if want := "list=sip:blf-" + d.Line.Username + "@hello.test"; string(out) != want {
		t.Fatalf("got %q, want %q", out, want)
	}
	d.BLF = nil
	out, err = Render(context.Background(), tmpl, d.Phone.MAC+".cfg", d)
	if err != nil || string(out) != "list=" {
		t.Fatalf("a phone without BLF keys got %q, %v", out, err)
	}

	// P188 (PUBLISH for Presence) and P2319 (SUBSCRIBE for Registration) are
	// in the WP8x6 (wp8x6_config_1.0.3.39) and WP810/822/825
	// (wp810_822_825_config_1.0.11.83) templates, not in the WP820 or WP856
	// ones. They are written off (the spec keeps them off), and no list URI
	// key exists for a WP.
	for _, c := range []struct {
		model string
		keys  bool
	}{{"WP836", true}, {"WP816", true}, {"WP822", true}, {"WP856", false}, {"WP820", false}} {
		d = renderSample(false)
		d.Phone.Vendor, d.Phone.Model = Grandstream, c.model
		gs, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
		if err != nil {
			t.Fatal(err)
		}
		for _, k := range []string{"<P188>0</P188>", "<P2319>0</P2319>"} {
			if bytes.Contains(gs, []byte(k)) != c.keys {
				t.Errorf("%s: %s written = %v, want %v", c.model, k, !c.keys, c.keys)
			}
		}
		for _, k := range []string{"<P188>1</P188>", "<P2319>1</P2319>", "<P134>", "<P26084>", "<P2389>"} {
			if bytes.Contains(gs, []byte(k)) {
				t.Errorf("%s writes %s", c.model, k)
			}
		}
	}
}
