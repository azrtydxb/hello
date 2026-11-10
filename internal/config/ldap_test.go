package config

import "testing"

// TestLoadLDAP fails if LDAP is on by default, if its defaults differ from
// the spec (S-24), or if enabling it without a certificate pair, or a bad
// base DN, is accepted.
func TestLoadLDAP(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	l := c.LDAP
	if l.Enabled || l.Anon || l.Addr != ":636" || l.StartTLSAddr != "" || l.Base != "dc=hello,dc=local" {
		t.Fatalf("defaults = %+v", l)
	}
	wantProvErr(t, env(map[string]string{"HELLO_LDAP_ENABLED": "true"}), "HELLO_LDAP_TLS_CERT", "enabled without certificate")
	wantProvErr(t, env(map[string]string{"HELLO_LDAP_BASE": "cn=x;y"}), "HELLO_LDAP_BASE", "bad base")
	wantProvErr(t, env(map[string]string{"HELLO_LDAP_ADDR": "nonsense"}), "HELLO_LDAP_ADDR", "bad address")
	wantProvErr(t, env(map[string]string{"HELLO_LDAP_STARTTLS_ADDR": "nonsense"}), "HELLO_LDAP_STARTTLS_ADDR", "bad starttls address")
	if _, err := LoadControl(env(map[string]string{"HELLO_LDAP_BASE": "dc=example,dc=com", "HELLO_LDAP_STARTTLS_ADDR": ":389"})); err != nil {
		t.Fatal(err)
	}
}
