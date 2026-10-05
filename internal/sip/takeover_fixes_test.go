package sip

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/emiago/sipgo/sip"
)

// TestTakeoverKeepsClaimUntilRecorded fails if a taker lets go of its
// takeover claim while the dialog record still names the dead owner (its
// first replication write failed): a survivor's next scan would find the
// dialog unclaimed and orphaned and take the call a second time, and the
// failed second takeover counts a zombie. It also fails if the taker, holding
// its own claim over a record that still names the dead node, yields the
// call to that dead node.
func TestTakeoverKeepsClaimUntilRecorded(t *testing.T) {
	ha, mem, owner, taker := haPair(t, ringAllDevices())
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	connect(t, a, b, "200")
	st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	// The replication store refuses writes while the taker re-homes.
	ha.mu.Lock()
	ha.failSave = true
	ha.mu.Unlock()
	taker.srv.takeoverPass(t.Context())
	ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
	eventually(t, "taken over", func() bool { return taker.metric(t, "hello_dialog_takeovers_total", nil) == 1 })
	// Several heartbeats with the record still naming sip-1: the claim
	// stays the taker's, and nothing takes the call a second time.
	time.Sleep(200 * time.Millisecond)
	if c := ha.claimOf(st.CallID); c != "sip-2" {
		t.Fatalf("claim = %q while the record names %q, want the taker's", c, ha.ownerOf(st.CallID))
	}
	taker.srv.takeoverPass(t.Context())
	select {
	case <-a.reinvites:
		t.Fatal("the call was taken over a second time")
	case <-time.After(300 * time.Millisecond):
	}
	// The store recovers: the heartbeat's write names the taker, and only
	// then does the claim go.
	ha.mu.Lock()
	ha.failSave = false
	ha.mu.Unlock()
	eventually(t, "the claim released once the record names the taker", func() bool {
		return ha.claimOf(st.CallID) == "" && ha.ownerOf(st.CallID) == "sip-2"
	})
	ha.mu.Lock()
	stale := ha.staleReleases
	ha.mu.Unlock()
	if stale != 0 {
		t.Fatalf("%d claims released while the record named another node", stale)
	}
	// The taker kept the call throughout: it still answers the caller's BYE.
	hangupHomed(t, a, taker, ra, st.Legs[0])
	waitReq(t, b.byes, "BYE to the callee after the caller hung up")
	if z := taker.metric(t, "hello_zombie_calls_total", nil); z != 0 {
		t.Fatalf("zombies = %v, want 0", z)
	}
	if v := taker.metric(t, "hello_dialog_takeovers_total", nil); v != 1 {
		t.Fatalf("takeovers = %v, want 1", v)
	}
}

// TestBackgroundWorkDuringShutdown fails (under -race) if background work
// started while a node shuts down can grow the server's WaitGroup while
// Serve waits on it: a call ending as its node stops (a yielded or
// taken-over call publishing presence) must be dropped, not started.
func TestBackgroundWorkDuringShutdown(t *testing.T) {
	for range 10 {
		pbx := startPBX(t, ringAllDevices())
		stop := make(chan struct{})
		var wg sync.WaitGroup
		for range 4 {
			wg.Go(func() {
				for {
					select {
					case <-stop:
						return
					default:
					}
					pbx.srv.goBG(func() {})
				}
			})
		}
		pbx.stop()
		eventually(t, "shutdown waits for background work", func() bool {
			pbx.srv.bgMu.Lock()
			defer pbx.srv.bgMu.Unlock()
			return pbx.srv.bgClosed
		})
		time.Sleep(20 * time.Millisecond) // keep starting work across the Wait
		close(stop)
		wg.Wait()
	}
}

