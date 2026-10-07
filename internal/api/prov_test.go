package api

import (
	"bytes"
	"context"
	"crypto/md5" //nolint:gosec // RFC 3261 digest HA1 is defined over MD5.
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/redirect"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
)

const testPublicURL = "https://prov.hello.test"

// provEnv is the API with provisioning on (a sealing box, a public URL)
// and extensions 101 and 102.
type provEnv struct {
	*env
	c   *client
	box *secret.Box
}

func newProvEnv(t *testing.T, pc ProvConfig) *provEnv {
	t.Helper()
	box := testBox(t)
	pc.Settings = store.ProvSettings{
		PublicURL: testPublicURL, SIPServer: "192.0.2.10:5060", Domain: testDomain, Expiry: time.Hour,
		Resync: 24 * time.Hour, Timezone: "UTC", NTP: "pool.ntp.org", TokenGrace: 7 * 24 * time.Hour,
	}
	e := newEnvConfig(t, Config{Prov: pc}, box)
	c := e.login()
	e.ext101 = c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	e.ext102 = c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"}).json(t)
	return &provEnv{env: e, c: c, box: box}
}

// phone creates a Yealink phone on extension 101 and returns the response.
func (p *provEnv) phone(mac string, extra map[string]any) map[string]any {
	p.t.Helper()
	body := map[string]any{"mac": mac, "vendor": "yealink", "model": "T54W", "extensionId": p.ext101["id"]}
	for k, v := range extra {
		body[k] = v
	}
	return p.c.must(http.StatusCreated, "POST", "/api/v1/phones", body).json(p.t)
}

func (p *provEnv) scalar(q string, args ...any) string {
	p.t.Helper()
	var v string
	if err := p.db.QueryRowContext(context.Background(), q, args...).Scan(&v); err != nil {
		p.t.Fatalf("%s: %v", q, err)
	}
	return v
}

// TestPhoneCRUD fails if a MAC with separators is not normalised, a
// duplicate MAC is accepted, a phone without an extension-bound device can
// be enabled, or the fetch-state fields are writable through the API.
func TestPhoneCRUD(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c

	created := p.phone("80:5E:C0:AA:BB:01", map[string]any{"label": "Front desk", "blf": []string{"102"}})
	if created["mac"] != "805ec0aabb01" || created["enabled"] != true || created["bootArmed"] != true ||
		created["extensionNumber"] != "101" || created["label"] != "Front desk" {
		t.Fatalf("created = %v", created)
	}
	if u, _ := created["provisioningUrl"].(string); !strings.HasPrefix(u, testPublicURL+"/p/") || len(u) != len(testPublicURL)+3+52+1 {
		t.Fatalf("provisioningUrl = %q", u)
	}
	if st, _ := created["redirectStatus"].(map[string]any); st["state"] != "not_configured" {
		t.Fatalf("redirectStatus = %v", created["redirectStatus"])
	}
	id := fmt.Sprint(created["id"])
	path := "/api/v1/phones/" + id

	// The same MAC in any spelling is a 409 naming the phone.
	r := c.must(http.StatusConflict, "POST", "/api/v1/phones", map[string]any{
		"mac": "805ec0aabb01", "vendor": "snom", "model": "D785", "extensionId": p.ext102["id"]})
	if !bytes.Contains(r.body, []byte("80:5e:c0:aa:bb:01")) {
		t.Fatalf("duplicate MAC answer does not name it: %s", r.body)
	}
	for _, bad := range []map[string]any{
		{"mac": "805ec0aabb0", "vendor": "yealink", "model": "T54W", "extensionId": p.ext101["id"]},
		{"mac": "805ec0aabb0g", "vendor": "yealink", "model": "T54W", "extensionId": p.ext101["id"]},
		{"mac": "805ec0aabb02", "vendor": "acme", "model": "T54W", "extensionId": p.ext101["id"]},
		{"mac": "805ec0aabb02", "vendor": "yealink", "model": "", "extensionId": p.ext101["id"]},
		{"mac": "805ec0aabb02", "vendor": "yealink", "model": "T54W"},
		{"mac": "805ec0aabb02", "vendor": "yealink", "model": "T54W", "extensionId": p.ext101["id"], "blf": []string{"9x"}},
		{"mac": "805ec0aabb02", "vendor": "yealink", "model": "T54W", "extensionId": p.ext101["id"], "blf": []string{"999"}},
	} {
		c.must(http.StatusBadRequest, "POST", "/api/v1/phones", bad)
	}

	// Fetch state is not writable.
	for _, f := range []string{"lastFetchAt", "lastFetchIp", "firstFetchAt", "firmwareSeen", "tokenExposed", "bootArmed", "mac"} {
		c.must(http.StatusBadRequest, "PATCH", path, map[string]any{f: "x"})
	}

	got := c.must(http.StatusOK, "GET", path, nil).json(t)
	if _, ok := got["provisioningUrl"]; ok {
		t.Fatalf("GET shows the provisioning URL: %v", got)
	}
	upd := c.must(http.StatusOK, "PATCH", path, map[string]any{"label": "Lobby", "model": "T57W", "blf": []string{}}).json(t)
	if upd["label"] != "Lobby" || upd["model"] != "T57W" || len(upd["blf"].([]any)) != 0 {
		t.Fatalf("updated = %v", upd)
	}

	// Unbinding disables; an unbound phone cannot be enabled.
	unbound := c.must(http.StatusOK, "PATCH", path, map[string]any{"deviceId": nil}).json(t)
	if unbound["deviceId"] != nil || unbound["enabled"] != false {
		t.Fatalf("unbound = %v", unbound)
	}
	c.must(http.StatusBadRequest, "PATCH", path, map[string]any{"enabled": true})
	rebound := c.must(http.StatusOK, "PATCH", path, map[string]any{"extensionId": p.ext102["id"], "enabled": true}).json(t)
	if rebound["extensionNumber"] != "102" || rebound["enabled"] != true {
		t.Fatalf("rebound = %v", rebound)
	}

	if l := c.must(http.StatusOK, "GET", "/api/v1/phones", nil).json(t)["items"].([]any); len(l) != 1 {
		t.Fatalf("list = %v", l)
	}
	c.must(http.StatusNoContent, "DELETE", path, nil)
	c.must(http.StatusNotFound, "GET", path, nil)
	c.must(http.StatusNotFound, "DELETE", path, nil)
	if n := p.scalar(`SELECT count(*) FROM audit_events WHERE resource = 'phone' AND resource_id = $1`, id); n != "5" {
		t.Fatalf("phone audit rows = %s, want create, 3 updates and delete", n)
	}
}

