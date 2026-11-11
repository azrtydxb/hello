package store

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"
)

// linkFixture inserts calls and recordings: c1 101 -> +32 11 111 22
// (recorded), c2 102 -> 101 (recorded), c3 +3211111222 -> 102 (not recorded),
// and a recording r3 whose CDR does not exist yet.
func linkFixture(t *testing.T) (*Store, map[string]int64) {
	t.Helper()
	st, db, _ := provStore(t)
	ctx := context.Background()
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	ins := func(corr, src, dst, orig string, at time.Time) {
		t.Helper()
		if _, err := db.ExecContext(ctx, `INSERT INTO cdrs (correlation_id, sip_call_id, source, destination, original_destination,
			start_time, end_time, duration_ms, billable_ms, sip_node, final_status, termination_side, direction)
			VALUES ($1, 'sip-'||$1, $2, $3, $4, $5, $5, 1000, 1000, 'n1', 200, 'caller', 'outbound')`, corr, src, dst, orig, at); err != nil {
			t.Fatal(err)
		}
	}
	ins("c1", "101", "+3211111222", "", base)
	ins("c2", "102", "101", "", base.Add(time.Hour))
	ins("c3", "+3211111222", "102", "+3200", base.Add(2*time.Hour))
	ids := map[string]int64{}
	for _, c := range []string{"c1", "c2", "c4"} { // c4 has no CDR yet
		id, err := st.InsertRecording(ctx, c, "obj/"+c+".wav", "default", 5000)
		if err != nil {
			t.Fatal(err)
		}
		ids[c] = id
	}
	return st, ids
}

func callIDsOf(t *testing.T, st *Store, f LookupFilter) []string {
	t.Helper()
	ms, _, err := st.LookupCalls(context.Background(), f, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, m := range ms {
		out = append(out, m.CallID)
	}
	slices.Sort(out)
	return out
}

func recIDsOf(t *testing.T, st *Store, f LookupFilter) []string {
	t.Helper()
	rs, _, err := st.LookupRecordings(context.Background(), f, 0, 50)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rs {
		out = append(out, r.CorrelationID)
	}
	slices.Sort(out)
	return out
}

// TestLookupFilters fails if the lookup's number, extension, time, call id or
// extension restriction does not select the right calls and recordings.
func TestLookupFilters(t *testing.T) {
	st, _ := linkFixture(t)
	base := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		name string
		f    LookupFilter
		want []string
	}{
		{"everything", LookupFilter{}, []string{"c1", "c2", "c3"}},
		{"number, exact digits", LookupFilter{Number: "+32 11 111 222"}, []string{"c1", "c3"}},
		{"number, national form (last eight digits)", LookupFilter{Number: "011 111 222"}, []string{"c1", "c3"}},
		{"number, 00 form", LookupFilter{Number: "0032 11 111 222"}, []string{"c1", "c3"}},
		{"number, seven digits is exact only", LookupFilter{Number: "1111222"}, nil},
		{"number, a short number is exact only", LookupFilter{Number: "10"}, nil},
		{"extension number", LookupFilter{Number: "101"}, []string{"c1", "c2"}},
		{"extension", LookupFilter{Extension: "102"}, []string{"c2", "c3"}},
		{"dialled number", LookupFilter{Number: "+3200"}, []string{"c3"}},
		{"from", LookupFilter{From: base.Add(time.Hour)}, []string{"c2", "c3"}},
		{"to is exclusive", LookupFilter{To: base.Add(time.Hour)}, []string{"c1"}},
		{"call id", LookupFilter{CallID: "c2"}, []string{"c2"}},
		{"sip call id", LookupFilter{CallID: "sip-c3"}, []string{"c3"}},
		{"recorded", LookupFilter{Recorded: true}, []string{"c1", "c2"}},
		{"restricted to 101", LookupFilter{Restrict: true, Allowed: []string{"101"}}, []string{"c1", "c2"}},
		{"restricted to 102", LookupFilter{Restrict: true, Allowed: []string{"102"}}, []string{"c2", "c3"}},
		{"restricted to nothing", LookupFilter{Restrict: true, Allowed: []string{}}, nil},
		{"restriction and filter", LookupFilter{Restrict: true, Allowed: []string{"101"}, Extension: "102"}, []string{"c2"}},
		{"restriction and number", LookupFilter{Restrict: true, Allowed: []string{"102"}, Number: "+3211111222"}, []string{"c3"}},
	} {
		if got := callIDsOf(t, st, c.f); !slices.Equal(got, c.want) {
			t.Errorf("calls, %s = %v, want %v", c.name, got, c.want)
		}
	}
	for _, c := range []struct {
		name string
		f    LookupFilter
		want []string
	}{
		{"everything, including a recording without a CDR", LookupFilter{}, []string{"c1", "c2", "c4"}},
		{"restricted to 101", LookupFilter{Restrict: true, Allowed: []string{"101"}}, []string{"c1", "c2"}},
		{"restricted never returns a recording without a CDR", LookupFilter{Restrict: true, Allowed: []string{"101", "102"}}, []string{"c1", "c2"}},
		{"number", LookupFilter{Number: "3211111222"}, []string{"c1"}},
		{"call id", LookupFilter{CallID: "c4"}, []string{"c4"}},
	} {
		if got := recIDsOf(t, st, c.f); !slices.Equal(got, c.want) {
			t.Errorf("recordings, %s = %v, want %v", c.name, got, c.want)
		}
	}
	ms, _, err := st.LookupCalls(context.Background(), LookupFilter{CallID: "c1"}, 0, 5)
	if err != nil || len(ms) != 1 || ms[0].RecordingID == nil {
		t.Fatalf("call c1 = %+v (%v), want its recording id", ms, err)
	}
	ms, _, _ = st.LookupCalls(context.Background(), LookupFilter{CallID: "c3"}, 0, 5)
	if len(ms) != 1 || ms[0].RecordingID != nil {
		t.Errorf("call c3 has recording %v, want none", ms)
	}
	// Paging: newest first, cursor is the last id.
	page, next, err := st.LookupCalls(context.Background(), LookupFilter{}, 0, 2)
	if err != nil || len(page) != 2 || next != strconv.FormatInt(page[1].ID, 10) || page[0].CallID != "c3" {
		t.Fatalf("page = %v next %q (%v)", page, next, err)
	}
}

