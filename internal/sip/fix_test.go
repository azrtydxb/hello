package sip

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
)

// TestChallengedAttemptStillTimesOut fails if, after a carrier's 407 is
// answered, a carrier that then stays silent never times out (failover
// must still happen within the attempt timeout).
func TestChallengedAttemptStillTimesOut(t *testing.T) {
	pbx, _, primary, backup, a := twoCarriers(t, nil)
	primary.challenge.Store(true)
	primary.inviteCode.Store(-1) // silent once authorized
	start := time.Now()
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("failover after a challenge took %s", d)
	}
	waitReq(t, backup.invites, "INVITE to the backup")
	hangup(t, r.dcs)
	wantTrace(t, pbx.nextCDR(t).Trace, "carrier-primary ("+primary.addr+") -> no answer", "carrier-backup -> 200 OK")
}

// TestCancelBetweenAttemptsSendsNoInvite fails if a CANCEL that arrives
// between two attempts still lets the next INVITE go out.
func TestCancelBetweenAttemptsSendsNoInvite(t *testing.T) {
	pbx, st, primary, backup, a := twoCarriers(t, nil)
	primary.inviteCode.Store(503)
	gate := st.gateAcquire(2) // the backup's slot acquire waits
	ctx, cancel := context.WithCancel(t.Context())
	res := dial(ctx, a, "0501234567")
	select {
	case <-st.acquireBlocked: // the primary failed; the next attempt is starting
	case <-time.After(10 * time.Second):
		t.Fatal("never reached the backup attempt")
	}
	cancel()
	waitCall(t, res) // 487: the CANCEL has been processed
	close(gate)
	noReq(t, backup.invites, 300*time.Millisecond, "INVITE after the caller cancelled")
	cd := pbx.nextCDR(t)
	if cd.FinalStatus != 487 || cd.TerminationSide != "caller" {
		t.Fatalf("CDR = %+v", cd)
	}
	wantTrace(t, cd.Trace, "Caller cancelled; no further trunks tried")
}

// TestReRegisterFloor fails if a carrier granting a very short expiry
// makes the node re-REGISTER sooner than the floor.
func TestReRegisterFloor(t *testing.T) {
	cr := newCarrier(t, "acct", "pw")
	cr.grant.Store(1) // 80% would be 800ms
	r := &fakeRouter{trunks: []routing.Trunk{cr.trunk(1, "carrier-reg", "registration")}}
	startPBX(t, nil, trunkCfg(newFakeTrunkState()), withRouter(r, nil),
		func(c *Config, _ *Deps) { c.TrunkReRegisterMin = 1500 * time.Millisecond })
	eventually(t, "two registrations", func() bool { return len(cr.registrations()) >= 2 })
	cr.mu.Lock()
	gap := cr.regTimes[1].Sub(cr.regTimes[0])
	cr.mu.Unlock()
	if gap < 1400*time.Millisecond {
		t.Fatalf("re-registered after %s, below the 1.5s floor", gap)
	}
}

// TestTrunkFreshnessPerTrunk fails if one trunk whose status cannot be
// read makes every trunk "state unavailable".
func TestTrunkFreshnessPerTrunk(t *testing.T) {
	pbx, st, primary, backup, a := twoCarriers(t, nil)
	st.failStatus(1, true)
	time.Sleep(300 * time.Millisecond) // trunk 1's cache entry goes stale
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r.err != nil {
		t.Fatalf("call: %v", r.err)
	}
	waitReq(t, backup.invites, "INVITE to the readable trunk")
	noReq(t, primary.invites, 50*time.Millisecond, "INVITE to the unreadable trunk")
	hangup(t, r.dcs)
	wantTrace(t, pbx.nextCDR(t).Trace, "Trunk carrier-primary skipped: state unavailable")
}

