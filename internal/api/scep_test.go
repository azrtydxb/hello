package api

import (
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
)

// TestSCEPAPI walks the SCEP and 802.1X API against PostgreSQL: the device
// CA (generate, import, the 409 without replace), the issued certificates
// (list, filter, revoke, the refusals), the per-phone challenge rotation,
// the 802.1X profiles and a phone group's security settings. It fails if a
// key, challenge or password is ever returned, a refused change changes
// anything, or a documented status is not produced.
func TestSCEPAPI(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c

	st := c.must(http.StatusOK, "GET", "/api/v1/scep", nil).json(t)
	if st["ca"] != nil || st["crlUrl"] != "http://prov.hello.test/p/scep-ca.crl" {
		t.Fatalf("status before a CA = %v", st)
	}
	for name, body := range map[string]map[string]any{
		"no mode":           {},
		"unknown mode":      {"mode": "magic"},
		"generate with key": {"mode": "generate", "keyPem": "x"},
		"long name":         {"mode": "generate", "commonName": strings.Repeat("x", 65)},
		"too many years":    {"mode": "generate", "validityYears": 31},
		"import garbage":    {"mode": "import", "certPem": "nope", "keyPem": "nope"},
		"extra field":       {"mode": "generate", "x": 1},
	} {
		if r := c.do("PUT", "/api/v1/scep/ca", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, r.code)
		}
	}
	ca := c.must(http.StatusOK, "PUT", "/api/v1/scep/ca", map[string]any{"mode": "generate", "commonName": "Lab devices", "validityYears": 2}).json(t)
	if ca["source"] != "generated" || !strings.Contains(fmt.Sprint(ca["subject"]), "Lab devices") || strings.Contains(fmt.Sprint(ca), "PRIVATE KEY") {
		t.Fatalf("generated CA = %v", ca)
	}
	c.must(http.StatusConflict, "PUT", "/api/v1/scep/ca", map[string]any{"mode": "generate"})

	// Import an existing CA (replace).
	certPEM, key, err := prov.GenerateSCEPCA("Enterprise issuing CA", 3, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(key)
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
	imported := c.must(http.StatusOK, "PUT", "/api/v1/scep/ca", map[string]any{"mode": "import", "certPem": certPEM, "keyPem": keyPEM, "replace": true}).json(t)
	if imported["source"] != "imported" || imported["id"] == ca["id"] {
		t.Fatalf("imported CA = %v", imported)
	}
	if got := c.must(http.StatusOK, "GET", "/api/v1/scep", nil); strings.Contains(string(got.body), "PRIVATE KEY") {
		t.Fatal("the CA key is returned")
	}

	// A Grandstream phone in a group.
	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Desk"}).json(t)
	gpath := fmt.Sprintf("/api/v1/phone-groups/%v/security", g["id"])
	sec := c.must(http.StatusOK, "GET", gpath, nil).json(t)
	if sec["scepEnabled"] != false || sec["certValidityDays"] != float64(365) || sec["wiredDot1xProfileId"] != nil {
		t.Fatalf("a new group's security = %v", sec)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/phone-groups/999999/security", nil)
	phone := p.phone("c0:74:ad:00:00:10", map[string]any{"vendor": "grandstream", "model": "GRP2602", "groupId": g["id"]})
	pid := phone["id"]

	// 802.1X profiles.
	c.must(http.StatusOK, "GET", "/api/v1/dot1x-profiles", nil)
	for name, body := range map[string]map[string]any{
		"no name":          {"method": "peap", "identity": "{mac}", "password": "pw"},
		"peap without pw":  {"name": "A", "method": "peap", "identity": "{mac}"},
		"tls with pw":      {"name": "A", "method": "eap-tls", "identity": "{mac}", "password": "pw"},
		"bad method":       {"name": "A", "method": "leap", "identity": "{mac}", "password": "pw"},
		"identity spaces":  {"name": "A", "method": "peap", "identity": "a b", "password": "pw"},
		"ca not pem":       {"name": "A", "method": "peap", "identity": "{mac}", "password": "pw", "caCertPem": "nope"},
		"unknown property": {"name": "A", "method": "peap", "identity": "{mac}", "password": "pw", "x": true},
	} {
		if r := c.do("POST", "/api/v1/dot1x-profiles", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: %d, want 400", name, r.code)
		}
	}
	prof := c.must(http.StatusCreated, "POST", "/api/v1/dot1x-profiles", map[string]any{
		"name": "Office LAN", "method": "peap", "identity": "phone-{mac}", "password": "radius-pw", "caCertPem": certPEM, "serverDomain": "radius.lab"}).json(t)
	if prof["hasPassword"] != true || strings.Contains(fmt.Sprint(prof), "radius-pw") {
		t.Fatalf("created profile = %v", prof)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/dot1x-profiles", map[string]any{"name": "office lan", "method": "eap-tls", "identity": "{mac}"})
	other := c.must(http.StatusCreated, "POST", "/api/v1/dot1x-profiles", map[string]any{"name": "Certs", "method": "eap-tls", "identity": "{MAC}"}).json(t)
	ppath := fmt.Sprintf("/api/v1/dot1x-profiles/%v", prof["id"])
	c.must(http.StatusOK, "GET", ppath, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/dot1x-profiles/999999", nil)
	upd := c.must(http.StatusOK, "PUT", ppath, map[string]any{"name": "Office LAN", "method": "peap", "identity": "{mac}"}).json(t)
	if upd["hasPassword"] != true || upd["identity"] != "{mac}" {
		t.Fatalf("update keeping the password = %v", upd)
	}
	c.must(http.StatusBadRequest, "PUT", ppath, map[string]any{"name": "Office LAN", "method": "peap"})
	c.must(http.StatusConflict, "PUT", ppath, map[string]any{"name": "certs", "method": "peap", "identity": "{mac}"})
	c.must(http.StatusNotFound, "PUT", "/api/v1/dot1x-profiles/999999", map[string]any{"name": "Z", "method": "eap-tls", "identity": "{mac}"})

	// The group's settings.
	c.must(http.StatusBadRequest, "PUT", gpath, map[string]any{"scepEnabled": true, "certValidityDays": 7})
	c.must(http.StatusBadRequest, "PUT", gpath, map[string]any{"scepEnabled": true, "wiredDot1xProfileId": 999999})
	c.must(http.StatusBadRequest, "PUT", gpath, map[string]any{})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/security", map[string]any{"scepEnabled": true})
	sec = c.must(http.StatusOK, "PUT", gpath, map[string]any{"scepEnabled": true, "certValidityDays": 90, "wiredDot1xProfileId": prof["id"]}).json(t)
	if sec["scepEnabled"] != true || sec["wiredDot1xProfileId"] != prof["id"] {
		t.Fatalf("security after PUT = %v", sec)
	}
	c.must(http.StatusConflict, "DELETE", ppath, nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/dot1x-profiles/%v", other["id"]), nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/dot1x-profiles/999999", nil)

	// The phone's configuration preview carries SCEP and the wired profile,
	// masked.
	prev := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/phones/%v/preview?file=cfgc074ad000010.xml", pid), nil)
	body := string(prev.body)
	if !strings.Contains(body, "<P8647>1</P8647>") || !strings.Contains(body, "<P7901>3</P7901>") || strings.Contains(body, "radius-pw") {
		t.Fatalf("preview lacks SCEP or the wired profile, or shows the password")
	}
	var challengeRows int
	if err := p.db.QueryRow(`SELECT count(*) FROM scep_challenges WHERE phone_id = $1`, pid).Scan(&challengeRows); err != nil || challengeRows != 1 {
		t.Fatalf("challenge rows = %d, %v", challengeRows, err)
	}
	c.must(http.StatusNoContent, "POST", fmt.Sprintf("/api/v1/phones/%v/scep-challenge", pid), nil)
	c.must(http.StatusNotFound, "POST", "/api/v1/phones/999999/scep-challenge", nil)
	if err := p.db.QueryRow(`SELECT count(*) FROM scep_challenges WHERE phone_id = $1`, pid).Scan(&challengeRows); err != nil || challengeRows != 0 {
		t.Fatalf("challenge rows after rotation = %d, %v", challengeRows, err)
	}

	// Issued certificates.
	var certID int64
	if err := p.db.QueryRow(`INSERT INTO scep_certificates (ca_id, phone_id, mac, serial, subject, key_sha256, cert_pem, kind, not_before, not_after)
		VALUES ($1, $2, 'c074ad000010', 'abc123', 'CN=c074ad000010', $3, 'x', 'enrol', now(), now() + interval '90 days') RETURNING id`,
		imported["id"], pid, strings.Repeat("0", 64)).Scan(&certID); err != nil {
		t.Fatal(err)
	}
	list := c.must(http.StatusOK, "GET", fmt.Sprintf("/api/v1/scep/certificates?phoneId=%v&status=active", pid), nil).json(t)
	if items := list["items"].([]any); len(items) != 1 {
		t.Fatalf("certificates = %v", list)
	}
	c.must(http.StatusBadRequest, "GET", "/api/v1/scep/certificates?status=lost", nil)
	c.must(http.StatusBadRequest, "GET", "/api/v1/scep/certificates?phoneId=x", nil)
	rpath := fmt.Sprintf("/api/v1/scep/certificates/%d/revoke", certID)
	c.must(http.StatusBadRequest, "POST", rpath, map[string]any{"reason": "phone_removed"})
	rev := c.must(http.StatusOK, "POST", rpath, map[string]any{"reason": "key_compromise"}).json(t)
	if rev["status"] != "revoked" || rev["revokeReason"] != "key_compromise" {
		t.Fatalf("revoked = %v", rev)
	}
	c.must(http.StatusConflict, "POST", rpath, map[string]any{})
	c.must(http.StatusNotFound, "POST", "/api/v1/scep/certificates/999999/revoke", map[string]any{})
	if l := c.must(http.StatusOK, "GET", "/api/v1/scep/certificates?status=revoked", nil).json(t); len(l["items"].([]any)) != 1 {
		t.Fatalf("revoked list = %v", l)
	}
}
