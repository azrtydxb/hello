// In-place restart and CDR continuity (kw 2026-10-06): a hello-sip process
// that crashed and was restarted by Kubernetes in ~2s under the same node
// ID never went OFFLINE, so no survivor took its calls over and its new
// process answered the caller's BYE 481 (the callee never got one, no CDR).
// A graceful handoff hid the call from the live view and lost its CDR, and
// the CDR of a taken-over call started at the takeover, named the wrong
// side and understated the media gap.
package sip

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/cluster"
	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/emiago/sipgo/sip"
)

func withIncarnation(inc string) pbxOpt {
	return func(c *Config, _ *Deps) { c.Incarnation = inc }
}

// withSharedState makes PBXs share one live-state store (as nodes share
// Valkey), so one node's live call record is visible to - and deletable
// by - the other.
func withSharedState(fs *fakeState) pbxOpt {
	return func(_ *Config, d *Deps) { d.State = fs }
}

var mediaGapRe = regexp.MustCompile(`^ha: taken over from \S+ in (\S+) \(media gap ([^)]+)\)$`)

// haTraceTimes reads the takeover step of a CDR trace: how long the
// takeover took from its claim, and the media gap.
func haTraceTimes(t *testing.T, cd cdr.Record) (took, gap time.Duration) {
	t.Helper()
	for _, s := range cd.Trace {
		if m := mediaGapRe.FindStringSubmatch(s.Text); m != nil {
			var err1, err2 error
			took, err1 = time.ParseDuration(m[1])
			gap, err2 = time.ParseDuration(m[2])
			if err1 != nil || err2 != nil {
				t.Fatalf("takeover step %q: %v %v", s.Text, err1, err2)
			}
			return took, gap
		}
	}
	t.Fatalf("CDR trace lacks the takeover step: %s", traceText(cd.Trace))
	return 0, 0
}

// byeOn sends an in-dialog BYE from p on the replicated leg l to pbx and
// returns the response code.
func byeOn(p *phone, pbx *testPBX, l livestate.DialogLeg, cseq uint32) int {
	bye := sip.NewRequest(sip.BYE, sip.Uri{Scheme: "sip", Host: hostOf(pbx.addr), Port: portOf(pbx.addr)})
	var from, to sip.Uri
	_ = sip.ParseUri(l.RemoteIdentity, &from)
	_ = sip.ParseUri(l.LocalIdentity, &to)
	bye.AppendHeader(&sip.FromHeader{Address: from, Params: sip.HeaderParams{{K: "tag", V: l.RemoteTag}}})
	bye.AppendHeader(&sip.ToHeader{Address: to, Params: sip.HeaderParams{{K: "tag", V: l.LocalTag}}})
	callID := sip.CallIDHeader(l.CallID)
	bye.AppendHeader(&callID)
	bye.AppendHeader(&sip.CSeqHeader{SeqNo: cseq, MethodName: sip.BYE})
	bye.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	bye.SetTransport("UDP")
	return p.do(bye).StatusCode
}

// restartedCall connects a1 -> 200 on node sip-1 (incarnation inc-1) and
// waits until it is replicated; the caller then crashes the node.
func restartedCall(t *testing.T) (*fakeHA, *fakeMembership, *testPBX, *phone, *phone, livestate.DialogState, time.Time) {
	t.Helper()
	ha, mem := newFakeHA(), &fakeMembership{}
	owner := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withIncarnation("inc-1"), withHA(ha, mem))
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	before := time.Now()
	connect(t, a, b, "200")
	st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	if st.OwnerIncarnation != "inc-1" || st.StartedAt.IsZero() || st.AnsweredAt.IsZero() {
		t.Fatalf("record lacks the incarnation or CDR continuity: %+v", st)
	}
	return ha, mem, owner, a, b, st, before
}

