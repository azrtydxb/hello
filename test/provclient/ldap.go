package provclient

// LDAP directory settings as each vendor's phone reads them from its
// provisioned file, and the search sequence the phone then runs (spec
// pbx-gaps S-26): connect over TLS, simple bind with its own DN, search the
// base with the vendor's name filter and read the number attributes.

import (
	"crypto/tls"
	"fmt"
	"net"
	"strconv"
	"strings"

	"github.com/go-ldap/ldap/v3"
)

// LDAPConfig is the directory a phone is provisioned with.
type LDAPConfig struct {
	Host     string
	Port     int
	TLS      bool // LDAPS
	Base     string
	BindDN   string
	Password string
	MaxHits  int
	// NameFilter and NumberFilter hold % where the typed text goes.
	NameFilter, NumberFilter string
	NameAttrs, NumberAttrs   []string
}

// Hit is one directory entry a phone found.
type Hit struct {
	DN      string
	Name    string
	Numbers []string
}

func fields(s string) []string { return strings.Fields(s) }

func atoi(s string) int { n, _ := strconv.Atoi(strings.TrimSpace(s)); return n }

func ldapFromYealink(kv map[string]string) *LDAPConfig {
	if kv["ldap.enable"] != "1" {
		return nil
	}
	return &LDAPConfig{Host: kv["ldap.host"], Port: atoi(kv["ldap.port"]), TLS: kv["ldap.tls_mode"] == "2", Base: kv["ldap.base"],
		BindDN: kv["ldap.user"], Password: kv["ldap.password"], MaxHits: atoi(kv["ldap.max_hits"]),
		NameFilter: kv["ldap.name_filter"], NumberFilter: kv["ldap.number_filter"],
		NameAttrs: fields(kv["ldap.name_attr"]), NumberAttrs: fields(kv["ldap.numb_attr"])}
}

// ldapFromPoly reads dir.corp.*: Poly builds its filter from the attributes
// marked searchable, ANDed with filterPrefix.
func ldapFromPoly(a map[string]string) *LDAPConfig {
	if a["dir.corp.address"] == "" {
		return nil
	}
	c := &LDAPConfig{Host: a["dir.corp.address"], Port: atoi(a["dir.corp.port"]), TLS: a["dir.corp.transport"] == "TLS", Base: a["dir.corp.baseDN"],
		BindDN: a["dir.corp.user"], Password: a["dir.corp.password"], MaxHits: atoi(a["dir.corp.pageSize"])}
	var name, number strings.Builder
	for i := 1; i <= 16; i++ {
		k := "dir.corp.attribute." + strconv.Itoa(i)
		attr := a[k+".name"]
		if attr == "" || a[k+".searchable"] != "1" {
			continue
		}
		term := "(" + attr + "=%)"
		switch a[k+".type"] {
		case "first_name", "last_name":
			c.NameAttrs = append(c.NameAttrs, attr)
			name.WriteString(term)
		case "phone_number":
			c.NumberAttrs = append(c.NumberAttrs, attr)
			number.WriteString(term)
		}
	}
	prefix := a["dir.corp.filterPrefix"]
	c.NameFilter = "(&" + prefix + "(|" + name.String() + "))"
	c.NumberFilter = "(&" + prefix + "(|" + number.String() + "))"
	return c
}

func ldapFromGrandstream(p map[string]string) *LDAPConfig {
	if p["P8020"] == "" {
		return nil
	}
	return &LDAPConfig{Host: p["P8020"], Port: atoi(p["P8021"]), TLS: p["P8032"] == "2", Base: p["P8022"],
		BindDN: p["P8023"], Password: p["P8024"], MaxHits: atoi(p["P8028"]),
		NameFilter: p["P8030"], NumberFilter: p["P8031"], NameAttrs: fields(p["P8025"]), NumberAttrs: fields(p["P8026"])}
}

func ldapFromSnom(t map[string][]string) *LDAPConfig {
	server := first(t, "ldap_server")
	if server == "" {
		return nil
	}
	host, tlsOn := strings.CutPrefix(server, "ldaps://")
	if !tlsOn {
		host = strings.TrimPrefix(server, "ldap://")
	}
	return &LDAPConfig{Host: host, Port: atoi(first(t, "ldap_port")), TLS: tlsOn, Base: first(t, "ldap_base"),
		BindDN: first(t, "ldap_username"), Password: first(t, "ldap_password"), MaxHits: atoi(first(t, "ldap_max_hits")),
		NameFilter: first(t, "ldap_search_filter"), NumberFilter: first(t, "ldap_number_filter"),
		NameAttrs: fields(first(t, "ldap_name_attributes")), NumberAttrs: fields(first(t, "ldap_number_attributes"))}
}

func ldapFromFanvil(kv map[string]string) *LDAPConfig {
	if strings.TrimSpace(kv["LDAP Enable"]) != "1" {
		return nil
	}
	return &LDAPConfig{Host: kv["LDAP Server Address"], Port: atoi(kv["LDAP Server Port"]), TLS: strings.TrimSpace(kv["LDAP TLS"]) == "1",
		Base: kv["LDAP Base"], BindDN: kv["LDAP User Name"], Password: kv["LDAP Password"], MaxHits: atoi(kv["LDAP Max Hits"]),
		NameFilter: kv["LDAP Name Filter"], NumberFilter: kv["LDAP Number Filter"],
		NameAttrs: fields(kv["LDAP Name Attr"]), NumberAttrs: fields(kv["LDAP Number Attr"])}
}

// Search runs the phone's directory lookup: connect (LDAPS when the config
// says so, with roots as the phone's trusted CAs and the dialled address
// standing in for the provisioned host), bind with the provisioned DN and
// password, search the base for text with the name filter (or the number
// filter when byNumber) and read the name and number attributes.
func (c LDAPConfig) Search(addr string, roots *tls.Config, text string, byNumber bool) ([]Hit, error) {
	if !c.TLS {
		return nil, fmt.Errorf("the phone is provisioned for plain LDAP")
	}
	if addr == "" {
		addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	}
	conn, err := ldap.DialURL("ldaps://"+addr, ldap.DialWithTLSConfig(roots))
	if err != nil {
		return nil, fmt.Errorf("ldaps %s: %w", addr, err)
	}
	defer func() { _ = conn.Close() }()
	if err := conn.Bind(c.BindDN, c.Password); err != nil {
		return nil, fmt.Errorf("bind: %w", err)
	}
	filter := c.NameFilter
	if byNumber {
		filter = c.NumberFilter
	}
	filter = strings.ReplaceAll(filter, "%", ldap.EscapeFilter(text)+"*")
	attrs := append(append([]string{}, c.NameAttrs...), c.NumberAttrs...)
	res, err := conn.Search(ldap.NewSearchRequest(c.Base, ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, c.MaxHits, 0, false, filter, attrs, nil))
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", filter, err)
	}
	var out []Hit
	for _, e := range res.Entries {
		h := Hit{DN: e.DN}
		var parts []string
		for _, a := range c.NameAttrs {
			if v := e.GetAttributeValue(a); v != "" {
				parts = append(parts, v)
			}
		}
		h.Name = strings.Join(parts, " ")
		for _, a := range c.NumberAttrs {
			h.Numbers = append(h.Numbers, e.GetAttributeValues(a)...)
		}
		out = append(out, h)
	}
	return out, nil
}
