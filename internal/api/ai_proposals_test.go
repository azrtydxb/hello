package api

import (
	"net/http"
	"testing"
)

// TestAIProposalsDisabled: without a proposal service every proposal route
// answers 503 ai_disabled. Fails if one answers anything else.
func TestAIProposalsDisabled(t *testing.T) {
	c := newEnv(t, noLive{}).login()
	id := "00000000-0000-0000-0000-000000000000"
	for _, r := range []struct{ method, path string }{
		{"GET", "/api/v1/ai/proposals"},
		{"GET", "/api/v1/ai/proposals/" + id},
		{"POST", "/api/v1/ai/proposals/" + id + "/apply"},
		{"POST", "/api/v1/ai/proposals/" + id + "/dismiss"},
	} {
		res := c.do(r.method, r.path, map[string]string{"reason": "wrong"})
		if res.code != http.StatusServiceUnavailable || res.json(t)["error"].(map[string]any)["code"] != "ai_disabled" {
			t.Errorf("%s %s = %d %s", r.method, r.path, res.code, res.body)
		}
	}
}