// TestTakeoverRestartInPlace fails if a node restarted in place under the
// same node ID (a new incarnation, never OFFLINE) does not reclaim the
// calls of its previous process at once: re-INVITE both endpoints, carry
// the call (live view taken-over), end it on a BYE from either side with
// a BYE to the other, and close one CDR with the call's original start,
// the side that hung up and an honest media gap.
func TestTakeoverRestartInPlace(t *testing.T) {
	for _, tc := range []struct {
		name     string
		byCallee bool
	}{{"caller hangs up", false}, {"callee hangs up", true}} {
		t.Run(tc.name, func(t *testing.T) {
			haReinviteTimeout = time.Second
			ha, mem, owner, a, b, st, before := restartedCall(t)
			// The process crashes; Kubernetes restarts it in place within
			// the OFFLINE window: membership lists sip-1 READY throughout,
			// now under its new incarnation.
			owner.stop()
			crashed := time.Now()
			time.Sleep(50 * time.Millisecond)
			mem.set(cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Ready, Incarnation: "inc-2"})
			restarted := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withIncarnation("inc-2"), withHA(ha, mem))

			ra := reinviteOn(t, a, st.Legs[0].CallID, "the restarted node's re-INVITE to the caller")
			rb := reinviteOn(t, b, st.Legs[1].CallID, "the restarted node's re-INVITE to the callee")
			anchoredSDP(t, restarted, ra.Body())
			anchoredSDP(t, restarted, rb.Body())
			eventually(t, "the call is carried by the restarted process", func() bool {
				return connectedOn(restarted, "200", livestate.HATakenOver)
			})
			eventually(t, "the record names the new incarnation", func() bool {
				ha.mu.Lock()
				defer ha.mu.Unlock()
				return ha.dialogs[st.CallID].OwnerIncarnation == "inc-2"
			})
			if ha.claimOf(st.CallID) != "" {
				eventually(t, "claim released", func() bool { return ha.claimOf(st.CallID) == "" })
			}

			if tc.byCallee {
				if code := byeOn(b, restarted, st.Legs[1], rb.CSeq().SeqNo+1); code != 200 {
					t.Fatalf("callee BYE to the restarted node = %d", code)
				}
				waitReq(t, a.byes, "BYE to the caller")
			} else {
				if code := byeOn(a, restarted, st.Legs[0], ra.CSeq().SeqNo+1); code != 200 {
					t.Fatalf("caller BYE to the restarted node = %d", code)
				}
				waitReq(t, b.byes, "BYE to the callee")
			}
			cd := restarted.nextCDR(t)
			want := cdr.SideCaller
			if tc.byCallee {
				want = cdr.SideCallee
			}
			if cd.CorrelationID != st.Correlation || cd.FinalStatus != 200 || cd.TerminationSide != want {
				t.Fatalf("CDR = status %d side %q corr %q; want 200 %q %q", cd.FinalStatus, cd.TerminationSide, cd.CorrelationID, want, st.Correlation)
			}
			if cd.StartTime.Before(before) || !cd.StartTime.Before(crashed) || !cd.AnswerTime.Before(crashed) {
				t.Fatalf("CDR start %s answer %s: want the call's original times (dialled %s, crash %s)",
					cd.StartTime, cd.AnswerTime, before, crashed)
			}
			tr := traceText(cd.Trace)
			if !strings.Contains(tr, "restarted in place") || !strings.Contains(tr, "ha: taken over from sip-1") {
				t.Fatalf("CDR trace = %s", tr)
			}
			// The gap runs from the dead process's last write, which is
			// before the crash: it covers at least crash -> restore.
			took, gap := haTraceTimes(t, cd)
			if gap < took || gap < 50*time.Millisecond {
				t.Fatalf("media gap %s (took %s): understates the outage from the crash", gap, took)
			}
			if z := restarted.metric(t, "hello_zombie_calls_total", nil); z != 0 {
				t.Fatalf("zombies = %v", z)
			}
		})
	}
}

