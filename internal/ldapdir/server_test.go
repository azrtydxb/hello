package ldapdir

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
	provbook "github.com/azrtydxb/hello/internal/prov/phonebook"
	"github.com/go-ldap/ldap/v3"
)

const testBase = "dc=hello,dc=local"

// fakeBackend serves two phones with different sets.
type fakeBackend struct {
	mu      sync.Mutex
	pw      map[int64]string
	books   map[int64][]provbook.Book
	version map[int64]string
	down    bool
	auths   int
}

func (f *fakeBackend) Authenticate(_ context.Context, id int64, pw string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.auths++
	if f.down {
		return false, fmt.Errorf("db down")
	}
	want, ok := f.pw[id]
	return ok && want == pw, nil
}

func (f *fakeBackend) LDAPVersion(_ context.Context, id int64) (prov.BooksVersion, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return prov.BooksVersion{}, fmt.Errorf("db down")
	}
	if _, ok := f.books[id]; !ok {
		return prov.BooksVersion{}, ErrNotFound
	}
	return prov.BooksVersion{Key: f.version[id], Personal: id == 2}, nil
}

func (f *fakeBackend) LDAPBooks(_ context.Context, id int64) ([]provbook.Book, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		return nil, fmt.Errorf("db down")
	}
	b, ok := f.books[id]
	if !ok {
		return nil, ErrNotFound
	}
	return b, nil
}

func contact(first, last, company string, nums ...string) phonebook.EntryInput {
	e := phonebook.EntryInput{FirstName: first, LastName: last, Company: company}
	labels := []string{"work", "mobile", "home", "other"}
	for i, n := range nums {
		e.Numbers = append(e.Numbers, phonebook.Number{Label: labels[i], Number: n, Key: n})
	}
	return e
}

func newBackend() *fakeBackend {
	return &fakeBackend{
		pw: map[int64]string{1: "pw-one", 2: "pw-two"},
		books: map[int64][]provbook.Book{
			1: {{Name: "Extensions", Entries: []phonebook.EntryInput{
				contact("Alice", "Anders", "Acme", "101", "0471000001"),
				contact("Bob", "Brown", "", "102"),
				contact("", "", "Front Desk", "100"),
			}}},
			2: {{Name: "Extensions", Entries: []phonebook.EntryInput{contact("Alice", "Anders", "Acme", "101")}},
				{Name: "Personal", Entries: []phonebook.EntryInput{contact("Carl", "Private", "", "0499 12-34")}}},
		},
		version: map[int64]string{1: "v1", 2: "v2"},
	}
}

type memCache struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (c *memCache) Get(_ context.Context, k string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[k]
	return b, ok
}

func (c *memCache) Set(_ context.Context, k string, b []byte, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string][]byte{}
	}
	c.m[k] = b
}

