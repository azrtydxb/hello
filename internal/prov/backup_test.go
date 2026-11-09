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

	for _, model := range []string{"WP836", "WP822", "WP856", "GRP2614"} {
		gs := renderDevice(t, Grandstream, backupData(Grandstream, model, withBackup))
		for _, want := range []string{"<P47>192.0.2.10:5060</P47>", "<P103>0</P103>", "<P48>192.0.2.10:5060</P48>", "<P2333>192.0.2.21:5062</P2333>"} {
			if !strings.Contains(gs, want) {
				t.Errorf("%s lacks %s", model, want)
			}
		}
		for _, not := range []string{"<P29095>", "<P60056>"} {
			if strings.Contains(gs, not) {
				t.Errorf("%s in A-record mode writes %s", model, not)
			}
		}
		plain := renderDevice(t, Grandstream, backupData(Grandstream, model, udpServer))
		if strings.Contains(plain, "<P2333>") || strings.Contains(plain, "<P48>") {
			t.Errorf("%s without a backup writes an outbound proxy", model)
		}

		srv := Server{Host: "sip.hello.example.com", Port: 0, Transport: "udp", DNSMode: DNSSRV, Expiry: 3600}
		gs = renderDevice(t, Grandstream, backupData(Grandstream, model, srv))
		for _, want := range []string{"<P47>sip.hello.example.com</P47>", "<P103>1</P103>", "<P29095>1</P29095>", "<P60056>60</P60056>"} {
			if !strings.Contains(gs, want) {
				t.Errorf("%s SRV lacks %s", model, want)
			}
		}
		if strings.Contains(gs, "sip.hello.example.com:") || strings.Contains(gs, "<P2333>") || strings.Contains(gs, "<P48>") {
			t.Errorf("%s SRV mode writes a port or an outbound proxy", model)
		}
	}
}
