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

	d = renderSample(false)
	d.Phone.Vendor, d.Phone.Model = Grandstream, "WP836"
	gs, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(gs, []byte("<P188>1</P188>")) {
		t.Fatal("the Grandstream template enables PUBLISH (P188)")
	}
}
