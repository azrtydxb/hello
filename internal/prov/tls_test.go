package prov

import (
	"context"
	"strings"
	"testing"
)

// TestTLSTransportTemplates fails if a built-in template does not follow
// Server.Transport: TLS must write the vendor's TLS transport (Grandstream
// P130 = 2 with P2329 and the TLS version keys, Yealink transport_type 2,
// Poly TLS, Fanvil 2) and UDP must keep writing the UDP values and none of
// the TLS-only keys (phone-feature-parity S-14).
func TestTLSTransportTemplates(t *testing.T) {
	cases := []struct {
		v         Vendor
		model     string
		file      string
		tlsWant   []string
		udpWant   []string
		tlsAbsent []string
		udpAbsent []string
	}{
		{Grandstream, "WP826", "cfg805ec0aabbcc.xml",
			[]string{"<P130>2</P130>", "<P2329>1</P2329>", "<P22293>12</P22293>", "<P22294>99</P22294>"},
			[]string{"<P130>0</P130>"},
			[]string{"<P130>0</P130>"}, []string{"<P2329>", "<P22293>", "<P22294>", "<P130>2</P130>"}},
		{Yealink, "T54W", "805ec0aabbcc.cfg",
			[]string{"account.1.sip_server.1.transport_type = 2"},
			[]string{"account.1.sip_server.1.transport_type = 0"},
			[]string{"transport_type = 0"}, []string{"transport_type = 2"}},
		{Poly, "VVX 450", "805ec0aabbcc-hello.cfg",
			[]string{`reg.1.server.1.transport="TLS"`},
			[]string{`reg.1.server.1.transport="UDPOnly"`},
			[]string{"UDPOnly"}, []string{`transport="TLS"`}},
		{Fanvil, "X5U", "805ec0aabbcc.cfg",
			[]string{"SIP1 Transport     :2"},
			[]string{"SIP1 Transport     :0"},
			[]string{"SIP1 Transport     :0"}, []string{"SIP1 Transport     :2"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.v), func(t *testing.T) {
			for _, transport := range []string{"tls", "udp"} {
				d := renderSample(false)
				d.Phone.Vendor, d.Phone.Model = tc.v, tc.model
				d.Server.Transport = transport
				want, absent := tc.tlsWant, tc.tlsAbsent
				if transport == "udp" {
					want, absent = tc.udpWant, tc.udpAbsent
				}
				tmpl := builtinFor(t, tc.v)
				files := []string{tc.file}
				var all strings.Builder
				for _, f := range files {
					out, err := Render(context.Background(), tmpl, f, d)
					if err != nil {
						t.Fatalf("%s %s: %v", tc.v, f, err)
					}
					all.Write(out)
				}
				got := all.String()
				for _, w := range want {
					if !strings.Contains(got, w) {
						t.Errorf("%s over %s: rendered config lacks %q", tc.v, transport, w)
					}
				}
				for _, a := range absent {
					if strings.Contains(got, a) {
						t.Errorf("%s over %s: rendered config carries %q", tc.v, transport, a)
					}
				}
			}
		})
	}
}

// TestTLSModelMatch fails if HELLO_PROV_SIP_TLS_MODELS does not select by
// vendor or by vendor and model prefix, or selects a vendor that has no
// transport key (Snom, Generic).
func TestTLSModelMatch(t *testing.T) {
	list := []string{"grandstream:WP8", "yealink", "snom"}
	for _, tc := range []struct {
		v     Vendor
		model string
		want  bool
	}{
		{Grandstream, "WP826", true},
		{Grandstream, "wp836", true},
		{Grandstream, "WP822", true}, // "WP8" is a prefix of WP822
		{Grandstream, "GRP2614", false},
		{Yealink, "T54W", true},
		{Poly, "VVX 450", false},
		{Snom, "D785", false}, // listed, but no confirmed transport key
		{Generic, "x", false},
	} {
		got := TLSModelMatch(list, tc.v, tc.model)
		if got != tc.want {
			t.Errorf("TLSModelMatch(%v, %s, %q) = %v, want %v", list, tc.v, tc.model, got, tc.want)
		}
	}
	if TLSModelMatch(nil, Grandstream, "WP826") {
		t.Error("an empty list selected a model")
	}
}
