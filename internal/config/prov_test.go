package config

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// provOn layers m over an environment with the provisioning listener on.
func provOn(m map[string]string) func(string) string {
	all := map[string]string{"HELLO_PROV_PUBLIC_URL": "https://prov.hello.test"}
	for k, v := range m {
		all[k] = v
	}
	return env(all)
}

// wantProvErr fails unless loading getenv fails naming key.
func wantProvErr(t *testing.T, getenv func(string) string, key, what string) {
	t.Helper()
	if _, err := LoadControl(getenv); err == nil || !strings.Contains(err.Error(), key) {
		t.Fatalf("%s: want an error naming %s, got %v", what, key, err)
	}
}

// TestLoadProvDefaults fails if provisioning is on without
// HELLO_PROV_PUBLIC_URL (existing deployments must keep starting), or if a
// default of the spec's Interfaces differs.
func TestLoadProvDefaults(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	p := c.Prov
	if p.Enabled() {
		t.Fatal("provisioning enabled without HELLO_PROV_PUBLIC_URL")
	}
	if p.Addr != ":8083" || p.TokenGrace != 7*24*time.Hour || p.Resync != 24*time.Hour ||
		p.AuditRetention != 90*24*time.Hour || p.RateIPPerMin != 60 || p.RateDeniedPer10Min != 10 ||
		p.RatePhonePerHour != 30 || p.Timezone != "UTC" || p.NTP != "pool.ntp.org" ||
		p.RegisterExpiry != time.Hour || p.SIPServer != "10.0.0.5:5060" {
		t.Fatalf("defaults = %+v", p)
	}
	if p.TrustedProxies != nil || p.BootCIDRs != nil || p.CACert != "" || p.TLSCert != "" || p.Redirect != (ProvRedirect{}) {
		t.Fatalf("optional settings not empty by default: %+v", p)
	}
}

// TestLoadProvPublicURL fails if an enabled listener accepts a public URL
// that is not https scheme-and-host, or HELLO_PROV_ADDR without a public
// URL, or if a valid URL is not parsed.
func TestLoadProvPublicURL(t *testing.T) {
	c, err := LoadControl(provOn(map[string]string{"HELLO_PROV_ADDR": "0.0.0.0:9443", "HELLO_PROV_PUBLIC_URL": "https://prov.hello.test:8443/"}))
	if err != nil {
		t.Fatal(err)
	}
	if !c.Prov.Enabled() || c.Prov.PublicURL.String() != "https://prov.hello.test:8443" || c.Prov.Addr != "0.0.0.0:9443" {
		t.Fatalf("prov = %+v (%v)", c.Prov, c.Prov.PublicURL)
	}
	for _, bad := range []string{"http://prov.hello.test", "prov.hello.test", "https://", "https://:443", "https://prov.hello.test/p/",
		"https://u:p@prov.hello.test", "https://prov.hello.test?x=1", "https://prov.hello.test#f", "::bad"} {
		wantProvErr(t, provOn(map[string]string{"HELLO_PROV_PUBLIC_URL": bad}), "HELLO_PROV_PUBLIC_URL", bad)
	}
	wantProvErr(t, env(map[string]string{"HELLO_PROV_ADDR": ":8083"}), "HELLO_PROV_PUBLIC_URL", "addr without public URL")
	for _, bad := range []string{"8083", "host:"} {
		wantProvErr(t, provOn(map[string]string{"HELLO_PROV_ADDR": bad}), "HELLO_PROV_ADDR", bad)
	}
}

