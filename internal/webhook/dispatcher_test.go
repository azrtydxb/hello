package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/events"
)

// TestDeliverSignsAndRetriesAServerError fails if a delivery is not signed
// with the published algorithm over the exact body, if a 5xx is not retried
// after 10 seconds (fake clock, no jitter), if the retry is attempted early,
// if the event id is not stable across the attempts while the delivery id is
// unique per attempt, or if the success is not recorded once.
func TestDeliverSignsAndRetriesAServerError(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, sec := r.endpoint(EndpointInput{})
		r.recv.respond(func(n int, _ *http.Request) (int, map[string]string, string) {
			if n == 1 {
				return 503, nil, "busy"
			}
			return 200, nil, ""
		})
		ev := event(events.CallEnded, "200")
		r.env.emit(t, ev)
		if n := r.run(); n != 1 {
			t.Fatalf("first run made %d attempts", n)
		}
		d := r.delivery(ep.ID)
		if d.Status != StatusRetrying || d.Attempts != 1 || d.LastStatus == nil || *d.LastStatus != 503 || d.NextAttemptAt == nil {
			t.Fatalf("after a 503: %+v", d)
		}
		if got := d.NextAttemptAt.Sub(r.clk.Now()); got != 10*time.Second {
			t.Fatalf("next attempt in %v, want 10s", got)
		}
		if n := r.run(); n != 0 {
			t.Fatalf("the retry ran %d attempts before it was due", n)
		}
		r.clk.Advance(10 * time.Second)
		if n := r.run(); n != 1 {
			t.Fatalf("the retry made %d attempts", n)
		}
		d = r.delivery(ep.ID)
		if d.Status != StatusSucceeded || d.Attempts != 2 || d.DeliveredAt == nil {
			t.Fatalf("after the retry: %+v", d)
		}
		reqs := r.recv.requests()
		if len(reqs) != 2 {
			t.Fatalf("receiver saw %d requests, want 2", len(reqs))
		}
		for _, q := range reqs {
			if q.Header.Get(HeaderEventID) != ev.ID || q.Header.Get(HeaderEventType) != events.CallEnded ||
				q.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("headers = %v", q.Header)
			}
			if err := Verify(q.Header.Get(HeaderSignature), q.Body, []string{sec}, r.clk.Now(), 0); err != nil {
				t.Fatalf("the signature does not verify: %v", err)
			}
			var got events.Event
			if err := json.Unmarshal(q.Body, &got); err != nil || got.ID != ev.ID || got.Data.Extension != "200" || got.Data.Sequence != 1 {
				t.Fatalf("body = %s (%v)", q.Body, err)
			}
		}
		if reqs[0].Header.Get(HeaderDelivery) == reqs[1].Header.Get(HeaderDelivery) || reqs[0].Header.Get(HeaderDelivery) == "" {
			t.Fatal("the delivery id must be unique per attempt")
		}
		det, err := r.svc.GetDelivery(context.Background(), d.ID)
		if err != nil || len(det.Log) != 2 || det.Log[0].Ref != reqs[0].Header.Get(HeaderDelivery) || det.Log[1].Status == nil || *det.Log[1].Status != 200 {
			t.Fatalf("attempt log = %+v, %v", det.Log, err)
		}
		if det.Event == nil {
			t.Fatal("the delivery detail lacks the event")
		}
		if got, _ := r.svc.GetEndpoint(context.Background(), ep.ID); got.ConsecutiveFailures != 0 || got.LastSuccessAt == nil {
			t.Fatalf("endpoint after success = %+v", got)
		}
	})
}

