package directory

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func ldapCfg(f *fakeLDAP, mut func(*LDAPConfig)) json.RawMessage {
	scheme := "ldap"
	if f.implicit {
		scheme = "ldaps"
	}
	c := LDAPConfig{URL: scheme + "://dc.corp.test:389", BindDN: f.BindDN, BaseDN: "ou=people,dc=corp", CACertPEM: f.CAPEM, PageSize: 10, TimeoutSecond: 5,
		GroupBaseDN: "ou=groups,dc=corp"}
	if mut != nil {
		mut(&c)
	}
	raw, _ := json.Marshal(c)
	out, err := ldapDriver.Validate(raw, ValidateOptions{})
	if err != nil {
		panic(err)
	}
	return out
}

func (f *fakeLDAP) opts() FetchOptions {
	return FetchOptions{Limit: 1000, Attributes: []string{"mail", "userAccountControl", "ipPhone"}, AllowPlaintext: false,
		Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, f.addr())
		}}
}

func (f *fakeLDAP) addUsers(n int) {
	for i := 0; i < n; i++ {
		f.Users = append(f.Users, fakeEntry{DN: fmt.Sprintf("cn=u%02d,ou=people,dc=corp", i), Attrs: map[string][][]byte{
			"objectGUID":         {guidBytes(uint32(0x11223300+i), 0x4455, 0x6677, []byte{0x88, 0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff})},
			"mail":               strs(fmt.Sprintf("u%02d@corp.test", i)),
			"userAccountControl": strs("512"),
		}})
	}
}

func TestLDAPFetchOverLDAPSWithPaging(t *testing.T) {
	f := startFakeLDAP(t, true)
	f.addUsers(25)
	res, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "pw", f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Users) != 25 {
		t.Fatalf("users = %d (paging lost entries)", len(res.Users))
	}
	u := res.Users[3]
	// objectGUID is converted from its binary form to the canonical string.
	if u.ExternalID != "11223303-4455-6677-8899-aabbccddeeff" {
		t.Errorf("external id = %q", u.ExternalID)
	}
	if u.Attrs["mail"][0] != "u03@corp.test" || u.Attrs["useraccountcontrol"][0] != "512" {
		t.Errorf("attrs = %v", u.Attrs)
	}
	if got := f.Filters[0]; got != DefaultUserFilter {
		t.Errorf("filter = %q", got)
	}
}

func TestLDAPStartTLS(t *testing.T) {
	f := startFakeLDAP(t, false)
	f.addUsers(3)
	res, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "pw", f.opts())
	if err != nil || len(res.Users) != 3 {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

func TestLDAPCertificateAlwaysVerified(t *testing.T) {
	f := startFakeLDAP(t, true)
	f.addUsers(1)
	// No CA supplied: the self-signed certificate is not trusted.
	_, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, func(c *LDAPConfig) { c.CACertPEM = "" }), "pw", f.opts())
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeTLSUntrusted {
		t.Fatalf("err = %v", err)
	}
	// A wrong server name fails too.
	_, err = ldapDriver.Fetch(context.Background(), ldapCfg(f, func(c *LDAPConfig) { c.ServerName = "other.corp.test" }), "pw", f.opts())
	if !errors.As(err, &de) || de.Code != CodeTLSUntrusted {
		t.Fatalf("wrong name: err = %v", err)
	}
}

func TestLDAPBindFailuresAndEmptyPassword(t *testing.T) {
	f := startFakeLDAP(t, true)
	_, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "wrong", f.opts())
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeAuthFailed {
		t.Fatalf("err = %v", err)
	}
	f.mu.Lock()
	before := f.Bound
	f.mu.Unlock()
	if _, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "", f.opts()); !errors.As(err, &de) || de.Code != CodeAuthFailed {
		t.Fatalf("empty password: %v", err)
	}
	f.mu.Lock()
	after := f.Bound
	f.mu.Unlock()
	if after != before {
		t.Fatal("an empty-password bind reached the server")
	}
}

func TestLDAPErrorsAreScrubbed(t *testing.T) {
	f := startFakeLDAP(t, true)
	f.addUsers(1)
	for code, want := range map[int]string{87: CodeBadFilter, 4: CodeQuota, 3: CodeTimeout, 50: CodeAuthFailed} {
		f.FailCode = code
		_, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "pw", f.opts())
		var de *Error
		if !errors.As(err, &de) || de.Code != want {
			t.Errorf("code %d: err = %v, want %s", code, err, want)
			continue
		}
		if strings.Contains(de.Detail, "secret server text") || strings.Contains(de.Error(), "pw") {
			t.Errorf("code %d leaks: %q", code, de.Detail)
		}
	}
}

