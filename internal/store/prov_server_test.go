package store

import (
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// TestProvServerTransport fails if a phone is given the TLS registrar when
// SIP over TLS is not configured, when its model is not selected, or when
// its vendor has no transport key; or is not given it when all hold
// (phone-feature-parity S-14).
func TestProvServerTransport(t *testing.T) {
	base := ProvSettings{SIPServer: "192.0.2.1:30508", Expiry: time.Hour}
	tls := base
	tls.SIPTLSServer, tls.SIPTLSModels = "192.0.2.1:30509", []string{"grandstream:WP", "snom"}
	for _, tc := range []struct {
		name  string
		p     ProvSettings
		v     prov.Vendor
		model string
		host  string
		port  int
		tr    string
	}{
		{"tcp selected", ProvSettings{SIPServer: base.SIPServer, SIPTCPModels: []string{"grandstream:WP"}, Expiry: time.Hour}, prov.Grandstream, "WP826", "192.0.2.1", 30508, "tcp"},
		{"tcp model not selected", ProvSettings{SIPServer: base.SIPServer, SIPTCPModels: []string{"grandstream:WP"}, Expiry: time.Hour}, prov.Grandstream, "GRP2614", "192.0.2.1", 30508, "udp"},
		{"tcp vendor without a key", ProvSettings{SIPServer: base.SIPServer, SIPTCPModels: []string{"snom"}, Expiry: time.Hour}, prov.Snom, "D785", "192.0.2.1", 30508, "udp"},
		{"tls wins over tcp", func() ProvSettings { p := tls; p.SIPTCPModels = []string{"grandstream"}; return p }(), prov.Grandstream, "WP826", "192.0.2.1", 30509, "tls"},
		{"tcp when tls server unset", ProvSettings{SIPServer: base.SIPServer, SIPTLSModels: []string{"grandstream"}, SIPTCPModels: []string{"grandstream"}, Expiry: time.Hour}, prov.Grandstream, "WP826", "192.0.2.1", 30508, "tcp"},
		{"off", base, prov.Grandstream, "WP826", "192.0.2.1", 30508, "udp"},
		{"selected", tls, prov.Grandstream, "WP826", "192.0.2.1", 30509, "tls"},
		{"model not selected", tls, prov.Grandstream, "GRP2614", "192.0.2.1", 30508, "udp"},
		{"vendor without a key", tls, prov.Snom, "D785", "192.0.2.1", 30508, "udp"},
		{"server set, no models", ProvSettings{SIPServer: base.SIPServer, SIPTLSServer: tls.SIPTLSServer, Expiry: time.Hour}, prov.Grandstream, "WP826", "192.0.2.1", 30508, "udp"},
	} {
		got := tc.p.server(tc.v, tc.model)
		if got.Host != tc.host || got.Port != tc.port || got.Transport != tc.tr {
			t.Errorf("%s: server = %+v, want %s:%d over %s", tc.name, got, tc.host, tc.port, tc.tr)
		}
		if got.Expiry != 3600 {
			t.Errorf("%s: expiry = %d, want 3600", tc.name, got.Expiry)
		}
	}
}

// TestProvServerBackupAndSRV fails if the first backup is not handed to a
// UDP phone, SRV mode reaches a vendor other than Grandstream, SRV keeps a
// port or a backup, or a TLS phone is given a UDP backup (S-20).
func TestProvServerBackupAndSRV(t *testing.T) {
	p := ProvSettings{SIPServer: "192.0.2.10:5060", SIPBackupServers: []string{"192.0.2.21:5062", "192.0.2.22:5060"}}
	s := p.server(prov.Yealink, "T54W")
	if s.Backup == nil || s.Backup.Host != "192.0.2.21" || s.Backup.Port != 5062 || s.DNSMode != prov.DNSA || s.Host != "192.0.2.10" {
		t.Errorf("udp = %+v %+v", s, s.Backup)
	}
	if s := (ProvSettings{SIPServer: "192.0.2.10:5060"}).server(prov.Yealink, "T54W"); s.Backup != nil {
		t.Errorf("no backups configured, got %+v", s.Backup)
	}

	p.SIPSRV = "sip.hello.example.com"
	gs := p.server(prov.Grandstream, "WP836")
	if gs.DNSMode != prov.DNSSRV || gs.Host != "sip.hello.example.com" || gs.Port != 0 || gs.Backup != nil {
		t.Errorf("grandstream srv = %+v", gs)
	}
	for _, v := range []prov.Vendor{prov.Yealink, prov.Poly} {
		if s := p.server(v, "T54W"); s.DNSMode != prov.DNSSRV || s.Host != "sip.hello.example.com" || s.Port != 0 || s.Backup != nil {
			t.Errorf("%s under srv = %+v", v, s)
		}
	}
	for _, v := range []prov.Vendor{prov.Snom, prov.Fanvil} {
		if s := p.server(v, "D785"); s.DNSMode != prov.DNSA || s.Host != "192.0.2.10" || s.Port != 5060 || len(s.Backups) != 2 {
			t.Errorf("%s under srv = %+v", v, s)
		}
	}
	tcp := p
	tcp.SIPTCPModels = []string{"yealink", "grandstream"}
	if s := tcp.server(prov.Yealink, "T54W"); s.DNSMode != prov.DNSA || s.Transport != "tcp" || s.Backup == nil {
		t.Errorf("yealink on tcp under srv = %+v", s)
	}
	if s := tcp.server(prov.Grandstream, "WP822"); s.DNSMode != prov.DNSSRV || s.Transport != "tcp" {
		t.Errorf("grandstream on tcp under srv = %+v", s)
	}

	p.SIPTLSServer, p.SIPTLSModels = "192.0.2.10:5061", []string{"grandstream:WP"}
	if tls := p.server(prov.Grandstream, "WP836"); tls.Transport != "tls" || tls.Backup != nil || tls.DNSMode != prov.DNSA || tls.Port != 5061 {
		t.Errorf("tls = %+v", tls)
	}
}

// TestProvBackupServersFromNodes fails if the HA node list does not supply
// the backups of a phone that registers with a hello-sip node directly (the
// other nodes, at most two, the primary never among them), if it supplies
// any behind an edge, or if HELLO_PROV_SIP_BACKUP_SERVERS does not override
// it (S-20).
func TestProvBackupServersFromNodes(t *testing.T) {
	nodes := []string{"192.0.2.11:5060", "192.0.2.12:5060", "192.0.2.13", "192.0.2.14:5060"}
	p := ProvSettings{SIPServer: "192.0.2.12", SIPNodes: func() []string { return nodes }}
	got, src := p.BackupServers()
	if src != BackupFromNodes || len(got) != 2 || got[0] != "192.0.2.11:5060" || got[1] != "192.0.2.13" {
		t.Errorf("direct to a node: %v from %s", got, src)
	}
	s := p.server(prov.Grandstream, "WP822")
	if s.Backup == nil || s.Backup.Host != "192.0.2.11" || len(s.Backups) != 2 || s.Backups[1].Port != 5060 {
		t.Errorf("server = %+v", s)
	}

	edge := ProvSettings{SIPServer: "192.0.2.100:30508", SIPNodes: func() []string { return nodes }}
	if got, src := edge.BackupServers(); got != nil || src != BackupNone {
		t.Errorf("behind an edge: %v from %s", got, src)
	}
	if s := edge.server(prov.Yealink, "T54W"); s.Backup != nil || s.Backups != nil {
		t.Errorf("behind an edge the server has backups: %+v", s)
	}
	alone := ProvSettings{SIPServer: "192.0.2.11:5060", SIPNodes: func() []string { return nodes[:1] }}
	if got, src := alone.BackupServers(); got != nil || src != BackupNone {
		t.Errorf("one node: %v from %s", got, src)
	}

	p.SIPBackupServers = []string{"198.51.100.5:5070"}
	if got, src := p.BackupServers(); src != BackupFromEnv || len(got) != 1 || got[0] != "198.51.100.5:5070" {
		t.Errorf("the env does not override: %v from %s", got, src)
	}
	if got, src := (ProvSettings{SIPServer: "192.0.2.12:5060"}).BackupServers(); got != nil || src != BackupNone {
		t.Errorf("no cluster view: %v from %s", got, src)
	}
}
