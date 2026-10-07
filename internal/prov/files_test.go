package prov

import (
	"strings"
	"testing"
	"time"
)

// TestVendorFileSets fails if a first-class vendor's documented request
// path (MAC case included) does not resolve to the right file kind, a name
// embedding another MAC resolves, or a path of one vendor resolves for a
// phone of another (spec S-7).
func TestVendorFileSets(t *testing.T) {
	const mac = "805ec0aabbcc"
	const other = "805ec0ddeeff"
	upper := strings.ToUpper(mac)
	for _, tc := range []struct {
		v     Vendor
		model string
		name  string
		kind  FileKind // "" means: not in the file set
	}{
		// Yealink: common y0000000000XX.cfg by model ID, per-MAC .cfg.
		{Yealink, "T54W", "y000000000096.cfg", KindCommon},
		{Yealink, "SIP-T46U", "y000000000108.cfg", KindCommon},
		{Yealink, "T54W", "y000000000108.cfg", ""}, // another model's common file
		{Yealink, "T99X", "y000000000096.cfg", ""}, // unknown model ID
		{Yealink, "T54W", mac + ".cfg", KindDevice},
		{Yealink, "T54W", upper + ".cfg", KindDevice},
		{Yealink, "T54W", mac + "-local.cfg", KindOther},
		{Yealink, "T54W", mac + "-contact.xml", KindOther},
		{Yealink, "T54W", "y000000000000.boot", KindOther},
		{Yealink, "T54W", mac + ".boot", KindOther},
		{Yealink, "T54W", other + ".cfg", ""},
		// Poly: per-MAC master, the 000000000000 fallback, Hello's device
		// file, and the uploads.
		{Poly, "VVX 450", mac + ".cfg", KindMaster},
		{Poly, "VVX 450", "000000000000.cfg", KindMaster},
		{Poly, "VVX 450", mac + "-hello.cfg", KindDevice},
		{Poly, "VVX 450", mac + "-phone.cfg", KindUpload},
		{Poly, "VVX 450", mac + "-web.cfg", KindUpload},
		{Poly, "VVX 450", mac + "-app.log", KindUpload},
		{Poly, "VVX 450", mac + "-boot.log", KindUpload},
		{Poly, "VVX 450", mac + "-directory.xml", KindUpload},
		{Poly, "VVX 450", other + "-hello.cfg", ""},
		// Grandstream: cfg<mac>, .bin, .xml, cfg<model>.xml, cfg.xml.
		{Grandstream, "GRP2614", "cfg" + mac, KindOther},
		{Grandstream, "GRP2614", "cfg" + mac + ".bin", KindOther},
		{Grandstream, "GRP2614", "cfg" + mac + ".xml", KindDevice},
		{Grandstream, "GRP2614", "cfgGRP2614.xml", KindCommon},
		{Grandstream, "GRP2614", "cfg.xml", KindCommon},
		{Grandstream, "GRP2614", "cfg" + other + ".xml", ""},
		// Snom: the {mac} URL (uppercase), snom<model>.htm,
		// snom<model>-<MAC>.htm, the firmware .htm.
		{Snom, "D785", upper, KindDevice},
		{Snom, "D785", "snomD785.htm", KindCommon},
		{Snom, "D785", "snomD785-" + upper + ".htm", KindDevice},
		{Snom, "D785", "snomD785-firmware.htm", KindOther},
		{Snom, "D785", "snomD735.htm", ""},
		{Snom, "D785", strings.ToUpper(other), ""},
		// Fanvil: the model common file, per-MAC .cfg in either case.
		{Fanvil, "X6", "F0V00X600000.cfg", KindCommon},
		{Fanvil, "X5U", "F0V00X5U0000.cfg", KindCommon},
		{Fanvil, "X5U", mac + ".cfg", KindDevice},
		{Fanvil, "X5U", upper + ".cfg", KindDevice},
		{Fanvil, "X5U", other + ".cfg", ""},
		// Cross-vendor negatives.
		{Yealink, "T54W", "cfg" + mac + ".xml", ""},
		{Yealink, "T54W", "snomD785.htm", ""},
		{Yealink, "T54W", "000000000000.cfg", ""},
		{Poly, "VVX 450", "y000000000096.cfg", ""},
		{Grandstream, "GRP2614", mac + ".cfg", ""},
		{Snom, "D785", mac + ".cfg", ""},
		{Fanvil, "X5U", mac + "-hello.cfg", ""},
		{Fanvil, "X5U", "y000000000096.cfg", ""},
		// Hostile names.
		{Yealink, "T54W", "../" + mac + ".cfg", ""},
		{Yealink, "T54W", "", ""},
		{Generic, "Any", "a/b", ""},
		// Generic: anything plain; a MAC must be the phone's.
		{Generic, "Any", "phone-" + mac + ".xml", KindDevice},
		{Generic, "Any", "site.xml", KindCommon},
		{Generic, "Any", "phone-" + other + ".xml", ""},
	} {
		kind, ok := MatchFile(tc.v, tc.model, mac, tc.name)
		if want := tc.kind != ""; ok != want || (ok && kind != tc.kind) {
			t.Errorf("MatchFile(%s, %q, %q) = %s, %v; want %q", tc.v, tc.model, tc.name, kind, ok, tc.kind)
		}
	}
}