// TestLoadProvSIPServer fails if the registrar does not default to
// HELLO_SIP_ADVERTISED_ADDR, is missing while enabled, or accepts a
// malformed, unspecified or out-of-range form.
func TestLoadProvSIPServer(t *testing.T) {
	c, err := LoadControl(provOn(map[string]string{"HELLO_PROV_SIP_SERVER": "192.168.10.101:30508"}))
	if err != nil || c.Prov.SIPServer != "192.168.10.101:30508" {
		t.Fatalf("sip server = %q, %v", c.Prov.SIPServer, err)
	}
	wantProvErr(t, provOn(map[string]string{"HELLO_SIP_ADVERTISED_ADDR": ""}), "HELLO_PROV_SIP_SERVER", "no registrar")
	if _, err := LoadControl(env(map[string]string{"HELLO_SIP_ADVERTISED_ADDR": ""})); err != nil {
		t.Fatalf("disabled provisioning needs no registrar: %v", err)
	}
	for _, bad := range []string{"sip.hello.test", "0.0.0.0:5060", ":5060", "h:sip", "h:0", "h:70000", "h:-1"} {
		wantProvErr(t, provOn(map[string]string{"HELLO_PROV_SIP_SERVER": bad}), "HELLO_PROV_SIP_SERVER", bad)
		wantProvErr(t, env(map[string]string{"HELLO_PROV_SIP_SERVER": bad}), "HELLO_PROV_SIP_SERVER", "disabled "+bad)
	}
}

// TestLoadProvDurations fails if the day form is not parsed, or a zero,
// negative or malformed grace, retention or re-check interval (or one
// under a minute) is accepted.
func TestLoadProvDurations(t *testing.T) {
	c, err := LoadControl(env(map[string]string{"HELLO_PROV_TOKEN_GRACE": "2d", "HELLO_PROV_AUDIT_RETENTION": "720h", "HELLO_PROV_RESYNC": "6h"}))
	if err != nil || c.Prov.TokenGrace != 48*time.Hour || c.Prov.AuditRetention != 720*time.Hour || c.Prov.Resync != 6*time.Hour {
		t.Fatalf("durations = %+v, %v", c.Prov, err)
	}
	for _, key := range []string{"HELLO_PROV_TOKEN_GRACE", "HELLO_PROV_AUDIT_RETENTION", "HELLO_PROV_RESYNC"} {
		for _, bad := range []string{"0", "0s", "0d", "-1h", "-1d", "soon", "d", "1.5d", "99999d"} {
			wantProvErr(t, env(map[string]string{key: bad}), key, key+"="+bad)
		}
	}
	wantProvErr(t, env(map[string]string{"HELLO_PROV_RESYNC": "30s"}), "HELLO_PROV_RESYNC", "under a minute")
}

// TestLoadProvRegisterExpiry fails if the rendered expiry is not
// HELLO_SIP_REGISTER_MAX_EXPIRES or an inverted or sub-second window is
// accepted.
func TestLoadProvRegisterExpiry(t *testing.T) {
	c, err := LoadControl(env(map[string]string{"HELLO_SIP_REGISTER_MAX_EXPIRES": "30m"}))
	if err != nil || c.Prov.RegisterExpiry != 30*time.Minute {
		t.Fatalf("expiry = %v, %v", c.Prov.RegisterExpiry, err)
	}
	wantProvErr(t, env(map[string]string{"HELLO_SIP_REGISTER_MIN_EXPIRES": "2h"}), "HELLO_SIP_REGISTER_MIN_EXPIRES", "inverted")
	for _, bad := range []string{"0s", "500ms", "-1m", "x"} {
		wantProvErr(t, env(map[string]string{"HELLO_SIP_REGISTER_MAX_EXPIRES": bad}), "HELLO_SIP_REGISTER_MAX_EXPIRES", bad)
	}
}

