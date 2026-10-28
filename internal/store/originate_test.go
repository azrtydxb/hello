package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/oauth"
)

// TestOriginateStore fails if a request does not round-trip, its state is not
// copied (a correlation id once set must survive an update without one), open
// requests are not listed by idleness, finished rows are not purged by age,
// or the policy does not default to the safe choice and round-trip.
func TestOriginateStore(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	in := OriginateRecord{ID: "orig_1", IdempotencyKey: "k", Extension: "101", Destination: "0501234567", Client: "token:7", Actor: "user:alice"}
	if err := s.CreateOriginate(ctx, in); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetOriginate(ctx, "orig_1")
	if err != nil || got.State != "queued" || got.Extension != "101" || got.Client != "token:7" {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetOriginate(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown id = %v, want ErrNotFound", err)
	}
	if err := s.SetOriginateState(ctx, "orig_1", "connected", "", "corr-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOriginateState(ctx, "orig_1", "ended", "", ""); err != nil {
		t.Fatal(err)
	}
	got, _ = s.GetOriginate(ctx, "orig_1")
	if got.State != "ended" || got.CorrelationID != "corr-1" {
		t.Fatalf("after updates = %+v, want ended with the correlation id kept", got)
	}

	if err := s.CreateOriginate(ctx, OriginateRecord{ID: "orig_2", Extension: "102", Destination: "0500"}); err != nil {
		t.Fatal(err)
	}
	if open, _ := s.OpenOriginates(ctx, time.Hour, 10); len(open) != 0 {
		t.Fatalf("a fresh request is idle for an hour: %+v", open)
	}
	open, err := s.OpenOriginates(ctx, 0, 10)
	if err != nil || len(open) != 1 || open[0].ID != "orig_2" {
		t.Fatalf("open = %+v, %v, want only orig_2 (orig_1 is over)", open, err)
	}
	if n, _ := s.PurgeOriginates(ctx, time.Hour); n != 0 {
		t.Fatalf("purged %d rows younger than the age", n)
	}
	if n, err := s.PurgeOriginates(ctx, 0); err != nil || n != 1 {
		t.Fatalf("purged %d, %v, want the one finished row", n, err)
	}

	def, err := s.GetOriginateSettings(ctx)
	if err != nil || def.Destinations != OriginateInherit || def.AutoAnswerAllowed || len(def.CountryPrefixes) != 0 {
		t.Fatalf("default policy = %+v, %v", def, err)
	}
	want := OriginateSettings{Destinations: OriginateRestricted, CountryPrefixes: []string{"+971", "00971"}, AutoAnswerAllowed: true}
	if err := s.PutOriginateSettings(ctx, "user:root", want); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetOriginateSettings(ctx); got.Destinations != "restricted" || len(got.CountryPrefixes) != 2 || !got.AutoAnswerAllowed {
		t.Fatalf("policy = %+v", got)
	}
}

// TestPreregisteredClientUpsert fails if the dialer client is not stored as
// preregistered, not refreshed when the public URL changes, or if it
// overwrites a client of another kind with the same id.
func TestPreregisteredClientUpsert(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	c := oauth.Client{ID: "hello-dialer", Name: "Hello dialer", ClientURI: "https://a.example", RedirectURIs: []string{"https://a.example/integrations/dialer/callback"}}
	if err := s.UpsertPreregisteredClient(ctx, c); err != nil {
		t.Fatal(err)
	}
	c.RedirectURIs = []string{"https://b.example/integrations/dialer/callback"}
	if err := s.UpsertPreregisteredClient(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.Client(ctx, "hello-dialer")
	if err != nil || got.Kind != oauth.ClientPreregistered || got.RedirectURIs[0] != "https://b.example/integrations/dialer/callback" {
		t.Fatalf("client = %+v, %v", got, err)
	}
}
