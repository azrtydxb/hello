package api

// Tests for the SCIM token and status routes and the /scim/v2 mount. They run
// through Handler over the in-memory account fake, the directory engine on
// directory.Mem and the SCIM service on scim.Mem, so they need no services;
// the documented status of every route is produced here.

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/directory"
	"github.com/azrtydxb/hello/internal/scim"
)

func newSCIMEnv(t *testing.T) (*dirEnv, *scim.Mem) {
	t.Helper()
	f, _, _, _ := newAcctFake(t)
	e := &dirEnv{f: f, mem: directory.NewMem(), drv: &dirDriver{res: &directory.Result{}}}
	sm := scim.NewMem()
	e.eng = &directory.Engine{Store: e.mem, Replica: "t",
		Drivers: map[string]directory.Driver{directory.KindLDAP: e.drv, directory.KindSCIM: scim.Driver{Store: sm}}}
	svc := &scim.Service{Store: sm, Engine: e.eng, PublicURL: "https://pbx.example.com"}
	e.h = Handler(Config{Store: whStore{f}, Directory: e.eng, SCIM: svc, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour})
	return e, sm
}

func TestSCIMTokenRoutes(t *testing.T) {
	e, sm := newSCIMEnv(t)
	c := wantJSON(t, e.admin("POST", "/api/v1/directory/connectors", map[string]any{"kind": "scim", "name": "Entra", "enabled": true,
		"options": map[string]any{"loginMethod": "oidc", "oidcProvider": "entra"}}),
		http.StatusCreated, "create scim connector")
	if c["kind"] != "scim" || c["scheduleMinutes"] != float64(0) || c["hasCredential"] != false {
		t.Fatalf("connector = %v", c)
	}
	id := strconv.Itoa(int(c["id"].(float64)))
	sm.Kinds[int64(c["id"].(float64))] = "scim"
	ldap := e.connector(t, nil)
	sm.Kinds[int64(ldap["id"].(float64))] = "ldap"
	ldapID := strconv.Itoa(int(ldap["id"].(float64)))
	base := "/api/v1/directory/connectors/"

	// A SCIM connector takes no password.
	if rec := e.admin("PATCH", base+id, map[string]any{"bindPassword": "x"}); rec.Code != http.StatusBadRequest {
		t.Fatalf("password on scim: %d %s", rec.Code, rec.Body)
	}

	tok := wantJSON(t, e.admin("POST", base+id+"/scim-token", nil), http.StatusCreated, "issue")
	secret, _ := tok["token"].(string)
	if !strings.HasPrefix(secret, scim.TokenPrefix) || tok["baseUrl"] != "https://pbx.example.com/scim/v2" ||
		!strings.HasPrefix(tok["hint"].(string), scim.TokenPrefix) || len(tok["hint"].(string)) > len(scim.TokenPrefix)+4 {
		t.Fatalf("issued = %v", tok)
	}
	wantJSON(t, e.admin("POST", base+id+"/scim-token", nil), http.StatusCreated, "second")
	if rec := e.admin("POST", base+id+"/scim-token", nil); rec.Code != http.StatusConflict || acctErrCode(rec) != "too_many_tokens" {
		t.Fatalf("third token: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("POST", base+ldapID+"/scim-token", nil); rec.Code != http.StatusConflict || acctErrCode(rec) != "not_scim" {
		t.Fatalf("token for ldap: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("POST", base+"999/scim-token", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("token for a missing connector: %d", rec.Code)
	}
	// Issuing needs the secrets scope; reading needs an administrator.
	if rec := e.call("", "tok-admin-only", "POST", base+id+"/scim-token", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("issue without secrets: %d", rec.Code)
	}
	for _, p := range []string{base + id + "/scim-tokens", base + id + "/scim"} {
		if rec := e.call("dave", "", "GET", p, nil); rec.Code != http.StatusForbidden {
			t.Fatalf("viewer %s: %d", p, rec.Code)
		}
	}
	if rec := e.call("dave", "", "DELETE", "/api/v1/directory/scim-tokens/1", nil); rec.Code != http.StatusForbidden {
		t.Fatalf("viewer revoke: %d", rec.Code)
	}

	list := wantJSON(t, e.admin("GET", base+id+"/scim-tokens", nil), http.StatusOK, "list")
	items := list["items"].([]any)
	if len(items) != 2 || strings.Contains(e.admin("GET", base+id+"/scim-tokens", nil).Body.String(), secret) {
		t.Fatalf("list = %v", list)
	}
	for _, p := range []string{"/scim-tokens", "/scim"} {
		if rec := e.admin("GET", base+ldapID+p, nil); rec.Code != http.StatusConflict {
			t.Fatalf("ldap %s: %d", p, rec.Code)
		}
		if rec := e.admin("GET", base+"999"+p, nil); rec.Code != http.StatusNotFound {
			t.Fatalf("missing %s: %d", p, rec.Code)
		}
	}

	// The token works on /scim/v2, through the same handler.
	scimCall := func(method, path, body, token string) *httptest.ResponseRecorder {
		var r *http.Request
		if body == "" {
			r = httptest.NewRequest(method, path, nil)
		} else {
			r = httptest.NewRequest(method, path, strings.NewReader(body))
			r.Header.Set("Content-Type", scim.ContentType)
		}
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		e.h.ServeHTTP(rec, r)
		return rec
	}
	if rec := scimCall("POST", "/scim/v2/Users", `{"userName":"ann@corp.org","displayName":"Ann"}`, secret); rec.Code != http.StatusCreated {
		t.Fatalf("scim create: %d %s", rec.Code, rec.Body)
	}
	// A SCIM token is no API credential.
	if rec := e.call("", secret, "GET", "/api/v1/directory/connectors", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("scim token on the API: %d", rec.Code)
	}

	st := wantJSON(t, e.admin("GET", base+id+"/scim", nil), http.StatusOK, "status")
	res := st["resources"].(map[string]any)
	if st["baseUrl"] != "https://pbx.example.com/scim/v2" || res["users"] != float64(1) || st["activeTokens"] != float64(2) ||
		st["pausedAt"] != nil || st["lastRequestAt"] == nil {
		t.Fatalf("status = %v", st)
	}
	runs := wantJSON(t, e.admin("GET", base+id+"/runs", nil), http.StatusOK, "runs")
	if r := runs["items"].([]any); len(r) != 1 || r[0].(map[string]any)["kind"] != "scim" {
		t.Fatalf("runs = %v", runs)
	}

	first := strconv.Itoa(int(items[1].(map[string]any)["id"].(float64)))
	if rec := e.admin("DELETE", "/api/v1/directory/scim-tokens/"+first, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("revoke: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("DELETE", "/api/v1/directory/scim-tokens/9999", nil); rec.Code != http.StatusNotFound {
		t.Fatalf("revoke missing: %d", rec.Code)
	}
	if rec := scimCall("GET", "/scim/v2/Users", "", secret); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token: %d", rec.Code)
	}
}

func TestSCIMNotConfigured(t *testing.T) {
	e := newDirEnv(t)
	r := httptest.NewRequest("GET", "/scim/v2/Users", nil)
	r.Header.Set("Authorization", "Bearer "+scim.TokenPrefix+"x")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	var b map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &b)
	if rec.Code != http.StatusNotFound || rec.Header().Get("Content-Type") != scim.ContentType || b["status"] != "404" {
		t.Fatalf("scim without the service: %d %s", rec.Code, rec.Body)
	}
	if rec := e.admin("POST", "/api/v1/directory/connectors/1/scim-token", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("issue without the service: %d", rec.Code)
	}
}