// TestTakeoverOnDemandBye fails if a BYE reaching a node that holds no
// dialog for it is answered 481 while a replicated record of a dead owner
// process exists (kw: the restarted process 481-ed the caller's BYE, the
// callee never got one, no CDR): the BYE must be answered 200, the other
// leg BYEd, the record removed, and one CDR closed with the original start
// and the side that hung up - with no poller help. A BYE with the wrong
// tags still gets 481 and takes nothing.
func TestTakeoverOnDemandBye(t *testing.T) {
	for _, tc := range []struct {
		name     string
		byCallee bool
	}{{"caller hangs up", false}, {"callee hangs up", true}} {
		t.Run(tc.name, func(t *testing.T) {
			ha, _, owner, a, b, st, before := restartedCall(t)
			owner.stop()
			crashed := time.Now()
			// No membership: the restarted node runs no takeover poller;
			// only the BYE can trigger the takeover.
			restarted := startPBX(t, ringAllDevices(), withNodeID("sip-1"), withIncarnation("inc-2"), withHA(ha, nil))

			forged := st.Legs[0]
			forged.RemoteTag = "not-the-caller"
			if code := byeOn(a, restarted, forged, 50); code != 481 {
				t.Fatalf("forged BYE = %d, want 481", code)
			}
			if ha.claimOf(st.CallID) != "" {
				t.Fatal("a forged BYE claimed the call")
			}

			if tc.byCallee {
				if code := byeOn(b, restarted, st.Legs[1], st.Legs[1].RemoteCSeq+1); code != 200 {
					t.Fatalf("callee BYE = %d, want 200 from the replicated state", code)
				}
				bye := waitReq(t, a.byes, "BYE to the caller")
				if bye.CSeq().SeqNo != st.Legs[0].LocalCSeq {
					t.Fatalf("caller BYE CSeq %d, want the replicated %d", bye.CSeq().SeqNo, st.Legs[0].LocalCSeq)
				}
			} else {
				if code := byeOn(a, restarted, st.Legs[0], st.Legs[0].RemoteCSeq+1); code != 200 {
					t.Fatalf("caller BYE = %d, want 200 from the replicated state", code)
				}
				bye := waitReq(t, b.byes, "BYE to the callee")
				if bye.CSeq().SeqNo != st.Legs[1].LocalCSeq {
					t.Fatalf("callee BYE CSeq %d, want the replicated %d", bye.CSeq().SeqNo, st.Legs[1].LocalCSeq)
				}
			}
			cd := restarted.nextCDR(t)
			want := cdr.SideCaller
			if tc.byCallee {
				want = cdr.SideCallee
			}
			if cd.CorrelationID != st.Correlation || cd.FinalStatus != 200 || cd.TerminationSide != want {
				t.Fatalf("CDR = status %d side %q; want 200 %q", cd.FinalStatus, cd.TerminationSide, want)
			}
			if cd.StartTime.Before(before) || !cd.StartTime.Before(crashed) || cd.BillableMs <= 0 {
				t.Fatalf("CDR start %s billable %dms: want the original start", cd.StartTime, cd.BillableMs)
			}
			if !strings.Contains(traceText(cd.Trace), "on demand") {
				t.Fatalf("CDR trace = %s", traceText(cd.Trace))
			}
			eventually(t, "the record is removed", func() bool { return ha.ownerOf(st.CallID) == "" })
			eventually(t, "the call is released", func() bool { return restarted.srv.ActiveCalls() == 0 })
			restarted.srv.mu.Lock()
			bound := len(restarted.srv.dialogs)
			restarted.srv.mu.Unlock()
			if bound != 0 {
				t.Fatalf("%d dialogs still bound after the call ended", bound)
			}
		})
	}
}

