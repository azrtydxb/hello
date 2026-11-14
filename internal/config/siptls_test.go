package config

import "testing"

// TestLoadSIPTLS fails if the TLS hop is on by default, if it is read wrongly,
// or if an address without its certificate, key and CA is accepted.
func TestLoadSIPTLS(t *testing.T) {
	s, err := LoadSIP(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.SIPTLSAddr != "" {
		t.Fatalf("SIP over TLS is on by default: %q", s.SIPTLSAddr)
	}
	full := map[string]string{"HELLO_SIP_TLS_ADDR": ":5061", "HELLO_SIP_TLS_CERT": "/c/tls.crt", "HELLO_SIP_TLS_KEY": "/c/tls.key", "HELLO_SIP_TLS_CA": "/c/ca.crt"}
	s, err = LoadSIP(env(full))
	if err != nil {
		t.Fatal(err)
	}
	if s.SIPTLSAddr != ":5061" || s.SIPTLSCert != "/c/tls.crt" || s.SIPTLSKey != "/c/tls.key" || s.SIPTLSCA != "/c/ca.crt" {
		t.Fatalf("tls = %q %q %q %q", s.SIPTLSAddr, s.SIPTLSCert, s.SIPTLSKey, s.SIPTLSCA)
	}
	for _, drop := range []string{"HELLO_SIP_TLS_CERT", "HELLO_SIP_TLS_KEY", "HELLO_SIP_TLS_CA"} {
		m := map[string]string{}
		for k, v := range full {
			if k != drop {
				m[k] = v
			}
		}
		if _, err := LoadSIP(env(m)); err == nil {
			t.Errorf("HELLO_SIP_TLS_ADDR without %s was accepted", drop)
		}
	}
}
