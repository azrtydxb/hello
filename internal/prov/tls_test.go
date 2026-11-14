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

// TestTCPTransportTemplates fails if a built-in template does not write the
// TCP transport for Server.Transport "tcp" (Grandstream P130 = 1 without
// the TLS-only keys, Yealink 1, Poly TCPOnly, Fanvil 1; S-13).
func TestTCPTransportTemplates(t *testing.T) {
	cases := []struct {
		v           Vendor
		model, file string
		want        []string
		absent      []string
	}{
		{Grandstream, "WP826", "cfg805ec0aabbcc.xml", []string{"<P130>1</P130>"}, []string{"<P130>0</P130>", "<P130>2</P130>", "<P2329>", "<P22293>"}},
		// P130 is the same key, 1 = TCP, in the WP8x6, WP810/822/825, WP820 and WP856 templates.
		{Grandstream, "WP822", "cfg805ec0aabbcc.xml", []string{"<P130>1</P130>"}, []string{"<P130>0</P130>", "<P130>2</P130>", "<P2329>", "<P22293>"}},
		{Grandstream, "WP856", "cfg805ec0aabbcc.xml", []string{"<P130>1</P130>"}, []string{"<P130>0</P130>", "<P130>2</P130>", "<P2329>", "<P22293>"}},
		{Grandstream, "WP820", "cfg805ec0aabbcc.xml", []string{"<P130>1</P130>"}, []string{"<P130>0</P130>", "<P130>2</P130>", "<P2329>", "<P22293>"}},
		{Yealink, "T54W", "805ec0aabbcc.cfg", []string{"account.1.sip_server.1.transport_type = 1"}, []string{"transport_type = 0", "transport_type = 2"}},
		{Poly, "VVX 450", "805ec0aabbcc-hello.cfg", []string{`reg.1.server.1.transport="TCPOnly"`}, []string{"UDPOnly", `"TLS"`}},
		{Fanvil, "X5U", "805ec0aabbcc.cfg", []string{"SIP1 Transport     :1"}, []string{"SIP1 Transport     :0", "SIP1 Transport     :2"}},
	}
	for _, tc := range cases {
		t.Run(string(tc.v)+"/"+tc.model, func(t *testing.T) {
			d := renderSample(false)
			d.Phone.Vendor, d.Phone.Model = tc.v, tc.model
			d.Server.Transport = "tcp"
			out, err := Render(context.Background(), builtinFor(t, tc.v), tc.file, d)
			if err != nil {
				t.Fatal(err)
			}
			got := string(out)
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("lacks %q", w)
				}
			}
			for _, a := range tc.absent {
				if strings.Contains(got, a) {
					t.Errorf("carries %q", a)
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

// TestGrandstreamSRTPMode fails if the Grandstream SRTP Mode (P183) does not
// follow the device policy on a TLS phone, or is written for a phone on UDP
// (where Hello offers no crypto and the phone keeps its own setting), or if
// another vendor's template writes an SRTP key (none is confirmed from vendor
// documentation).
func TestGrandstreamSRTPMode(t *testing.T) {
	// P183 takes 0, 1 and 2 in the WP8x6, WP810/822/825, WP820 and WP856
	// templates; P60078 (SRTP Key Exchange, 0 = SDES) exists in the WP8x6 and
	// WP856 templates only.
	for _, m := range []struct {
		model  string
		sdesKX bool
	}{{"WP836", true}, {"WP856", true}, {"WP822", false}, {"WP820", false}} {
		for _, tc := range []struct{ policy, want string }{
			{"off", "<P183>0</P183>"}, {"optional", "<P183>1</P183>"}, {"required", "<P183>2</P183>"},
		} {
			d := renderSample(false)
			d.Phone.Vendor, d.Phone.Model = Grandstream, m.model
			d.Server.Transport, d.Server.SRTP = "tls", tc.policy
			out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg805ec0aabbcc.xml", d)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), tc.want) {
				t.Errorf("%s policy %s: rendered config lacks %s", m.model, tc.policy, tc.want)
			}
			if got := strings.Contains(string(out), "<P60078>0</P60078>"); got != m.sdesKX {
				t.Errorf("%s policy %s: P60078 = 0 written %v, want %v", m.model, tc.policy, got, m.sdesKX)
			}
		}
	}
	d := renderSample(false)
	d.Phone.Vendor, d.Phone.Model = Grandstream, "WP826"
	out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg805ec0aabbcc.xml", d)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "<P183>") || strings.Contains(string(out), "<P60078>") {
		t.Error("P183 or P60078 is written for a phone on UDP")
	}
	for _, v := range []Vendor{Yealink, Poly, Fanvil} {
		d := renderSample(false)
		d.Phone.Vendor = v
		d.Server.Transport, d.Server.SRTP = "tls", "required"
		files := map[Vendor]string{Yealink: "805ec0aabbcc.cfg", Poly: "805ec0aabbcc-hello.cfg", Fanvil: "805ec0aabbcc.cfg"}
		models := map[Vendor]string{Yealink: "T54W", Poly: "VVX 450", Fanvil: "X5U"}
		d.Phone.Model = models[v]
		out, err := Render(context.Background(), builtinFor(t, v), files[v], d)
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.ToLower(string(out)); strings.Contains(s, "srtp") {
			t.Errorf("%s template writes an SRTP key that is not confirmed from vendor documentation", v)
		}
	}
}

// TestGrandstreamDTLSAndICE fails if the Grandstream SRTP Key Exchange
// (P60078: 0 SDES, 1 DTLS) is not written with P183 for a TLS phone of a model
// that has it, if the Media NAT Traversal (P95002: 4 ICE, 1 Auto) does not
// follow the device's ICE setting, if either key is written for a model whose
// vendor template does not list it, or if another vendor's template writes an
// ICE or DTLS key (none is confirmed from vendor documentation). The keys and
// values are from Grandstream's WP8x6 config templates (wp8x6_config 1.0.3.39:
// P60078, P95002; wp856_config 1.0.3.16: P60078).
func TestGrandstreamDTLSAndICE(t *testing.T) {
	render := func(model string, edit func(*RenderData)) string {
		d := renderSample(false)
		d.Phone.Vendor, d.Phone.Model = Grandstream, model
		edit(&d)
		out, err := Render(context.Background(), builtinFor(t, Grandstream), "cfg805ec0aabbcc.xml", d)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	for _, tc := range []struct {
		name  string
		model string
		keyex string
		ice   string
		want  []string
		deny  []string
	}{
		{"WP826 dtls and ice", "WP826", "dtls", "ice", []string{"<P60078>1</P60078>", "<P95002>4</P95002>"}, nil},
		{"WP836 sdes and auto", "WP836", "sdes", "auto", []string{"<P60078>0</P60078>", "<P95002>1</P95002>"}, nil},
		{"WP856 has the key exchange only", "WP856", "dtls", "", []string{"<P60078>1</P60078>"}, []string{"<P95002>"}},
		{"WP822 has neither key", "WP822", "", "", nil, []string{"<P60078>", "<P95002>"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := render(tc.model, func(d *RenderData) {
				d.Server.Transport, d.Server.SRTP = "tls", "optional"
				d.Server.SRTPKeyEx, d.Server.ICE = tc.keyex, tc.ice
			})
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("rendered config lacks %s", w)
				}
			}
			for _, w := range tc.deny {
				if strings.Contains(out, w) {
					t.Errorf("rendered config has %s", w)
				}
			}
		})
	}
	t.Run("a phone on UDP gets no key exchange but keeps its ICE setting", func(t *testing.T) {
		out := render("WP826", func(d *RenderData) { d.Server.ICE = "ice" })
		if strings.Contains(out, "<P60078>") {
			t.Error("P60078 is written for a phone on UDP")
		}
		if !strings.Contains(out, "<P95002>4</P95002>") {
			t.Error("ICE is not provisioned on UDP")
		}
	})
	for _, c := range []struct {
		v     Vendor
		model string
		want  bool
	}{
		{Grandstream, "WP826", true}, {Grandstream, "wp836", true}, {Grandstream, "WP856", true},
		{Grandstream, "WP822", false}, {Grandstream, "GRP2602", false}, {Yealink, "WP826", false},
	} {
		if got := GrandstreamDTLSKey(c.v, c.model); got != c.want {
			t.Errorf("GrandstreamDTLSKey(%s, %s) = %v, want %v", c.v, c.model, got, c.want)
		}
	}
	if GrandstreamICEKey(Grandstream, "WP856") || !GrandstreamICEKey(Grandstream, "WP836") {
		t.Error("GrandstreamICEKey does not follow the templates")
	}
	for _, v := range []Vendor{Yealink, Poly, Fanvil} {
		d := renderSample(false)
		d.Phone.Vendor = v
		d.Server.Transport, d.Server.SRTP, d.Server.SRTPKeyEx, d.Server.ICE = "tls", "required", "dtls", "ice"
		files := map[Vendor]string{Yealink: "805ec0aabbcc.cfg", Poly: "805ec0aabbcc-hello.cfg", Fanvil: "805ec0aabbcc.cfg"}
		models := map[Vendor]string{Yealink: "T54W", Poly: "VVX 450", Fanvil: "X5U"}
		d.Phone.Model = models[v]
		out, err := Render(context.Background(), builtinFor(t, v), files[v], d)
		if err != nil {
			t.Fatal(err)
		}
		if s := strings.ToLower(string(out)); strings.Contains(s, "dtls") || strings.Contains(s, "ice") && strings.Contains(s, "nat") {
			t.Errorf("%s template writes a DTLS or ICE key that is not confirmed from vendor documentation", v)
		}
	}
}
