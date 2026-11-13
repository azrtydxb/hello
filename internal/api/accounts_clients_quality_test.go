package api

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// The reports a fake store was given, by fake (the fake itself stays as it is
// in accounts_test.go).
var (
	acctQualityMu sync.Mutex
	acctQuality   = map[*acctFake]map[string]store.ClientQualityInput{}
)

// RecordClientQuality keeps the reports a user filed, keyed like the table.
func (f *acctFake) RecordClientQuality(_ context.Context, uid, id int64, callID string, in store.ClientQualityInput) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.clients {
		if c.ID == id && c.user == uid {
			acctQualityMu.Lock()
			defer acctQualityMu.Unlock()
			if acctQuality[f] == nil {
				acctQuality[f] = map[string]store.ClientQualityInput{}
			}
			acctQuality[f][strconv.FormatInt(id, 10)+"/"+callID] = in
			return nil
		}
	}
	return store.ErrNotFound
}

// TestCallQualityReport fails if a client can file a report for a device that
// is not its own, if an implausible value is accepted, or if a valid report is
// not stored under the Call-ID and device it names.
func TestCallQualityReport(t *testing.T) {
	f, h, _, _ := newAcctFake(t)
	wantJSON(t, acctCall(h, "alice", "POST", "/api/v1/me/devices", map[string]any{"label": "x"}), 201, "enrol")
	body := func(m map[string]any) map[string]any {
		b := map[string]any{"deviceId": 1, "codec": "opus", "lossPct": 1.5, "jitterMs": 12, "rttMs": 40,
			"candidateLocal": "host", "candidateRemote": "relay", "durationMs": 61000,
			"video": map[string]any{"width": 1280, "height": 720, "fps": 30, "codec": "H264"}}
		for k, v := range m {
			b[k] = v
		}
		return b
	}
	if rec := acctCall(h, "alice", "POST", "/api/v1/me/calls/abc@host/quality", body(nil)); rec.Code != 204 {
		t.Fatalf("report = %d %s, want 204", rec.Code, rec.Body)
	}
	got, ok := acctQuality[f]["1/abc@host"]
	if !ok || got.Codec != "opus" || got.Video == nil || got.Video.Width != 1280 || got.CandidateRemote != "relay" {
		t.Fatalf("stored report = %+v, %v", got, ok)
	}
	// Another user's device is a 404, not a stored report.
	if rec := acctCall(h, "bob", "POST", "/api/v1/me/calls/abc@host/quality", body(nil)); rec.Code != 404 {
		t.Errorf("another user's device = %d, want 404", rec.Code)
	}
	for name, m := range map[string]map[string]any{
		"loss over 100": {"lossPct": 101}, "negative jitter": {"jitterMs": -1}, "no device": {"deviceId": 0},
		"long codec": {"codec": strings.Repeat("x", 33)}, "huge video": {"video": map[string]any{"width": 99999, "height": 1, "fps": 1, "codec": "VP8"}},
	} {
		if rec := acctCall(h, "alice", "POST", "/api/v1/me/calls/abc@host/quality", body(m)); rec.Code != 400 {
			t.Errorf("%s = %d %s, want 400", name, rec.Code, rec.Body)
		}
	}
	if rec := acctCall(h, "svc", "POST", "/api/v1/me/calls/abc@host/quality", body(nil)); rec.Code != 403 {
		t.Errorf("service account = %d, want 403", rec.Code)
	}
	// A script cannot flood the table: the credential rate limit applies.
	limited := false
	for i := 0; i < credentialRate+2 && !limited; i++ {
		limited = acctCall(h, "alice", "POST", "/api/v1/me/calls/flood/quality", body(nil)).Code == 429
	}
	if !limited {
		t.Error("no 429 after more reports than the rate limit allows")
	}
	if rec := acctCall(h, "", "POST", "/api/v1/me/calls/abc@host/quality", body(nil)); rec.Code != 401 {
		t.Errorf("anonymous = %d, want 401", rec.Code)
	}
}