// TestLoadProvRates fails if a rate limit is not parsed or a zero,
// negative or malformed one is accepted.
func TestLoadProvRates(t *testing.T) {
	c, err := LoadControl(env(map[string]string{"HELLO_PROV_RATE_IP_PER_MIN": "600", "HELLO_PROV_RATE_DENIED_PER_10MIN": "3", "HELLO_PROV_RATE_PHONE_PER_HOUR": "5"}))
	if err != nil || c.Prov.RateIPPerMin != 600 || c.Prov.RateDeniedPer10Min != 3 || c.Prov.RatePhonePerHour != 5 {
		t.Fatalf("rates = %+v, %v", c.Prov, err)
	}
	for _, key := range []string{"HELLO_PROV_RATE_IP_PER_MIN", "HELLO_PROV_RATE_DENIED_PER_10MIN", "HELLO_PROV_RATE_PHONE_PER_HOUR"} {
		for _, bad := range []string{"0", "-1", "many", "1.5"} {
			wantProvErr(t, env(map[string]string{key: bad}), key, key+"="+bad)
		}
	}
}

// TestLoadProvCIDRs fails if the trusted-proxy or boot CIDR lists are not
// parsed, or a malformed or /0 entry is accepted.
func TestLoadProvCIDRs(t *testing.T) {
	c, err := LoadControl(env(map[string]string{"HELLO_PROV_TRUSTED_PROXIES": "10.42.0.0/16", "HELLO_PROV_BOOT_CIDRS": "192.168.10.0/24, 192.168.20.5/32"}))
	if err != nil || len(c.Prov.TrustedProxies) != 1 || len(c.Prov.BootCIDRs) != 2 || c.Prov.BootCIDRs[1].String() != "192.168.20.5/32" {
		t.Fatalf("cidrs = %v %v, %v", c.Prov.TrustedProxies, c.Prov.BootCIDRs, err)
	}
	for _, key := range []string{"HELLO_PROV_TRUSTED_PROXIES", "HELLO_PROV_BOOT_CIDRS"} {
		for _, bad := range []string{"10.0.0.1", "0.0.0.0/0", "10.0.0.0/8,", "nope"} {
			wantProvErr(t, env(map[string]string{key: bad}), key, key+"="+bad)
		}
	}
}

// TestLoadProvClock fails if a time zone or NTP server of the wrong shape
// is accepted or a valid one is not kept.
func TestLoadProvClock(t *testing.T) {
	c, err := LoadControl(env(map[string]string{"HELLO_PROV_TIMEZONE": "Europe/Brussels", "HELLO_PROV_NTP": "192.168.10.1"}))
	if err != nil || c.Prov.Timezone != "Europe/Brussels" || c.Prov.NTP != "192.168.10.1" {
		t.Fatalf("clock = %q %q, %v", c.Prov.Timezone, c.Prov.NTP, err)
	}
	for _, bad := range []string{"Europe/../etc/passwd", "Mid Atlantic", "<x>", strings.Repeat("a", 65)} {
		wantProvErr(t, env(map[string]string{"HELLO_PROV_TIMEZONE": bad}), "HELLO_PROV_TIMEZONE", bad)
	}
	for _, bad := range []string{"ntp server", "ntp/x", "\"x\""} {
		wantProvErr(t, env(map[string]string{"HELLO_PROV_NTP": bad}), "HELLO_PROV_NTP", bad)
	}
}

