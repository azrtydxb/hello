package api

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/prov"
)

const wifiTestKey = "correct-horse-battery-staple"

// TestWifiAPI walks the Wi-Fi API against PostgreSQL (spec phone-feature-parity
// S-23 and S-24): profile CRUD with the write-only key and password, a sealed
// key in the database, assignment to a phone group and a phone with the
// 10-profile limit, the roaming settings, and every refusal. It fails if a
// secret appears in any response, a key is stored unsealed, a profile in use
// can be deleted, a verified profile is not required for pushing, or a bad
// field is accepted.
func TestWifiAPI(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c

	office := c.must(http.StatusCreated, "POST", "/api/v1/wifi-profiles", map[string]any{
		"name": "Office", "ssid": "corp-wifi", "security": "wpa2-psk", "psk": wifiTestKey, "priority": 5,
	}).json(t)
	id := fmt.Sprint(office["id"])
	if office["pskSet"] != true || office["passwordSet"] != false || office["verified"] != false {
		t.Fatalf("created = %v", office)
	}
	if _, ok := office["psk"]; ok {
		t.Fatalf("the response carries the psk: %v", office)
	}

	// The key is sealed: not in the column, and it opens under its context.
	var enc []byte
	if err := p.db.QueryRowContext(context.Background(), `SELECT psk_enc FROM wifi_profiles WHERE id = $1`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if len(enc) == 0 || bytes.Contains(enc, []byte(wifiTestKey)) {
		t.Fatalf("psk_enc = %q, want a sealed value", enc)
	}
	var pid int64
	_, _ = fmt.Sscan(id, &pid)
	if got, err := p.box.Open(enc, prov.WifiPSKAAD(pid)); err != nil || got != wifiTestKey {
		t.Fatalf("sealed key opens to %q, %v", got, err)
	}

	// No read returns the secret.
	for _, path := range []string{"/api/v1/wifi-profiles", "/api/v1/wifi-profiles/" + id} {
		if r := c.must(http.StatusOK, "GET", path, nil); bytes.Contains(r.body, []byte(wifiTestKey)) {
			t.Fatalf("GET %s returns the key: %s", path, r.body)
		}
	}
	if l := c.must(http.StatusOK, "GET", "/api/v1/wifi-profiles", nil).json(t); len(l["items"].([]any)) != 1 {
		t.Fatalf("list = %v", l)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/wifi-profiles/9999", nil)

	// Refusals on create: a duplicate name (409), missing and bad fields (400).
	c.must(http.StatusConflict, "POST", "/api/v1/wifi-profiles", map[string]any{
		"name": "office", "ssid": "other", "security": "open"})
	for name, body := range map[string]map[string]any{
		"no name":        {"ssid": "a", "security": "open"},
		"short key":      {"name": "x", "ssid": "a", "security": "wpa2-psk", "psk": "short"},
		"no key":         {"name": "x", "ssid": "a", "security": "wpa3-sae"},
		"key on open":    {"name": "x", "ssid": "a", "security": "open", "psk": wifiTestKey},
		"long ssid":      {"name": "x", "ssid": strings.Repeat("s", 33), "security": "open"},
		"unknown":        {"name": "x", "ssid": "a", "security": "wep"},
		"priority":       {"name": "x", "ssid": "a", "security": "open", "priority": 11},
		"no 802.1X":      {"name": "x", "ssid": "a", "security": "wpa2-enterprise"},
		"unknown 802.1X": {"name": "x", "ssid": "a", "security": "wpa2-enterprise", "dot1xProfileId": 9999},
		"802.1X on open": {"name": "x", "ssid": "a", "security": "open", "dot1xProfileId": 1},
		"old eap field":  {"name": "x", "ssid": "a", "security": "wpa2-enterprise", "identity": "u"},
		"extra field":    {"name": "x", "ssid": "a", "security": "open", "x": 1},
	} {
		if r := c.do("POST", "/api/v1/wifi-profiles", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s, want 400", name, r.code, r.body)
		}
	}

	// An enterprise profile names an 802.1X profile; the credentials live there.
	dx := c.must(http.StatusCreated, "POST", "/api/v1/dot1x-profiles", map[string]any{
		"name": "Radius", "method": "peap", "identity": "phones", "password": "radius-secret-pw"}).json(t)
	corp := c.must(http.StatusCreated, "POST", "/api/v1/wifi-profiles", map[string]any{
		"name": "Corp", "ssid": "corp-dot1x", "security": "wpa2-enterprise", "dot1xProfileId": dx["id"],
	}).json(t)
	if corp["dot1xProfileId"] != dx["id"] || corp["pskSet"] != false || corp["password"] != nil || corp["passwordSet"] != nil {
		t.Fatalf("enterprise = %v", corp)
	}
	c.must(http.StatusConflict, "DELETE", "/api/v1/dot1x-profiles/"+fmt.Sprint(dx["id"]), nil)

	// PATCH keeps the key when it is left out, and replaces or clears it.
	up := c.must(http.StatusOK, "PATCH", "/api/v1/wifi-profiles/"+id, map[string]any{"verified": true, "priority": 7}).json(t)
	if up["pskSet"] != true || up["verified"] != true || up["priority"] != float64(7) {
		t.Fatalf("patched = %v", up)
	}
	var again []byte
	_ = p.db.QueryRowContext(context.Background(), `SELECT psk_enc FROM wifi_profiles WHERE id = $1`, id).Scan(&again)
	if !bytes.Equal(again, enc) {
		t.Fatal("a PATCH without psk changed the sealed key")
	}
	c.must(http.StatusOK, "PATCH", "/api/v1/wifi-profiles/"+id, map[string]any{"security": "open"})
	if g := c.must(http.StatusOK, "GET", "/api/v1/wifi-profiles/"+id, nil).json(t); g["pskSet"] != false {
		t.Fatalf("a profile that turned open still holds a key: %v", g)
	}
	c.must(http.StatusOK, "PATCH", "/api/v1/wifi-profiles/"+id, map[string]any{"security": "wpa2-psk", "psk": wifiTestKey})
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/wifi-profiles/"+id, map[string]any{"security": "wpa3-sae", "psk": "x"})
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/wifi-profiles/"+id, map[string]any{"priority": -1})
	c.must(http.StatusConflict, "PATCH", "/api/v1/wifi-profiles/"+id, map[string]any{"name": "CORP"})
	c.must(http.StatusNotFound, "PATCH", "/api/v1/wifi-profiles/9999", map[string]any{"priority": 1})

	// A group takes profiles and roaming settings.
	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Handsets"}).json(t)
	gpath := fmt.Sprintf("/api/v1/phone-groups/%v/wifi", g["id"])
	empty := c.must(http.StatusOK, "GET", gpath, nil).json(t)
	if empty["pushing"] != false || len(empty["profileIds"].([]any)) != 0 {
		t.Fatalf("empty group = %v", empty)
	}
	put := c.must(http.StatusOK, "PUT", gpath, map[string]any{
		"profileIds": []any{office["id"]},
		"radio":      map[string]any{"band": "5", "threshold": -62, "dot11v": true},
	}).json(t)
	radio := put["radio"].(map[string]any)
	if put["pushing"] != true || radio["band"] != "5" || radio["threshold"] != float64(-62) || radio["dot11v"] != true {
		t.Fatalf("group wifi = %v", put)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/phone-groups/9999/wifi", nil)
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/9999/wifi", map[string]any{"profileIds": []any{}})
	for name, body := range map[string]map[string]any{
		"no list":      {},
		"unknown id":   {"profileIds": []any{9999}},
		"twice":        {"profileIds": []any{office["id"], office["id"]}},
		"bad band":     {"profileIds": []any{}, "radio": map[string]any{"band": "6"}},
		"threshold":    {"profileIds": []any{}, "radio": map[string]any{"threshold": -20}},
		"unknown key":  {"profileIds": []any{}, "radio": map[string]any{"dot11k": true}},
		"eleven items": {"profileIds": []any{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11}},
	} {
		if r := c.do("PUT", gpath, body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d %s, want 400", name, r.code, r.body)
		}
	}
	if kept := c.must(http.StatusOK, "GET", gpath, nil).json(t); len(kept["profileIds"].([]any)) != 1 {
		t.Fatalf("a refused PUT changed the group: %v", kept)
	}

	// A profile in use cannot be deleted.
	c.must(http.StatusConflict, "DELETE", "/api/v1/wifi-profiles/"+id, nil)
	if used := c.must(http.StatusOK, "GET", "/api/v1/wifi-profiles/"+id, nil).json(t); used["groups"] != float64(1) {
		t.Fatalf("groups = %v", used["groups"])
	}

	// A phone: its own profiles come after the group's; pushing needs a verified one.
	ph := p.phone("80:5E:C0:AA:BB:77", map[string]any{"vendor": "grandstream", "model": "WP836", "groupId": g["id"]})
	phpath := fmt.Sprintf("/api/v1/phones/%v/wifi", ph["id"])
	pw := c.must(http.StatusOK, "GET", phpath, nil).json(t)
	if pw["pushing"] != true || len(pw["inheritedProfileIds"].([]any)) != 1 {
		t.Fatalf("phone inherits = %v", pw)
	}
	own := c.must(http.StatusOK, "PUT", phpath, map[string]any{"profileIds": []any{corp["id"]}}).json(t)
	if len(own["profileIds"].([]any)) != 1 || own["radio"] != nil {
		t.Fatalf("phone own = %v", own)
	}
	c.must(http.StatusBadRequest, "PUT", phpath, map[string]any{"profileIds": []any{9999}})
	c.must(http.StatusBadRequest, "PUT", phpath, map[string]any{})
	c.must(http.StatusNotFound, "GET", "/api/v1/phones/9999/wifi", nil)
	c.must(http.StatusNotFound, "PUT", "/api/v1/phones/9999/wifi", map[string]any{"profileIds": []any{}})

	// The preview shows the networks with the secrets masked.
	prev := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/phones/%v/preview?file=cfg805ec0aabb77.xml", ph["id"]), nil)
	for _, secret := range []string{wifiTestKey, "radius-secret-pw"} {
		if bytes.Contains(prev.body, []byte(secret)) {
			t.Fatalf("the preview shows a Wi-Fi secret")
		}
	}
	if !bytes.Contains(prev.body, []byte("<P83003>********</P83003>")) || !bytes.Contains(prev.body, []byte("<P25438>2</P25438>")) {
		t.Fatalf("the preview does not carry the masked key and the band:\n%s", "(withheld)")
	}

	// Unassigned, the profiles can go.
	c.must(http.StatusOK, "PUT", phpath, map[string]any{"profileIds": []any{}})
	c.must(http.StatusOK, "PUT", gpath, map[string]any{"profileIds": []any{}})
	c.must(http.StatusNoContent, "DELETE", "/api/v1/wifi-profiles/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/wifi-profiles/"+id, nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/wifi-profiles/"+fmt.Sprint(corp["id"]), nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/dot1x-profiles/"+fmt.Sprint(dx["id"]), nil)
}

// TestWifiPreviewMask fails if a Wi-Fi key survives the
// preview masking (no database needed).
func TestWifiPreviewMask(t *testing.T) {
	d := prov.RenderData{Wifi: &prov.WifiInfo{Profiles: []prov.WifiProfile{
		{ID: 1, SSID: "a", Security: "wpa2-psk", PSK: wifiTestKey},
		{ID: 2, SSID: "b", Security: "wpa2-enterprise", Dot1XID: 7},
		{ID: 3, SSID: "c", Security: "open"},
	}}}
	maskSecrets(&d)
	for _, p := range d.Wifi.Profiles {
		if p.PSK == wifiTestKey {
			t.Fatalf("profile %d keeps a secret: %+v", p.ID, p)
		}
	}
	if d.Wifi.Profiles[0].PSK != mask || d.Wifi.Profiles[2].PSK != "" {
		t.Fatalf("masked = %+v", d.Wifi.Profiles)
	}
}
