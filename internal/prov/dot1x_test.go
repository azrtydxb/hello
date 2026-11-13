package prov

import (
	"bytes"
	"context"
	"encoding/pem"
	"strings"
	"testing"
)

func renderGS(t *testing.T, d RenderData) string {
	t.Helper()
	got, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg"+d.Phone.MAC+".xml", d)
	if err != nil {
		t.Fatal(err)
	}
	return string(got)
}

// TestDot1XProvisioning fails if a SCEP-capable Grandstream model with SCEP
// on does not get P8647 = 1, its own SCEP URL, its challenge and the MAC as
// applicant, if one with SCEP off does not get P8647 = 0, if a model whose
// template has no SCEP keys gets any, if a GRP26xx with a wired profile does
// not get P7901 to P7903 and P8439, if a WP handset gets wired keys, if an
// EAP-TLS profile is written to a phone without SCEP, or if a phone whose
// group names no wired profile gets P7901 at all (Hello must not switch off
// 802.1X a port depends on).
func TestDot1XProvisioning(t *testing.T) {
	scepInfo := &SCEPInfo{URL: "https://prov.hello.example/p/sample/scep", Challenge: "c4allenge7", KeyBits: 2048}
	caPEM := strings.TrimSpace(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: selfSigned(t, newKey(t), "radius-ca").Raw})))

	for _, tc := range []struct {
		model string
		scep  *SCEPInfo
		want  []string
		not   []string
	}{
		{"WP836", scepInfo, []string{"<P8647>1</P8647>", "<P8648>https://prov.hello.example/p/sample/scep</P8648>", "<P8653>c4allenge7</P8653>", "<P8654>1</P8654>", "<P8655>0015651234ab</P8655>"}, []string{"<P8652>", "<P7901>"}},
		{"WP836", nil, []string{"<P8647>0</P8647>"}, []string{"<P8648>", "<P8653>"}},
		{"GRP2602", scepInfo, []string{"<P8647>1</P8647>", "<P8653>c4allenge7</P8653>"}, nil},
		{"WP822", scepInfo, nil, []string{"<P8647>", "<P8653>", "c4allenge7"}},
		{"WP856", scepInfo, nil, []string{"<P8647>", "c4allenge7"}},
		{"GRP2614", scepInfo, nil, []string{"<P8647>", "c4allenge7"}},
	} {
		d := gsFamilyData(tc.model)
		d.SCEP = tc.scep
		got := renderGS(t, d)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s (scep %v): missing %s", tc.model, tc.scep != nil, w)
			}
		}
		for _, w := range tc.not {
			if strings.Contains(got, w) {
				t.Errorf("%s (scep %v): carries %s", tc.model, tc.scep != nil, w)
			}
		}
	}

	peap := NewDot1XProfile(7, "Office LAN", Dot1XPEAP, "phone-{mac}", "", "pw-secret", caPEM, "radius.example", "0015651234ab", false)
	tls := NewDot1XProfile(8, "Cert LAN", Dot1XEAPTLS, "{MAC}", "", "", "", "", "0015651234ab", false)
	md5 := NewDot1XProfile(9, "Old LAN", Dot1XEAPMD5, "{mac}", "", "md5-secret", "", "", "0015651234ab", true)
	for _, tc := range []struct {
		name, model string
		wired       *Dot1XProfile
		want, not   []string
	}{
		{"peap on a GRP", "GRP2614", &peap, []string{"<P7901>3</P7901>", "<P7902>phone-0015651234ab</P7902>", "<P7903>pw-secret</P7903>", "<P8439>-----BEGIN CERTIFICATE-----"}, nil},
		{"md5 on a GRP", "GRP2614", &md5, []string{"<P7901>1</P7901>", "<P7902>0015651234ab</P7902>", "<P7903>md5-secret</P7903>"}, []string{"<P8439>"}},
		{"eap-tls without scep", "GRP2614", &tls, nil, []string{"<P7901>", "<P7902>"}},
		{"no wired port on a WP", "WP836", &peap, nil, []string{"<P7901>", "pw-secret"}},
		{"no profile", "GRP2614", nil, nil, []string{"<P7901>", "<P7902>", "<P7903>"}},
	} {
		d := gsFamilyData(tc.model)
		d.Dot1X = &Dot1XInfo{Wired: tc.wired, Profiles: []Dot1XProfile{peap, tls, md5}}
		got := renderGS(t, d)
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%s: missing %s", tc.name, w)
			}
		}
		for _, w := range tc.not {
			if strings.Contains(got, w) {
				t.Errorf("%s: carries %s", tc.name, w)
			}
		}
	}

	// EAP-TLS with SCEP on the phone: mode 2 and no password key.
	tlsOK := NewDot1XProfile(8, "Cert LAN", Dot1XEAPTLS, "{MAC}", "", "", "", "", "0015651234ab", true)
	d := gsFamilyData("GRP2602")
	d.SCEP, d.Dot1X = scepInfo, &Dot1XInfo{Wired: &tlsOK, Profiles: []Dot1XProfile{tlsOK}}
	got := renderGS(t, d)
	if !strings.Contains(got, "<P7901>2</P7901>") || !strings.Contains(got, "<P7902>0015651234AB</P7902>") || strings.Contains(got, "<P7903>") {
		t.Errorf("eap-tls with scep: wired keys wrong")
	}
	// A Yealink never gets Grandstream keys from the same data.
	d.Phone.Vendor = Yealink
	if out, err := Render(context.Background(), builtinFor(t, Yealink), d.Phone.MAC+".cfg", d); err != nil || bytes.Contains(out, []byte("c4allenge7")) {
		t.Errorf("yealink: %v", err)
	}
}