// TestPermanentFailureIsDeadAndRedeliverable fails if a 400 is retried, if a
// dead letter cannot be redelivered, or if the redelivery is not a new
// attempt of the same event.
func TestPermanentFailureIsDeadAndRedeliverable(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, _ := r.endpoint(EndpointInput{})
		r.recv.respond(status(400))
		r.env.emit(t, event(events.CallMissed, "200"))
		r.run()
		d := r.delivery(ep.ID)
		if d.Status != StatusDead || d.Attempts != 1 || d.NextAttemptAt != nil {
			t.Fatalf("after a 400: %+v", d)
		}
		r.clk.Advance(24 * time.Hour)
		if n := r.run(); n != 0 || len(r.recv.requests()) != 1 {
			t.Fatalf("a dead letter was sent again by itself (%d attempts)", n)
		}
		if got, _ := r.svc.GetEndpoint(context.Background(), ep.ID); got.Dead != 1 || got.ConsecutiveFailures != 1 || !got.Enabled {
			t.Fatalf("endpoint = %+v", got)
		}
		r.recv.respond(status(200))
		if _, err := r.svc.Redeliver(context.Background(), "user:test", d.ID); err != nil {
			t.Fatal(err)
		}
		if n := r.run(); n != 1 {
			t.Fatalf("redelivery made %d attempts", n)
		}
		d = r.delivery(ep.ID)
		if d.Status != StatusSucceeded || d.Attempts != 2 {
			t.Fatalf("after redelivery: %+v", d)
		}
		reqs := r.recv.requests()
		if reqs[0].Header.Get(HeaderEventID) != reqs[1].Header.Get(HeaderEventID) {
			t.Fatal("a redelivery must carry the same event id")
		}
		if _, err := r.svc.Redeliver(context.Background(), "user:test", 999999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("redeliver of an unknown delivery = %v", err)
		}
		if r.env.audit(t, "redeliver") != 1 {
			t.Fatal("the redelivery was not audited")
		}
	})
}

// TestGoneDisablesTheEndpoint fails if a 410 is retried, does not disable the
// endpoint with reason endpoint_gone, does not notify, or if events still
// queue deliveries for the disabled endpoint.
func TestGoneDisablesTheEndpoint(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, _ := r.endpoint(EndpointInput{})
		r.recv.respond(status(410))
		r.env.emit(t, event(events.CallEnded, "200"))
		r.run()
		d := r.delivery(ep.ID)
		if d.Status != StatusDead {
			t.Fatalf("after a 410: %+v", d)
		}
		got, _ := r.svc.GetEndpoint(context.Background(), ep.ID)
		if got.Enabled || got.DisabledReason != ReasonGone {
			t.Fatalf("endpoint after 410 = %+v", got)
		}
		if n := r.notified(); !slices.Equal(n, []string{strconv.FormatInt(ep.ID, 10) + ":" + ReasonGone}) {
			t.Fatalf("notified %v", n)
		}
		if r.env.audit(t, "disable") != 1 {
			t.Fatal("the disabling was not audited")
		}
		r.env.emit(t, event(events.CallEnded, "200"))
		if ds := r.deliveries(ep.ID, ""); len(ds) != 1 {
			t.Fatalf("a disabled endpoint queued a delivery: %d deliveries", len(ds))
		}
		// Switching it back on clears the reason and the failure count.
		on := true
		got, err := r.svc.Update(context.Background(), "user:test", ep.ID, Patch{Enabled: &on})
		if err != nil || !got.Enabled || got.DisabledReason != "" || got.ConsecutiveFailures != 0 {
			t.Fatalf("re-enabled endpoint = %+v, %v", got, err)
		}
	})
}

// TestRetriesFollowTheSchedule fails if a delivery that always fails is not
// tried eight times, at 10 s, 30 s, 2 min, 10 min, 1 h, 4 h and 12 h after
// each failure, and then dead with its last status and "retries exhausted".
func TestRetriesFollowTheSchedule(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, _ := r.endpoint(EndpointInput{})
		r.recv.respond(status(502))
		r.env.emit(t, event(events.CallEnded, "200"))
		for i, want := range Delays {
			if n := r.run(); n != 1 {
				t.Fatalf("attempt %d: %d attempts", i+1, n)
			}
			d := r.delivery(ep.ID)
			if d.Status != StatusRetrying || d.NextAttemptAt == nil || d.NextAttemptAt.Sub(r.clk.Now()) != want {
				t.Fatalf("after attempt %d: %+v, want a retry in %v", i+1, d, want)
			}
			r.clk.Advance(want - time.Second)
			if n := r.run(); n != 0 {
				t.Fatalf("attempt %d ran a second early", i+2)
			}
			r.clk.Advance(time.Second)
		}
		if n := r.run(); n != 1 {
			t.Fatalf("last attempt: %d", n)
		}
		d := r.delivery(ep.ID)
		if d.Status != StatusDead || d.Attempts != MaxAttempts || d.LastStatus == nil || *d.LastStatus != 502 || !strings.Contains(d.LastError, "retries exhausted") {
			t.Fatalf("after the last attempt: %+v", d)
		}
		if got := len(r.recv.requests()); got != MaxAttempts {
			t.Fatalf("receiver saw %d requests, want %d", got, MaxAttempts)
		}
	})
}

