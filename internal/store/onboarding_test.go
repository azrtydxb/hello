package store

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/prov"
)

func candidate(mac string) prov.Candidate {
	return prov.Candidate{MAC: mac, Vendor: prov.Yealink, Model: "T54W", Firmware: "96.86.0.70",
		IP: netip.MustParseAddr("192.0.2.10"), Path: "/p/boot/" + mac + ".cfg"}
}

// TestCaptureCandidate fails if a first sighting does not insert, a repeat
// does not update count, last_seen and the newly known fields in place, a
// MAC that is a phone or ignored is written, or a MAC no device can have
// reaches the table.
func TestCaptureCandidate(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	out, err := st.CaptureCandidate(ctx, candidate("805ec0000001"))
	if err != nil || out != prov.CaptureStored {
		t.Fatalf("first capture = %v, %v", out, err)
	}
	// Second sighting: a different IP and path, and no model this time.
	again := candidate("805ec0000001")
	again.Model, again.Firmware, again.Vendor = "", "", prov.Generic
	again.IP, again.Path = netip.MustParseAddr("192.0.2.11"), "/p/boot/other.cfg"
	if out, err = st.CaptureCandidate(ctx, again); err != nil || out != prov.CaptureStored {
		t.Fatalf("second capture = %v, %v", out, err)
	}
	cs, err := st.ListCandidates(ctx)
	if err != nil || len(cs) != 1 {
		t.Fatalf("list = %+v, %v", cs, err)
	}
	c := cs[0]
	if c.MAC != "805ec0000001" || c.Vendor != "yealink" || c.Model != "T54W" || c.Firmware != "96.86.0.70" ||
		c.SourceIP != "192.0.2.11" || c.RequestedPath != "/p/boot/other.cfg" || c.RequestCount != 2 ||
		c.LastSeen.Before(c.FirstSeen) {
		t.Fatalf("candidate = %+v", c)
	}

	// A MAC that is already a phone is never captured.
	if _, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000002", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"}); err != nil {
		t.Fatal(err)
	}
	if out, err = st.CaptureCandidate(ctx, candidate("805ec0000002")); err != nil || out != prov.CaptureKnown {
		t.Fatalf("known phone = %v, %v", out, err)
	}

	// An ignored MAC is never captured.
	if _, err := db.ExecContext(ctx, `INSERT INTO onboarding_ignored (mac) VALUES ('805ec0000003')`); err != nil {
		t.Fatal(err)
	}
	if out, err = st.CaptureCandidate(ctx, candidate("805ec0000003")); err != nil || out != prov.CaptureIgnored {
		t.Fatalf("ignored = %v, %v", out, err)
	}
	for _, bad := range []string{"000000000000", "ffffffffffff", "01005e000001", "805EC0000004", "zz"} {
		if _, err := st.CaptureCandidate(ctx, candidate(bad)); err == nil {
			t.Errorf("%q was captured", bad)
		}
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM onboarding_candidates`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d rows, want 1 (%v)", n, err)
	}
	// The table itself refuses a malformed MAC.
	if _, err := db.ExecContext(ctx, `INSERT INTO onboarding_candidates (mac) VALUES ('NOT-A-MAC')`); err == nil {
		t.Fatal("the table accepted a malformed MAC")
	}
}

// TestCaptureCandidateCap fails if the queue grows past prov.MaxCandidates,
// including under concurrent captures of new MACs from several pods, or if
// a full queue stops refreshing the candidates it holds.
func TestCaptureCandidateCap(t *testing.T) {
	st, db, _ := provStore(t)
	ctx := context.Background()
	const free = 5
	if _, err := db.ExecContext(ctx, `
		INSERT INTO onboarding_candidates (mac)
		SELECT '02' || lpad(to_hex(g), 10, '0') FROM generate_series(1, $1::int) g`, prov.MaxCandidates-free); err != nil {
		t.Fatal(err)
	}
	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		outs = map[prov.CaptureOutcome]int{}
	)
	for i := range 20 {
		wg.Go(func() {
			out, err := st.CaptureCandidate(ctx, candidate(fmt.Sprintf("805ec10000%02x", i)))
			if err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			outs[out]++
			mu.Unlock()
		})
	}
	wg.Wait()
	if outs[prov.CaptureStored] != free || outs[prov.CaptureFull] != 20-free {
		t.Fatalf("outcomes %v, want %d stored and %d full", outs, free, 20-free)
	}
	if n, err := st.CountCandidates(ctx); err != nil || n != prov.MaxCandidates {
		t.Fatalf("queue holds %d (%v), want %d", n, err, prov.MaxCandidates)
	}
	// A full queue still refreshes a candidate it holds.
	if out, err := st.CaptureCandidate(ctx, candidate("020000000001")); err != nil || out != prov.CaptureStored {
		t.Fatalf("refresh when full = %v, %v", out, err)
	}
	var cnt int64
	if err := db.QueryRowContext(ctx, `SELECT request_count FROM onboarding_candidates WHERE mac = '020000000001'`).Scan(&cnt); err != nil || cnt != 2 {
		t.Fatalf("request_count %d (%v)", cnt, err)
	}
}

// TestCandidateExpiry fails if a candidate unseen for 30 days is listed,
// counted, kept by the pruner, or if its return does not start it afresh.
func TestCandidateExpiry(t *testing.T) {
	st, db, _ := provStore(t)
	ctx := context.Background()
	for _, mac := range []string{"805ec0000001", "805ec0000002"} {
		if _, err := st.CaptureCandidate(ctx, candidate(mac)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `
		UPDATE onboarding_candidates SET last_seen = now() - interval '31 days', first_seen = now() - interval '60 days',
		       request_count = 9 WHERE mac = '805ec0000001'`); err != nil {
		t.Fatal(err)
	}
	if cs, err := st.ListCandidates(ctx); err != nil || len(cs) != 1 || cs[0].MAC != "805ec0000002" {
		t.Fatalf("list = %+v, %v", cs, err)
	}
	if n, err := st.CountCandidates(ctx); err != nil || n != 1 {
		t.Fatalf("count = %d, %v", n, err)
	}
	if _, err := st.GetCandidate(ctx, "805ec0000001"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get expired = %v", err)
	}
	// Seen again before the pruner ran: starts afresh.
	if _, err := st.CaptureCandidate(ctx, candidate("805ec0000001")); err != nil {
		t.Fatal(err)
	}
	c, err := st.GetCandidate(ctx, "805ec0000001")
	if err != nil || c.RequestCount != 1 {
		t.Fatalf("returned candidate = %+v, %v", c, err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE onboarding_candidates SET last_seen = now() - interval '31 days' WHERE mac = '805ec0000002'`); err != nil {
		t.Fatal(err)
	}
	if n, err := st.PruneCandidates(ctx); err != nil || n != 1 {
		t.Fatalf("pruned %d, %v", n, err)
	}
}

