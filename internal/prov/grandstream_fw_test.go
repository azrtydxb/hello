package prov

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestGrandstreamFirmwareDirectory fails if the Grandstream P192 is not the
// firmware directory (the phone appends its own file name), if P6767 does
// not follow the URL scheme, if the prefix or suffix is set, or if the
// phone is not set to check for firmware at boot.
func TestGrandstreamFirmwareDirectory(t *testing.T) {
	d := renderSample(true)
	d.Phone.Vendor, d.Phone.Model = Grandstream, "WP822"
	wantDir := strings.TrimSuffix(d.Prov.URL, "/") + "/fw"
	for _, c := range []struct{ dir, via string }{{wantDir, "2"}, {strings.Replace(wantDir, "https://", "http://", 1), "1"}} {
		d.Firmware.Dir, d.Firmware.Via = c.dir, UpgradeVia(c.dir)
		out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
		if err != nil {
			t.Fatal(err)
		}
		s := string(out)
		for _, w := range []string{"<P192>" + noScheme(c.dir) + "</P192>", "<P6767>" + c.via + "</P6767>", "<P232></P232>", "<P233></P233>", "<P238>0</P238>"} {
			if !strings.Contains(s, w) {
				t.Errorf("missing %q in:\n%s", w, s)
			}
		}
		if strings.Contains(s, "fw/T54W") {
			t.Errorf("full file URL leaked into the Grandstream file:\n%s", s)
		}
	}
}

// TestGrandstreamFirmwareDirectoryFetch fails if the phone's own file name
// appended to the firmware directory does not resolve to the file pinned
// under that name, if the doubled path of the old full-URL setting is
// served, or if the ring tone probes are anything but an empty 404.
func TestGrandstreamFirmwareDirectoryFetch(t *testing.T) {
	e := newEnv(t)
	_, tok := e.s.addPhone(Grandstream, "WP822", "000000000001")
	names := []string{"wp810fw.bin", "wp8x6fw.bin"}
	for _, name := range names {
		key := "grandstream/" + name
		e.s.firmware[key] = Firmware{ID: 1, Vendor: Grandstream, Filename: name, ObjectKey: key, SHA256: strings.Repeat("c", 64), UploadedAt: time.Now()}
		e.o[key] = []byte("img-" + name)
	}
	for _, name := range names {
		w := e.do(req{path: devPath(tok, "fw/"+name)})
		if w.Code != http.StatusOK || w.Body.String() != "img-"+name {
			t.Fatalf("%s: %d %q", name, w.Code, w.Body.String())
		}
		if row := e.last(); row.Kind != KindFirmware || row.Result != ResultServed {
			t.Fatalf("%s: %+v", name, row)
		}
		expectEmpty(t, e, e.do(req{path: devPath(tok, "fw/"+name+"/"+name)}), 404, ResultNotFound)
	}
	for _, name := range []string{"ring1.bin", "ring1.ogg", "ring10.ogg"} {
		expectEmpty(t, e, e.do(req{path: devPath(tok, "fw/"+name)}), 404, ResultNotFound)
	}
}
