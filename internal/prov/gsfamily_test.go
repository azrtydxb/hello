package prov

import (
	"bytes"
	"context"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite the golden files")

func TestGrandstreamFamily(t *testing.T) {
	for model, want := range map[string]string{
		"WP816": FamilyWP8x6, "wp826": FamilyWP8x6, "WP 836": FamilyWP8x6,
		"WP856": FamilyWP856,
		"WP810": FamilyWP8xx, "WP820": FamilyWP8xx, "WP822": FamilyWP8xx, "WP825": FamilyWP8xx,
		"GRP2614": "", "": "", "WP899": "",
	} {
		if got := GrandstreamFamily(model); got != want {
			t.Errorf("GrandstreamFamily(%q) = %q, want %q", model, got, want)
		}
	}
}

func TestSupportsBLF(t *testing.T) {
	for _, c := range []struct {
		v     Vendor
		model string
		want  bool
	}{
		{Grandstream, "WP822", false}, {Grandstream, "WP856", false}, {Grandstream, "WP836", false},
		{Grandstream, "GRP2614", true}, {Yealink, "T54W", true}, {Poly, "VVX 450", true},
	} {
		if got := SupportsBLF(c.v, c.model); got != c.want {
			t.Errorf("SupportsBLF(%s, %s) = %v, want %v", c.v, c.model, got, c.want)
		}
	}
}

func TestBookRefreshFor(t *testing.T) {
	for _, c := range []struct {
		family string
		in     int
		want   int
	}{
		{FamilyWP856, 0, 0}, {FamilyWP856, 1, 5}, {FamilyWP856, 15, 30}, {FamilyWP856, 30, 30}, {FamilyWP856, 61, 120},
		{FamilyWP856, 720, 720}, {FamilyWP856, 5000, 720},
		{FamilyWP8x6, 15, 15}, {FamilyWP8x6, 2, 5}, {FamilyWP8x6, 9999, 720},
		{FamilyWP8xx, 0, 0}, {FamilyWP8xx, 15, 15}, {"", 15, 15},
	} {
		if got := BookRefreshFor(c.family, c.in); got != c.want {
			t.Errorf("BookRefreshFor(%q, %d) = %d, want %d", c.family, c.in, got, c.want)
		}
	}
}

func TestAddEmergencyNumber(t *testing.T) {
	var n []string
	for _, m := range []string{"112", " 911 ", "112", "^0[0-9]+$", "", "9-1-1", "*67"} {
		n = AddEmergencyNumber(n, m)
	}
	if got := strings.Join(n, ","); got != "112,911,*67" {
		t.Errorf("got %q", got)
	}
}

// gsFamilyData is a full sample (BLF keys, CA, firmware, book, multicast,
// PTT) for model, with emergency numbers and park codes set.
func gsFamilyData(model string) RenderData {
	d := SampleData(true)
	d.Phone.Vendor, d.Phone.Model = Grandstream, model
	d.Phone.PTTCapable = SupportsPTT(Grandstream, model)
	if !d.Phone.PTTCapable {
		d.PTT = nil // the store sets it only for a phone that supports it
	}
	d.Emergency = "112,911"
	d.Line.ParkCode, d.Line.ParkRetrieveCode = "*85", "*86"
	return d
}

// TestGrandstreamFamilyGolden pins the rendered device file of each model
// family and checks the P-values the family's official template does not
// define are absent, and the ones it needs are present.
func TestGrandstreamFamilyGolden(t *testing.T) {
	cases := []struct {
		name, model string
		want, not   []string
	}{
		{"wp8x6", "WP836",
			[]string{"<P298>1</P298>", "<P194>1</P194>", "<P8433>", "<P8385>112,911</P8385>", "<P60069>*85</P60069>", "<P2336>*86</P2336>", "<P333>1</P333>", "<P4428>0</P4428>", "<P22177>1</P22177>", "<P22556>1</P22556>", "<P82317>0</P82317>"},
			[]string{"<P323>", "<P324>", "<P325>", "<P90>", "<P22296>", "<P25675>"}},
		{"wp856", "WP856",
			[]string{"<P90>2</P90>", "<P22296>1</P22296>", "<P25675>112,911</P25675>", "<P60069>*85</P60069>", "<P26072>1</P26072>", "<P332>30</P332>", "<P4428>0</P4428>", "<P22177>1</P22177>", "<P82317>0</P82317>"},
			[]string{"<P323>", "<P298>", "<P194>", "<P8433>", "<P8385>", "<P333>", "<P22556>", "<P8381>", "<P22532>"}},
		{"wp8xx", "WP822",
			[]string{"<P298>1</P298>", "<P194>1</P194>", "<P333>1</P333>", "<P332>15</P332>", "<P4428>0</P4428>", "<P22177>1</P22177>", "<P8381>"},
			[]string{"<P323>", "<P90>", "<P22296>", "<P8433>", "<P8385>", "<P25675>", "<P60069>", "<P2336>", "<P22556>", "<P82317>", "<P26072>"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := gsFamilyData(c.model)
			got, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
			if err != nil {
				t.Fatal(err)
			}
			for _, w := range c.want {
				if !bytes.Contains(got, []byte(w)) {
					t.Errorf("%s: missing %s", c.model, w)
				}
			}
			for _, w := range c.not {
				if bytes.Contains(got, []byte(w)) {
					t.Errorf("%s: carries %s", c.model, w)
				}
			}
			path := filepath.Join("testdata", "grandstream_"+c.name+".xml")
			if *updateGolden {
				if err := os.WriteFile(path, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from %s (run with -update)\n%s", c.model, path, got)
			}
		})
	}
}

// TestGrandstreamGenericKeepsBLF: a Grandstream phone that is not a WP
// keeps the BLF block.
func TestGrandstreamGenericKeepsBLF(t *testing.T) {
	d := gsFamilyData("GRP2614")
	got, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(got, []byte("<P323>1</P323>")) {
		t.Errorf("BLF block missing:\n%s", got)
	}
}