func TestNormalizeMAC(t *testing.T) {
	for in, want := range map[string]string{
		"805EC0AABBCC": "805ec0aabbcc", "80:5e:c0:aa:bb:cc": "805ec0aabbcc",
		"80-5E-C0-AA-BB-CC": "805ec0aabbcc", "805e.c0aa.bbcc": "805ec0aabbcc",
		"80:5e:c0:aa:bb": "", "805ec0aabbcg": "", "80:5e-c0:aa:bb:cc": "", "8:05e:c0:aa:bb:cc": "", "": "",
	} {
		got, err := NormalizeMAC(in)
		if (err != nil) != (want == "") || got != want {
			t.Errorf("NormalizeMAC(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestRedactPath(t *testing.T) {
	tok, _ := NewToken()
	for in, want := range map[string]string{
		"/p/" + tok + "/y000000000096.cfg":    "/p/****/y000000000096.cfg",
		"/p/" + tok:                           "/p/****",
		"/p/" + tok + "/fw/a.rom":             "/p/****/fw/a.rom",
		"/p/" + strings.ToUpper(tok) + "/x":   "/p/****/x",
		"/p/nearmiss/x":                       "/p/****/x",
		"/p/boot/cfg.xml":                     "/p/boot/cfg.xml",
		"/p/ca.crt":                           "/p/ca.crt",
		"/other/" + tok:                       "/other/****",
		"/p/boot/" + tok:                      "/p/boot/****",
		"/p/boot/1" + strings.Repeat("a", 60): "/p/boot/1****",
	} {
		if got := RedactPath(in); got != want {
			t.Errorf("RedactPath(%q) = %q; want %q", in, got, want)
		}
	}
}

func TestNewToken(t *testing.T) {
	a, ha := NewToken()
	b, _ := NewToken()
	if len(a) != TokenLen || !tokenRE.MatchString(a) || a == b {
		t.Fatalf("tokens %q, %q", a, b)
	}
	if string(ha) != string(HashToken(a)) || len(ha) != 32 {
		t.Fatal("hash is not the token's SHA-256")
	}
}

func TestResyncSecondsJitter(t *testing.T) {
	a, b := ResyncSeconds("805ec0aabbcc", 24*time.Hour), ResyncSeconds("805ec0ddeeff", 24*time.Hour)
	if a < 86400 || a >= 86400+8640 || a != ResyncSeconds("805ec0aabbcc", 24*time.Hour) || a == b {
		t.Fatalf("resync %d, %d", a, b)
	}
}
