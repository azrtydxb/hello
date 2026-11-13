package prov

import (
	"strings"
	"testing"
)

// TestGrandstreamPerLineKeys fails if a Grandstream line 2 or later lacks its
// own SRTP mode, presence, RTCP-XR collector or backup server keys, or gets
// one the model's official template does not have. The expected keys are
// typed in from Grandstream's configuration templates (config-template-10212026.zip),
// not computed: wp8x6_config_1.0.3.39 accounts 2 and 3 (SRTP P443, P543; key
// exchange P60178, P60278; presence P488, P588 and P2419, P2519; RTCP-XR P26193,
// P60187, P26194, P26195 and P26293, P60287, P26294, P26295; backup P2412,
// P403, P2433 and P2512, P503, P2533), wp810_822_825_config_1.0.11.83 account 2
// (P443; P488, P2419; P26193, P60187, P26194, P26195; P2412, P403, P2433),
// wp820_config_1.0.7.90 account 2 (P443; P51102, P403, P2433) and
// wp856_config_1.0.3.16 accounts 2 to 6 (SRTP P443, P543, P643, P1743, P1843
// with key exchange P60178 to P60578; RTCP-XR P26193 to P26593 and P26194 to
// P26594 and P26195 to P26595; backup P2412 to P2812, P403, P503, P603, P1703,
// P1803 and P2433 to P2833). No account 2 key may be written for a model whose
// template has none (WP820 and WP856 presence, WP820 RTCP-XR).
func TestGrandstreamPerLineKeys(t *testing.T) {
	type acct struct {
		srtp, kx, pub, sub string // SRTP Mode, Key Exchange, PUBLISH, SUBSCRIBE for Registration
		xr                 []string
		sec, proxy, backup string
	}
	cases := []struct {
		model string
		lines int
		accts []acct
		// keys that are in no account block of the template
		never []string
	}{
		{"WP836", 2, []acct{
			{"P443", "P60178", "P488", "P2419", []string{"P26193", "P60187", "P26194", "P26195"}, "P2412", "P403", "P2433"},
			{"P543", "P60278", "P588", "P2519", []string{"P26293", "P60287", "P26294", "P26295"}, "P2512", "P503", "P2533"},
		}, nil},
		{"WP822", 1, []acct{
			{"P443", "", "P488", "P2419", []string{"P26193", "P60187", "P26194", "P26195"}, "P2412", "P403", "P2433"},
		}, []string{"P60178"}},
		{"WP820", 1, []acct{
			{"P443", "", "", "", nil, "P51102", "P403", "P2433"},
		}, []string{"P60178", "P488", "P2419", "P26193", "P60187", "P26195", "P2412"}},
		{"WP856", 5, []acct{
			{"P443", "P60178", "", "", []string{"P26193", "P26194", "P26195"}, "P2412", "P403", "P2433"},
			{"P543", "P60278", "", "", []string{"P26293", "P26294", "P26295"}, "P2512", "P503", "P2533"},
			{"P643", "P60378", "", "", []string{"P26393", "P26394", "P26395"}, "P2612", "P603", "P2633"},
			{"P1743", "P60478", "", "", []string{"P26493", "P26494", "P26495"}, "P2712", "P1703", "P2733"},
			{"P1843", "P60578", "", "", []string{"P26593", "P26594", "P26595"}, "P2812", "P1803", "P2833"},
		}, []string{"P488", "P2419", "P60187", "P60287"}},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			d := linesSample(Grandstream, c.model, 2)
			extra := d.Lines[0]
			d.Lines = nil
			for range c.lines {
				d.Lines = append(d.Lines, extra)
			}
			d.Prov.RTCPXR = true
			d.Server = Server{Host: "192.0.2.10", Port: 5060, Transport: "tls", SRTP: "required", DNSMode: DNSA, Expiry: 3600,
				Backup: &Backup{Host: "192.0.2.21", Port: 5062}}
			out := renderFor(t, Grandstream, linesFile(Grandstream, d), d)
			has := func(key, val string) bool {
				return strings.Contains(out, "<"+key+">"+val+"</"+key+">")
			}
			for i, a := range c.accts {
				n := i + 2
				if !has(a.srtp, "2") {
					t.Errorf("line %d lacks SRTP Mode %s = 2", n, a.srtp)
				}
				if a.kx != "" && !has(a.kx, "0") {
					t.Errorf("line %d lacks SRTP Key Exchange %s = 0", n, a.kx)
				}
				for _, k := range []string{a.pub, a.sub} {
					if k != "" && !has(k, "0") {
						t.Errorf("line %d lacks presence key %s = 0", n, k)
					}
				}
				for _, k := range a.xr {
					if !strings.Contains(out, "<"+k+">") {
						t.Errorf("line %d lacks RTCP-XR key %s", n, k)
					}
				}
				if !has(a.sec, "192.0.2.21:5062") || !has(a.proxy, "192.0.2.10:5060") || !has(a.backup, "192.0.2.21:5062") {
					t.Errorf("line %d lacks backup keys %s, %s, %s", n, a.sec, a.proxy, a.backup)
				}
			}
			for _, k := range c.never {
				if strings.Contains(out, "<"+k+">") {
					t.Errorf("%s writes %s, which its template does not have", c.model, k)
				}
			}
			// Without HELLO_PROV_RTCPXR, a UDP server and no backup, none of it is written.
			d.Prov.RTCPXR = false
			d.Server = Server{Host: "192.0.2.10", Port: 5060, Transport: "udp", DNSMode: DNSA, Expiry: 3600}
			out = renderFor(t, Grandstream, linesFile(Grandstream, d), d)
			for _, a := range c.accts {
				for _, k := range append([]string{a.srtp, a.kx, a.sec, a.proxy, a.backup}, a.xr...) {
					if k != "" && strings.Contains(out, "<"+k+">") {
						t.Errorf("%s with no TLS, backup or RTCP-XR writes %s", c.model, k)
					}
				}
			}
			// SRV mode names the servers in DNS: no backup keys.
			d.Server = Server{Host: "sip.hello.example.com", Transport: "udp", DNSMode: DNSSRV, Expiry: 3600,
				Backup: &Backup{Host: "192.0.2.21", Port: 5062}}
			out = renderFor(t, Grandstream, linesFile(Grandstream, d), d)
			for _, a := range c.accts {
				for _, k := range []string{a.sec, a.proxy, a.backup} {
					if strings.Contains(out, "<"+k+">") {
						t.Errorf("%s in SRV mode writes backup key %s", c.model, k)
					}
				}
			}
		})
	}
}