// TestRetryAfterIsHonouredUpToAnHour fails if a 429 with Retry-After is retried
// sooner than the receiver asked, or later than an hour.
func TestRetryAfterIsHonouredUpToAnHour(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, _ := r.endpoint(EndpointInput{})
		for _, tc := range []struct {
			header string
			want   time.Duration
		}{{"120", 2 * time.Minute}, {"5", 10 * time.Second}, {"99999", time.Hour}} {
			r.recv.respond(func(int, *http.Request) (int, map[string]string, string) {
				return 429, map[string]string{"Retry-After": tc.header}, ""
			})
			r.env.emit(t, event(events.CallEnded, "200"))
			r.run()
			ds := r.deliveries(ep.ID, StatusRetrying)
			if len(ds) != 1 || ds[0].NextAttemptAt.Sub(r.clk.Now()) != tc.want {
				t.Fatalf("Retry-After %q: %+v, want a retry in %v", tc.header, ds, tc.want)
			}
			if _, err := r.svc.Redeliver(context.Background(), "u", ds[0].ID); err != nil {
				t.Fatal(err)
			}
			r.recv.respond(status(200))
			r.run()
		}
	})
}

// TestConsecutiveFailuresDisableTheEndpoint fails if an endpoint whose last
// 50 attempts failed is not disabled with reason consecutive_failures (and
// the administrators notified once), or if a success in between did not
// reset the count.
func TestConsecutiveFailuresDisableTheEndpoint(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, _ := r.endpoint(EndpointInput{})
		r.recv.respond(status(400))
		for range 49 {
			r.env.emit(t, event(events.CallEnded, "200"))
		}
		for r.run() > 0 {
		}
		got, _ := r.svc.GetEndpoint(context.Background(), ep.ID)
		if !got.Enabled || got.ConsecutiveFailures != 49 {
			t.Fatalf("after 49 failures: %+v", got)
		}
		// A success resets the count: 49 more failures do not disable it.
		r.recv.respond(status(200))
		r.env.emit(t, event(events.CallEnded, "200"))
		r.run()
		if got, _ = r.svc.GetEndpoint(context.Background(), ep.ID); got.ConsecutiveFailures != 0 {
			t.Fatalf("a success did not reset the failures: %+v", got)
		}
		r.recv.respond(status(400))
		for range MaxConsecutiveFailures + 5 {
			r.env.emit(t, event(events.CallEnded, "200"))
		}
		for r.run() > 0 {
		}
		got, _ = r.svc.GetEndpoint(context.Background(), ep.ID)
		// Attempts already in flight when the 50th failed still finish (up
		// to four per endpoint), so the count may run a little past 50.
		extra := got.ConsecutiveFailures - MaxConsecutiveFailures
		if got.Enabled || got.DisabledReason != ReasonFailures || extra < 0 || extra >= DefaultPerEndpoint {
			t.Fatalf("after %d failures: %+v", MaxConsecutiveFailures, got)
		}
		if n := r.notified(); len(n) != 1 {
			t.Fatalf("notified %v, want once", n)
		}
		if got.Pending != int64(5-extra) {
			t.Fatalf("%d deliveries wait on the disabled endpoint, want the %d not yet sent", got.Pending, 5-extra)
		}
	})
}