// TestTakeoverIncarnationWatch fails if a survivor takes over the calls of
// a node alive under the incarnation that owns them, or does not take
// them over once that node shows up under another incarnation (restarted
// in place, never OFFLINE).
func TestTakeoverIncarnationWatch(t *testing.T) {
	haReinviteTimeout = time.Second
	// A short watch window: the change of incarnation must re-open it
	// after the first sighting's window has passed.
	// Restored after the PBXs stop (cleanups run last-registered first).
	old := haIncWindow
	t.Cleanup(func() { haIncWindow = old })
	haIncWindow = 150 * time.Millisecond
	ha, mem, owner, a, b, st, _ := restartedCall(t)
	mem.set(
		cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Ready, Incarnation: "inc-1"},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready, Incarnation: "inc-9"},
	)
	survivor := startPBX(t, ringAllDevices(), withNodeID("sip-2"), withIncarnation("inc-9"), withHA(ha, mem))
	survivor.srv.takeoverPass(t.Context())
	noReq(t, a.reinvites, 300*time.Millisecond, "a takeover of a live process's call")
	if ha.ownerOf(st.CallID) != "sip-1" {
		t.Fatal("the live owner's call moved")
	}
	// Past the first sighting's window: only the change re-opens it.

	owner.stop()
	mem.set(
		cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Ready, Incarnation: "inc-2"},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready, Incarnation: "inc-9"},
	)
	reinviteOn(t, a, st.Legs[0].CallID, "the survivor's re-INVITE to the caller")
	reinviteOn(t, b, st.Legs[1].CallID, "the survivor's re-INVITE to the callee")
	eventually(t, "the survivor carries the call", func() bool { return connectedOn(survivor, "200", livestate.HATakenOver) })
	ha.mu.Lock()
	restartClaims, offlineClaims := ha.taken["sip-1@inc-1"], ha.taken["sip-1"]
	ha.mu.Unlock()
	if restartClaims != 1 || offlineClaims != 0 {
		t.Fatalf("claims counted %d under the dead incarnation, %d under the node: a restart's claims must not offset an OFFLINE reap",
			restartClaims, offlineClaims)
	}
}

// TestTakeoverCDRContinuity fails if the CDR of a call taken over after its
// node died does not continue the call: its start and answer must be the
// original ones (not the takeover), the termination side the side that
// hung up, and the media gap must run from the owner's last sign of life
// (not from the claim, which hid the detection time: "3ms" on kw for an
// ~11s outage).
func TestTakeoverCDRContinuity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		byCallee bool
	}{{"caller hangs up", false}, {"callee hangs up", true}} {
		t.Run(tc.name, func(t *testing.T) {
			haReinviteTimeout = time.Second
			ha, mem, owner, taker := haPair(t, ringAllDevices())
			a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
			a.register(t)
			b.register(t)
			before := time.Now()
			connect(t, a, b, "200")
			st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
			owner.stop()
			killed := time.Now()
			// The outage before anyone notices: membership's OFFLINE.
			time.Sleep(300 * time.Millisecond)
			killNode(nil, mem, map[string]int{"sip-1": 1}, "sip-2")
			claimed := time.Now()
			taker.srv.takeoverPass(t.Context())
			ra := reinviteOn(t, a, st.Legs[0].CallID, "re-INVITE to the caller")
			rb := reinviteOn(t, b, st.Legs[1].CallID, "re-INVITE to the callee")
			eventually(t, "the call talks on the taker", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
			live := liveOn(taker)
			if len(live) != 1 || live[0].StartedAt.After(killed) || live[0].AnsweredAt.After(killed) {
				t.Fatalf("live view = %+v: want the original start and answer", live)
			}
			if tc.byCallee {
				hangupHomed(t, b, taker, rb, st.Legs[1])
				waitReq(t, a.byes, "BYE to the caller")
			} else {
				hangupHomed(t, a, taker, ra, st.Legs[0])
				waitReq(t, b.byes, "BYE to the callee")
			}
			cd := taker.nextCDR(t)
			want := cdr.SideCaller
			if tc.byCallee {
				want = cdr.SideCallee
			}
			if cd.TerminationSide != want {
				t.Fatalf("termination side = %q, want %q (the side that sent the BYE)", cd.TerminationSide, want)
			}
			if cd.StartTime.Before(before) || cd.StartTime.After(killed) || cd.AnswerTime.After(killed) || cd.RingTime.After(killed) {
				t.Fatalf("CDR start %s ring %s answer %s: want the original times (dialled %s, killed %s)",
					cd.StartTime, cd.RingTime, cd.AnswerTime, before, killed)
			}
			if cd.BillableMs < time.Since(killed).Milliseconds() {
				t.Fatalf("billable %dms is shorter than the time since the kill", cd.BillableMs)
			}
			took, gap := haTraceTimes(t, cd)
			if took > time.Since(claimed) {
				t.Fatalf("takeover took %s, longer than since the claim", took)
			}
			if gap < claimed.Sub(killed) {
				t.Fatalf("media gap %s < the %s before the claim: it must run from the owner's last heartbeat", gap, claimed.Sub(killed))
			}
			if !strings.Contains(traceText(cd.Trace), "the owner's last heartbeat") {
				t.Fatalf("trace does not say where the gap runs from: %s", traceText(cd.Trace))
			}
		})
	}
}

