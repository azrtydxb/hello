package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
)

// haCalls is live state with one owned, one taken-over and one legacy call
// (written by a node that predates the ha field).
type haCalls struct{ noLive }

func (haCalls) Calls(context.Context) ([]livestate.Call, error) {
	now := time.Now()
	return []livestate.Call{
		{ID: "owned-1", State: "connected", Node: "sip-1", StartedAt: now, HA: livestate.HAOwned},
		{ID: "taken-1", State: "connected", Node: "sip-2", StartedAt: now, HA: livestate.HATakenOver},
		{ID: "legacy-1", State: "connected", Node: "sip-1", StartedAt: now},
	}, nil
}

// TestCallsHAFlag fails if GET /api/v1/calls does not report every call's
// in-call HA state (incall-ha S-6): "taken-over" for a call a survivor
// re-homed, "owned" for a call still on the node that set it up, and
// "owned" for a record that carries no state at all.
func TestCallsHAFlag(t *testing.T) {
	h := Handler(Config{Store: tokenStore{}, Live: haCalls{}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/calls", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/calls = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"owned-1": "owned", "taken-1": "taken-over", "legacy-1": "owned"}
	if len(out.Items) != len(want) {
		t.Fatalf("calls = %v", out.Items)
	}
	for _, c := range out.Items {
		id, _ := c["id"].(string)
		if got, _ := c["ha"].(string); got != want[id] {
			t.Errorf("call %s ha = %q, want %q", id, got, want[id])
		}
	}
}
