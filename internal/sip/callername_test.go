package sip

import (
	"context"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo"

	"github.com/azrtydxb/hello/internal/routing"
)

type fakeNamer struct {
	mu     sync.Mutex
	calls  []string
	delay  time.Duration
	name   string
	budget []time.Duration
}

func (f *fakeNamer) Name(ctx context.Context, number string, budget time.Duration) (string, bool) {
	f.mu.Lock()
	f.calls = append(f.calls, number)
	f.budget = append(f.budget, budget)
	f.mu.Unlock()
	select {
	case <-time.After(f.delay):
	case <-ctx.Done():
		return "", false
	}
	return f.name, f.name != ""
}

func (f *fakeNamer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// inboundCallerName places an inbound trunk call and returns the display name
// the callee's INVITE carries and how long the callee took to be rung.
func inboundCallerName(t *testing.T, namer CallerNamer, budget time.Duration) (string, time.Duration) {
	t.Helper()
	cr := newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-in", "ip")
	tr.SourceCIDRs = []netip.Prefix{netip.MustParsePrefix("127.0.0.1/32")}
	cfg := routing.Config{
		Trunks: []routing.Trunk{tr},
		Inbound: []routing.InboundRoute{{ID: 1, Position: 1, Name: "Main DID", DIDKind: "exact", DID: "+97140000100",
			DestinationKind: "extension", Destination: "100", Enabled: true}},
		Extensions: map[string]string{"100": "", "200": ""},
	}
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withEngine(t, cfg), func(_ *Config, deps *Deps) {
		deps.CallerNames, deps.CallerNameBudget = namer, budget
	})
	callee := newPhone(t, pbx, "a1", "pa")
	callee.register(t)
	from := newPhone(t, pbx, "carrier", "")
	start := time.Now()
	dcs, err := from.dua.WriteInvite(t.Context(), carrierInvite(from, pbx, "+97140000100"))
	if err != nil {
		t.Fatal(err)
	}
	inv := waitReq(t, callee.invites, "INVITE to extension 100")
	rang := time.Since(start)
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("inbound call: %v", err)
	}
	_ = dcs.Ack(t.Context())
	hangup(t, dcs)
	pbx.nextCDR(t)
	return inv.From().DisplayName, rang
}

// TestInboundCallerNameFromCRM fails if an inbound trunk call does not ring
// the callee with the CRM's name for the caller's number, or looks the number
// up under any other form than the one the call carries.
func TestInboundCallerNameFromCRM(t *testing.T) {
	n := &fakeNamer{name: "Ann Smith (ACME)"}
	name, _ := inboundCallerName(t, n, time.Second)
	if name != "Ann Smith (ACME)" {
		t.Fatalf("callee saw display name %q, want the CRM's", name)
	}
	if n.count() != 1 || n.calls[0] != "+971555000111" {
		t.Fatalf("looked up %v", n.calls)
	}
}

// TestSlowCRMNameNeverDelaysTheRing fails if a CRM that does not answer holds
// the inbound call longer than the lookup budget, or if the call then rings
// without the carrier's number as the name.
func TestSlowCRMNameNeverDelaysTheRing(t *testing.T) {
	n := &fakeNamer{name: "Too Late", delay: 5 * time.Second}
	name, rang := inboundCallerName(t, n, 150*time.Millisecond)
	if rang > 2*time.Second {
		t.Fatalf("the callee was rung after %v: the slow CRM held the call", rang)
	}
	if name == "Too Late" {
		t.Fatal("a name that arrived after the budget was used")
	}
}

func TestCRMCallerNameKeepsARealCarrierName(t *testing.T) {
	s := &Server{deps: Deps{CallerNames: &fakeNamer{name: "CRM Name"}}}
	for _, tc := range []struct{ num, carrier, want string }{
		{"+3211223344", "", "CRM Name"},
		{"+3211223344", "+3211223344", "CRM Name"},
		{"+3211223344", "0032 11 22 33 44", "CRM Name"},
		{"+3211223344", "ACME NV", "ACME NV"},
		{"", "", ""},
	} {
		if got := s.crmCallerName(tc.num, tc.carrier); got != tc.want {
			t.Errorf("crmCallerName(%q, %q) = %q, want %q", tc.num, tc.carrier, got, tc.want)
		}
	}
	s = &Server{}
	if got := s.crmCallerName("+3211223344", "x"); got != "x" {
		t.Fatalf("without a namer the name changed to %q", got)
	}
}