// TestHandoffKeepsLiveCallAndCDR fails if a graceful handoff hides the call
// from the live view (the drainer's yield deleted the record the taker had
// just published under the same correlation id) or loses its CDR (the
// drainer's "taken over" CDR took the correlation id, so the taker's final
// one was dropped): the call must stay listed on the taker, and the one CDR
// must be the taker's, closed at the call's end with its original start.
func TestHandoffKeepsLiveCallAndCDR(t *testing.T) {
	oldPoll := handoffPoll
	t.Cleanup(func() { handoffPoll = oldPoll })
	handoffPoll = 20 * time.Millisecond
	haReinviteTimeout = time.Second
	fs := newFakeState()
	ha, mem, owner, taker := haPair(t, ringAllDevices(), withSharedState(fs))
	a, b := newPhone(t, owner, "a1", "pa"), newPhone(t, owner, "b1", "pb1")
	a.register(t)
	b.register(t)
	before := time.Now()
	connect(t, a, b, "200")
	st := waitRecord(t, ha, "the call is replicated", "sip-1", twoLegged)
	mem.set(
		cluster.Member{ID: "sip-1", Kind: cluster.KindSIP, State: cluster.Draining, ActiveCalls: 1},
		cluster.Member{ID: "sip-2", Kind: cluster.KindSIP, State: cluster.Ready},
	)
	owner.srv.Drain()
	handedOff := time.Now()
	waitRecord(t, ha, "the call is marked for handoff", "sip-1", func(s livestate.DialogState) bool { return s.Handoff })
	taker.srv.takeoverPass(t.Context())
	ra := reinviteOn(t, a, st.Legs[0].CallID, "handoff re-INVITE to the caller")
	reinviteOn(t, b, st.Legs[1].CallID, "handoff re-INVITE to the callee")
	eventually(t, "the call is listed on the taker", func() bool { return connectedOn(taker, "200", livestate.HATakenOver) })
	eventually(t, "the drainer yielded", func() bool { return owner.srv.ActiveCalls() == 0 })
	// Past the yield, the call stays listed on the taker: the drainer
	// neither deletes the record nor writes its own over it.
	for range 5 {
		if !connectedOn(taker, "200", livestate.HATakenOver) {
			t.Fatalf("the handed-off call vanished from the live view: %+v", liveOn(taker))
		}
		time.Sleep(20 * time.Millisecond)
	}
	owner.noCDR(t, 100*time.Millisecond)
	hangupHomed(t, a, taker, ra, st.Legs[0])
	waitReq(t, b.byes, "BYE to the callee")
	cd := taker.nextCDR(t)
	if cd.CorrelationID != st.Correlation || cd.FinalStatus != 200 || cd.TerminationSide != cdr.SideCaller {
		t.Fatalf("CDR = %+v", cd)
	}
	if cd.StartTime.Before(before) || cd.StartTime.After(handedOff) {
		t.Fatalf("CDR start %s: want the call's original start (dialled %s, handed off %s)", cd.StartTime, before, handedOff)
	}
	if tr := traceText(cd.Trace); !strings.Contains(tr, "handed off") || !strings.Contains(tr, "ha: taken over from sip-1") {
		t.Fatalf("the CDR trace does not carry the whole call: %s", tr)
	}
	took, gap := haTraceTimes(t, cd)
	if gap != took {
		t.Fatalf("handoff gap %s != takeover %s: the drainer relays until the claim", gap, took)
	}
	if len(liveOn(taker)) != 0 {
		t.Fatalf("live record left after the end: %+v", liveOn(taker))
	}
}