func TestLDAPUnreachableAndBlockedAddress(t *testing.T) {
	f := startFakeLDAP(t, true)
	opts := f.opts()
	_ = f.ln.Close()
	_, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "pw", opts)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeUnreachable {
		t.Fatalf("closed port: %v", err)
	}
	// Without a test dialer the guarded dialer refuses loopback and private
	// addresses unless they are allowed.
	g := startFakeLDAP(t, true)
	cfg := ldapCfg(g, func(c *LDAPConfig) { c.URL = "ldaps://127.0.0.1:" + strings.Split(g.addr(), ":")[1] })
	_, err = ldapDriver.Fetch(context.Background(), cfg, "pw", FetchOptions{Limit: 10})
	if !errors.As(err, &de) || de.Code != CodeUnreachable || !strings.Contains(de.Detail, "not allowed") {
		t.Fatalf("loopback: %v", err)
	}
}

func TestLDAPUserCap(t *testing.T) {
	f := startFakeLDAP(t, true)
	f.addUsers(30)
	o := f.opts()
	o.Limit = 20
	_, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "pw", o)
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeUserCap {
		t.Fatalf("err = %v", err)
	}
}

func TestLDAPEntriesWithoutStableIDFail(t *testing.T) {
	f := startFakeLDAP(t, true)
	f.Users = []fakeEntry{{DN: "cn=x,ou=people,dc=corp", Attrs: map[string][][]byte{"mail": strs("x@y")}}}
	_, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "pw", f.opts())
	var de *Error
	if !errors.As(err, &de) || de.Code != CodeBadFilter {
		t.Fatalf("err = %v", err)
	}
}

func TestLDAPEntryUUIDFallback(t *testing.T) {
	f := startFakeLDAP(t, true)
	f.Users = []fakeEntry{{DN: "cn=x,ou=people,dc=corp", Attrs: map[string][][]byte{"entryUUID": strs("ABCD-1234")}}}
	res, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "pw", f.opts())
	if err != nil || res.Users[0].ExternalID != "abcd-1234" {
		t.Fatalf("res = %+v, %v", res, err)
	}
}

func TestLDAPGroupMembership(t *testing.T) {
	f := startFakeLDAP(t, true)
	f.addUsers(3)
	g1 := guidBytes(1, 2, 3, []byte{4, 5, 6, 7, 8, 9, 10, 11})
	f.Groups = []fakeEntry{{DN: "cn=sales,ou=groups,dc=corp", Attrs: map[string][][]byte{"objectGUID": {g1}, "cn": strs("sales")}}}
	f.Users[1].Attrs["memberOf"] = strs("CN=Sales,OU=Groups,DC=corp", "cn=other,dc=corp")
	f.Groups[0].DN = "CN=Sales,OU=Groups,DC=corp"
	gid := "00000001-0002-0003-0405-060708090a0b"
	o := f.opts()
	o.NeedGroups = []string{gid}
	res, err := ldapDriver.Fetch(context.Background(), ldapCfg(f, nil), "pw", o)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Groups) != 1 || res.Groups[0].ID != gid || res.Groups[0].Name != "sales" {
		t.Fatalf("groups = %+v", res.Groups)
	}
	for i, u := range res.Users {
		in := len(u.Groups) == 1 && u.Groups[0] == gid
		if in != (i == 1) {
			t.Errorf("user %d groups = %v", i, u.Groups)
		}
	}
	// Nested: one chain search per rule group, with the DN escaped.
	f.Groups[0].DN = `CN=Sa(les)*,OU=Groups,DC=corp`
	f.Members[`CN=Sa\28les\29\2a,OU=Groups,DC=corp`] = []string{f.Users[2].DN}
	res, err = ldapDriver.Fetch(context.Background(), ldapCfg(f, func(c *LDAPConfig) { c.NestedGroups = true }), "pw", o)
	if err != nil {
		t.Fatal(err)
	}
	for i, u := range res.Users {
		in := len(u.Groups) == 1 && u.Groups[0] == gid
		if in != (i == 2) {
			t.Errorf("nested: user %d groups = %v", i, u.Groups)
		}
	}
	last := f.Filters[len(f.Filters)-1]
	if !strings.Contains(last, `memberOf:1.2.840.113556.1.4.1941:=CN=Sa\28les\29\2a,OU=Groups,DC=corp`) {
		t.Errorf("chain filter = %q", last)
	}
}