// TestStatusPollNotStalledByLeases fails if a blocked lease operation stops
// the status poll, leaving every trunk "state unavailable".
func TestStatusPollNotStalledByLeases(t *testing.T) {
	pbx, st, _, backup, a := twoCarriers(t, nil)
	backup.inviteCode.Store(200)
	gate := st.gateLeases()
	defer close(gate)
	time.Sleep(400 * time.Millisecond) // the lease loop is stuck; many polls pass
	r := waitCall(t, dial(t.Context(), a, "0501234567"))
	if r.err != nil {
		t.Fatalf("call while leases are stuck: %v", r.err)
	}
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestTrunkMetricsPruned fails if the metric series of a renamed trunk, or
// of a removed destination, outlive it.
func TestTrunkMetricsPruned(t *testing.T) {
	cr, other := newCarrier(t, "acct", "pw"), newCarrier(t, "acct", "pw")
	tr := cr.trunk(1, "carrier-old", "ip")
	tr.Destinations = append(tr.Destinations, other.dest())
	r := &fakeRouter{trunks: []routing.Trunk{tr}}
	pbx := startPBX(t, nil, trunkCfg(newFakeTrunkState()), withRouter(r, nil))
	old := map[string]string{"trunk": "carrier-old", "destination": other.destKey()}
	eventually(t, "series for the old name", func() bool { return pbx.metric(t, "hello_trunk_status", old) == 1 })

	renamed := cr.trunk(1, "carrier-new", "ip") // renamed, and one destination removed
	r2 := &fakeRouter{trunks: []routing.Trunk{renamed}}
	pbx.snaps.p.Store(snapshot.New(2, testDomain, nil).WithRouting(&snapshot.RoutingState{Router: r2, Config: routing.Config{Trunks: r2.trunks}}))
	eventually(t, "old series dropped", func() bool {
		return !seriesExists(t, pbx, "hello_trunk_status", map[string]string{"trunk": "carrier-old"}) &&
			!seriesExists(t, pbx, "hello_trunk_registered", map[string]string{"trunk": "carrier-old"})
	})
	eventually(t, "new series", func() bool {
		return seriesExists(t, pbx, "hello_trunk_status", map[string]string{"trunk": "carrier-new", "destination": cr.destKey()})
	})
	if seriesExists(t, pbx, "hello_trunk_status", map[string]string{"trunk": "carrier-new", "destination": other.destKey()}) {
		t.Fatal("removed destination has a series")
	}
}

func seriesExists(t *testing.T, pbx *testPBX, name string, labels map[string]string) bool {
	t.Helper()
	mfs, err := pbx.reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range mfs {
		if mf.GetName() != name {
			continue
		}
	next:
		for _, m := range mf.GetMetric() {
			for k, v := range labels {
				found := false
				for _, lp := range m.GetLabel() {
					if lp.GetName() == k && lp.GetValue() == v {
						found = true
					}
				}
				if !found {
					continue next
				}
			}
			return true
		}
	}
	return false
}

// TestSIPSDestinationRejected fails if an inbound route to a sips: URI is
// attempted over UDP instead of refused with a clear trace step.
func TestSIPSDestinationRejected(t *testing.T) {
	r := &fakeRouter{trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true}}}
	r.decide = func(routing.Call, routing.TrunkUsability) routing.Decision {
		return routing.Decision{Kind: routing.KindInbound, SIPURI: "sips:agent@voice.example", Route: "AI"}
	}
	pbx := startPBX(t, callerDevices(), trunkCfg(newFakeTrunkState()), withRouter(r, nil))
	from := newPhone(t, pbx, "carrier", "")
	r.setSource(mustAddr(t, from.addr), 7)
	dcs, err := from.dua.WriteInvite(t.Context(), carrierInvite(from, pbx, "+97140000100"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); responseCode(err) != 503 {
		t.Fatalf("sips: destination = %v, want 503", err)
	}
	wantTrace(t, pbx.nextCDR(t).Trace, "SIP URI destination sips:agent@voice.example uses sips:, which needs TLS; Hello only sends UDP")
}

func mustAddr(t *testing.T, hostport string) netip.Addr {
	t.Helper()
	ap, err := netip.ParseAddrPort(hostport)
	if err != nil {
		t.Fatal(err)
	}
	return ap.Addr()
}
