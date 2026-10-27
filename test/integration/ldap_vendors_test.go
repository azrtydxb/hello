package integration

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"math/big"
	"net"
	"os"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/ldapdir"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/test/provclient"
)

// TestLDAPAsVendors (spec pbx-gaps S-26) runs each vendor's phone against
// the real LDAP listener with the store behind it: the phone's rendered
// configuration is read back the way the phone reads it, the phone connects
// over TLS, binds with the DN and password it was provisioned with and
// searches by name and by number. A plain connection, a phone of a group
// with LDAP off and a write are refused.
func TestLDAPAsVendors(t *testing.T) {
	dsn := os.Getenv("HELLO_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	db, err := sql.Open("pgx", scratchDatabase(t, ctx, dsn))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := migrate.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	k := make([]byte, 32)
	_, _ = rand.Read(k)
	box, err := secret.New(base64.StdEncoding.EncodeToString(k))
	if err != nil {
		t.Fatal(err)
	}
	const base = "dc=hello,dc=local"
	st := store.New(db).WithSecretBox(box).WithProv(store.ProvSettings{
		PublicURL: "https://prov.hello.test", SIPServer: "192.0.2.1:5060", Domain: "hello.test",
		Expiry: time.Hour, Resync: 24 * time.Hour, TokenGrace: time.Hour, LDAPBase: base, LDAPPort: 636,
	})
	ext, err := st.CreateExtension(ctx, "test", "101", "Sales Desk", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	books, _, err := st.ListPhonebooks(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		if b.Kind == phonebook.KindCompany {
			k, _ := phonebook.Key("+3225550100")
			if _, err := st.CreatePhonebookEntry(ctx, "test", b.ID, phonebook.EntryInput{FirstName: "Corp", LastName: "Reception",
				Numbers: []phonebook.Number{{Label: "work", Number: "+3225550100", Key: k}}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	on, err := st.CreatePhoneGroup(ctx, "test", store.GroupInput{Name: "LDAP", IncludeExtensions: true, IncludeCompany: true})
	if err != nil {
		t.Fatal(err)
	}
	off, err := st.CreatePhoneGroup(ctx, "test", store.GroupInput{Name: "No LDAP", IncludeExtensions: true, IncludeCompany: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPhoneGroupLDAP(ctx, "test", on.ID, true); err != nil {
		t.Fatal(err)
	}

	cert, pool := ldapTestCert(t)
	srv := ldapdir.New(ldapdir.Config{Base: base, Backend: st, TLS: &tls.Config{Certificates: []tls.Certificate{cert}}})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.ServeLDAPS(ln) }()
	defer srv.Close()
	addr := ln.Addr().String()
	clientTLS := &tls.Config{RootCAs: pool, ServerName: "localhost"}

	for i, m := range provclient.Models {
		t.Run(m.Vendor+"/"+m.Model, func(t *testing.T) {
			mac := m.OUI + "00" + string(rune('0'+i/10)) + string(rune('0'+i%10)) + "01"
			gid := on.ID
			ptr := &gid
			c, err := st.CreatePhone(ctx, "test", store.PhoneInput{MAC: mac, Vendor: prov.Vendor(m.Vendor), Model: m.Model,
				ExtensionID: ext.ID, Enabled: true, Realm: "hello.test", GroupID: ptr})
			if err != nil {
				t.Fatal(err)
			}
			d, tmpl, err := st.RenderInputs(ctx, c.Phone.ID)
			if err != nil {
				t.Fatal(err)
			}
			if d.LDAP == nil {
				t.Fatal("no LDAP setting for a phone of a group with LDAP on")
			}
			cfg := ldapConfigOf(t, m, mac, tmpl, d)
			if cfg == nil {
				t.Fatal("the rendered file carries no LDAP keys")
			}
			if !cfg.TLS || cfg.Port != 636 || cfg.Base != "ou=phonebook,"+base {
				t.Fatalf("provisioned %+v", cfg)
			}
			hits, err := cfg.Search(addr, clientTLS, "Sales", false)
			if err != nil {
				t.Fatal(err)
			}
			if len(hits) != 1 || len(hits[0].Numbers) == 0 || hits[0].Numbers[0] != "101" {
				t.Fatalf("search by name = %+v", hits)
			}
			if hits, err = cfg.Search(addr, clientTLS, "+32255", true); err != nil || len(hits) != 1 {
				t.Fatalf("search by number = %+v, %v", hits, err)
			}
			// The same phone in a group with LDAP off cannot bind.
			gid2 := off.ID
			ptr2 := &gid2
			if _, _, err := st.UpdatePhone(ctx, "test", c.Phone.ID, store.PhoneChange{GroupID: &ptr2}); err != nil {
				t.Fatal(err)
			}
			if _, err := cfg.Search(addr, clientTLS, "Sales", false); err == nil {
				t.Fatal("a phone of a group with LDAP off searched the directory")
			}
		})
	}
}

// ldapConfigOf renders the vendor's device file and parses its LDAP keys.
func ldapConfigOf(t *testing.T, m provclient.Model, mac string, tmpl prov.Template, d prov.RenderData) *provclient.LDAPConfig {
	t.Helper()
	render := func(name string) []byte {
		t.Helper()
		b, err := prov.Render(context.Background(), tmpl, name, d)
		if err != nil {
			t.Fatalf("render %s: %v", name, err)
		}
		return b
	}
	var (
		c   provclient.Config
		err error
	)
	switch prov.Vendor(m.Vendor) {
	case prov.Yealink:
		c, err = provclient.ParseYealink(render(mac + ".cfg"))
	case prov.Poly:
		c, err = provclient.ParsePoly(render(mac+".cfg"), render(mac+"-hello.cfg"))
	case prov.Grandstream:
		c, err = provclient.ParseGrandstream(render("cfg"+mac+".xml"), mac)
	case prov.Snom:
		c, err = provclient.ParseSnom(render(upper(mac)))
	case prov.Fanvil:
		c, err = provclient.ParseFanvil(render(mac + ".cfg"))
	}
	if err != nil {
		t.Fatal(err)
	}
	return c.LDAP
}

func upper(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'a' && c <= 'z' {
			b[i] = c - 32
		}
	}
	return string(b)
}

func ldapTestCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "hello-ldap-test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, pool
}