// TestPhoneDeviceSecretSealed fails if binding a device does not rotate and
// seal its secret and update its HA1 values in one transaction, if the
// sealed secret is returned by any endpoint, or if unbinding leaves
// secret_enc set.
func TestPhoneDeviceSecretSealed(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	dev := c.must(http.StatusCreated, "POST", "/api/v1/devices", map[string]any{"extensionId": p.ext101["id"], "sipUsername": "101-desk"}).json(t)
	devID := int64(dev["id"].(float64))
	oldSecret := dev["secret"].(string)
	rev0 := p.scalar(`SELECT config_revision FROM schema_info`)

	created := p.phone("0004f2000001", map[string]any{"deviceId": devID})
	if created["secretRotated"] != true || created["deviceId"] != dev["id"] {
		t.Fatalf("binding = %v, want secretRotated", created)
	}
	if rev := p.scalar(`SELECT config_revision FROM schema_info`); rev == rev0 {
		t.Fatal("binding did not bump the configuration revision")
	}
	var enc []byte
	var ha1 string
	if err := p.db.QueryRow(`SELECT secret_enc, ha1_md5 FROM devices WHERE id = $1`, devID).Scan(&enc, &ha1); err != nil || enc == nil {
		t.Fatalf("secret_enc = %v, %v", enc, err)
	}
	sec, err := p.box.Open(enc, prov.DeviceSecretAAD(devID))
	if err != nil || sec == oldSecret {
		t.Fatalf("sealed secret opens = %v, rotated = %v", err, sec != oldSecret)
	}
	sum := md5.Sum([]byte("101-desk:" + testDomain + ":" + sec)) //nolint:gosec // digest HA1
	if ha1 != hex.EncodeToString(sum[:]) {
		t.Fatal("HA1 does not match the sealed secret")
	}
	if _, err := p.box.Open(enc, prov.DeviceSecretAAD(devID+1)); err == nil {
		t.Fatal("sealed secret opens for another device")
	}
	id := fmt.Sprint(created["id"])
	for _, path := range []string{"/api/v1/phones", "/api/v1/phones/" + id, "/api/v1/devices", fmt.Sprintf("/api/v1/devices/%d", devID)} {
		if r := c.must(http.StatusOK, "GET", path, nil); bytes.Contains(r.body, []byte(sec)) || bytes.Contains(r.body, []byte("secret_enc")) {
			t.Fatalf("GET %s reveals the secret", path)
		}
	}

	// A device bound to a phone is not deleted from under it.
	r := c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/devices/%d", devID), nil)
	if !bytes.Contains(r.body, []byte("00:04:f2:00:00:01")) {
		t.Fatalf("409 does not name the phone: %s", r.body)
	}
	c.must(http.StatusConflict, "DELETE", fmt.Sprintf("/api/v1/extensions/%v", p.ext101["id"]), nil)

	// Rotating the bound device's secret re-seals it.
	rot := c.must(http.StatusOK, "POST", fmt.Sprintf("/api/v1/devices/%d/rotate-secret", devID), nil).json(t)
	if err := p.db.QueryRow(`SELECT secret_enc FROM devices WHERE id = $1`, devID).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	if s2, err := p.box.Open(enc, prov.DeviceSecretAAD(devID)); err != nil || s2 != rot["secret"] {
		t.Fatalf("rotate-secret did not re-seal: %v", err)
	}

	// Unbinding drops the sealed secret; the device keeps its HA1.
	c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+id, map[string]any{"deviceId": nil})
	if n := p.scalar(`SELECT count(*) FROM devices WHERE id = $1 AND secret_enc IS NULL AND ha1_md5 <> ''`, devID); n != "1" {
		t.Fatal("unbinding left secret_enc set or cleared the HA1")
	}

	// A created device is <ext>-<last 6 MAC hex>, sealed; deleting the
	// phone unbinds it.
	c2 := p.phone("0004f2abcdef", nil)
	if n := p.scalar(`SELECT count(*) FROM devices WHERE sip_username = '101-abcdef' AND secret_enc IS NOT NULL`); n != "1" {
		t.Fatalf("created device %v not 101-abcdef with a sealed secret", c2["deviceId"])
	}
	c.must(http.StatusNoContent, "DELETE", fmt.Sprint("/api/v1/phones/", c2["id"]), nil)
	if n := p.scalar(`SELECT count(*) FROM devices WHERE sip_username = '101-abcdef' AND secret_enc IS NULL`); n != "1" {
		t.Fatal("deleting the phone left the device's sealed secret")
	}
	// Adding the same phone again rebinds that device instead of failing.
	again := p.phone("0004f2abcdef", nil)
	if again["deviceId"] != c2["deviceId"] || again["secretRotated"] != true {
		t.Fatalf("re-added phone = %v, want device %v rebound", again, c2["deviceId"])
	}
}

