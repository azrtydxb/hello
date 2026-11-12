package prov

import (
	"strings"
	"testing"
)

func backupData(v Vendor, model string, srv Server) RenderData {
	d := SampleData(true)
	d.Phone.Vendor, d.Phone.Model = v, model
	d.Server = srv
	return d
}

var udpServer = Server{Host: "192.0.2.10", Port: 5060, Transport: "udp", DNSMode: DNSA, Expiry: 3600}

// TestSRVAndBackupProvisioning fails if a backup server is missing from the
// Yealink, Poly or Grandstream files, if Grandstream SRV mode lacks the DNS
// mode, the name without a port, register-before-failover or the failback
// timer, if A-record mode (the default) writes any of them, or if a phone
// without a backup gets backup keys.
func TestSRVAndBackupProvisioning(t *testing.T) {
	withBackup := udpServer
	withBackup.Backup = &Backup{Host: "192.0.2.21", Port: 5062}

	yl := renderDevice(t, Yealink, backupData(Yealink, "T54W", withBackup))
	for _, want := range []string{"account.1.sip_server.2.address = 192.0.2.21\n", "account.1.sip_server.2.port = 5062\n",
		"account.1.sip_server.2.transport_type = 0\n", "account.1.sip_server.2.expires = 3600"} {
		if !strings.Contains(yl, want) {
			t.Errorf("yealink lacks %q", want)
		}
	}
	if strings.Contains(renderDevice(t, Yealink, backupData(Yealink, "T54W", udpServer)), "sip_server.2") {
		t.Error("yealink without a backup has a second server")
	}

	pd := backupData(Poly, "VVX 450", withBackup)
	pl, err := Render(t.Context(), builtinFor(t, Poly), pd.Phone.MAC+"-hello.cfg", pd)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`reg.1.server.2.address="192.0.2.21"`, `reg.1.server.2.port="5062"`, `reg.1.server.2.transport="UDPOnly"`} {
		if !strings.Contains(string(pl), want) {
			t.Errorf("poly lacks %q", want)
		}
	}
	pd = backupData(Poly, "VVX 450", udpServer)
	if pl, _ := Render(t.Context(), builtinFor(t, Poly), pd.Phone.MAC+"-hello.cfg", pd); strings.Contains(string(pl), "server.2") {
		t.Error("poly without a backup has a second server")
	}

	// Keys per model, from wp8x6_config_1.0.3.39, wp810_822_825_config_1.0.11.83,
	// wp820_config_1.0.7.90 and wp856_config_1.0.3.16: every template has P48,
	// P2333, P103, P29095 and P26040; P2312 (Secondary SIP Server) all but the
	// WP820; P60056 (Failback Timer) all but the WP810/822/825.
	for _, c := range []struct {
		model     string
		secondary bool // P2312 in the template
		failback  bool // P60056 in the template
	}{{"WP836", true, true}, {"WP822", true, false}, {"WP856", true, true}, {"WP820", false, true}, {"GRP2614", true, true}} {
		model := c.model
		gs := renderDevice(t, Grandstream, backupData(Grandstream, model, withBackup))
		for _, want := range []string{"<P47>192.0.2.10:5060</P47>", "<P103>0</P103>", "<P48>192.0.2.10:5060</P48>", "<P2333>192.0.2.21:5062</P2333>"} {
			if !strings.Contains(gs, want) {
				t.Errorf("%s lacks %s", model, want)
			}
		}
		if got := strings.Contains(gs, "<P2312>192.0.2.21:5062</P2312>"); got != c.secondary {
			t.Errorf("%s: P2312 written = %v, want %v", model, got, c.secondary)
		}
		for _, not := range []string{"<P29095>", "<P60056>", "<P26040>"} {
			if strings.Contains(gs, not) {
				t.Errorf("%s in A-record mode writes %s", model, not)
			}
		}
		plain := renderDevice(t, Grandstream, backupData(Grandstream, model, udpServer))
		if strings.Contains(plain, "<P2333>") || strings.Contains(plain, "<P48>") || strings.Contains(plain, "<P2312>") {
			t.Errorf("%s without a backup writes a second server or an outbound proxy", model)
		}

		srv := Server{Host: "sip.hello.example.com", Port: 0, Transport: "udp", DNSMode: DNSSRV, Expiry: 3600}
		gs = renderDevice(t, Grandstream, backupData(Grandstream, model, srv))
		for _, want := range []string{"<P47>sip.hello.example.com</P47>", "<P103>1</P103>", "<P29095>1</P29095>", "<P26040>3</P26040>"} {
			if !strings.Contains(gs, want) {
				t.Errorf("%s SRV lacks %s", model, want)
			}
		}
		if got := strings.Contains(gs, "<P60056>60</P60056>"); got != c.failback {
			t.Errorf("%s SRV: P60056 written = %v, want %v", model, got, c.failback)
		}
		if strings.Contains(gs, "sip.hello.example.com:") || strings.Contains(gs, "<P2333>") || strings.Contains(gs, "<P48>") || strings.Contains(gs, "<P2312>") {
			t.Errorf("%s SRV mode writes a port, a second server or an outbound proxy", model)
		}
	}
}