// TestDot1XProfileValues fails if a method maps to the wrong Grandstream
// wired mode or WP8x6 EAP method, the CA type is "skip verification", or
// the identity placeholders are not filled in.
func TestDot1XProfileValues(t *testing.T) {
	for _, tc := range []struct {
		method          string
		wired, wifi, ca int
	}{{Dot1XEAPTLS, 2, 1, 1}, {Dot1XPEAP, 3, 2, 1}, {Dot1XEAPMD5, 1, 0, 1}} {
		p := NewDot1XProfile(1, "x", tc.method, "a-{mac}-{MAC}", "anon@{mac}", "", "", "", "c074ad112233", true)
		if p.GSWiredMode != tc.wired || p.GSWifiEAP != tc.wifi || p.GSWifiCAType != tc.ca {
			t.Errorf("%s: wired %d wifi %d ca %d", tc.method, p.GSWiredMode, p.GSWifiEAP, p.GSWifiCAType)
		}
		if p.Identity != "a-c074ad112233-C074AD112233" || p.AnonymousIdentity != "anon@c074ad112233" {
			t.Errorf("%s: identity %q anonymous %q", tc.method, p.Identity, p.AnonymousIdentity)
		}
	}
	if p := NewDot1XProfile(1, "x", Dot1XPEAP, "i", "", "pw", "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----", "", "c074ad112233", false); p.GSWifiCAType != 3 || !p.Usable {
		t.Errorf("peap with a CA: type %d usable %v", p.GSWifiCAType, p.Usable)
	}
}

// TestValidateDot1X fails if a profile with a missing password for PEAP or
// MD5, a password for EAP-TLS, an unknown method, an identity with spaces or
// too long once {mac} is filled, or a CA that is not PEM certificates is
// accepted, or a valid one is refused.
func TestValidateDot1X(t *testing.T) {
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: selfSigned(t, newKey(t), "ca").Raw}))
	ok := func(name, method, identity, anon, caPEM, domain string, has, set bool) bool {
		return len(ValidateDot1X(name, method, identity, anon, caPEM, domain, has, set)) == 0
	}
	if !ok("LAN", Dot1XPEAP, "{mac}", "anonymous", ca, "radius.example", true, true) || !ok("TLS", Dot1XEAPTLS, "{mac}", "", "", "", false, false) {
		t.Fatal("a valid profile is refused")
	}
	for name, bad := range map[string]bool{
		"peap without password":   ok("LAN", Dot1XPEAP, "{mac}", "", "", "", false, false),
		"md5 without password":    ok("LAN", Dot1XEAPMD5, "{mac}", "", "", "", false, false),
		"tls with a password":     ok("LAN", Dot1XEAPTLS, "{mac}", "", "", "", true, true),
		"unknown method":          ok("LAN", "leap", "{mac}", "", "", "", true, true),
		"identity with a space":   ok("LAN", Dot1XPEAP, "a b", "", "", "", true, true),
		"identity too long":       ok("LAN", Dot1XPEAP, strings.Repeat("{mac}", 6), "", "", "", true, true),
		"empty name":              ok(" ", Dot1XPEAP, "{mac}", "", "", "", true, true),
		"ca not a certificate":    ok("LAN", Dot1XPEAP, "{mac}", "", "-----BEGIN PRIVATE KEY-----\nAA\n-----END PRIVATE KEY-----\n", "", true, true),
		"ca with trailing junk":   ok("LAN", Dot1XPEAP, "{mac}", "", ca+"junk", "", true, true),
		"domain with markup":      ok("LAN", Dot1XPEAP, "{mac}", "", "", "<x>", true, true),
		"anonymous with a space ": ok("LAN", Dot1XPEAP, "{mac}", "a b", "", "", true, true),
	} {
		if bad {
			t.Errorf("%s: accepted", name)
		}
	}
	if !SupportsSCEP(Grandstream, "WP836") || !SupportsSCEP(Grandstream, "GRP2604P") || SupportsSCEP(Grandstream, "WP822") || SupportsSCEP(Grandstream, "GRP2614") || SupportsSCEP(Yealink, "T54W") {
		t.Error("SupportsSCEP")
	}
	if !SupportsWired8021X(Grandstream, "GRP2614") || SupportsWired8021X(Grandstream, "WP836") || SupportsWired8021X(Yealink, "T54W") {
		t.Error("SupportsWired8021X")
	}
}
