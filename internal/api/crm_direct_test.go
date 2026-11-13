package api

import (
	"archive/zip"
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/crm/zendesk"
)

// newDirectEnv is a CRM console over the real Zendesk connector pointed at a
// fake Zendesk that accepts one API token.
func newDirectEnv(t *testing.T, publicURL string) *crmEnv {
	t.Helper()
	zd := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, tok, ok := r.BasicAuth(); !ok || tok != "GOOD-TOKEN" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"Couldn't authenticate you"}`))
			return
		}
		_, _ = w.Write([]byte(`{"user":{"id":1}}`))
	}))
	t.Cleanup(zd.Close)
	conn := zendesk.New(zd.Client())
	conn.Base = zd.URL
	repo := crm.NewMem()
	tm := &crm.TokenManager{Repo: repo, Providers: func(k string) (crm.Connector, bool) { return conn, k == "zendesk" }}
	svc := &crm.Service{Repo: repo, Tokens: tm, Lookup: &crm.Lookup{Repo: repo, Tokens: tm}, PublicURL: publicURL}
	f, _, _, _ := newAcctFake(t)
	srv := httptest.NewServer(Handler(Config{Store: evStore{f}, CRM: svc, Log: slog.New(slog.DiscardHandler), SessionTTL: time.Hour}))
	t.Cleanup(srv.Close)
	return &crmEnv{repo: repo, svc: svc, srv: srv}
}

// TestCRMDirectConnectorAndSidebarApp: a token-based connector connects
// without a browser, a refused token is a field error that never echoes the
// token, and the helpdesk sidebar app downloads as a zip for an
// administrator only.
func TestCRMDirectConnectorAndSidebarApp(t *testing.T) {
	e := newDirectEnv(t, "https://hello.example.com")
	cfg := map[string]string{"clientId": "agent@acme.test", "subdomain": "acme"}
	created := e.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{
		"kind": "zendesk", "name": "Zendesk", "clientSecret": "BAD-TOKEN", "config": cfg})
	id := created["id"]
	if created["auth"] != "direct" || created["connection"] != nil {
		t.Fatalf("created = %v", created)
	}
	code, body, _ := e.do(t, "carol", "POST", connPath(id)+"/connect", nil)
	if code != 400 || strings.Contains(string(body), "BAD-TOKEN") || !strings.Contains(string(body), "clientSecret") {
		t.Fatalf("refused token = %d %s", code, body)
	}
	// Replacing the secret with a good one, then connecting.
	e.want(t, 200, "carol", "PATCH", connPath(id), map[string]any{"clientSecret": "GOOD-TOKEN"})
	got := e.want(t, 200, "carol", "POST", connPath(id)+"/connect", nil)
	if got["authorizeUrl"] != "" {
		t.Fatalf("connect = %v", got)
	}
	v := e.want(t, 200, "carol", "GET", connPath(id), nil)
	conn, _ := v["connection"].(map[string]any)
	if conn["status"] != "connected" || conn["externalAccount"] != "acme" {
		t.Fatalf("connection = %v", v["connection"])
	}
	// A changed subdomain needs the secret again.
	e.want(t, 400, "carol", "PATCH", connPath(id), map[string]any{"config": map[string]string{"subdomain": "other"}})

	// The sidebar app.
	code, zipBytes, h := e.do(t, "carol", "GET", connPath(id)+"/app", nil)
	if code != 200 || h.Get("Content-Type") != "application/zip" || !strings.Contains(h.Get("Content-Disposition"), "hello-zendesk-app.zip") {
		t.Fatalf("app = %d %v", code, h)
	}
	zr, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil || len(zr.File) == 0 {
		t.Fatalf("zip: %v", err)
	}
	if code, _, _ := e.do(t, "alice", "GET", connPath(id)+"/app", nil); code != 403 {
		t.Fatalf("a non-administrator got the app: %d", code)
	}
	// No public URL: no app.
	e2 := newDirectEnv(t, "")
	c2 := e2.want(t, 201, "carol", "POST", "/api/v1/integrations/crm", map[string]any{"kind": "zendesk", "name": "Z", "clientSecret": "x", "config": cfg})
	e2.want(t, 503, "carol", "GET", connPath(c2["id"])+"/app", nil)
}