// TestTakeoverByeFromAnotherProxyAddress fails if a taken-over call only
// accepts in-dialog requests from the exact source the old owner recorded
// (kw: the record named the edge pod's 10.42.0.128:5070, the edge reached
// the survivor from its node's 192.168.10.102:5070, and every BYE got
// 481). The dialog identity (Call-ID and tags) selects the call; any
// trusted proxy may carry the request. An untrusted source is still
// refused.
func TestTakeoverByeFromAnotherProxyAddress(t *testing.T) {
	for _, trusted := range []bool{true, false} {
		name := "trusted"
		if !trusted {
			name = "untrusted"
		}
		t.Run(name, func(t *testing.T) {
			var opts []pbxOpt
			if trusted {
				opts = append(opts, withTrusted("127.0.0.1/32"))
			}
			ha, mem, owner, taker := haPair(t, ringAllDevices(), opts...)
			a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
			a.register(t)
			b.register(t)
			connect(t, a, b, "200")
			st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
			haReinviteTimeout = time.Second
			killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
			taker.srv.takeoverPass(t.Context())
			ra := waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
			waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
			eventually(t, "the call talks on the taker", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
			// The caller's BYE arrives from another address than the one
			// replicated for it (the edge as the dead node saw it).
			edge := newPhone(t, taker, "edge", "x")
			if edge.addr == st.Legs[0].Source {
				t.Fatalf("the edge must send from another address than %s", st.Legs[0].Source)
			}
			bye := sip.NewRequest(sip.BYE, sip.Uri{Scheme: "sip", Host: hostOf(taker.addr), Port: portOf(taker.addr)})
			bye.AppendHeader(&sip.FromHeader{Address: *ra.To().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: st.Legs[0].RemoteTag}}})
			bye.AppendHeader(&sip.ToHeader{Address: *ra.From().Address.Clone(), Params: sip.HeaderParams{{K: "tag", V: st.Legs[0].LocalTag}}})
			callID := sip.CallIDHeader(st.Legs[0].CallID)
			bye.AppendHeader(&callID)
			bye.AppendHeader(&sip.CSeqHeader{SeqNo: ra.CSeq().SeqNo + 1, MethodName: sip.BYE})
			bye.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
			bye.SetTransport("UDP")
			res := edge.do(bye)
			if !trusted {
				if res.StatusCode != sip.StatusCallTransactionDoesNotExists {
					t.Fatalf("BYE from an untrusted source = %d, want 481", res.StatusCode)
				}
				return
			}
			if res.StatusCode != 200 {
				t.Fatalf("BYE from a trusted proxy address = %d, want 200", res.StatusCode)
			}
			waitReq(t, b.byes, "BYE to the callee after the caller hung up")
			cd := taker.nextCDR(t)
			if cd.FinalStatus != 200 || !strings.Contains(traceText(cd.Trace), "ha: taken over from sip-1") {
				t.Fatalf("CDR = %d, trace %s", cd.FinalStatus, traceText(cd.Trace))
			}
		})
	}
}

// TestTakeoverKeepsMaxDuration fails if a taken-over call escapes the
// maximum call duration (an orphan that never clears), or if the taker
// restarts the clock instead of continuing it from the original answer.
func TestTakeoverKeepsMaxDuration(t *testing.T) {
	const maxDur = 1500 * time.Millisecond
	ha, mem := newFakeHA(), &fakeMembership{}
	owner := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withHA(ha, mem))
	taker := startPBX(t, ringAllDevices(), withNodeID("sip-2"), withHA(ha, mem),
		func(c *Config, _ *Deps) { c.MaxCallDuration = maxDur })
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	connect(t, a, b, "200")
	st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	if st.AnsweredAt.IsZero() {
		t.Fatal("the record lacks the call's answer time")
	}
	time.Sleep(time.Second) // most of the call's duration passes on the owner
	haReinviteTimeout = time.Second
	killNode(owner, mem, map[string]int{"sip-1": 1}, "sip-2")
	taker.srv.takeoverPass(t.Context())
	waitReq(t, a.reinvites, "takeover re-INVITE to the caller")
	waitReq(t, b.reinvites, "takeover re-INVITE to the callee")
	waitReq(t, a.byes, "the taker's max-duration BYE to the caller")
	waitReq(t, b.byes, "the taker's max-duration BYE to the callee")
	if over := time.Since(st.AnsweredAt); over > maxDur+600*time.Millisecond {
		t.Fatalf("cleared %s after the answer, want about %s (the clock restarted on the taker)", over, maxDur)
	}
	if rec := ha.ownerOf(st.CallID); rec != "" {
		eventually(t, "the record removed", func() bool { return ha.ownerOf(st.CallID) == "" })
	}
}

// TestEndedCallRecordNotResurrected fails if a replication write in flight
// when a call ends can land after the record's deletion: the ended call's
// record would come back, and a survivor would take that ghost over
// (re-INVITEs on dialogs the phones closed, a zombie counted for it).
func TestEndedCallRecordNotResurrected(t *testing.T) {
	ha, _, owner, _ := haPair(t, ringAllDevices())
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	r := connect(t, a, b, "200")
	st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	c := ownedCall(t, owner.srv)
	gate, entered := make(chan struct{}), make(chan struct{}, 1)
	var once sync.Once
	ha.mu.Lock()
	ha.saveHook = func() {
		select {
		case entered <- struct{}{}:
		default:
		}
		<-gate
	}
	ha.mu.Unlock()
	defer once.Do(func() { close(gate) })
	go c.replicate()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("no replication write started")
	}
	// The caller hangs up while that write is in flight.
	go func() { _ = r.dcs.Bye(t.Context()) }()
	eventually(t, "the call ended", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.ended
	})
	ha.mu.Lock()
	ha.saveHook = nil
	ha.mu.Unlock()
	once.Do(func() { close(gate) })
	eventually(t, "the record deleted", func() bool {
		ha.mu.Lock()
		defer ha.mu.Unlock()
		return ha.deleted[st.CallID] > 0
	})
	time.Sleep(100 * time.Millisecond) // any write still in flight lands
	if owner := ha.ownerOf(st.CallID); owner != "" {
		t.Fatalf("the ended call's record came back (owner %q)", owner)
	}
}