// TestClaimLimitsConcurrencyPerEndpoint fails if more than four deliveries of
// one endpoint are leased at once (across callers, as across replicas), if
// another endpoint is starved by it, if a lease that ran out is not taken
// again, or if a delivery being sent can be redelivered.
func TestClaimLimitsConcurrencyPerEndpoint(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		a, _ := r.endpoint(EndpointInput{Name: "a", URL: r.recv.srv.URL + "/a"})
		b, _ := r.endpoint(EndpointInput{Name: "b", URL: r.recv.srv.URL + "/b"})
		for range 10 {
			r.env.emit(t, event(events.CallEnded, "200"))
		}
		ctx := context.Background()
		first, err := r.svc.Store.Claim(ctx, r.clk.Now(), 32, 4, 30*time.Second)
		if err != nil {
			t.Fatal(err)
		}
		count := map[int64]int{}
		for _, c := range first {
			count[c.EndpointID]++
		}
		if len(first) != 8 || count[a.ID] != 4 || count[b.ID] != 4 {
			t.Fatalf("first claim = %v (%d rows), want 4 per endpoint", count, len(first))
		}
		second, _ := r.svc.Store.Claim(ctx, r.clk.Now(), 32, 4, 30*time.Second)
		if len(second) != 0 {
			t.Fatalf("a second replica claimed %d rows while every slot was leased", len(second))
		}
		if _, err := r.svc.Redeliver(ctx, "u", first[0].ID); !errors.Is(err, ErrInFlight) {
			t.Fatalf("redeliver of a leased delivery = %v, want ErrInFlight", err)
		}
		// A replica that dies mid-delivery: its lease runs out and the rows
		// are claimed again, none lost.
		r.clk.Advance(31 * time.Second)
		third, _ := r.svc.Store.Claim(ctx, r.clk.Now(), 32, 4, 30*time.Second)
		if len(third) != 8 {
			t.Fatalf("after the lease ran out %d rows were claimed, want 8", len(third))
		}
		// Rows claimed once are never claimed by two callers at the same time.
		seen := map[int64]bool{}
		for _, c := range third {
			if seen[c.ID] {
				t.Fatalf("delivery %d claimed twice in one claim", c.ID)
			}
			seen[c.ID] = true
		}
		// A claim limited by the replica's capacity takes only that many.
		r.clk.Advance(31 * time.Second)
		small, _ := r.svc.Store.Claim(ctx, r.clk.Now(), 3, 4, 30*time.Second)
		if len(small) != 3 {
			t.Fatalf("a claim with limit 3 took %d rows", len(small))
		}
	})
}

// TestEventFilters fails if an endpoint receives an event outside its type
// or extension filter, misses one inside it, receives an event twice, or
// gets one while disabled.
func TestEventFilters(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		missed, _ := r.endpoint(EndpointInput{Name: "missed", URL: r.recv.srv.URL + "/1", Events: []string{events.CallMissed}})
		calls200, _ := r.endpoint(EndpointInput{Name: "calls200", URL: r.recv.srv.URL + "/2", Events: []string{"call.*"}, Extensions: []string{"200"}})
		all, _ := r.endpoint(EndpointInput{Name: "all", URL: r.recv.srv.URL + "/3"})
		off, _ := r.endpoint(EndpointInput{Name: "off", URL: r.recv.srv.URL + "/4"})
		no := false
		if _, err := r.svc.Update(context.Background(), "u", off.ID, Patch{Enabled: &no}); err != nil {
			t.Fatal(err)
		}
		r.env.emit(t, event(events.CallMissed, "200"))
		dup := event(events.CallRinging, "300")
		r.env.emit(t, dup)
		r.env.emit(t, dup) // at-least-once ingest: the same event again
		r.env.emit(t, event(events.CallEnded, "200"))
		for _, tc := range []struct {
			ep   Endpoint
			want int
		}{{missed, 1}, {calls200, 2}, {all, 4 - 0}, {off, 0}} {
			want := tc.want
			if tc.ep.ID == all.ID {
				want = 3
			}
			if got := len(r.deliveries(tc.ep.ID, "")); got != want {
				t.Errorf("%s has %d deliveries, want %d", tc.ep.Name, got, want)
			}
		}
	})
}

// TestRotationSignsWithBothSecretsDuringTheWindow fails if, after a
// rotation, the old secret does not verify for 24 hours while the new one
// does, or if the old one still signs afterwards.
func TestRotationSignsWithBothSecretsDuringTheWindow(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, old := r.endpoint(EndpointInput{})
		ctx := context.Background()
		fresh, rotated, err := r.svc.Rotate(ctx, "user:test", ep.ID)
		if err != nil || fresh == "" || fresh == old || rotated.PreviousSecretExpires == nil {
			t.Fatalf("Rotate = %q, %+v, %v", fresh, rotated, err)
		}
		r.env.emit(t, event(events.CallEnded, "200"))
		r.run()
		q := r.recv.requests()[0]
		if strings.Count(q.Header.Get(HeaderSignature), "v1=") != 2 {
			t.Fatalf("a rotation window must sign twice: %s", q.Header.Get(HeaderSignature))
		}
		for name, s := range map[string]string{"old": old, "new": fresh} {
			if err := Verify(q.Header.Get(HeaderSignature), q.Body, []string{s}, r.clk.Now(), 0); err != nil {
				t.Errorf("the %s secret does not verify in the window: %v", name, err)
			}
		}
		r.clk.Advance(SecretWindow + time.Minute)
		r.env.emit(t, event(events.CallEnded, "200"))
		r.run()
		q = r.recv.requests()[1]
		if strings.Count(q.Header.Get(HeaderSignature), "v1=") != 1 {
			t.Fatalf("after the window one signature: %s", q.Header.Get(HeaderSignature))
		}
		if err := Verify(q.Header.Get(HeaderSignature), q.Body, []string{fresh}, r.clk.Now(), 0); err != nil {
			t.Errorf("the new secret does not verify: %v", err)
		}
		if err := Verify(q.Header.Get(HeaderSignature), q.Body, []string{old}, r.clk.Now(), 0); err == nil {
			t.Error("the old secret still verifies after the window")
		}
		if r.env.audit(t, "rotate-secret") != 1 {
			t.Fatal("the rotation was not audited")
		}
	})
}