// TestSRVExtraLines fails if a Grandstream line 2 or later lacks its own DNS
// mode (account N: P408, P508, P608, P1708, P1808), register-before-failover
// (P29095 + 100 (N-1)), failover mode (P26040 + 100 (N-1)) or failback timer
// (P60056 + 100 (N-1)) in SRV mode, if a key is written for a model whose
// template does not take it, or if A-record mode writes any but DNS Mode 0.
func TestSRVExtraLines(t *testing.T) {
	srv := Server{Host: "sip.hello.example.com", Port: 0, Transport: "udp", DNSMode: DNSSRV, Expiry: 3600}
	cases := []struct {
		model string
		lines int
		want  [][]string // per extra line: keys that must be written
		not   []string
	}{
		{"WP836", 2, [][]string{
			{"<P408>1</P408>", "<P29195>1</P29195>"},
			{"<P508>1</P508>", "<P29295>1</P29295>"},
		}, []string{"<P26140>", "<P26240>", "<P60156>", "<P60256>"}},
		{"WP822", 1, [][]string{
			{"<P408>1</P408>", "<P29195>1</P29195>", "<P26140>3</P26140>"},
		}, []string{"<P60155>", "<P60156>"}},
		{"WP820", 1, [][]string{
			{"<P408>1</P408>", "<P29195>1</P29195>", "<P26140>3</P26140>", "<P60156>60</P60156>"},
		}, nil},
		{"WP856", 2, [][]string{
			{"<P408>1</P408>", "<P29195>1</P29195>", "<P26140>3</P26140>", "<P60156>60</P60156>"},
			{"<P508>1</P508>", "<P29295>1</P29295>", "<P26240>3</P26240>", "<P60256>60</P60256>"},
		}, nil},
	}
	for _, c := range cases {
		t.Run(c.model, func(t *testing.T) {
			d := linesSample(Grandstream, c.model, c.lines)
			d.Server = srv
			out := renderFor(t, Grandstream, linesFile(Grandstream, d), d)
			for _, keys := range c.want {
				for _, want := range keys {
					if !strings.Contains(out, want) {
						t.Errorf("SRV mode lacks %s", want)
					}
				}
			}
			for _, not := range c.not {
				if strings.Contains(out, not) {
					t.Errorf("SRV mode writes %s", not)
				}
			}
			d.Server.DNSMode = DNSA
			d.Server.Host, d.Server.Port = "192.0.2.10", 5060
			out = renderFor(t, Grandstream, linesFile(Grandstream, d), d)
			if !strings.Contains(out, "<P408>0</P408>") {
				t.Error("A-record mode does not reset line 2 to DNS Mode 0")
			}
			for _, not := range []string{"<P29195>", "<P26140>", "<P60156>"} {
				if strings.Contains(out, not) {
					t.Errorf("A-record mode writes %s", not)
				}
			}
		})
	}
}

// TestSRVWP856SixLines fails if the WP856's lines 5 and 6 (account blocks at
// P1701 and P1801) do not take their DNS mode at b+7.
func TestSRVWP856SixLines(t *testing.T) {
	d := linesSample(Grandstream, "WP856", 2)
	extra := d.Lines[0]
	d.Lines = nil
	for range 5 {
		d.Lines = append(d.Lines, extra)
	}
	d.Server = Server{Host: "sip.hello.example.com", Transport: "udp", DNSMode: DNSSRV, Expiry: 3600}
	out := renderFor(t, Grandstream, linesFile(Grandstream, d), d)
	for _, want := range []string{"<P608>1</P608>", "<P1708>1</P1708>", "<P1808>1</P1808>", "<P29495>1</P29495>", "<P29595>1</P29595>",
		"<P26440>3</P26440>", "<P26540>3</P26540>", "<P60456>60</P60456>", "<P60556>60</P60556>"} {
		if !strings.Contains(out, want) {
			t.Errorf("SRV mode lacks %s", want)
		}
	}
}
