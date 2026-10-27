package api

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// TestPhoneLDAPAPI (spec pbx-gaps S-26) fails if the group switch cannot be
// read and written, if turning LDAP on is accepted while the server does not
// serve it, if a phone's LDAP state shows its password, or if the rotation
// does not return a new password once, audited, and for a missing phone 404.
func TestPhoneLDAPAPI(t *testing.T) {
	// Without LDAP served, the switch cannot go on.
	off := newProvEnv(t, ProvConfig{})
	g := off.c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "A", "includeExtensions": true, "includeCompany": true}).json(t)
	gpath := fmt.Sprintf("/api/v1/phone-groups/%d/ldap", int64(g["id"].(float64)))
	if v := off.c.must(http.StatusOK, "GET", gpath, nil).json(t); v["enabled"] != false || v["served"] != false {
		t.Fatalf("group ldap = %v", v)
	}
	off.c.must(http.StatusConflict, "PUT", gpath, map[string]any{"enabled": true})
	off.c.must(http.StatusBadRequest, "PUT", gpath, map[string]any{})
	off.c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/99999/ldap", map[string]any{"enabled": false})

	p := newProvEnv(t, ProvConfig{Settings: store.ProvSettings{LDAPBase: "dc=hello,dc=local", LDAPPort: 636}})
	c := p.c
	g = c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "B", "includeExtensions": true, "includeCompany": true}).json(t)
	gid := int64(g["id"].(float64))
	gpath = fmt.Sprintf("/api/v1/phone-groups/%d/ldap", gid)
	v := c.must(http.StatusOK, "PUT", gpath, map[string]any{"enabled": true}).json(t)
	if v["enabled"] != true || v["served"] != true || v["host"] != "prov.hello.test" || v["port"] != float64(636) || v["base"] != "ou=phonebook,dc=hello,dc=local" {
		t.Fatalf("PUT = %v", v)
	}
	if got := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/phone-groups/%d", gid), nil).json(t); got["ldapEnabled"] != true {
		t.Fatalf("group = %v", got)
	}

	ph := p.phone("0015650000cc", map[string]any{"groupId": gid})
	id := int64(ph["id"].(float64))
	path := fmt.Sprintf("/api/v1/phones/%d/ldap", id)
	st := c.must(http.StatusOK, "GET", path, nil)
	state := st.json(t)
	if state["groupEnabled"] != true || state["supported"] != true || state["served"] != true || state["passwordSet"] != false ||
		state["bindDn"] != fmt.Sprintf("uid=%d,ou=phones,dc=hello,dc=local", id) {
		t.Fatalf("state = %v", state)
	}
	r := c.must(http.StatusOK, "POST", path+"/password", nil)
	out := r.json(t)
	pw, _ := out["password"].(string)
	if len(pw) != 32 || out["bindDn"] != state["bindDn"] {
		t.Fatalf("rotate = %d chars, %v", len(pw), out["bindDn"])
	}
	if after := c.must(http.StatusOK, "GET", path, nil); bytes.Contains(after.body, []byte(pw)) || after.json(t)["passwordSet"] != true {
		t.Fatalf("state after rotation: %s", after.body)
	}
	if again := c.must(http.StatusOK, "POST", path+"/password", nil).json(t); again["password"] == pw {
		t.Fatal("two rotations gave the same password")
	}
	if n := p.scalar(`SELECT count(*) FROM audit_events WHERE action = 'ldap-password-rotate' AND resource_id = $1`, fmt.Sprint(id)); n != "2" {
		t.Fatalf("audit rows = %s", n)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/phones/99999/ldap", nil)
	c.must(http.StatusNotFound, "POST", "/api/v1/phones/99999/ldap/password", nil)
	// A server that does not serve LDAP refuses a rotation.
	op := off.phone("0015650000dd", nil)
	off.c.must(http.StatusConflict, "POST", fmt.Sprintf("/api/v1/phones/%d/ldap/password", int64(op["id"].(float64))), nil)
}
