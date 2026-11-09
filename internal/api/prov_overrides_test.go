package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

func overrideItems(t *testing.T, r response) []map[string]any {
	t.Helper()
	raw, _ := r.json(t)["items"].([]any)
	out := make([]map[string]any, len(raw))
	for i, v := range raw {
		out[i], _ = v.(map[string]any)
	}
	return out
}

// TestSettingOverridesAPI fails if a group or phone override is not stored,
// a key Hello owns is accepted, a secret reads back unmasked, the mask does
// not keep the stored secret, or the phone's rendered file ignores the order
// template, group, phone.
func TestSettingOverridesAPI(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "warehouse"}).json(t)
	gid := fmt.Sprint(g["id"])
	ph := p.phone("80:5E:C0:AA:BB:21", map[string]any{"groupId": g["id"]})
	pid := fmt.Sprint(ph["id"])

	gpath, ppath := "/api/v1/phone-groups/"+gid+"/settings", "/api/v1/phones/"+pid+"/settings"
	c.must(http.StatusOK, "PUT", gpath, map[string]any{"overrides": []map[string]any{
		{"key": "phone_setting.backlight_time", "value": "30"},
		{"key": "static.network.qos.signaltos", "value": "10"},
		{"key": "P71", "value": "ignored-on-yealink"},
	}})
	got := overrideItems(t, c.must(http.StatusOK, "PUT", ppath, map[string]any{"overrides": []map[string]any{
		{"key": "static.network.qos.signaltos", "value": "12"},
		{"key": "features.pin", "value": "hunter2-pin", "secret": true},
	}}))
	// By key: features.pin (the secret) before static.network.qos.signaltos.
	if len(got) != 2 || got[0]["key"] != "features.pin" || got[0]["value"] != "********" || got[0]["secret"] != true {
		t.Fatalf("phone overrides = %v", got)
	}

	body := string(c.must(http.StatusOK, "GET", "/api/v1/phones/"+pid+"/preview?file=805ec0aabb21.cfg", nil).body)
	for _, want := range []string{"phone_setting.backlight_time = 30", "static.network.qos.signaltos = 12"} {
		if !strings.Contains(body, want) {
			t.Errorf("preview lacks %q", want)
		}
	}
	if strings.Contains(body, "signaltos = 10") || strings.Contains(body, "ignored-on-yealink") {
		t.Error("preview carries a group value the phone replaced or another vendor's key")
	}
	if strings.Contains(body, "hunter2-pin") || !strings.Contains(body, "features.pin = ********") {
		t.Error("the preview does not mask a secret override")
	}
	if strings.Contains(string(c.must(http.StatusOK, "GET", ppath, nil).body), "hunter2-pin") {
		t.Error("a secret read back in clear")
	}
	if p.scalar(`SELECT count(*) FROM prov_setting_overrides WHERE value LIKE '%hunter2%'`) != "0" {
		t.Error("a secret value is stored in clear")
	}

	// The mask keeps the stored secret; a new value replaces it.
	c.must(http.StatusOK, "PUT", ppath, map[string]any{"overrides": []map[string]any{{"key": "features.pin", "value": "********", "secret": true}}})
	c.must(http.StatusOK, "PUT", ppath, map[string]any{"overrides": []map[string]any{{"key": "features.pin", "secret": true}}})
	c.must(http.StatusBadRequest, "PUT", ppath, map[string]any{"overrides": []map[string]any{{"key": "features.other", "secret": true}}})

	// Refused keys.
	for _, bad := range []map[string]any{
		{"key": "account.1.password", "value": "x"}, {"key": "static.auto_provision.server.url", "value": "x"},
		{"key": "P35", "value": "x"}, {"key": "bad key", "value": "x"}, {"key": "features.x.y", "value": "a\nb"},
		{"key": "phone_setting.a.b", "value": "1"}, // twice below
	} {
		in := []map[string]any{bad}
		if bad["key"] == "phone_setting.a.b" {
			in = append(in, bad)
		}
		c.must(http.StatusBadRequest, "PUT", gpath, map[string]any{"overrides": in})
	}
	// A phone refuses another vendor's key.
	c.must(http.StatusBadRequest, "PUT", ppath, map[string]any{"overrides": []map[string]any{{"key": "P71", "value": "1"}}})
	c.must(http.StatusNotFound, "GET", "/api/v1/phones/999999/settings", nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/phone-groups/999999/settings", nil)
	// Only an administrator changes them; an operator reads and is refused.
	op, _ := p.asRole("overrides-operator", auth.RoleOperator)
	op.must(http.StatusOK, "GET", ppath, nil)
	op.must(http.StatusOK, "GET", gpath, nil)
	op.must(http.StatusForbidden, "PUT", ppath, map[string]any{"overrides": []any{}})
	op.must(http.StatusForbidden, "PUT", gpath, map[string]any{"overrides": []any{}})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phones/999999/settings", map[string]any{"overrides": []any{}})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/settings", map[string]any{"overrides": []any{}})
	c.must(http.StatusBadRequest, "PUT", ppath, map[string]any{})

	// Clearing removes them; deleting the phone removes its rows.
	c.must(http.StatusOK, "PUT", ppath, map[string]any{"overrides": []any{}})
	if n := p.scalar(`SELECT count(*) FROM prov_setting_overrides WHERE phone_id = ` + pid); n != "0" {
		t.Errorf("%s phone overrides left", n)
	}
}