func auditCount(t *testing.T, st *Store, action, resource, id string) int {
	t.Helper()
	var n int
	if err := st.db.QueryRow(`SELECT count(*) FROM audit_events WHERE action = $1 AND resource = $2 AND resource_id = $3`,
		action, resource, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// TestAdoptCandidate fails if adopting does not create the phone exactly as
// CreatePhone does (device on the extension, sealed secrets, armed boot
// hand-off), remove the candidate and audit both, or if an adopted MAC is
// captured again.
func TestAdoptCandidate(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	if _, err := st.CaptureCandidate(ctx, candidate("805ec0000001")); err != nil {
		t.Fatal(err)
	}
	out, err := st.AdoptCandidates(ctx, "user:op", []PhoneInput{{MAC: "805ec0000001", Vendor: prov.Yealink, Model: "T54W",
		Label: "Front desk", ExtensionID: ext, Enabled: true, Realm: "hello.test"}})
	if err != nil || len(out) != 1 {
		t.Fatalf("adopt = %+v, %v", out, err)
	}
	p := out[0].Phone
	if p.MAC != "805ec0000001" || p.Label != "Front desk" || p.ExtensionNumber != "101" || p.DeviceID == nil || !p.BootArmed || !p.Enabled || out[0].Token == "" {
		t.Fatalf("phone = %+v", p)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM onboarding_candidates`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("candidate left behind: %d (%v)", n, err)
	}
	if auditCount(t, st, "adopt", "onboarding_candidate", "805ec0000001") != 1 ||
		auditCount(t, st, "create", "phone", fmt.Sprint(p.ID)) != 1 {
		t.Fatal("adopt or create not audited")
	}
	if got, err := st.CaptureCandidate(ctx, candidate("805ec0000001")); err != nil || got != prov.CaptureKnown {
		t.Fatalf("adopted MAC captured again = %v, %v", got, err)
	}
	// Adopting what is not queued is refused.
	_, err = st.AdoptCandidates(ctx, "user:op", []PhoneInput{{MAC: "805ec0000009", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"}})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("adopt of an unqueued MAC = %v", err)
	}
}

// TestAdoptCandidatesAllOrNone fails if a bulk adoption with a failing row
// creates any phone or removes any candidate, and if a good batch does not
// adopt every row with one audit row per candidate.
func TestAdoptCandidatesAllOrNone(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	for _, mac := range []string{"805ec0000001", "805ec0000002"} {
		if _, err := st.CaptureCandidate(ctx, candidate(mac)); err != nil {
			t.Fatal(err)
		}
	}
	mk := func(mac string, ext int64) PhoneInput {
		return PhoneInput{MAC: mac, Vendor: prov.Yealink, Model: "T54W", ExtensionID: ext, Enabled: true, Realm: "hello.test"}
	}
	if _, err := st.AdoptCandidates(ctx, "user:op", []PhoneInput{mk("805ec0000001", ext), mk("805ec0000002", ext+99)}); err == nil {
		t.Fatal("a row naming no extension was adopted")
	}
	var phones, cands int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM phones), (SELECT count(*) FROM onboarding_candidates)`).Scan(&phones, &cands); err != nil ||
		phones != 0 || cands != 2 {
		t.Fatalf("after a failed batch: %d phones, %d candidates (%v)", phones, cands, err)
	}
	out, err := st.AdoptCandidates(ctx, "user:op", []PhoneInput{mk("805ec0000001", ext), mk("805ec0000002", ext)})
	if err != nil || len(out) != 2 {
		t.Fatalf("adopt batch = %d, %v", len(out), err)
	}
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM phones), (SELECT count(*) FROM onboarding_candidates)`).Scan(&phones, &cands); err != nil ||
		phones != 2 || cands != 0 {
		t.Fatalf("after the batch: %d phones, %d candidates (%v)", phones, cands, err)
	}
	if auditCount(t, st, "adopt-batch", "onboarding_candidate", "2") != 1 ||
		auditCount(t, st, "adopt", "onboarding_candidate", "805ec0000001") != 1 ||
		auditCount(t, st, "adopt", "onboarding_candidate", "805ec0000002") != 1 {
		t.Fatal("batch not audited")
	}
}

// TestManualCreateLeavesQueue fails if creating a phone by hand leaves its
// MAC in the queue.
func TestManualCreateLeavesQueue(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	if _, err := st.CaptureCandidate(ctx, candidate("805ec0000001")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000001", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"}); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM onboarding_candidates`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d candidates left (%v)", n, err)
	}
}

