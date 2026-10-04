package livestate

import (
	"context"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/valkey-io/valkey-go"
)

func presenceStore(t *testing.T) valkey.Client {
	t.Helper()
	addr := os.Getenv("HELLO_TEST_VALKEY_ADDR")
	if addr == "" {
		t.Skip("HELLO_TEST_VALKEY_ADDR not set")
	}
	c, err := valkey.NewClient(valkey.ClientOption{InitAddress: []string{addr}, ForceSingleClient: true, SelectDB: 9})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(c.Close)
	if err := c.Do(context.Background(), c.B().Flushdb().Build()).Error(); err != nil {
		t.Fatal(err)
	}
	return c
}

// TestPresenceKeyspace fails if a published state is not listed, if an
// expired record disappears from the listing, or if a device's state does
// not overwrite its previous one.
func TestPresenceKeyspace(t *testing.T) {
	c := presenceStore(t)
	s, ctx := New(c), context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	for _, st := range []DeviceState{
		{Device: "phone1", Extension: "101", State: "ringing", UpdatedAt: now},
		{Device: "phone2", Extension: "102", State: "on-call", UpdatedAt: now},
	} {
		if err := s.SetDeviceState(ctx, st, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.DeviceStates(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("states = %+v, %v", got, err)
	}
	slices.SortFunc(got, func(a, b DeviceState) int { return strings.Compare(a.Device, b.Device) })
	if got[0].State != "ringing" || got[1].State != "on-call" {
		t.Fatalf("states = %+v", got)
	}
	// Overwrite phone1: idle replaces ringing, not a second record.
	if err := s.SetDeviceState(ctx, DeviceState{Device: "phone1", Extension: "101", State: "idle", UpdatedAt: now}, time.Minute); err != nil {
		t.Fatal(err)
	}
	got, _ = s.DeviceStates(ctx)
	if len(got) != 2 {
		t.Fatalf("overwrite duplicated records: %+v", got)
	}
	phone1 := got[0]
	if phone1.Device != "phone1" || phone1.State != "idle" {
		t.Fatalf("phone1 = %+v", phone1)
	}
	// An expired record leaves the listing.
	if err := s.SetDeviceState(ctx, DeviceState{Device: "gone", Extension: "103", State: "idle", UpdatedAt: now}, 1500*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	time.Sleep(1700 * time.Millisecond)
	if got, _ = s.DeviceStates(ctx); len(got) != 2 {
		t.Fatalf("expired presence still listed: %+v", got)
	}
}