// TestPhoneAdminPassword fails if a new phone has no sealed random admin
// password, if it appears in a list or get response, or if a reveal does
// not write an audit row.
func TestPhoneAdminPassword(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	created := p.phone("0015650000aa", nil)
	other := p.phone("0015650000bb", nil)
	id := int64(created["id"].(float64))
	path := fmt.Sprintf("/api/v1/phones/%d", id)

	var enc []byte
	if err := p.db.QueryRow(`SELECT admin_password_enc FROM phones WHERE id = $1`, id).Scan(&enc); err != nil {
		t.Fatal(err)
	}
	pw, err := p.box.Open(enc, prov.PhoneAdminAAD(id))
	if err != nil || len(pw) != 20 {
		t.Fatalf("admin password = %d chars, %v", len(pw), err)
	}
	var otherEnc []byte
	_ = p.db.QueryRow(`SELECT admin_password_enc FROM phones WHERE id = $1`, other["id"]).Scan(&otherEnc)
	if o, _ := p.box.Open(otherEnc, prov.PhoneAdminAAD(int64(other["id"].(float64)))); o == pw {
		t.Fatal("two phones share an admin password")
	}
	for _, r := range []response{c.must(http.StatusOK, "GET", path, nil), c.must(http.StatusOK, "GET", "/api/v1/phones", nil)} {
		if bytes.Contains(r.body, []byte(pw)) || bytes.Contains(r.body, []byte("adminPassword")) {
			t.Fatalf("a read shows the admin password: %s", r.body)
		}
	}
	audits := func() string {
		return p.scalar(`SELECT count(*) FROM audit_events WHERE action = 'reveal-admin-password' AND resource_id = $1`, fmt.Sprint(id))
	}
	got := c.must(http.StatusOK, "POST", path+"/admin-password/reveal", nil).json(t)
	if got["adminPassword"] != pw || audits() != "1" {
		t.Fatalf("reveal = %v, audit rows %s", got, audits())
	}
	c.must(http.StatusNoContent, "POST", path+"/admin-password/rotate", nil)
	if again := c.must(http.StatusOK, "POST", path+"/admin-password/reveal", nil).json(t); again["adminPassword"] == pw {
		t.Fatal("rotate kept the admin password")
	}
	if audits() != "2" {
		t.Fatalf("audit rows after two reveals = %s", audits())
	}
	c.must(http.StatusNotFound, "POST", "/api/v1/phones/999999/admin-password/reveal", nil)
}

