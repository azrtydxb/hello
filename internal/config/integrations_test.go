package config

import (
	"net/netip"
	"strings"
	"testing"
)

// TestIntegrationsAllowedCIDRs fails if the private ranges outbound webhooks
// may reach are not RFC 1918 by default, cannot be narrowed or switched off,
// or if a range that includes loopback, link-local or metadata space, or a
// public range, is accepted.
func TestIntegrationsAllowedCIDRs(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}
	if len(c.IntegrationsAllowedCIDRs) != 3 {
		t.Fatalf("default = %v, want %v", c.IntegrationsAllowedCIDRs, want)
	}
	for i, p := range c.IntegrationsAllowedCIDRs {
		if p.String() != want[i] {
			t.Errorf("default[%d] = %s, want %s", i, p, want[i])
		}
	}
	c, err = LoadControl(env(map[string]string{"HELLO_INTEGRATIONS_ALLOWED_CIDRS": "10.42.0.0/16, 192.168.7.9"}))
	if err != nil || len(c.IntegrationsAllowedCIDRs) != 2 || c.IntegrationsAllowedCIDRs[1] != netip.MustParsePrefix("192.168.7.9/32") {
		t.Fatalf("narrowed = %v, %v", c.IntegrationsAllowedCIDRs, err)
	}
	if c, err = LoadControl(env(map[string]string{"HELLO_INTEGRATIONS_ALLOWED_CIDRS": "none"})); err != nil || len(c.IntegrationsAllowedCIDRs) != 0 {
		t.Fatalf("none = %v, %v", c.IntegrationsAllowedCIDRs, err)
	}
	for _, bad := range []string{"0.0.0.0/0", "127.0.0.0/8", "169.254.0.0/16", "169.254.169.254", "10.0.0.0/7", "8.8.8.0/24", "nonsense", "::1/128", "fe80::/10"} {
		_, err := LoadControl(env(map[string]string{"HELLO_INTEGRATIONS_ALLOWED_CIDRS": bad}))
		if err == nil || !strings.Contains(err.Error(), "HELLO_INTEGRATIONS_ALLOWED_CIDRS") {
			t.Errorf("%q was accepted: %v", bad, err)
		}
	}
}

// TestDirectoryAllowPlaintextLDAP fails if plaintext LDAP is on by default or
// the switch accepts anything but true and false.
func TestDirectoryAllowPlaintextLDAP(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil || c.DirectoryAllowPlaintextLDAP {
		t.Fatalf("default = %v, %v", c.DirectoryAllowPlaintextLDAP, err)
	}
	if c, err = LoadControl(env(map[string]string{"HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP": "true"})); err != nil || !c.DirectoryAllowPlaintextLDAP {
		t.Fatalf("true = %v, %v", c.DirectoryAllowPlaintextLDAP, err)
	}
	if _, err = LoadControl(env(map[string]string{"HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP": "yes"})); err == nil ||
		!strings.Contains(err.Error(), "HELLO_DIRECTORY_ALLOW_PLAINTEXT_LDAP") {
		t.Fatalf("yes accepted: %v", err)
	}
}

// TestCRMUploadAndSIPAllowList fails if the CRM upload limit is not 50 MB by
// default, accepts a bad value, or if hello-sip does not read the same
// private-range allow-list as hello-control (a self-hosted Odoo or Bitrix24
// must be reachable for the caller-name lookup too).
func TestCRMUploadAndSIPAllowList(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil || c.CRMUploadMaxMB != 50 {
		t.Fatalf("default = %d, %v", c.CRMUploadMaxMB, err)
	}
	if c, err = LoadControl(env(map[string]string{"HELLO_CRM_UPLOAD_MAX_MB": "20"})); err != nil || c.CRMUploadMaxMB != 20 {
		t.Fatalf("set = %d, %v", c.CRMUploadMaxMB, err)
	}
	for _, bad := range []string{"0", "-1", "lots"} {
		if _, err := LoadControl(env(map[string]string{"HELLO_CRM_UPLOAD_MAX_MB": bad})); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	s, err := LoadSIP(env(nil))
	if err != nil || len(s.IntegrationsAllowedCIDRs) != 3 {
		t.Fatalf("sip default = %v, %v", s.IntegrationsAllowedCIDRs, err)
	}
	if s, err = LoadSIP(env(map[string]string{"HELLO_INTEGRATIONS_ALLOWED_CIDRS": "none"})); err != nil || len(s.IntegrationsAllowedCIDRs) != 0 {
		t.Fatalf("sip none = %v, %v", s.IntegrationsAllowedCIDRs, err)
	}
}
