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
