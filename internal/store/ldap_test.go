package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// TestLDAPPhone (spec S-24, S-26) fails if a phone of a group with LDAP off
// is given LDAP keys or can bind; if turning the group on does not give the
// phone one stable 32-character password and its own DN; if a wrong or
// unknown phone binds; if a rotation does not keep the old password until
// the phone fetches its configuration and then fail it; or if the public
// set holds more than the extensions and company books.
func TestLDAPPhone(t *testing.T) {
	st, _, ext := provStore(t)
	st.prov.LDAPBase, st.prov.LDAPPort = "dc=hello,dc=local", 636
	ctx := context.Background()
	c, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000042", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	id := c.Phone.ID
	g, err := st.CreatePhoneGroup(ctx, "test", GroupInput{Name: "LDAP", IncludeExtensions: true, IncludeCompany: true})
	if err != nil {
		t.Fatal(err)
	}
	grp := &g.ID
	if _, _, err := st.UpdatePhone(ctx, "test", id, PhoneChange{GroupID: &grp}); err != nil {
		t.Fatal(err)
	}
	d, _, err := st.RenderInputs(ctx, id)
	if err != nil || d.LDAP != nil {
		t.Fatalf("group off: LDAP = %+v, %v", d.LDAP, err)
	}
	if ok, err := st.Authenticate(ctx, id, "anything"); ok || err != nil {
		t.Fatalf("a phone of a group with LDAP off bound: %v %v", ok, err)
	}
	if _, err := st.LDAPVersion(ctx, id); !errors.Is(err, prov.ErrNotFound) {
		t.Fatalf("LDAPVersion with LDAP off = %v", err)
	}

	if _, err := st.SetPhoneGroupLDAP(ctx, "test", g.ID, true); err != nil {
		t.Fatal(err)
	}
	d, _, err = st.RenderInputs(ctx, id)
	if err != nil || d.LDAP == nil {
		t.Fatalf("group on: LDAP = %+v, %v", d.LDAP, err)
	}
	pw := d.LDAP.Password
	if len(pw) != 32 || d.LDAP.BindDN != prov.LDAPBindDN("dc=hello,dc=local", id) || d.LDAP.Host != "prov.hello.test" || d.LDAP.Port != 636 || d.LDAP.Base != "ou=phonebook,dc=hello,dc=local" {
		t.Fatalf("LDAP setting = %+v", d.LDAP)
	}
	if d2, _, _ := st.RenderInputs(ctx, id); d2.LDAP == nil || d2.LDAP.Password != pw {
		t.Fatal("the password changed between renders")
	}
	if ok, err := st.Authenticate(ctx, id, pw); !ok || err != nil {
		t.Fatalf("right password: %v %v", ok, err)
	}
	for _, bad := range []string{"", "x", pw + "x", pw[:31]} {
		if ok, _ := st.Authenticate(ctx, id, bad); ok {
			t.Fatalf("password %q bound", bad)
		}
	}
	if ok, err := st.Authenticate(ctx, id+1000, pw); ok || err != nil {
		t.Fatalf("unknown phone: %v %v", ok, err)
	}
	if l, err := st.GetPhoneLDAP(ctx, id); err != nil || !l.PasswordSet || !l.GroupEnabled || l.LastBindAt == nil || l.BindDN == "" {
		t.Fatalf("state = %+v, %v", l, err)
	}

	// Rotation: the old password lives until the phone fetches.
	time.Sleep(10 * time.Millisecond)
	npw, err := st.RotateLDAPPassword(ctx, "test", id)
	if err != nil || len(npw) != 32 || npw == pw {
		t.Fatalf("rotate = %q, %v", npw, err)
	}
	for name, p := range map[string]string{"new": npw, "old": pw} {
		if ok, _ := st.Authenticate(ctx, id, p); !ok {
			t.Fatalf("the %s password does not bind after rotation", name)
		}
	}
	if err := st.MarkFetched(ctx, id, prov.HashToken(c.Token), prov.FetchState{At: time.Now(), File: "pb/0.xml"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Authenticate(ctx, id, pw); !ok {
		t.Fatal("a phonebook fetch dropped the old password")
	}
	if err := st.MarkFetched(ctx, id, prov.HashToken(c.Token), prov.FetchState{At: time.Now(), File: "805ec0000042.cfg"}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Authenticate(ctx, id, pw); ok {
		t.Fatal("the old password still binds after the phone fetched its configuration")
	}
	if ok, _ := st.Authenticate(ctx, id, npw); !ok {
		t.Fatal("the new password does not bind")
	}
	if d, _, _ := st.RenderInputs(ctx, id); d.LDAP == nil || d.LDAP.Password != npw {
		t.Fatal("the render does not carry the new password")
	}

	// The public set is the extensions and company books only.
	company := bookByKind(t, st, phonebook.KindCompany)
	if _, err := st.CreatePhonebookEntry(ctx, "test", company.ID, entry("Corp", "+3225550100")); err != nil {
		t.Fatal(err)
	}
	pub, err := st.LDAPBooks(ctx, 0)
	if err != nil || len(pub) != 2 {
		t.Fatalf("public set = %+v, %v", pub, err)
	}

	// Off again: no keys, no bind, no directory.
	if _, err := st.SetPhoneGroupLDAP(ctx, "test", g.ID, false); err != nil {
		t.Fatal(err)
	}
	if ok, _ := st.Authenticate(ctx, id, npw); ok {
		t.Fatal("a phone bound after its group turned LDAP off")
	}
	if d, _, _ := st.RenderInputs(ctx, id); d.LDAP != nil {
		t.Fatal("LDAP keys after the group turned LDAP off")
	}
}
