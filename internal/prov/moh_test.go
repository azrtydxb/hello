package prov

import (
	"bytes"
	"context"
	"testing"
)

// TestPhonesOriginNoMusicOnHold fails if a built-in vendor template writes a
// music on hold setting. Hello's anchor plays the music (S-33), so a phone
// that also originates its own (Grandstream P2350 MOH URI, Yealink
// music_server_uri, Poly musicOnHold, Fanvil/Snom MOH) would stack two
// sources on one hold, or send the held party a second call.
func TestPhonesOriginNoMusicOnHold(t *testing.T) {
	d := renderSample(false)
	for _, tc := range []struct {
		v    Vendor
		file string
	}{
		{Grandstream, "cfg" + d.Phone.MAC + ".xml"},
		{Yealink, d.Phone.MAC + ".cfg"},
		{Poly, d.Phone.MAC + "-hello.cfg"},
		{Snom, d.Phone.MACUpper},
		{Fanvil, d.Phone.MAC + ".cfg"},
	} {
		out, err := Render(context.Background(), builtinFor(t, tc.v), tc.file, d)
		if err != nil {
			t.Fatalf("%s: %v", tc.v, err)
		}
		low := bytes.ToLower(out)
		for _, banned := range []string{"p2350", "music_server", "musiconhold", "moh"} {
			if bytes.Contains(low, []byte(banned)) {
				t.Errorf("%s template mentions %q: the phone must not originate its own music on hold\n%s", tc.v, banned, out)
			}
		}
	}
}
