package sip

import (
	"context"
	"sort"
	"testing"
	"time"
)

// TestValkeyPresence fails if node records are not listed with their
// advertised addresses, or are kept without a TTL.
func TestValkeyPresence(t *testing.T) {
	_, _, c := valkeyState(t)
	ctx := context.Background()
	p := ValkeyPresence{Client: c}
	for id, addr := range map[string]string{"sip-1": "hello-sip-1:5060", "sip-2": "10.0.0.2:5060"} {
		if err := p.Announce(ctx, id, addr, 30*time.Second); err != nil {
			t.Fatal(err)
		}
	}
	got, err := p.Nodes(ctx)
	sort.Strings(got)
	if err != nil || len(got) != 2 || got[0] != "10.0.0.2:5060" || got[1] != "hello-sip-1:5060" {
		t.Fatalf("nodes = %v, %v", got, err)
	}
	if ttl, _ := c.Do(ctx, c.B().Ttl().Key("hello:node:sip-1").Build()).AsInt64(); ttl <= 0 || ttl > 30 {
		t.Fatalf("node record TTL = %d", ttl)
	}
}

// TestResolvePeers fails if an advertised hostname is not matched by the
// IP:port packets come from, or an unknown address is.
func TestResolvePeers(t *testing.T) {
	set := resolvePeers(context.Background(), []string{"localhost:5070", "192.0.2.1:5060", "junk"})
	for addr, want := range map[string]bool{
		"127.0.0.1:5070": true, "localhost:5070": true, "LOCALHOST:5070": true, "192.0.2.1:5060": true,
		"127.0.0.1:5071": false, "192.0.2.2:5060": false, "junk": false,
	} {
		if got := set.has(addr); got != want {
			t.Errorf("has(%s) = %v, want %v", addr, got, want)
		}
	}
}
