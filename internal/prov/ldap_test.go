package prov

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/test/provclient"
)

// ldapFiles renders the device file of a vendor's phone and parses its LDAP
// setting back the way the phone reads it.
func ldapFiles(t *testing.T, v Vendor, model string, d RenderData) (*provclient.LDAPConfig, []byte) {
	t.Helper()
	d.Phone.Vendor, d.Phone.Model = v, model
	tmpl := builtinFor(t, v)
	render := func(name string) []byte {
		t.Helper()
		b, err := Render(context.Background(), tmpl, name, d)
		if err != nil {
			t.Fatalf("%s %s: %v", v, name, err)
		}
		return b
	}
	var (
		c    provclient.Config
		err  error
		body []byte
	)
	switch v {
	case Yealink:
		body = render(d.Phone.MAC + ".cfg")
		c, err = provclient.ParseYealink(body)
	case Poly:
		body = render(d.Phone.MAC + "-hello.cfg")
		c, err = provclient.ParsePoly(render(d.Phone.MAC+".cfg"), body)
	case Grandstream:
		body = render("cfg" + d.Phone.MAC + ".xml")
		c, err = provclient.ParseGrandstream(body, d.Phone.MAC)
	case Snom:
		body = render(d.Phone.MACUpper)
		c, err = provclient.ParseSnom(body)
	case Fanvil:
		body = render(d.Phone.MAC + ".cfg")
		c, err = provclient.ParseFanvil(body)
	}
	if err != nil {
		t.Fatalf("%s: parse: %v", v, err)
	}
	return c.LDAP, body
}

// TestLDAPKeys: each vendor's built-in template writes its LDAP keys only
// when the phone has an LDAP setting, with the host, port, TLS, base, the
// phone's own bind DN and password, and the attribute and filter mapping the
// server serves; and writes none for a phone with no setting.
func TestLDAPKeys(t *testing.T) {
	models := map[Vendor]string{Yealink: "T54W", Poly: "VVX 450", Grandstream: "GRP2614", Snom: "D785", Fanvil: "X5U"}
	const pw = `ld<ap>&"p'w0rd`
	for _, v := range Vendors[:5] {
		t.Run(string(v), func(t *testing.T) {
			if !SupportsLDAP(v, models[v]) {
				t.Fatalf("%s has no LDAP support", v)
			}
			d := renderSample(false)
			if got, _ := ldapFiles(t, v, models[v], d); got != nil {
				t.Fatalf("LDAP keys without a setting: %+v", got)
			}
			d.LDAP = NewLDAPInfo("prov.hello.test", 636, "dc=hello,dc=local", "uid=42,ou=phones,dc=hello,dc=local", pw)
			got, body := ldapFiles(t, v, models[v], d)
			if got == nil {
				t.Fatalf("no LDAP keys written\n%s", redact(body))
			}
			want := &provclient.LDAPConfig{
				Host: "prov.hello.test", Port: 636, TLS: true, Base: "ou=phonebook,dc=hello,dc=local",
				BindDN: "uid=42,ou=phones,dc=hello,dc=local", Password: pw, MaxHits: LDAPMaxHits,
			}
			// Poly builds its filters from the searchable attributes, behind filterPrefix.
			if v != Poly {
				want.NameFilter, want.NumberFilter = "(|(cn=%)(sn=%))", "(|(telephoneNumber=%)(mobile=%)(homePhone=%))"
				want.NameAttrs, want.NumberAttrs = []string{"cn", "sn"}, []string{"telephoneNumber", "mobile", "homePhone"}
			} else {
				want.NameFilter, want.NumberFilter = "(&(telephoneNumber=*)(|(cn=%)(sn=%)))", "(&(telephoneNumber=*)(|(telephoneNumber=%)(mobile=%)))"
				want.NameAttrs, want.NumberAttrs = []string{"cn", "sn"}, []string{"telephoneNumber", "mobile"}
			}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("parsed LDAP setting:\n got %+v\nwant %+v", got, want)
			}
			// A model with no LDAP setting (the store sets none) writes none, whatever the vendor.
			d.LDAP = nil
			if got, _ := ldapFiles(t, v, models[v], d); got != nil {
				t.Errorf("LDAP keys for a phone with no setting: %+v", got)
			}
		})
	}
	if SupportsLDAP(Generic, "MODEL") {
		t.Error("a generic phone has no built-in LDAP template")
	}
}

func redact(b []byte) string {
	return strings.ReplaceAll(string(b), "s3cr&amp;t", "****")
}

func TestNewLDAPInfo(t *testing.T) {
	if NewLDAPInfo("", 636, "dc=x", "uid=1", "pw") != nil || NewLDAPInfo("h", 636, "dc=x", "", "pw") != nil || NewLDAPInfo("h", 636, "dc=x", "uid=1", "") != nil {
		t.Fatal("an incomplete setting must be nil")
	}
	l := NewLDAPInfo("h", 0, "dc=x", "uid=1", "pw")
	if l.Port != 636 || l.Base != "ou=phonebook,dc=x" || l.MaxHits != 200 {
		t.Fatalf("%+v", l)
	}
	if bytes.Contains([]byte(l.Base), []byte("uid")) {
		t.Fatal("the search base carries a bind DN")
	}
}