func testCert(t testing.TB) (tls.Certificate, *x509.CertPool) {
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

type rig struct {
	srv      *Server
	be       *fakeBackend
	ldaps    string
	starttls string
	pool     *x509.CertPool
}

func newRig(t *testing.T, mut func(*Config)) *rig {
	t.Helper()
	cert, pool := testCert(t)
	be := newBackend()
	cfg := Config{Base: testBase, TLS: &tls.Config{Certificates: []tls.Certificate{cert}}, Backend: be, Cache: &memCache{}}
	if mut != nil {
		mut(&cfg)
	}
	srv := New(cfg)
	r := &rig{srv: srv, be: be, pool: pool}
	for _, implicit := range []bool{true, false} {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		if implicit {
			r.ldaps = ln.Addr().String()
			go func() { _ = srv.ServeLDAPS(ln) }()
		} else {
			r.starttls = ln.Addr().String()
			go func() { _ = srv.ServeStartTLS(ln) }()
		}
	}
	t.Cleanup(srv.Close)
	return r
}

func (r *rig) dialTLS(t *testing.T) *ldap.Conn {
	t.Helper()
	c, err := ldap.DialURL("ldaps://"+r.ldaps, ldap.DialWithTLSConfig(&tls.Config{RootCAs: r.pool, ServerName: "localhost"}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func (r *rig) dialPlain(t *testing.T) *ldap.Conn {
	t.Helper()
	c, err := ldap.DialURL("ldap://" + r.starttls)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func wantCode(t *testing.T, err error, code uint16, what string) {
	t.Helper()
	if !ldap.IsErrorWithCode(err, code) {
		t.Fatalf("%s: err = %v, want LDAP result %d", what, err, code)
	}
}

func phoneDN(id int64) string { return PhoneDN(testBase, id) }

func TestLDAPBind(t *testing.T) {
	t.Run("anonymous and unauthenticated binds are refused", func(t *testing.T) {
		r := newRig(t, nil)
		c := r.dialTLS(t)
		wantCode(t, c.UnauthenticatedBind(""), ResultUnwillingToPerform, "anonymous")
		wantCode(t, c.UnauthenticatedBind(phoneDN(1)), ResultUnwillingToPerform, "unauthenticated")
	})
	t.Run("anonymous allowed by HELLO_LDAP_ANON", func(t *testing.T) {
		r := newRig(t, func(c *Config) { c.Anon = true })
		c := r.dialTLS(t)
		if err := c.UnauthenticatedBind(""); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a phone DN binds", func(t *testing.T) {
		r := newRig(t, nil)
		c := r.dialTLS(t)
		if err := c.Bind(phoneDN(1), "pw-one"); err != nil {
			t.Fatal(err)
		}
		// DN spacing and case do not matter.
		if err := c.Bind("UID=1, ou=Phones , DC=hello, dc=LOCAL", "pw-one"); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("a wrong DN or password is invalid credentials", func(t *testing.T) {
		r := newRig(t, nil)
		c := r.dialTLS(t)
		wantCode(t, c.Bind(phoneDN(1), "nope"), ResultInvalidCredentials, "bad password")
		wantCode(t, c.Bind(phoneDN(9), "pw-one"), ResultInvalidCredentials, "unknown phone")
		wantCode(t, c.Bind("cn=admin,"+testBase, "pw-one"), ResultInvalidCredentials, "other DN")
		wantCode(t, c.Bind("uid=01,ou=phones,"+testBase, "pw-one"), ResultInvalidCredentials, "non-canonical id")
	})
	t.Run("a bad password is limited to 5 per DN", func(t *testing.T) {
		r := newRig(t, nil)
		c := r.dialTLS(t)
		for i := 0; i < BindFailLimit; i++ {
			wantCode(t, c.Bind(phoneDN(1), "bad"), ResultInvalidCredentials, "failure")
		}
		wantCode(t, c.Bind(phoneDN(1), "pw-one"), ResultBusy, "right password while blocked")
		r.be.mu.Lock()
		n := r.be.auths
		r.be.mu.Unlock()
		if n != BindFailLimit {
			t.Fatalf("backend asked %d times, want %d (blocked binds must not reach it)", n, BindFailLimit)
		}
	})
	t.Run("the limit is per source address too", func(t *testing.T) {
		r := newRig(t, nil)
		c := r.dialTLS(t)
		// Five different DNs from one address.
		for i := 0; i < BindFailLimit; i++ {
			wantCode(t, c.Bind(phoneDN(int64(10+i)), "bad"), ResultInvalidCredentials, "failure")
		}
		wantCode(t, c.Bind(phoneDN(1), "pw-one"), ResultBusy, "good phone from the blocked address")
	})
	t.Run("a bind that is not version 3 or not simple is refused", func(t *testing.T) {
		r := newRig(t, nil)
		c := r.dialTLS(t)
		_, err := c.SimpleBind(&ldap.SimpleBindRequest{Username: phoneDN(1), Password: "pw-one", AllowEmptyPassword: false})
		if err != nil {
			t.Fatal(err)
		}
		wantCode(t, c.ExternalBind(), ResultAuthMethodNotSupported, "SASL")
	})
	t.Run("plain LDAP is refused, StartTLS works", func(t *testing.T) {
		r := newRig(t, nil)
		c := r.dialPlain(t)
		wantCode(t, c.Bind(phoneDN(1), "pw-one"), ResultUnwillingToPerform, "plain bind")
		_, err := c.Search(ldap.NewSearchRequest(testBase, ldap.ScopeWholeSubtree, 0, 0, 0, false, "(cn=*)", nil, nil))
		wantCode(t, err, ResultUnwillingToPerform, "plain search")
		wantCode(t, c.Add(ldap.NewAddRequest("cn=x,"+testBase, nil)), ResultUnwillingToPerform, "plain add")
		// The Root DSE is readable in the clear.
		res, err := c.Search(ldap.NewSearchRequest("", ldap.ScopeBaseObject, 0, 0, 0, false, "(objectClass=*)", []string{"*"}, nil))
		if err != nil || len(res.Entries) != 1 {
			t.Fatalf("root DSE: %v %v", res, err)
		}
		dse := res.Entries[0]
		if dse.GetAttributeValue("vendorName") != "Hello" || dse.GetAttributeValue("supportedLDAPVersion") != "3" ||
			dse.GetAttributeValue("supportedControl") != oidPagedResults || dse.GetAttributeValue("supportedExtension") != oidStartTLS ||
			dse.GetAttributeValue("namingContexts") != testBase {
			t.Fatalf("root DSE = %+v", dse.Attributes)
		}
		if err := c.StartTLS(&tls.Config{RootCAs: r.pool, ServerName: "localhost"}); err != nil {
			t.Fatal(err)
		}
		if err := c.Bind(phoneDN(1), "pw-one"); err != nil {
			t.Fatal(err)
		}
		if res, err := c.Search(ldap.NewSearchRequest(testBase, ldap.ScopeWholeSubtree, 0, 0, 0, false, "(sn=Anders)", nil, nil)); err != nil || len(res.Entries) != 1 {
			t.Fatalf("search after StartTLS: %v %v", res, err)
		}
	})
	t.Run("TLS older than 1.2 is refused", func(t *testing.T) {
		r := newRig(t, nil)
		_, err := ldap.DialURL("ldaps://"+r.ldaps, ldap.DialWithTLSConfig(&tls.Config{RootCAs: r.pool, ServerName: "localhost", MaxVersion: tls.VersionTLS11}))
		if err == nil {
			t.Fatal("a TLS 1.1 client connected")
		}
	})
	t.Run("StartTLS on a TLS connection is refused", func(t *testing.T) {
		r := newRig(t, nil)
		c := r.dialTLS(t)
		if err := c.StartTLS(&tls.Config{RootCAs: r.pool, ServerName: "localhost"}); err == nil {
			t.Fatal("StartTLS inside TLS succeeded")
		}
	})
	t.Run("the directory being down is unavailable, not invalid credentials", func(t *testing.T) {
		r := newRig(t, nil)
		r.be.down = true
		c := r.dialTLS(t)
		wantCode(t, c.Bind(phoneDN(1), "pw-one"), ResultUnavailable, "db down")
	})
}

func searchAll(t *testing.T, c *ldap.Conn, base string, scope int, filter string, attrs ...string) (*ldap.SearchResult, error) {
	t.Helper()
	return c.Search(ldap.NewSearchRequest(base, scope, ldap.NeverDerefAliases, 0, 0, false, filter, attrs, nil))
}

func names(res *ldap.SearchResult) []string {
	var out []string
	for _, e := range res.Entries {
		out = append(out, e.GetAttributeValue("cn"))
	}
	return out
}

func TestLDAPSearchScope(t *testing.T) {
	r := newRig(t, nil)
	c1 := r.dialTLS(t)
	if err := c1.Bind(phoneDN(1), "pw-one"); err != nil {
		t.Fatal(err)
	}
	pb := "ou=phonebook," + testBase

	t.Run("a phone sees its own set, sorted like the XML directory", func(t *testing.T) {
		res, err := searchAll(t, c1, pb, ldap.ScopeSingleLevel, "(objectClass=inetOrgPerson)")
		if err != nil {
			t.Fatal(err)
		}
		// The company-only entry is listed under its company, sorted by it.
		if got := strings.Join(names(res), ","); got != "Alice Anders,Bob Brown,Front Desk" {
			t.Fatalf("names = %q", got)
		}
		for _, e := range res.Entries {
			if !strings.HasSuffix(e.DN, ","+pb) || !strings.HasPrefix(e.DN, "cn=") {
				t.Fatalf("DN %q", e.DN)
			}
			if strings.Contains(strings.ToLower(e.DN), "alice") || strings.Contains(strings.ToLower(e.DN), "anders") {
				t.Fatalf("the DN carries the name: %q", e.DN)
			}
		}
	})
	t.Run("attributes", func(t *testing.T) {
		res, err := searchAll(t, c1, testBase, ldap.ScopeWholeSubtree, "(sn=anders)")
		if err != nil || len(res.Entries) != 1 {
			t.Fatalf("%v %v", res, err)
		}
		e := res.Entries[0]
		for attr, want := range map[string]string{"cn": "Alice Anders", "givenName": "Alice", "sn": "Anders", "displayName": "Alice Anders",
			"o": "Acme", "telephoneNumber": "101", "mobile": "0471000001"} {
			if got := e.GetAttributeValue(attr); got != want {
				t.Errorf("%s = %q, want %q", attr, got, want)
			}
		}
		if oc := e.GetAttributeValues("objectClass"); len(oc) != 4 || oc[3] != "inetOrgPerson" {
			t.Errorf("objectClass = %v", oc)
		}
		if len(e.Attributes) != 8+0 && len(e.Attributes) != 8 {
			t.Errorf("attributes = %d", len(e.Attributes))
		}
		// A company-only entry is listed under its company.
		res, err = searchAll(t, c1, testBase, ldap.ScopeWholeSubtree, "(telephoneNumber=100)")
		if err != nil || len(res.Entries) != 1 || res.Entries[0].GetAttributeValue("sn") != "Front Desk" {
			t.Fatalf("company entry: %v %v", res, err)
		}
	})
	t.Run("requested attributes only, and hidden ones never", func(t *testing.T) {
		res, err := searchAll(t, c1, testBase, ldap.ScopeWholeSubtree, "(sn=Anders)", "cn", "userPassword", "uid", "mobile")
		if err != nil || len(res.Entries) != 1 {
			t.Fatalf("%v %v", res, err)
		}
		var got []string
		for _, a := range res.Entries[0].Attributes {
			got = append(got, a.Name)
		}
		if strings.Join(got, ",") != "cn,mobile" {
			t.Fatalf("attributes = %v", got)
		}
		res, err = searchAll(t, c1, testBase, ldap.ScopeWholeSubtree, "(userPassword=*)", "*", "+")
		if err != nil || len(res.Entries) != 0 {
			t.Fatalf("a filter on a hidden attribute matched: %v %v", res, err)
		}
		res, err = searchAll(t, c1, testBase, ldap.ScopeWholeSubtree, "(uid=*)")
		if err != nil || len(res.Entries) != 0 {
			t.Fatalf("uid matched: %v %v", res, err)
		}
	})
	t.Run("filters", func(t *testing.T) {
		for filter, want := range map[string]int{
			"(cn=*)": 3, "(cn=alice*)": 1, "(cn=*brown)": 1, "(cn=*lic*)": 1, "(!(cn=Alice Anders))": 2 + 2, // + two containers
			"(&(sn=anders)(o=acme))": 1, "(|(sn=anders)(sn=brown))": 2, "(telephoneNumber~=101)": 1,
			"(telephoneNumber=1*)": 3, "(sn>=a)": 0, "(&(sn=anders)(!(mobile=*)))": 0, "(cn=*a*i*e*)": 1,
		} {
			res, err := searchAll(t, c1, pb, ldap.ScopeSingleLevel, filter)
			if err != nil {
				t.Fatalf("%s: %v", filter, err)
			}
			if filter == "(!(cn=Alice Anders))" {
				want = 2
			}
			if len(res.Entries) != want {
				t.Errorf("%s: %d entries, want %d (%v)", filter, len(res.Entries), want, names(res))
			}
		}
	})
	t.Run("phone numbers match without separators", func(t *testing.T) {
		c2 := r.dialTLS(t)
		if err := c2.Bind(phoneDN(2), "pw-two"); err != nil {
			t.Fatal(err)
		}
		res, err := searchAll(t, c2, testBase, ldap.ScopeWholeSubtree, "(telephoneNumber=049912*34)")
		if err != nil || len(res.Entries) != 1 {
			t.Fatalf("%v %v", res, err)
		}
	})
	t.Run("another phone sees its own set, and a personal book only its own", func(t *testing.T) {
		c2 := r.dialTLS(t)
		if err := c2.Bind(phoneDN(2), "pw-two"); err != nil {
			t.Fatal(err)
		}
		res, err := searchAll(t, c2, pb, ldap.ScopeSingleLevel, "(cn=*)")
		if err != nil || len(res.Entries) != 2 {
			t.Fatalf("%v %v", res, err)
		}
		res, err = searchAll(t, c1, pb, ldap.ScopeSingleLevel, "(cn=carl*)")
		if err != nil || len(res.Entries) != 0 {
			t.Fatalf("phone 1 saw phone 2's personal book: %v %v", res, err)
		}
		// Phone 2's entry DN is not readable by phone 1.
		res2, _ := searchAll(t, c2, pb, ldap.ScopeSingleLevel, "(cn=carl*)")
		if len(res2.Entries) != 1 {
			t.Fatal("phone 2 lost its personal contact")
		}
		_, err = searchAll(t, c1, res2.Entries[0].DN, ldap.ScopeBaseObject, "(objectClass=*)")
		if err != nil {
			wantCode(t, err, ResultNoSuchObject, "foreign entry")
		}
	})
	t.Run("scopes", func(t *testing.T) {
		res, err := searchAll(t, c1, testBase, ldap.ScopeBaseObject, "(objectClass=*)")
		if err != nil || len(res.Entries) != 1 || res.Entries[0].DN != testBase {
			t.Fatalf("base: %v %v", res, err)
		}
		res, err = searchAll(t, c1, testBase, ldap.ScopeSingleLevel, "(objectClass=*)")
		if err != nil || len(res.Entries) != 1 || res.Entries[0].DN != pb {
			t.Fatalf("single on base: %v %v", res, err)
		}
		res, err = searchAll(t, c1, testBase, ldap.ScopeWholeSubtree, "(objectClass=*)")
		if err != nil || len(res.Entries) != 5 {
			t.Fatalf("subtree: %d %v", len(res.Entries), err)
		}
		one, _ := searchAll(t, c1, pb, ldap.ScopeSingleLevel, "(sn=Anders)")
		res, err = searchAll(t, c1, one.Entries[0].DN, ldap.ScopeBaseObject, "(objectClass=*)")
		if err != nil || len(res.Entries) != 1 {
			t.Fatalf("leaf base: %v %v", res, err)
		}
		res, err = searchAll(t, c1, one.Entries[0].DN, ldap.ScopeSingleLevel, "(objectClass=*)")
		if err != nil || len(res.Entries) != 0 {
			t.Fatalf("leaf single: %v %v", res, err)
		}
		_, err = searchAll(t, c1, "dc=other,dc=org", ldap.ScopeWholeSubtree, "(cn=*)")
		wantCode(t, err, ResultNoSuchObject, "other base")
		_, err = searchAll(t, c1, "ou=phones,"+testBase, ldap.ScopeWholeSubtree, "(cn=*)")
		wantCode(t, err, ResultNoSuchObject, "phones container is not readable")
	})
	t.Run("a search before bind is refused", func(t *testing.T) {
		c := r.dialTLS(t)
		_, err := searchAll(t, c, testBase, ldap.ScopeWholeSubtree, "(cn=*)")
		wantCode(t, err, ResultInsufficientAccess, "unbound")
	})
	t.Run("writes are refused", func(t *testing.T) {
		dn := "cn=x," + pb
		wantCode(t, c1.Add(ldap.NewAddRequest(dn, nil)), ResultUnwillingToPerform, "add")
		wantCode(t, c1.Modify(ldap.NewModifyRequest(dn, nil)), ResultUnwillingToPerform, "modify")
		wantCode(t, c1.Del(ldap.NewDelRequest(dn, nil)), ResultUnwillingToPerform, "delete")
		wantCode(t, c1.ModifyDN(ldap.NewModifyDNRequest(dn, "cn=y", true, "")), ResultUnwillingToPerform, "modifyDN")
		_, err := c1.Compare(dn, "cn", "x")
		wantCode(t, err, ResultUnwillingToPerform, "compare")
	})
	t.Run("size limit", func(t *testing.T) {
		r.be.mu.Lock()
		var es []phonebook.EntryInput
		for i := 0; i < 450; i++ {
			es = append(es, contact("P", fmt.Sprintf("Person%04d", i), "", fmt.Sprintf("%d", 3000+i)))
		}
		r.be.books[1] = []provbook.Book{{Name: "Big", Entries: es}}
		r.be.version[1] = "big"
		r.be.mu.Unlock()
		res, err := searchAll(t, c1, pb, ldap.ScopeSingleLevel, "(cn=*)")
		wantCode(t, err, ResultSizeLimitExceeded, "size limit")
		if res == nil || len(res.Entries) != MaxSize {
			t.Fatalf("entries = %v, want the first %d", res, MaxSize)
		}
		req := ldap.NewSearchRequest(pb, ldap.ScopeSingleLevel, 0, 20, 0, false, "(cn=*)", nil, nil)
		res, err = c1.Search(req)
		wantCode(t, err, ResultSizeLimitExceeded, "client size limit")
		if len(res.Entries) != 20 {
			t.Fatalf("client limit gave %d", len(res.Entries))
		}
	})
	t.Run("paged results", func(t *testing.T) {
		req := ldap.NewSearchRequest(pb, ldap.ScopeSingleLevel, 0, 0, 0, false, "(cn=*)", []string{"cn"}, nil)
		res, err := c1.SearchWithPaging(req, 70)
		if err != nil || len(res.Entries) != 450 {
			t.Fatalf("paged: %d entries, %v", len(res.Entries), err)
		}
		seen := map[string]bool{}
		for _, e := range res.Entries {
			if seen[e.DN] {
				t.Fatalf("duplicate %s", e.DN)
			}
			seen[e.DN] = true
		}
		// A page size over the maximum is cut to 100.
		req = ldap.NewSearchRequest(pb, ldap.ScopeSingleLevel, 0, 0, 0, false, "(cn=*)", []string{"cn"}, nil)
		res, err = c1.SearchWithPaging(req, 1000)
		if err != nil || len(res.Entries) != 450 {
			t.Fatalf("large pages: %v, %v", res, err)
		}
		// A cookie from another search is refused.
		req = ldap.NewSearchRequest(pb, ldap.ScopeSingleLevel, 0, 0, 0, false, "(cn=*)", []string{"cn"}, []ldap.Control{ldap.NewControlPaging(10)})
		sr, err := c1.Search(req)
		if err != nil {
			t.Fatal(err)
		}
		cookie := ldap.FindControl(sr.Controls, ldap.ControlTypePaging).(*ldap.ControlPaging).Cookie
		other := ldap.NewSearchRequest(pb, ldap.ScopeSingleLevel, 0, 0, 0, false, "(sn=*1)", nil, []ldap.Control{&ldap.ControlPaging{PagingSize: 10, Cookie: cookie}})
		_, err = c1.Search(other)
		wantCode(t, err, ResultUnwillingToPerform, "foreign cookie")
	})
	t.Run("filter limits", func(t *testing.T) {
		deep := "(cn=a)"
		for i := 0; i < maxFilterDepth; i++ {
			deep = "(!" + deep + ")"
		}
		_, err := searchAll(t, c1, pb, ldap.ScopeSingleLevel, deep)
		wantCode(t, err, ResultAdminLimitExceeded, "depth 11")
		ok := "(cn=a)"
		for i := 0; i < maxFilterDepth-1; i++ {
			ok = "(!" + ok + ")"
		}
		if _, err := searchAll(t, c1, pb, ldap.ScopeSingleLevel, ok); err != nil && !ldap.IsErrorWithCode(err, ResultSizeLimitExceeded) {
			t.Fatalf("depth 10: %v", err)
		}
		wide := "(|" + strings.Repeat("(cn=a)", maxFilterElements) + ")" // 51 elements with the OR
		_, err = searchAll(t, c1, pb, ldap.ScopeSingleLevel, wide)
		wantCode(t, err, ResultAdminLimitExceeded, "51 elements")
		wide = "(|" + strings.Repeat("(cn=a)", maxFilterElements-1) + ")"
		if _, err := searchAll(t, c1, pb, ldap.ScopeSingleLevel, wide); err != nil {
			t.Fatalf("50 elements: %v", err)
		}
	})
}

func TestLDAPAnonymousSeesNothingOfAPhone(t *testing.T) {
	r := newRig(t, func(c *Config) { c.Anon = true })
	r.be.books[0] = []provbook.Book{{Name: "Extensions", Entries: []phonebook.EntryInput{contact("Pub", "Lic", "", "900")}}}
	r.be.version[0] = "pub"
	c := r.dialTLS(t)
	if err := c.UnauthenticatedBind(""); err != nil {
		t.Fatal(err)
	}
	res, err := searchAll(t, c, testBase, ldap.ScopeWholeSubtree, "(sn=*)")
	if err != nil || len(res.Entries) != 1 || res.Entries[0].GetAttributeValue("sn") != "Lic" {
		t.Fatalf("anonymous: %v %v", res, err)
	}
}

func TestLDAPCachedAndLastRender(t *testing.T) {
	r := newRig(t, nil)
	c := r.dialTLS(t)
	if err := c.Bind(phoneDN(1), "pw-one"); err != nil {
		t.Fatal(err)
	}
	if res, err := searchAll(t, c, testBase, ldap.ScopeWholeSubtree, "(sn=anders)"); err != nil || len(res.Entries) != 1 {
		t.Fatalf("%v %v", res, err)
	}
	// PostgreSQL goes away: the last rendered set answers.
	r.be.mu.Lock()
	r.be.down = true
	r.be.mu.Unlock()
	if res, err := searchAll(t, c, testBase, ldap.ScopeWholeSubtree, "(sn=anders)"); err != nil || len(res.Entries) != 1 {
		t.Fatalf("with the database down: %v %v", res, err)
	}
	// A phone that never rendered has nothing to fall back on.
	c2 := r.dialTLS(t)
	r.be.mu.Lock()
	r.be.down = false
	r.be.mu.Unlock()
	if err := c2.Bind(phoneDN(2), "pw-two"); err != nil {
		t.Fatal(err)
	}
	r.be.mu.Lock()
	r.be.down = true
	r.be.mu.Unlock()
	_, err := searchAll(t, c2, testBase, ldap.ScopeWholeSubtree, "(sn=anders)")
	wantCode(t, err, ResultUnavailable, "no render")
}

func TestFilterParseLimits(t *testing.T) {
	if NormDN(" UID=7 , OU=Phones,DC=Hello ") != "uid=7,ou=phones,dc=hello" {
		t.Fatal(NormDN(" UID=7 , OU=Phones,DC=Hello "))
	}
}

func BenchmarkSearch5000(b *testing.B) {
	cert, pool := testCert(b)
	be := newBackend()
	var es []phonebook.EntryInput
	for i := 0; i < 5000; i++ {
		es = append(es, contact("First", fmt.Sprintf("Last%05d", i), "Co", fmt.Sprintf("%d", 100000+i), fmt.Sprintf("04%08d", i)))
	}
	be.books[1] = []provbook.Book{{Name: "Big", Entries: es}}
	srv := New(Config{Base: testBase, TLS: &tls.Config{Certificates: []tls.Certificate{cert}}, Backend: be, Cache: &memCache{}})
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { _ = srv.ServeLDAPS(ln) }()
	defer srv.Close()
	c, err := ldap.DialURL("ldaps://"+ln.Addr().String(), ldap.DialWithTLSConfig(&tls.Config{RootCAs: pool, ServerName: "localhost"}))
	if err != nil {
		b.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Bind(phoneDN(1), "pw-one"); err != nil {
		b.Fatal(err)
	}
	req := ldap.NewSearchRequest("ou=phonebook,"+testBase, ldap.ScopeSingleLevel, 0, 0, 0, false, "(|(cn=*Last04*)(telephoneNumber=1049*))", []string{"cn", "telephoneNumber"}, nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := c.Search(req); err != nil && !ldap.IsErrorWithCode(err, ResultSizeLimitExceeded) {
			b.Fatal(err)
		}
	}
}

// TestLDAPGarbage: bytes that are not LDAP close the connection without a
// panic, and the server keeps serving.
func TestLDAPGarbage(t *testing.T) {
	r := newRig(t, nil)
	for _, junk := range [][]byte{{0x30, 0x84, 0xff, 0xff, 0xff, 0xff}, []byte("GET / HTTP/1.0\r\n\r\n"), {0x30, 0x03, 0x02, 0x01, 0x01}, {0x30, 0x05, 0x02, 0x01, 0x01, 0x63, 0x00}} {
		c, err := tls.Dial("tcp", r.ldaps, &tls.Config{RootCAs: r.pool, ServerName: "localhost"})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = c.Write(junk)
		_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
		buf := make([]byte, 512)
		_, _ = c.Read(buf)
		_ = c.Close()
	}
	c := r.dialTLS(t)
	if err := c.Bind(phoneDN(1), "pw-one"); err != nil {
		t.Fatalf("the server stopped serving: %v", err)
	}
}