// TestLoadProvCertificates fails if a missing or non-certificate CA file is
// accepted, if a TLS certificate is accepted without its key (or the other
// way round) or unreadable, or if valid files are refused. No error may
// echo the key file's content.
func TestLoadProvCertificates(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := selfSigned(t)
	write := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	cert, key := write("tls.crt", certPEM), write("tls.key", keyPEM)
	junk := write("junk.pem", []byte("not a certificate"))
	keyAsCA := write("key-as-ca.pem", keyPEM)
	badDER := write("bad.pem", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: []byte{1, 2, 3}}))

	c, err := LoadControl(provOn(map[string]string{"HELLO_PROV_CA_CERT": cert, "HELLO_PROV_TLS_CERT": cert, "HELLO_PROV_TLS_KEY": key}))
	if err != nil || c.Prov.CACert != cert || c.Prov.TLSCert != cert || c.Prov.TLSKey != key {
		t.Fatalf("certificates = %+v, %v", c.Prov, err)
	}
	for _, bad := range []string{filepath.Join(dir, "missing.pem"), junk, keyAsCA, badDER} {
		wantProvErr(t, env(map[string]string{"HELLO_PROV_CA_CERT": bad}), "HELLO_PROV_CA_CERT", bad)
	}
	wantProvErr(t, env(map[string]string{"HELLO_PROV_TLS_CERT": cert}), "HELLO_PROV_TLS_CERT", "cert without key")
	wantProvErr(t, env(map[string]string{"HELLO_PROV_TLS_KEY": key}), "HELLO_PROV_TLS_CERT", "key without cert")
	_, err = LoadControl(env(map[string]string{"HELLO_PROV_TLS_CERT": cert, "HELLO_PROV_TLS_KEY": junk}))
	if err == nil || !strings.Contains(err.Error(), "HELLO_PROV_TLS_KEY") {
		t.Fatalf("unusable key accepted: %v", err)
	}
	_, err = LoadControl(env(map[string]string{"HELLO_PROV_TLS_CERT": key, "HELLO_PROV_TLS_KEY": cert}))
	if err == nil || strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), string(keyPEM[40:60])) {
		t.Fatalf("swapped pair: want a static error, got %v", err)
	}
}

// TestLoadProvRedirect fails if a vendor's deployment credentials are
// accepted half-set, if the error does not name each missing key or echoes
// a value, or if a complete group is not read.
func TestLoadProvRedirect(t *testing.T) {
	val := "s3cr" + "et-value" // assembled so scanners do not flag a literal
	c, err := LoadControl(env(map[string]string{"HELLO_PROV_SNOM_KEY_ID": "kid", "HELLO_PROV_SNOM_KEY_SECRET": val}))
	if err != nil || c.Prov.Redirect.SnomKeyID != "kid" || c.Prov.Redirect.SnomKeySecret != val {
		t.Fatalf("snom = %+v, %v", c.Prov.Redirect, err)
	}
	for set, missing := range map[string][]string{
		"HELLO_PROV_SNOM_KEY_ID":        {"HELLO_PROV_SNOM_KEY_SECRET"},
		"HELLO_PROV_YEALINK_SECRET":     {"HELLO_PROV_YEALINK_KEY"},
		"HELLO_PROV_YMCS_CLIENT_SECRET": {"HELLO_PROV_YMCS_CLIENT_ID", "HELLO_PROV_YMCS_REGION"},
		"HELLO_PROV_GDMS_PASSWORD": {"HELLO_PROV_GDMS_CLIENT_ID", "HELLO_PROV_GDMS_CLIENT_SECRET", "HELLO_PROV_GDMS_USERNAME",
			"HELLO_PROV_GDMS_REGION", "HELLO_PROV_GDMS_SITE_ID"},
	} {
		_, err := LoadControl(env(map[string]string{set: val}))
		if err == nil || strings.Contains(err.Error(), val) {
			t.Fatalf("%s alone: want an error without the value, got %v", set, err)
		}
		for _, key := range missing {
			if !strings.Contains(err.Error(), key) {
				t.Fatalf("%s alone: error does not name %s: %v", set, key, err)
			}
		}
	}
	gdms := map[string]string{}
	for _, k := range []string{"CLIENT_ID", "CLIENT_SECRET", "USERNAME", "PASSWORD", "REGION", "SITE_ID"} {
		gdms["HELLO_PROV_GDMS_"+k] = strings.ToLower(k)
	}
	if c, err := LoadControl(env(gdms)); err != nil || c.Prov.Redirect.GDMSSiteID != "site_id" || c.Prov.Redirect.GDMSPassword != "password" {
		t.Fatalf("gdms = %+v, %v", c.Prov.Redirect, err)
	}
}

// selfSigned returns a throwaway certificate and key in PEM.
func selfSigned(t *testing.T) (certPEM, keyPEM []byte) {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "prov.hello.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"prov.hello.test"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: kd})
}