// TestExpiredEventIsDeadWithoutPenalty fails if a delivery whose event was
// purged is retried or counted against the receiver.
func TestExpiredEventIsDeadWithoutPenalty(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, _ := r.endpoint(EndpointInput{})
		ev := event(events.CallEnded, "200")
		r.env.emit(t, ev)
		r.env.drop(t, ev.ID)
		r.run()
		d := r.delivery(ep.ID)
		if d.Status != StatusDead || d.LastError != "event_expired" || len(r.recv.requests()) != 0 {
			t.Fatalf("delivery = %+v, receiver saw %d requests", d, len(r.recv.requests()))
		}
		if got, _ := r.svc.GetEndpoint(context.Background(), ep.ID); got.ConsecutiveFailures != 0 {
			t.Fatalf("an expired event counted against the endpoint: %+v", got)
		}
	})
}

// TestPurgeAndCounts fails if the retention job deletes unfinished
// deliveries, keeps finished ones past their retention or dead letters past
// the cap, or if the gauges' counts are wrong.
func TestPurgeAndCounts(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, _ := r.endpoint(EndpointInput{})
		ctx := context.Background()
		r.recv.respond(status(200))
		r.env.emit(t, event(events.CallEnded, "200")) // succeeds
		r.run()
		r.recv.respond(status(400))
		for range 3 {
			r.env.emit(t, event(events.CallEnded, "200")) // three dead letters
			r.clk.Advance(time.Second)
			r.run()
		}
		r.recv.respond(status(500))
		r.env.emit(t, event(events.CallEnded, "200")) // retrying
		r.run()
		c, err := r.svc.Store.Counts(ctx)
		if err != nil || c.Pending != 1 || c.Dead != 3 || c.DisabledEndpoints != 0 {
			t.Fatalf("Counts = %+v, %v", c, err)
		}
		now := r.clk.Now()
		res, err := r.svc.Store.Purge(ctx, now.Add(-time.Hour), now.Add(-time.Hour), 2)
		if err != nil || res.Succeeded != 0 || res.Dead != 0 || res.Dropped != 1 {
			t.Fatalf("Purge with a cap of 2 = %+v, %v", res, err)
		}
		r.clk.Advance(48 * time.Hour)
		now = r.clk.Now()
		res, err = r.svc.Store.Purge(ctx, now.Add(-24*time.Hour), now.Add(-24*time.Hour), 100)
		if err != nil || res.Succeeded != 1 || res.Dead != 2 {
			t.Fatalf("Purge past the retention = %+v, %v", res, err)
		}
		if ds := r.deliveries(ep.ID, ""); len(ds) != 1 || ds[0].Status != StatusRetrying {
			t.Fatalf("the unfinished delivery did not survive: %+v", ds)
		}
	})
}