// TestRecordingLinkLifecycle fails if a link plays after it expired, was
// revoked, ran out of uses or lost its recording, or if creating and revoking
// one is not audited.
func TestRecordingLinkLifecycle(t *testing.T) {
	st, ids := linkFixture(t)
	ctx := context.Background()
	hash := func(s string) []byte { return []byte("hash-" + s + "-0123456789abcdef0123456789") }
	mk := func(name string, ttl time.Duration, maxUses int) RecordingLink {
		t.Helper()
		l, err := st.CreateRecordingLink(ctx, "user:tester", ids["c1"], hash(name), time.Now().Add(ttl), maxUses)
		if err != nil {
			t.Fatal(err)
		}
		return l
	}
	use := func(name string) LinkResult {
		t.Helper()
		obj, _, res, err := st.UseRecordingLink(ctx, hash(name))
		if err != nil {
			t.Fatal(err)
		}
		if (res == LinkOK) != (obj == "obj/c1.wav") {
			t.Fatalf("%s: result %s with object %q", name, res, obj)
		}
		return res
	}
	live, expired, revoked := mk("live", time.Hour, 0), mk("expired", -time.Minute, 0), mk("revoked", time.Hour, 0)
	mk("once", time.Hour, 1)
	if r := use("live"); r != LinkOK {
		t.Errorf("live = %s", r)
	}
	if r := use("expired"); r != LinkExpired {
		t.Errorf("expired = %s", r)
	}
	if r := use("once"); r != LinkOK {
		t.Errorf("once, first = %s", r)
	}
	if r := use("once"); r != LinkUsedUp {
		t.Errorf("once, second = %s", r)
	}
	if r := use("nope"); r != LinkUnknown {
		t.Errorf("unknown = %s", r)
	}
	if err := st.RevokeRecordingLink(ctx, "user:tester", revoked.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.RevokeRecordingLink(ctx, "user:tester", revoked.ID); err != nil {
		t.Errorf("revoking twice: %v", err)
	}
	if r := use("revoked"); r != LinkRevoked {
		t.Errorf("revoked = %s", r)
	}
	if err := st.RevokeRecordingLink(ctx, "user:tester", 99999); !errors.Is(err, ErrNotFound) {
		t.Errorf("revoking a missing link: %v", err)
	}
	if got, _ := st.GetRecordingLink(ctx, live.ID); got.Uses != 1 || got.LastUsedAt == nil {
		t.Errorf("live link %+v, want one use", got)
	}
	if n, err := st.CountActiveRecordingLinks(ctx); err != nil || n != 1 {
		t.Errorf("active links = %d (%v), want 1 (live)", n, err)
	}
	// A link dies with its recording.
	if _, err := st.DeleteRecording(ctx, "user:tester", ids["c1"]); err != nil {
		t.Fatal(err)
	}
	if r := use("live"); r != LinkUnknown {
		t.Errorf("after the recording was deleted = %s, want unknown", r)
	}
	if _, err := st.GetRecordingLink(ctx, expired.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("link kept after its recording went: %v", err)
	}
	// A link for a recording that does not exist is not created.
	if _, err := st.CreateRecordingLink(ctx, "user:tester", 99999, hash("x"), time.Now().Add(time.Hour), 0); err == nil {
		t.Error("link created for a missing recording")
	}
	var n int
	if err := st.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE resource = 'recording_link' AND action IN ('create','revoke')`).Scan(&n); err != nil || n != 6 {
		t.Errorf("audit rows = %d (%v), want 4 creates and 2 revokes", n, err)
	}
}

// TestGetCDRRecordingID fails if a CDR detail does not name the call's
// recording.
func TestGetCDRRecordingID(t *testing.T) {
	st, ids := linkFixture(t)
	ctx := context.Background()
	for corr, want := range map[string]*int64{"c1": ptrTo(ids["c1"]), "c3": nil} {
		ms, _, err := st.LookupCalls(ctx, LookupFilter{CallID: corr}, 0, 1)
		if err != nil || len(ms) != 1 {
			t.Fatal(err)
		}
		c, _, err := st.GetCDR(ctx, ms[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		if (c.RecordingID == nil) != (want == nil) || (want != nil && *c.RecordingID != *want) {
			t.Errorf("CDR %s recordingId = %v, want %v", corr, c.RecordingID, want)
		}
	}
}
