package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/store"
)

// concStore answers the concurrency query with no recorded calls.
type concStore struct{ tokenStore }

func (concStore) CDRConcurrency(_ context.Context, from, to time.Time, step time.Duration) ([]store.ConcurrencyPoint, error) {
	var pts []store.ConcurrencyPoint
	for t := from; !t.After(to); t = t.Add(step) {
		pts = append(pts, store.ConcurrencyPoint{At: t})
	}
	return pts, nil
}

// activeCalls is live state with one answered internal call that started
// ten seconds ago, so no CDR exists for it.
type activeCalls struct{ noLive }

func (activeCalls) Calls(context.Context) ([]livestate.Call, error) {
	now := time.Now()
	return []livestate.Call{{
		ID: "live-1", State: "connected", Node: "sip-1", Direction: "internal",
		StartedAt: now.Add(-10 * time.Second), AnsweredAt: now.Add(-9 * time.Second),
	}}, nil
}

// TestCDRConcurrencyCountsLiveCalls fails if GET /api/v1/cdrs/concurrency
// leaves out a call still in progress (it has no CDR yet): the latest sample
// must count it, by direction, and the peak must report it.
func TestCDRConcurrencyCountsLiveCalls(t *testing.T) {
	h := Handler(Config{Store: concStore{}, Live: activeCalls{}})
	req := httptest.NewRequest(http.MethodGet, "/api/v1/cdrs/concurrency?range=1h", nil)
	req.Header.Set("Authorization", "Bearer anything")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET concurrency = %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Points []store.ConcurrencyPoint `json:"points"`
		Peak   *concurrencyPeak         `json:"peak"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Points) == 0 {
		t.Fatal("no points")
	}
	last := out.Points[len(out.Points)-1]
	if last.Internal != 1 || last.Inbound != 0 || last.Outbound != 0 {
		t.Fatalf("latest sample = %+v, want one internal call", last)
	}
	if out.Peak == nil || out.Peak.Calls != 1 {
		t.Fatalf("peak = %+v, want 1", out.Peak)
	}
	// The call began after every earlier whole step.
	if first := out.Points[0]; first.Internal != 0 {
		t.Fatalf("first sample = %+v, want no calls", first)
	}
}
