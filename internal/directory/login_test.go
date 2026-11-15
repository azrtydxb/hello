package directory

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// bindRig is an enabled LDAP connector with loginMethod ldap_bind on the
// fake server, and the external id of its third user.
func bindRig(t *testing.T) (*fakeLDAP, *Engine, Connector, string) {
	t.Helper()
	f := startFakeLDAP(t, true)
	f.addUsers(5)
	f.Passwords = map[string]string{"cn=u02,ou=people,dc=corp": "user-pw"}
	e := &Engine{Store: NewMem(), Drivers: map[string]Driver{KindLDAP: ldapDriver}, Dial: f.opts().Dial}
	pw := "pw"
	c, err := e.Create(context.Background(), "admin", ConnectorInput{Kind: KindLDAP, Name: strPtr("AD"), Secret: &pw, Enabled: boolPtr(true),
		Config: ldapCfg(f, nil), Options: &Options{LoginMethod: LoginLDAPBind}})
	if err != nil {
		t.Fatal(err)
	}
	ext := guidString(f.Users[2].Attrs["objectGUID"][0])
	return f, e, c, ext
}

// TestBindLogin fails if a synced AD user cannot sign in with their own
// password, or a wrong or empty password, another user's id, or a
// connector without ldap_bind lets them in (I-30, AC-7).
func TestBindLogin(t *testing.T) {
	f, e, c, ext := bindRig(t)
	ctx := context.Background()
	if err := e.BindLogin(ctx, c.ID, ext, "user-pw"); err != nil {
		t.Fatalf("right password: %v", err)
	}
	last := f.Filters[len(f.Filters)-1]
	if !strings.Contains(last, "objectGUID=") || strings.Contains(last, "user-pw") {
		t.Fatalf("lookup filter = %q", last)
	}
	if err := e.BindLogin(ctx, c.ID, ext, "wrong"); !errors.Is(err, ErrBindRefused) {
		t.Fatalf("wrong password: %v", err)
	}
	bound := f.Bound
	if err := e.BindLogin(ctx, c.ID, ext, ""); !errors.Is(err, ErrBindRefused) || f.Bound != bound {
		t.Fatalf("empty password: %v, binds %d -> %d (it must not reach the server)", err, bound, f.Bound)
	}
	other := guidString(f.Users[3].Attrs["objectGUID"][0])
	if err := e.BindLogin(ctx, c.ID, other, "user-pw"); !errors.Is(err, ErrBindRefused) {
		t.Fatalf("another user's password: %v", err)
	}
	// An injection attempt in the id is escaped and matches nothing.
	if err := e.BindLogin(ctx, c.ID, "*)(uid=*", "user-pw"); !errors.Is(err, ErrBindRefused) {
		t.Fatalf("filter injection: %v", err)
	}
	if !strings.Contains(f.Filters[len(f.Filters)-1], `\2a\29\28uid=\2a`) {
		t.Fatalf("injected id not escaped: %q", f.Filters[len(f.Filters)-1])
	}
	if _, err := e.Update(ctx, "admin", c.ID, ConnectorInput{Options: &Options{LoginMethod: LoginOIDC, OIDCProvider: KindEntra}}); err != nil {
		t.Fatal(err)
	}
	if err := e.BindLogin(ctx, c.ID, ext, "user-pw"); !errors.Is(err, ErrBindRefused) {
		t.Fatalf("connector without ldap_bind: %v", err)
	}
}

// TestIDFilter fails if a GUID is not looked up by its binary objectGUID.
func TestIDFilter(t *testing.T) {
	g := guidBytes(0x11223344, 0x5566, 0x7788, []byte{0x99, 0xaa, 0xbb, 0xcc, 0xdd, 0xee, 0xff, 0x00})
	f := idFilter(guidString(g))
	if !strings.HasPrefix(f, `(|(objectGUID=\44\33\22\11\66\55\88\77\99\aa\bb\cc\dd\ee\ff\00)`) {
		t.Fatalf("filter = %s", f)
	}
}