// TestEndpointLifecycle fails if creation is not idempotent on URL and events,
// if the secret is returned more than once, if the sealed secret or the
// secret itself reaches the JSON of an endpoint, if deleting an endpoint
// leaves deliveries, or if changes are not audited.
func TestEndpointLifecycle(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ctx := context.Background()
		ep, sec := r.endpoint(EndpointInput{Name: "crm", Events: []string{"call.missed", "call.ended", "call.ended"}})
		if !slices.Equal(ep.Events, []string{"call.ended", "call.missed"}) {
			t.Fatalf("events = %v, want sorted and deduplicated", ep.Events)
		}
		again, sec2, created, err := r.svc.Create(ctx, "user:test", EndpointInput{Name: "crm again", URL: ep.URL, Events: []string{"call.ended", "call.missed"}, Enabled: true})
		if err != nil || created || sec2 != "" || again.ID != ep.ID {
			t.Fatalf("a repeated subscription = %+v, %q, %v, %v", again, sec2, created, err)
		}
		b, _ := json.Marshal(ep)
		if strings.Contains(string(b), sec) || strings.Contains(strings.ToLower(string(b)), "secret") && !strings.Contains(string(b), "previousSecretExpiresAt") {
			t.Fatalf("endpoint JSON carries a secret: %s", b)
		}
		if got, _ := r.svc.GetEndpoint(ctx, ep.ID); len(got.SecretEnc) == 0 || strings.Contains(string(got.SecretEnc), sec) {
			t.Fatal("the secret is not sealed")
		}
		name := "renamed"
		up, err := r.svc.Update(ctx, "user:test", ep.ID, Patch{Name: &name})
		if err != nil || up.Name != name {
			t.Fatalf("Update = %+v, %v", up, err)
		}
		r.env.emit(t, event(events.CallEnded, "200"))
		if err := r.svc.DeleteEndpoint(ctx, "user:test", ep.ID); err != nil {
			t.Fatal(err)
		}
		if ds, _ := r.svc.ListDeliveries(ctx, DeliveryQuery{}); len(ds) != 0 {
			t.Fatalf("deleting the endpoint left %d deliveries", len(ds))
		}
		if _, err := r.svc.GetEndpoint(ctx, ep.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get after delete = %v", err)
		}
		if err := r.svc.DeleteEndpoint(ctx, "user:test", ep.ID); !errors.Is(err, ErrNotFound) {
			t.Fatalf("second delete = %v", err)
		}
		for _, action := range []string{"create", "update", "delete"} {
			if got := r.env.audit(t, action); got != 1 {
				t.Errorf("audit rows for %s = %d, want 1", action, got)
			}
		}
	})
}

// TestTestPing fails if the test ping is not signed and typed webhook.test,
// lacks the Date header, reports the receiver's status, latency and at most
// the first kilobyte of its answer, or creates a delivery or counts against
// the endpoint.
func TestTestPing(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, sec := r.endpoint(EndpointInput{})
		ctx := context.Background()
		r.recv.respond(func(int, *http.Request) (int, map[string]string, string) { return 200, nil, "pong" })
		res, err := r.svc.Test(ctx, ep.ID)
		if err != nil || !res.Delivered || res.Status != 200 || res.Body != "pong" || res.EventID == "" {
			t.Fatalf("Test = %+v, %v", res, err)
		}
		q := r.recv.requests()[0]
		if q.Header.Get(HeaderEventType) != TestEventType || q.Header.Get("Date") == "" ||
			Verify(q.Header.Get(HeaderSignature), q.Body, []string{sec}, r.clk.Now(), 0) != nil {
			t.Fatalf("ping headers = %v", q.Header)
		}
		r.recv.respond(func(int, *http.Request) (int, map[string]string, string) {
			return 500, nil, strings.Repeat("x", 5000)
		})
		res, _ = r.svc.Test(ctx, ep.ID)
		if res.Delivered || res.Status != 500 || len(res.Body) != TestBodyLimit {
			t.Fatalf("Test against a 500 = %+v (body %d bytes)", res, len(res.Body))
		}
		r.recv.srv.Close()
		res, _ = r.svc.Test(ctx, ep.ID)
		if res.Delivered || res.Status != 0 || res.Error == "" || strings.Contains(res.Error, "127.0.0.1") {
			t.Fatalf("Test against a closed receiver = %+v", res)
		}
		if ds := r.deliveries(ep.ID, ""); len(ds) != 0 {
			t.Fatalf("a test ping created %d deliveries", len(ds))
		}
		if got, _ := r.svc.GetEndpoint(ctx, ep.ID); got.ConsecutiveFailures != 0 {
			t.Fatalf("a test ping counted against the endpoint: %+v", got)
		}
	})
}

// TestRunLoopDelivers fails if the background dispatcher does not deliver a
// queued event by itself.
func TestRunLoopDelivers(t *testing.T) {
	forEachStore(t, func(t *testing.T, r *rig) {
		ep, _ := r.endpoint(EndpointInput{})
		r.disp.Poll = 10 * time.Millisecond
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { r.disp.Run(ctx); close(done) }()
		defer func() { cancel(); <-done }()
		r.env.emit(t, event(events.CallEnded, "200"))
		deadline := time.Now().Add(10 * time.Second)
		for {
			if ds := r.deliveries(ep.ID, StatusSucceeded); len(ds) == 1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the event was not delivered")
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
}
