package flow

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/store"
)

// benchInput is a large installation: 10 000 extensions, 1 000 inbound routes
// and the groups, menus and queues a site that size has.
func benchInput() Input {
	in := Input{Revision: 1, Trunks: []store.Trunk{{ID: 1, Name: "carrier", Enabled: true}}}
	for i := range 10000 {
		in.Extensions = append(in.Extensions, store.Extension{
			ID: int64(i + 1), Number: fmt.Sprintf("%d", 1000+i), Name: fmt.Sprintf("User %d", i), VoicemailEnabled: i%2 == 0,
		})
	}
	for g := range 200 {
		rg := store.RingGroup{ID: int64(g + 1), Name: fmt.Sprintf("group%d", g), Strategy: "ring-all", FailureKind: "voicemail", FailureTarget: "1000"}
		for m := range 20 {
			rg.Members = append(rg.Members, store.RingGroupMember{ExtensionID: int64(g*20 + m + 1), Position: m + 1, Weight: 1})
		}
		in.RingGroups = append(in.RingGroups, rg)
	}
	in.Announcements = []store.Announcement{{ID: 1, Name: "welcome", Object: "w.wav"}}
	for m := range 50 {
		menu := store.IVRMenu{ID: int64(m + 1), Name: fmt.Sprintf("menu%d", m), Enabled: true, GreetingID: 1,
			Timeout: store.IVRDestination{Kind: "hangup"}, Invalid: store.IVRDestination{Kind: "hangup"}}
		for d := range 9 {
			menu.Options = append(menu.Options, store.IVROption{Digit: fmt.Sprintf("%d", d+1), Kind: "ring_group", Target: fmt.Sprintf("group%d", (m*9+d)%200)})
		}
		in.IVRMenus = append(in.IVRMenus, menu)
	}
	for q := range 20 {
		in.Queues = append(in.Queues, store.Queue{ID: int64(q + 1), Name: fmt.Sprintf("q%d", q), Number: fmt.Sprintf("8%02d", q),
			Timeout: store.IVRDestination{Kind: "hangup"}, Full: store.IVRDestination{Kind: "hangup"}, NoAgents: store.IVRDestination{Kind: "hangup"},
			Members: []store.QueueMember{{ExtensionID: int64(q + 1), Position: 1}}})
	}
	for r := range 1000 {
		tid := int64(1)
		kind, dst := "ring_group", fmt.Sprintf("group%d", r%200)
		if r%5 == 0 {
			kind, dst = "ivr", fmt.Sprintf("menu%d", r%50)
		}
		in.Inbound = append(in.Inbound, store.InboundRoute{ID: int64(r + 1), Position: r + 1, Name: fmt.Sprintf("did%d", r),
			DIDKind: "exact", DID: fmt.Sprintf("+3225%06d", r), TrunkID: &tid, DestinationKind: kind, Destination: dst, Enabled: true})
	}
	return in
}

// TestGraphBench fails if building the graph takes 200 ms or more with 10 000
// extensions and 1 000 routes (spec flow-canvas, performance). It runs only
// with HELLO_BENCH=1: timing assertions do not belong in the default run on a
// shared runner.
func TestGraphBench(t *testing.T) {
	if os.Getenv("HELLO_BENCH") != "1" {
		t.Skip("set HELLO_BENCH=1 to run the graph build benchmark")
	}
	in := benchInput()
	best := time.Hour
	for range 5 {
		start := time.Now()
		g, err := Build(in, Options{})
		if err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d < best {
			best = d
		}
		if len(g.Nodes) < 10000 {
			t.Fatalf("only %d nodes", len(g.Nodes))
		}
	}
	t.Logf("graph build: %v", best)
	if best >= 200*time.Millisecond {
		t.Fatalf("graph build took %v, want under 200ms", best)
	}
}

func BenchmarkGraphBuild(b *testing.B) {
	in := benchInput()
	b.ResetTimer()
	for range b.N {
		if _, err := Build(in, Options{}); err != nil {
			b.Fatal(err)
		}
	}
}
