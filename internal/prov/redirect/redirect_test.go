package redirect

import (
	"encoding/json"
	"testing"
	"time"
)

// TestStatusJSON fails if a phone's redirect status leaves the API shape
// of the plan's contract 7, {"state","reason","at"}, or drops the reason
// of a failure.
func TestStatusJSON(t *testing.T) {
	at := time.Date(2026, 10, 6, 12, 0, 0, 0, time.UTC)
	b, err := json.Marshal(Status{State: StateFailed, Reason: "drift", At: at})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"state":"failed","reason":"drift","at":"2026-10-06T12:00:00Z"}`; string(b) != want {
		t.Fatalf("status JSON = %s, want %s", b, want)
	}
	var back Status
	if err := json.Unmarshal([]byte(`{"state":"not_configured"}`), &back); err != nil || back.State != StateNotConfigured {
		t.Fatalf("default column value = %+v, %v", back, err)
	}
}
