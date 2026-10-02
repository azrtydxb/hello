package livestate

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
)

func store(t *testing.T) *Store {
	t.Helper()
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.Do(context.Background(), c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	return New(c)
}

func TestBindingsRefreshAndExpiry(t *testing.T) {
	s, ctx := store(t), context.Background()
	aor := "sip:101-desk@hello.test"
	b := Binding{AOR: aor, ContactURI: "sip:101-desk@10.0.0.9:5060", ReceivedNode: "sip-1", Expires: time.Now().Add(time.Hour)}
	short := Binding{AOR: aor, ContactURI: "sip:101-desk@10.0.0.10:5060", ReceivedNode: "sip-1", Expires: time.Now().Add(1100 * time.Millisecond)}
	for _, x := range []Binding{b, short} {
		if err := s.PutBinding(ctx, x); err != nil {
			t.Fatal(err)
		}
	}
	b.ReceivedNode = "sip-2" // refresh through the other node
	if err := s.PutBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	got, err := s.Bindings(ctx, aor)
	if err != nil || len(got) != 2 {
		t.Fatalf("bindings = %v, %v; want 2 (refresh must not duplicate)", got, err)
	}
	time.Sleep(1500 * time.Millisecond)
	got, err = s.AllBindings(ctx)
	if err != nil || len(got) != 1 || got[0].ReceivedNode != "sip-2" {
		t.Fatalf("after expiry = %+v, %v; want only the refreshed binding", got, err)
	}
	b.Expires = time.Now() // Expires: 0
	if err := s.PutBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.Bindings(ctx, aor); len(got) != 0 {
		t.Fatalf("unregister left %v", got)
	}
}

func TestCallsTTL(t *testing.T) {
	s, ctx := store(t), context.Background()
	if err := s.PutCall(ctx, Call{ID: "c1", Node: "sip-1", State: "ringing"}, 500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Calls(ctx); err != nil || len(got) != 1 {
		t.Fatalf("calls = %v, %v", got, err)
	}
	time.Sleep(800 * time.Millisecond)
	if got, _ := s.Calls(ctx); len(got) != 0 {
		t.Fatalf("call outlived its TTL: %v", got)
	}
}

// TestBindingAlwaysHasTTL fails if PutBinding can leave a field without its
// expiry.
func TestBindingAlwaysHasTTL(t *testing.T) {
	s, ctx := store(t), context.Background()
	b := Binding{AOR: "sip:x@hello.test", ContactURI: "sip:x@10.0.0.1:5060", Expires: time.Now().Add(time.Minute)}
	if err := s.PutBinding(ctx, b); err != nil {
		t.Fatal(err)
	}
	ttls, err := s.c.Do(ctx, s.c.B().Hpttl().Key(regPrefix+b.AOR).Fields().Numfields(1).Field(b.ContactURI).Build()).AsIntSlice()
	if err != nil || len(ttls) != 1 || ttls[0] <= 0 || ttls[0] > 60000 {
		t.Fatalf("HPTTL = %v, %v; want within a minute", ttls, err)
	}
}
