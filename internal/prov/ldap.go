package prov

import "strconv"

// LDAPInfo is the phone's LDAP directory setting (spec pbx-gaps S-26): the
// server, the base the contacts are under and the phone's own bind DN and
// password. It is nil unless the phone's group has LDAP on, hello-control
// serves LDAP and the phone's model takes it (SupportsLDAP). The CA the
// phone trusts for the server is Prov.CAURL and Prov.CACertPEM.
type LDAPInfo struct {
	// Host and Port are where the phone reaches the LDAPS listener (the
	// host of HELLO_PROV_PUBLIC_URL, port 636 unless the deployment
	// publishes it elsewhere).
	Host string
	Port int
	// Base is the base DN the contacts are searched under
	// (ou=phonebook,<HELLO_LDAP_BASE>); Root is HELLO_LDAP_BASE itself.
	Base string
	Root string
	// BindDN and Password are the phone's own credentials; Password is
	// masked in previews.
	BindDN   string
	Password string
	// MaxHits is the most entries a search returns (the server's limit).
	MaxHits int
}

// LDAPBindDN is a phone's bind DN under the base DN (spec S-24).
func LDAPBindDN(base string, phoneID int64) string {
	return "uid=" + strconv.FormatInt(phoneID, 10) + ",ou=phones," + base
}

// LDAPMaxHits is the server's size limit (spec S-25).
const LDAPMaxHits = 200

// NewLDAPInfo builds the setting; nil when there is no host or no
// password yet.
func NewLDAPInfo(host string, port int, root, bindDN, password string) *LDAPInfo {
	if host == "" || bindDN == "" || password == "" {
		return nil
	}
	if port <= 0 {
		port = 636
	}
	return &LDAPInfo{Host: host, Port: port, Base: "ou=phonebook," + root, Root: root, BindDN: bindDN, Password: password, MaxHits: LDAPMaxHits}
}

// SupportsLDAP reports whether the built-in template of the vendor writes
// LDAP keys for the model. Every first-class vendor's phones have an LDAP
// directory client; a generic phone has no built-in template, and an
// administrator template takes the keys from RenderData.LDAP itself.
func SupportsLDAP(v Vendor, model string) bool {
	switch v {
	case Yealink, Poly, Grandstream, Snom, Fanvil:
		return true
	}
	return false
}