func TestLDAPTest(t *testing.T) {
	f := startFakeLDAP(t, true)
	f.addUsers(30)
	g := guidBytes(1, 2, 3, []byte{4, 5, 6, 7, 8, 9, 10, 11})
	f.Groups = []fakeEntry{{DN: "cn=sales", Attrs: map[string][][]byte{"objectGUID": {g}, "cn": strs("sales")}}}
	res, err := ldapDriver.Test(context.Background(), ldapCfg(f, func(c *LDAPConfig) { c.PageSize = 25 }), "pw", f.opts())
	if err != nil {
		t.Fatal(err)
	}
	if !res.OK || res.MatchedUsers != 25 || len(res.Sample) != 20 || len(res.Groups) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if res.ServerCertSubject != "CN=fake-dc" || res.ServerCertExpires == nil || res.ServerCertExpires.Before(time.Now()) {
		t.Errorf("cert = %q %v", res.ServerCertSubject, res.ServerCertExpires)
	}
	if res.Sample[0].Raw["mail"][0] != "u00@corp.test" {
		t.Errorf("sample = %+v", res.Sample[0])
	}
}

func TestLDAPValidate(t *testing.T) {
	d := LDAP{}
	v := func(raw string, o ValidateOptions) (LDAPConfig, error) {
		out, err := d.Validate(json.RawMessage(raw), o)
		var c LDAPConfig
		if err == nil {
			_ = json.Unmarshal(out, &c)
		}
		return c, err
	}
	c, err := v(`{"url":"ldaps://dc.corp:636","bindDn":"cn=svc","baseDn":"dc=corp"}`, ValidateOptions{})
	if err != nil || c.UserFilter != DefaultUserFilter || c.PageSize != 500 || c.Scope != "subtree" || c.TimeoutSecond != 30 {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	_, perr := v(`{"url":"ldap://dc.corp","bindDn":"cn=svc","baseDn":"dc=corp","startTls":false}`, ValidateOptions{})
	var pe *Error
	if !errors.As(perr, &pe) || pe.Code != CodeTLSRequired {
		t.Fatalf("plaintext: %v", perr)
	}
	if _, err := v(`{"url":"ldap://dc.corp","bindDn":"cn=svc","baseDn":"dc=corp","startTls":false}`, ValidateOptions{AllowPlaintext: true}); err != nil {
		t.Fatalf("lab switch: %v", err)
	}
	if c, err := v(`{"url":"ldap://dc.corp","bindDn":"cn=svc","baseDn":"dc=corp"}`, ValidateOptions{}); err != nil || !c.startTLS() {
		t.Fatalf("ldap:// defaults to StartTLS: %v", err)
	}
	for name, raw := range map[string]string{
		"filter":      `{"url":"ldaps://dc","bindDn":"b","baseDn":"d","userFilter":"(&(a=b)"}`,
		"scheme":      `{"url":"http://dc","bindDn":"b","baseDn":"d"}`,
		"userinfo":    `{"url":"ldaps://u:p@dc","bindDn":"b","baseDn":"d"}`,
		"no bind dn":  `{"url":"ldaps://dc","baseDn":"d"}`,
		"no base":     `{"url":"ldaps://dc","bindDn":"b"}`,
		"bad ca":      `{"url":"ldaps://dc","bindDn":"b","baseDn":"d","caCertPem":"nope"}`,
		"page size":   `{"url":"ldaps://dc","bindDn":"b","baseDn":"d","pageSize":3}`,
		"unknown":     `{"url":"ldaps://dc","bindDn":"b","baseDn":"d","insecureSkipVerify":true}`,
		"no group dn": `{"url":"ldaps://dc","bindDn":"b","baseDn":"d"}`,
	} {
		o := ValidateOptions{GroupRules: name == "no group dn"}
		if _, err := v(raw, o); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	// Identity binds a secret to the host and the bind DN, not to the rest.
	a := d.Identity(json.RawMessage(`{"url":"ldaps://DC.corp","bindDn":"CN=svc","baseDn":"x"}`))
	b := d.Identity(json.RawMessage(`{"url":"ldaps://dc.corp:636","bindDn":"cn=svc","baseDn":"y"}`))
	c2 := d.Identity(json.RawMessage(`{"url":"ldaps://evil.corp","bindDn":"cn=svc","baseDn":"y"}`))
	if a != b || a == c2 {
		t.Errorf("identities: %q %q %q", a, b, c2)
	}
}

func TestGUIDString(t *testing.T) {
	// The documented AD example: bytes as stored, canonical string as shown.
	b := []byte{0x78, 0x56, 0x34, 0x12, 0xab, 0x90, 0xef, 0xcd, 0x01, 0x23, 0x45, 0x67, 0x89, 0xab, 0xcd, 0xef}
	if got := guidString(b); got != "12345678-90ab-cdef-0123-456789abcdef" {
		t.Fatalf("guid = %q", got)
	}
}

var ldapDriver = LDAP{}