// TestDismissAndIgnore fails if dismissing does not remove the candidate
// and audit, if ignoring does not keep the MAC out until it is removed from
// the list, or if either of them accepts a MAC that is not there.
func TestDismissAndIgnore(t *testing.T) {
	st, _, _ := provStore(t)
	ctx := context.Background()
	for _, mac := range []string{"805ec0000001", "805ec0000002"} {
		if _, err := st.CaptureCandidate(ctx, candidate(mac)); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.DismissCandidate(ctx, "user:op", "805ec0000001", false); err != nil {
		t.Fatal(err)
	}
	if err := st.DismissCandidate(ctx, "user:op", "805ec0000001", false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second dismiss = %v", err)
	}
	// A plain dismissal comes back on the next request.
	if out, err := st.CaptureCandidate(ctx, candidate("805ec0000001")); err != nil || out != prov.CaptureStored {
		t.Fatalf("after dismiss = %v, %v", out, err)
	}
	if err := st.DismissCandidate(ctx, "user:op", "805ec0000002", true); err != nil {
		t.Fatal(err)
	}
	if out, err := st.CaptureCandidate(ctx, candidate("805ec0000002")); err != nil || out != prov.CaptureIgnored {
		t.Fatalf("after ignore = %v, %v", out, err)
	}
	ig, err := st.ListIgnored(ctx)
	if err != nil || len(ig) != 1 || ig[0].MAC != "805ec0000002" || ig[0].CreatedBy != "user:op" || ig[0].Model != "T54W" {
		t.Fatalf("ignored = %+v, %v", ig, err)
	}
	if auditCount(t, st, "ignore", "onboarding_candidate", "805ec0000002") != 1 ||
		auditCount(t, st, "dismiss", "onboarding_candidate", "805ec0000001") != 1 {
		t.Fatal("dismiss or ignore not audited")
	}
	if err := st.Unignore(ctx, "user:op", "805ec0000002"); err != nil {
		t.Fatal(err)
	}
	if err := st.Unignore(ctx, "user:op", "805ec0000002"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second unignore = %v", err)
	}
	if out, err := st.CaptureCandidate(ctx, candidate("805ec0000002")); err != nil || out != prov.CaptureStored {
		t.Fatalf("after unignore = %v, %v", out, err)
	}
}