// TestPhoneTokenRotation fails if rotation does not keep the old token
// as the in-grace previous one, if immediate or re-arm leave it valid, if
// re-arm does not arm the phone again, or if only the hash and the sealed
// token are not what is stored.
func TestPhoneTokenRotation(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	created := p.phone("0015650000cc", nil)
	id := int64(created["id"].(float64))
	path := fmt.Sprintf("/api/v1/phones/%d", id)
	token := func(u any) string {
		s := strings.TrimPrefix(u.(string), testPublicURL+"/p/")
		return strings.TrimSuffix(s, "/")
	}
	t1 := token(created["provisioningUrl"])
	h1 := auth.HashToken(t1)
	if n := p.scalar(`SELECT count(*) FROM phones WHERE id = $1 AND token_hash = $2 AND position($3 in row_to_json(phones)::text) = 0`, id, h1, t1); n != "1" {
		t.Fatal("stored token is not its hash, or the plaintext is stored")
	}
	ctx := context.Background()
	rec, err := p.st.PhoneByToken(ctx, h1)
	if err != nil || rec.ID != id || rec.ViaPrevious || !rec.Allowlisted {
		t.Fatalf("PhoneByToken = %+v, %v", rec, err)
	}

	rot := c.must(http.StatusOK, "POST", path+"/rotate-token", nil).json(t)
	t2 := token(rot["provisioningUrl"])
	if t2 == t1 {
		t.Fatal("rotation kept the token")
	}
	if rec, err := p.st.PhoneByToken(ctx, h1); err != nil || !rec.ViaPrevious {
		t.Fatalf("previous token in grace = %+v, %v", rec, err)
	}
	if rec, err := p.st.PhoneByToken(ctx, auth.HashToken(t2)); err != nil || rec.ViaPrevious || !rec.HasPrevious {
		t.Fatalf("new token = %+v, %v", rec, err)
	}
	d, _, err := p.st.RenderInputs(ctx, id)
	if (err != nil && !errors.Is(err, prov.ErrNoTemplate)) || !strings.Contains(d.Prov.URL, t2) {
		t.Fatalf("render URL %q (%v) does not carry the new token", d.Prov.URL, err)
	}

	c.must(http.StatusOK, "POST", path+"/rotate-token?immediate=true", nil)
	if _, err := p.st.PhoneByToken(ctx, auth.HashToken(t2)); !errors.Is(err, prov.ErrNotFound) {
		t.Fatalf("immediate left the old token valid: %v", err)
	}
	c.must(http.StatusBadRequest, "POST", path+"/rotate-token?immediate=maybe", nil)

	// The first HTTPS fetch disarms; re-arm rotates at once and arms.
	curHash, _ := hex.DecodeString(p.scalar(`SELECT encode(token_hash, 'hex') FROM phones WHERE id = $1`, id))
	if err := p.st.MarkFetched(ctx, id, curHash, prov.FetchState{At: time.Now(), File: "x.cfg"}); err != nil {
		t.Fatal(err)
	}
	if g := c.must(http.StatusOK, "GET", path, nil).json(t); g["bootArmed"] != false || g["lastFetchFile"] != "x.cfg" {
		t.Fatalf("after a fetch = %v", g)
	}
	cur := p.scalar(`SELECT encode(token_hash, 'hex') FROM phones WHERE id = $1`, id)
	re := c.must(http.StatusOK, "POST", path+"/rearm", nil).json(t)
	if re["bootArmed"] != true || p.scalar(`SELECT count(*) FROM phones WHERE id = $1 AND prev_token_hash IS NULL`, id) != "1" ||
		p.scalar(`SELECT encode(token_hash, 'hex') FROM phones WHERE id = $1`, id) == cur {
		t.Fatalf("rearm = %v, want armed with a new token and no previous one", re)
	}
	// A fetch in flight with the replaced token (matched before the
	// re-arm) changes nothing: the hand-off stays armed, the old file
	// stays recorded, and no promote touches the new token.
	if err := p.st.MarkFetched(ctx, id, curHash, prov.FetchState{At: time.Now(), File: "stale.cfg"}); err != nil {
		t.Fatal(err)
	}
	if err := p.st.PromoteToken(ctx, id, curHash); err != nil {
		t.Fatal(err)
	}
	if g := c.must(http.StatusOK, "GET", path, nil).json(t); g["bootArmed"] != true || g["lastFetchFile"] != "x.cfg" {
		t.Fatalf("a stale-token fetch after the re-arm changed the phone: %v", g)
	}
	// A rolling rotation, then an overtaken current-token promote: the
	// grace of the newer rotation survives.
	c.must(http.StatusOK, "POST", path+"/rotate-token", nil)
	mid, _ := hex.DecodeString(p.scalar(`SELECT encode(token_hash, 'hex') FROM phones WHERE id = $1`, id))
	c.must(http.StatusOK, "POST", path+"/rotate-token", nil)
	if err := p.st.PromoteToken(ctx, id, mid); err != nil {
		t.Fatal(err)
	}
	if p.scalar(`SELECT count(*) FROM phones WHERE id = $1 AND prev_token_hash = decode($2, 'hex')`, id, hex.EncodeToString(mid)) != "1" {
		t.Fatal("a promote with the replaced token ended the newer rotation's grace")
	}
}

// previewTemplate is a stored template for the preview tests: it puts every
// secret in the body, raw and transformed.
var previewTemplate = map[string]any{
	"vendor": "yealink", "modelGlob": "T5*", "priority": 10, "name": "Preview test",
	"files": []map[string]string{{
		"pattern": "{mac}.cfg", "contentType": "text/plain",
		"body": "user={{.Line.Username}}\npass={{.Line.Password}}\nPASS={{upper .Line.Password}}\nadmin={{.Phone.AdminPassword}}\nurl={{.Prov.URL}}\n",
	}},
}

