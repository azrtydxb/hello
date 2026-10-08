package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// lateFindings forwards to the store the test env creates after its
// Handler is configured.
type lateFindings struct{ AIFindings }

func finding(id, sev string) store.AIFindingInput {
	return store.AIFindingInput{CandidateID: "trunk_down:" + id, Type: "trunk_down", Subject: id, Severity: sev, Title: "t " + id, Evidence: json.RawMessage(`{"n":1}`)}
}

// TestAIFindingsAPI fails if the findings routes list, filter, show,
// acknowledge or dismiss wrongly, if a viewer can change one, if a dismissal
// needs no reason, or if AI off does not answer 503 ai_disabled.
func TestAIFindingsAPI(t *testing.T) {
	ctx := context.Background()
	late := &lateFindings{}
	e := newEnvConfig(t, Config{Findings: late}, nil)
	late.AIFindings = e.st
	if err := e.st.UpsertAIFindings(ctx, time.Now(), []store.AIFindingInput{
		finding("a", "critical"), finding("b", "warning"), finding("c", "warning"), finding("d", "info")}, nil); err != nil {
		t.Fatal(err)
	}
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateUser(ctx, "test", "viewer", hash, auth.RoleViewer); err != nil {
		t.Fatal(err)
	}
	viewer := e.client()
	viewer.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": "viewer", "password": testPassword})
	op := e.login()

	list := func(c *client, q string) (items []store.AIFinding, score float64) {
		var out struct {
			Items       []store.AIFinding `json:"items"`
			HealthScore float64           `json:"healthScore"`
		}
		if err := json.Unmarshal(c.must(http.StatusOK, "GET", "/api/v1/ai/findings"+q, nil).body, &out); err != nil {
			t.Fatal(err)
		}
		return out.Items, out.HealthScore
	}
	all, score := list(viewer, "")
	if len(all) != 4 || all[0].Severity != "critical" || all[3].Severity != "info" || score != 10-3-2 {
		t.Fatalf("list = %d findings, first %s, score %v", len(all), all[0].Severity, score)
	}
	if items, _ := list(viewer, "?severity=warning&limit=1"); len(items) != 1 || items[0].Severity != "warning" {
		t.Fatalf("filtered = %+v", items)
	}
	if items, _ := list(viewer, "?type=nope"); len(items) != 0 {
		t.Fatalf("type filter = %+v", items)
	}
	for _, q := range []string{"?status=bogus", "?severity=bogus", "?limit=0", "?limit=201", "?limit=x"} {
		viewer.must(http.StatusBadRequest, "GET", "/api/v1/ai/findings"+q, nil)
	}

	a := all[0]
	got := viewer.must(http.StatusOK, "GET", "/api/v1/ai/findings/"+a.ID, nil).json(t)
	if got["candidateId"] != "trunk_down:a" || got["explained"] != false || got["explanation"] != nil {
		t.Fatalf("finding = %v", got)
	}
	viewer.must(http.StatusNotFound, "GET", "/api/v1/ai/findings/not-a-uuid", nil)
	viewer.must(http.StatusNotFound, "GET", "/api/v1/ai/findings/00000000-0000-0000-0000-000000000000", nil)

	for _, p := range []string{"acknowledge", "dismiss"} {
		viewer.must(http.StatusForbidden, "POST", "/api/v1/ai/findings/"+a.ID+"/"+p, map[string]string{"reason": "x"})
	}
	if r := op.must(http.StatusOK, "POST", "/api/v1/ai/findings/"+a.ID+"/acknowledge", nil).json(t); r["status"] != "acknowledged" || r["acknowledgedBy"] == nil {
		t.Fatalf("acknowledged = %v", r)
	}
	op.must(http.StatusConflict, "POST", "/api/v1/ai/findings/"+a.ID+"/acknowledge", nil)
	op.must(http.StatusNotFound, "POST", "/api/v1/ai/findings/00000000-0000-0000-0000-000000000000/acknowledge", nil)

	op.must(http.StatusBadRequest, "POST", "/api/v1/ai/findings/"+a.ID+"/dismiss", map[string]string{})
	op.must(http.StatusBadRequest, "POST", "/api/v1/ai/findings/"+a.ID+"/dismiss", map[string]string{"reason": ""})
	op.must(http.StatusBadRequest, "POST", "/api/v1/ai/findings/"+a.ID+"/dismiss", map[string]string{"reason": string(make([]rune, 501))})
	if r := op.must(http.StatusOK, "POST", "/api/v1/ai/findings/"+a.ID+"/dismiss", map[string]string{"reason": "known"}).json(t); r["status"] != "dismissed" || r["dismissReason"] != "known" {
		t.Fatalf("dismissed = %v", r)
	}
	op.must(http.StatusConflict, "POST", "/api/v1/ai/findings/"+a.ID+"/dismiss", map[string]string{"reason": "again"})
	if _, score := list(viewer, ""); score != 10-2 {
		t.Fatalf("score after dismissing the critical = %v, want 8", score)
	}
	var audits int
	if err := e.db.QueryRow(`SELECT count(*) FROM audit_events WHERE resource = 'ai_finding'`).Scan(&audits); err != nil || audits != 2 {
		t.Fatalf("audit rows = %d, %v; want acknowledge and dismiss", audits, err)
	}
}

// TestAIFindingsOff fails if the findings handlers answer anything but 503
// ai_disabled while AI is off. They are called directly: the Error schema's
// code enum in openapi.json (the AI core stream's) lists the code.
func TestAIFindingsOff(t *testing.T) {
	s := &server{}
	for name, h := range map[string]http.HandlerFunc{"list": s.listAIFindings, "get": s.getAIFinding,
		"acknowledge": s.acknowledgeAIFinding, "dismiss": s.dismissAIFinding} {
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest("GET", "/", nil))
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"ai_disabled"`) {
			t.Errorf("%s = %d %s", name, rec.Code, rec.Body)
		}
	}
}
