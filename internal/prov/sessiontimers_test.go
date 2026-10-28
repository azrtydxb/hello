package prov

import (
	"os"
	"strings"
	"testing"
)

// TestSupportsSessionTimers fails if a first-class vendor's phone stops
// being asked for RFC 4028 timers, or an unidentified phone starts to be.
func TestSupportsSessionTimers(t *testing.T) {
	for _, c := range []struct {
		v     Vendor
		model string
		want  bool
	}{
		{Grandstream, "WP822", true}, {Grandstream, "WP836", true}, {Grandstream, "GRP2614", true},
		{Yealink, "T54W", true}, {Poly, "VVX 450", true}, {Snom, "D785", true}, {Fanvil, "X6U", true},
		{Generic, "", false}, {"", "", false},
	} {
		if got := SupportsSessionTimers(c.v, c.model); got != c.want {
			t.Errorf("SupportsSessionTimers(%q, %q) = %v, want %v", c.v, c.model, got, c.want)
		}
	}
}

// TestGrandstreamKeepAliveKeys fails if the WP8x6 template stops enabling the
// OPTIONS keep-alive (P2397), the WP822 template its RFC 5626 setting
// (P84989), or if either leaks into the other family.
func TestGrandstreamKeepAliveKeys(t *testing.T) {
	for family, c := range map[string]struct{ file, has, not string }{
		"8x6": {"testdata/grandstream_wp8x6.xml", "<P2397>1</P2397>", "<P84989>"},
		"8xx": {"testdata/grandstream_wp8xx.xml", "<P84989>0</P84989>", "<P2397>"},
	} {
		raw, err := os.ReadFile(c.file)
		if err != nil {
			t.Fatal(err)
		}
		b := string(raw)
		if !strings.Contains(b, c.has) {
			t.Errorf("%s: %s is missing", family, c.has)
		}
		if strings.Contains(b, c.not) {
			t.Errorf("%s: %s must not be written", family, c.not)
		}
	}
}