// TestPreviewMasksSecrets fails if a preview contains the secret, the admin
// password or the token, differs from the served body other than in the
// masked values, or writes a fetch record.
func TestPreviewMasksSecrets(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	c.must(http.StatusCreated, "POST", "/api/v1/prov/templates", previewTemplate)
	created := p.phone("805ec0000001", nil)
	id := int64(created["id"].(float64))
	path := fmt.Sprintf("/api/v1/phones/%d/preview?file=805ec0000001.cfg", id)

	r := c.must(http.StatusOK, "GET", path, nil)
	ctx := context.Background()
	d, tpl, err := p.st.RenderInputs(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	served, err := prov.Render(ctx, tpl, "805ec0000001.cfg", d)
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimSuffix(strings.TrimPrefix(d.Prov.URL, testPublicURL+"/p/"), "/")
	for name, secretValue := range map[string]string{"secret": d.Line.Password, "upper secret": strings.ToUpper(d.Line.Password),
		"admin password": d.Phone.AdminPassword, "token": token} {
		if secretValue == "" || bytes.Contains(r.body, []byte(secretValue)) {
			t.Fatalf("preview shows the %s: %s", name, r.body)
		}
	}
	want := strings.NewReplacer(d.Line.Password, mask, strings.ToUpper(d.Line.Password), mask,
		d.Phone.AdminPassword, mask, token, mask).Replace(string(served))
	if string(r.body) != want {
		t.Fatalf("preview = %q\nwant the served body masked: %q", r.body, want)
	}
	if n := p.scalar(`SELECT count(*) FROM prov_fetches`); n != "0" {
		t.Fatalf("preview wrote %s fetch rows", n)
	}
	c.must(http.StatusNotFound, "GET", fmt.Sprintf("/api/v1/phones/%d/preview?file=other.cfg", id), nil)
	c.must(http.StatusBadRequest, "GET", fmt.Sprintf("/api/v1/phones/%d/preview?file=../x", id), nil)
}

// TestProvTemplates fails if a template is saved invalid, a save keeps no
// version, a built-in can be changed, a copy is not above its source, or
// an override template can be deleted from under its phone.
func TestProvTemplates(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	tpl := c.must(http.StatusCreated, "POST", "/api/v1/prov/templates", previewTemplate).json(t)
	id := fmt.Sprint(tpl["id"])
	if tpl["builtin"] != false || tpl["version"] != float64(1) {
		t.Fatalf("created = %v", tpl)
	}
	bad := map[string]any{"vendor": "yealink", "modelGlob": "*", "name": "x",
		"files": []map[string]string{{"pattern": "a.cfg", "body": "{{.Line.Password"}}}
	r := c.must(http.StatusBadRequest, "POST", "/api/v1/prov/templates", bad)
	if !bytes.Contains(r.body, []byte(`"fields"`)) || !bytes.Contains(r.body, []byte("files[0].body")) {
		t.Fatalf("invalid template answer = %s", r.body)
	}
	c.must(http.StatusBadRequest, "POST", "/api/v1/prov/templates/validate", bad)
	c.must(http.StatusOK, "POST", "/api/v1/prov/templates/validate", previewTemplate)
	c.must(http.StatusBadRequest, "PATCH", "/api/v1/prov/templates/"+id, map[string]any{"modelGlob": ""})
	upd := c.must(http.StatusOK, "PATCH", "/api/v1/prov/templates/"+id, map[string]any{"priority": 20}).json(t)
	if upd["version"] != float64(2) || upd["priority"] != float64(20) {
		t.Fatalf("updated = %v", upd)
	}
	if n := p.scalar(`SELECT count(*) FROM prov_template_versions WHERE template_id = $1`, id); n != "2" {
		t.Fatalf("versions kept = %s", n)
	}
	cp := c.must(http.StatusCreated, "POST", "/api/v1/prov/templates/"+id+"/copy", nil).json(t)
	if cp["priority"] != float64(21) || cp["id"] == tpl["id"] {
		t.Fatalf("copy = %v", cp)
	}
	phone := p.phone("805ec0000002", map[string]any{"templateId": tpl["id"]})
	r = c.must(http.StatusConflict, "DELETE", "/api/v1/prov/templates/"+id, nil)
	if !bytes.Contains(r.body, []byte("80:5e:c0:00:00:02")) {
		t.Fatalf("409 does not name the phone: %s", r.body)
	}
	c.must(http.StatusOK, "PATCH", fmt.Sprint("/api/v1/phones/", phone["id"]), map[string]any{"templateId": nil})
	c.must(http.StatusNoContent, "DELETE", "/api/v1/prov/templates/"+id, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/prov/templates/"+id, nil)
	for _, b := range prov.Builtins() {
		c.must(http.StatusConflict, "PATCH", "/api/v1/prov/templates/"+b.BuiltinRef, map[string]any{"priority": 1})
		c.must(http.StatusCreated, "POST", "/api/v1/prov/templates/"+b.BuiltinRef+"/copy", nil)
	}
}

// TestPhoneImportCSV fails if a dry run does not report each bad row
// (bad MAC, unknown extension, duplicate MAC in the file or the store,
// unknown vendor), if an apply with a bad row creates anything, or if a
// 500-row file is not created in one go.
func TestPhoneImportCSV(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	p.phone("001122334455", nil)
	csvBody := "mac,vendor,model,extension,label,blf\n" +
		"aa:bb:cc:00:00:01,yealink,T54W,101,Desk,102\n" +
		"zz,yealink,T54W,101\n" +
		"aabbcc000003,yealink,T54W,999\n" +
		"aa-bb-cc-00-00-01,snom,D785,102\n" +
		"001122334455,yealink,T54W,101\n" +
		"aabbcc000006,acme,X,101\n"
	r := c.must(http.StatusOK, "POST", "/api/v1/phones/import?dryRun=true", csvBody).json(t)
	rows := r["rows"].([]any)
	if r["ok"] != false || len(rows) != 6 {
		t.Fatalf("dry run = %v", r)
	}
	wantErr := []string{"", "bad MAC", "unknown extension", "duplicate MAC (also on line 2)", "duplicate MAC (already phone", "unknown vendor"}
	for i, w := range wantErr {
		errs := fmt.Sprint(rows[i].(map[string]any)["errors"])
		if (w == "" && errs != "[]") || !strings.Contains(errs, w) {
			t.Errorf("row %d errors = %s, want %q", i+1, errs, w)
		}
	}
	before := p.scalar(`SELECT count(*) FROM phones`)
	c.must(http.StatusBadRequest, "POST", "/api/v1/phones/import?dryRun=false", csvBody)
	if p.scalar(`SELECT count(*) FROM phones`) != before {
		t.Fatal("a failed apply created phones")
	}

	var big strings.Builder
	for i := range 500 {
		fmt.Fprintf(&big, "02%010x,grandstream,GRP2614,%s,Desk %d\n", i, []string{"101", "102"}[i%2], i)
	}
	if d := c.must(http.StatusOK, "POST", "/api/v1/phones/import?dryRun=true", big.String()).json(t); d["ok"] != true {
		t.Fatalf("500-row dry run = %v", d["ok"])
	}
	if n := p.scalar(`SELECT count(*) FROM phones`); n != before {
		t.Fatal("a dry run created phones")
	}
	a := c.must(http.StatusOK, "POST", "/api/v1/phones/import?dryRun=false", big.String()).json(t)
	if a["ok"] != true || a["created"] != float64(500) || p.scalar(`SELECT count(*) FROM phones`) != "501" {
		t.Fatalf("apply = ok %v created %v", a["ok"], a["created"])
	}
	c.must(http.StatusBadRequest, "POST", "/api/v1/phones/import", "")
	c.must(http.StatusBadRequest, "POST", "/api/v1/phones/import", "\"unterminated\n")
}

// fakeRedirect records checks and fails for the credential "bad".
type fakeRedirect struct {
	mu     sync.Mutex
	checks []redirect.Credentials
}

type fakeClient struct {
	f     *fakeRedirect
	v     prov.Vendor
	creds redirect.Credentials
}

func (c fakeClient) Vendor() prov.Vendor { return c.v }
func (fakeClient) Capabilities() redirect.Caps {
	return redirect.Caps{Supported: true, RegistersURL: true}
}
func (fakeClient) Register(context.Context, string, string, string) error { return nil }
func (fakeClient) Unregister(context.Context, string) error               { return nil }
func (fakeClient) Lookup(context.Context, string) (string, bool, error)   { return "", false, nil }
func (c fakeClient) Check(context.Context) error {
	c.f.mu.Lock()
	defer c.f.mu.Unlock()
	c.f.checks = append(c.f.checks, c.creds)
	if c.creds["snomSrapsAccessKeySecret"] == "bad" {
		return errors.New("401 from SRAPS")
	}
	return nil
}

func (f *fakeRedirect) New(v prov.Vendor, creds redirect.Credentials, _ []byte) (redirect.Client, error) {
	return fakeClient{f: f, v: v, creds: creds}, nil
}

// TestRedirectAccounts fails if a stored credential is returned, a
// credential that fails its check is saved, the sealed form does not open
// with the vendor's context, connecting an account does not queue the
// vendor's phones, or a deployment account can be changed.
func TestRedirectAccounts(t *testing.T) {
	f := &fakeRedirect{}
	dep := map[prov.Vendor]redirect.Credentials{prov.Yealink: {"yealinkRpsAccessKey": "k", "yealinkRpsAccessSecret": "s"}}
	p := newProvEnv(t, ProvConfig{RedirectClient: f.New, Deployment: dep})
	c := p.c
	ph := p.phone("000413000001", map[string]any{"vendor": "snom", "model": "D785"})
	if st := ph["redirectStatus"].(map[string]any); st["state"] != "not_configured" {
		t.Fatalf("status before an account = %v", st)
	}
	secretValue := "s3cr3t-" + strings.Repeat("x", 8)
	path := "/api/v1/prov/redirect/snom"
	c.must(http.StatusBadRequest, "PUT", path, map[string]any{"credentials": map[string]string{"snomSrapsAccessKeyId": "id", "snomSrapsAccessKeySecret": "bad"}})
	if n := p.scalar(`SELECT count(*) FROM prov_redirect_accounts WHERE credentials_enc IS NOT NULL`); n != "0" {
		t.Fatal("credentials that failed their check were saved")
	}
	acct := c.must(http.StatusOK, "PUT", path, map[string]any{"enabled": true,
		"credentials": map[string]string{"snomSrapsAccessKeyId": "id", "snomSrapsAccessKeySecret": secretValue}}).json(t)
	if acct["hasCredentials"] != true || acct["enabled"] != true || acct["lastCheckResult"] != "ok" || acct["supported"] != true {
		t.Fatalf("account = %v", acct)
	}
	for _, r := range []response{c.must(http.StatusOK, "GET", "/api/v1/prov/redirect", nil), c.must(http.StatusOK, "POST", path+"/check", nil)} {
		if bytes.Contains(r.body, []byte(secretValue)) || bytes.Contains(r.body, []byte(`"credentials"`)) {
			t.Fatalf("a response shows the credentials: %s", r.body)
		}
	}
	a, err := p.st.RedirectAccountCredentials(context.Background(), prov.Snom)
	if err != nil || a.Credentials["snomSrapsAccessKeySecret"] != secretValue {
		t.Fatalf("stored account = %v", err)
	}
	if n := p.scalar(`SELECT count(*) FROM audit_events WHERE resource = 'prov_redirect_account'`); n != "1" {
		t.Fatalf("account audit rows = %s", n)
	}
	// Connecting the account queued the vendor's phone.
	if n := p.scalar(`SELECT count(*) FROM prov_redirect_jobs WHERE vendor = 'snom' AND mac = '000413000001' AND op = 'register'`); n != "1" {
		t.Fatal("the vendor's phone was not queued")
	}
	if g := c.must(http.StatusOK, "GET", fmt.Sprint("/api/v1/phones/", ph["id"]), nil).json(t); g["redirectStatus"].(map[string]any)["state"] != "pending" {
		t.Fatalf("status after connecting = %v", g["redirectStatus"])
	}
	// Deleting the phone queues an unregister in place of the register.
	seq := p.scalar(`SELECT seq FROM prov_redirect_jobs WHERE mac = '000413000001'`)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprint("/api/v1/phones/", ph["id"]), nil)
	if n := p.scalar(`SELECT count(*) FROM prov_redirect_jobs WHERE mac = '000413000001' AND op = 'unregister' AND seq <> $1`, seq); n != "1" {
		t.Fatal("deleting the phone did not replace the job with an unregister")
	}

	// The deployment's account is read-only and counts as configured.
	list := c.must(http.StatusOK, "GET", "/api/v1/prov/redirect", nil).json(t)["items"].([]any)
	if y := list[0].(map[string]any); y["vendor"] != "yealink" || y["fromDeployment"] != true || y["hasCredentials"] != true {
		t.Fatalf("yealink = %v", y)
	}
	c.must(http.StatusConflict, "PUT", "/api/v1/prov/redirect/yealink", map[string]any{"enabled": false})
	c.must(http.StatusConflict, "DELETE", "/api/v1/prov/redirect/yealink", nil)
	c.must(http.StatusNotFound, "PUT", "/api/v1/prov/redirect/generic", map[string]any{"enabled": true})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/prov/redirect/poly", map[string]any{"credentials": map[string]string{"k": "v"}})
	c.must(http.StatusNoContent, "DELETE", path, nil)
}

// TestProvSettings fails if the DHCP values are not the boot URL on the
// public host over plain HTTP.
func TestProvSettings(t *testing.T) {
	p := newProvEnv(t, ProvConfig{CASHA256: strings.Repeat("ab", 32)})
	s := p.c.must(http.StatusOK, "GET", "/api/v1/prov/settings", nil).json(t)
	if s["bootUrl"] != "http://prov.hello.test/p/boot/" || s["caUrl"] != "http://prov.hello.test/p/ca.crt" ||
		s["publicUrl"] != testPublicURL+"/" || s["sipServer"] != "192.0.2.10:5060" || s["caSha256"] != strings.Repeat("ab", 32) {
		t.Fatalf("settings = %v", s)
	}
	b, _ := json.Marshal(s["dhcp"])
	for _, want := range []string{`{"option":66,"value":"http://prov.hello.test/p/boot/","vendor":"snom"}`, `"option":160`} {
		if !bytes.Contains(b, []byte(want)) {
			t.Fatalf("dhcp = %s, want %s", b, want)
		}
	}
}

// TestFirmwareHosting fails if an uploaded firmware is not stored with its
// SHA-256 under <vendor>/<sha256>/<filename>, cannot be read back with a
// seek (Range), if a pinned version is missing from the render data, or if
// deleting a pinned file is allowed. Serving it under a token, and never
// on /p/boot/, is the handler's TestFirmwareHosting half (internal/prov).
func TestFirmwareHosting(t *testing.T) {
	if testMinioEndpoint == "" {
		t.Skip("HELLO_TEST_MINIO_ENDPOINT not set")
	}
	objs := startMinio(t)
	p := newProvEnv(t, ProvConfig{Firmware: objs})
	payload := bytes.Repeat([]byte("firmware-"), 100000)
	upload := func(name string) response {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		// The filename field, unlike the part's name, is not reduced to
		// its base by the multipart reader.
		for k, v := range map[string]string{"vendor": "yealink", "modelGlob": "T5*", "version": "96.86.0.70", "filename": name} {
			_ = mw.WriteField(k, v)
		}
		fw, _ := mw.CreateFormFile("file", "upload.bin")
		_, _ = fw.Write(payload)
		_ = mw.Close()
		p.c.header.Set("Content-Type", mw.FormDataContentType())
		defer p.c.header.Del("Content-Type")
		return p.c.do("POST", "/api/v1/prov/firmware", buf.String())
	}
	r := upload("T54W-96.86.0.70.rom")
	if r.code != http.StatusCreated {
		t.Fatalf("upload = %d %s", r.code, r.body)
	}
	f := r.json(t)
	sum := sha256.Sum256(payload)
	if f["sha256"] != hex.EncodeToString(sum[:]) || f["size"] != float64(len(payload)) || f["pinned"] != false {
		t.Fatalf("firmware = %v", f)
	}
	if r := upload("T54W-96.86.0.70.rom"); r.code != http.StatusConflict {
		t.Fatalf("second upload of the name = %d", r.code)
	}
	// The refused duplicate left the live object in place (checked by the
	// read below).
	if r := upload("../evil.rom"); r.code != http.StatusBadRequest {
		t.Fatalf("path-like name = %d", r.code)
	}
	ctx := context.Background()
	rc, err := objs.OpenFirmware(ctx, "yealink/"+hex.EncodeToString(sum[:])+"/T54W-96.86.0.70.rom")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rc.Seek(9, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	head := make([]byte, 9)
	if _, err := io.ReadFull(rc, head); err != nil || string(head) != "firmware-" {
		t.Fatalf("ranged read = %q, %v", head, err)
	}
	_ = rc.Close()

	id := f["id"]
	p.c.must(http.StatusOK, "PUT", "/api/v1/prov/firmware/pins", []map[string]any{{"vendor": "yealink", "modelGlob": "T5*", "firmwareId": id}})
	p.c.must(http.StatusBadRequest, "PUT", "/api/v1/prov/firmware/pins", []map[string]any{{"vendor": "snom", "modelGlob": "D*", "firmwareId": id}})
	phone := p.phone("805ec0000009", nil)
	d, _, err := p.st.RenderInputs(ctx, int64(phone["id"].(float64)))
	if (err != nil && !errors.Is(err, prov.ErrNoTemplate)) || d.Firmware == nil || !strings.HasSuffix(d.Firmware.URL, "/fw/T54W-96.86.0.70.rom") {
		t.Fatalf("render firmware = %+v, %v", d.Firmware, err)
	}
	p.c.must(http.StatusConflict, "DELETE", fmt.Sprint("/api/v1/prov/firmware/", id), nil)
	p.c.must(http.StatusOK, "PUT", "/api/v1/prov/firmware/pins", []map[string]any{})
	p.c.must(http.StatusNoContent, "DELETE", fmt.Sprint("/api/v1/prov/firmware/", id), nil)
	if _, err := objs.OpenFirmware(ctx, "yealink/"+hex.EncodeToString(sum[:])+"/T54W-96.86.0.70.rom"); err == nil {
		t.Fatal("the deleted firmware's object is still there")
	}
}

// TestMaskSecrets fails if any secret survives in the render data a
// preview renders from: the SIP secret, the admin password, or the token
// in the provisioning or firmware URL.
func TestMaskSecrets(t *testing.T) {
	token := strings.Repeat("a2", 26)
	d := prov.RenderData{
		Phone:    prov.Phone{AdminPassword: "admin-pw"},
		Line:     prov.Line{Password: "sip-secret"},
		Prov:     prov.ProvInfo{URL: testPublicURL + "/p/" + token + "/"},
		Firmware: &prov.FirmwareInfo{URL: testPublicURL + "/p/" + token + "/fw/x.rom"},
	}
	fw := d.Firmware
	maskSecrets(&d)
	b, _ := json.Marshal(d)
	for _, s := range []string{token, "admin-pw", "sip-secret"} {
		if bytes.Contains(b, []byte(s)) {
			t.Fatalf("masked data still holds %q: %s", s, b)
		}
	}
	if d.Prov.URL != testPublicURL+"/p/"+mask+"/" || d.Firmware.URL != testPublicURL+"/p/"+mask+"/fw/x.rom" {
		t.Fatalf("masked URLs = %s, %s", d.Prov.URL, d.Firmware.URL)
	}
	if strings.Contains(fw.URL, mask) {
		t.Fatal("masking changed the caller's firmware value in place")
	}
}
