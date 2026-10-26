package sip

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
)

// failoverSnapshot builds ring groups from name, members and failure
// destination; members are extension numbers of the test devices.
type failoverGroup struct {
	name, strategy string
	hunt           bool
	members        []string
	kind, target   string
}

func withGroups(gs ...failoverGroup) pbxOpt {
	return withFeatures(func(s *snapshot.Snapshot) {
		var groups []snapshot.RingGroup
		var members [][]snapshot.RingGroupMember
		for i, g := range gs {
			strategy := g.strategy
			if strategy == "" {
				strategy = "ring-all"
			}
			groups = append(groups, snapshot.RingGroup{
				ID: int64(i + 1), Name: g.name, Strategy: strategy, Hunt: g.hunt, RingTimeout: 30, MemberDelay: 1,
				FailureKind: g.kind, FailureTarget: g.target,
			})
			var ms []snapshot.RingGroupMember
			for j, m := range g.members {
				ms = append(ms, snapshot.RingGroupMember{ExtensionID: int64(j + 1), Extension: m, Position: j + 1, Weight: 1})
			}
			members = append(members, ms)
		}
		s.WithRingGroups(groups, members)
	})
}

// TestGroupFailoverToExtension fails if a ring group (ring-all or hunt)
// whose failure is an extension does not ring that extension after its own
// members gave up, or if the first stage's leg is left ringing.
func TestGroupFailoverToExtension(t *testing.T) {
	for _, hunt := range []bool{false, true} {
		t.Run(fmt.Sprintf("hunt=%v", hunt), func(t *testing.T) {
			pbx := startPBX(t, threeDevices(), shortRing(300*time.Millisecond),
				withGroups(failoverGroup{name: "700", hunt: hunt, strategy: "sequential", members: []string{"200"}, kind: "extension", target: "300"}))
			a := newPhone(t, pbx, "a1", "pa")
			b := newPhone(t, pbx, "b1", "pb1")
			c := newPhone(t, pbx, "c1", "pc")
			for _, p := range []*phone{a, b, c} {
				p.register(t)
			}
			b.setCallee(ringForever())
			r := waitCall(t, dial(t.Context(), a, "700"))
			if r.err != nil {
				t.Fatalf("group call: %v", r.err)
			}
			waitReq(t, b.invites, "INVITE to the group member")
			waitReq(t, c.invites, "INVITE to the failover extension")
			waitReq(t, b.cancels, "CANCEL to the member after the stage ended")
			hangup(t, r.dcs)
			rec := pbx.nextCDR(t)
			if got := traceText(rec.Trace); !strings.Contains(got, "fails over to extension 300") {
				t.Fatalf("trace lacks the failover:\n%s", got)
			}
			if v := pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "group_failover"}); v != 1 {
				t.Fatalf("forwarded{group_failover} = %v", v)
			}
		})
	}
}

// TestGroupFailoverExtensionVoicemail fails if a failover extension that is
// unreachable, or that does not answer, leaves its message in a box other
// than its own (the dialled string is the group number).
func TestGroupFailoverExtensionVoicemail(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprintf("registered=%v", registered), func(t *testing.T) {
			vm := newVMStore()
			vm.boxes["300"] = VoicemailBox{ID: 9, Extension: "300"}
			vm.boxes["700"] = VoicemailBox{ID: 8, Extension: "700"}
			pbx := startPBX(t, threeDevices(), shortRing(300*time.Millisecond),
				withFeatures(func(s *snapshot.Snapshot) {
					s.WithExtensionData([]snapshot.Extension{{Number: "300", VoicemailEnabled: true, VoicemailBoxID: 9}})
				}),
				withGroups(failoverGroup{name: "700", members: []string{"200"}, kind: "extension", target: "300"}),
				withVoicemail(vm, &fakeObjects{}))
			anchored(pbx)
			a := newPhone(t, pbx, "a1", "pa")
			b := newPhone(t, pbx, "b1", "pb1")
			a.register(t)
			b.register(t)
			b.setCallee(ringForever())
			if registered {
				c := newPhone(t, pbx, "c1", "pc")
				c.register(t)
				c.setCallee(ringForever())
			}
			r := waitCall(t, dial(t.Context(), a, "700"))
			if r.err != nil {
				t.Fatalf("group call: %v", r.err)
			}
			deadline := time.Now().Add(5 * time.Second)
			for {
				vm.mu.Lock()
				got := append([]string(nil), vm.looked...)
				vm.mu.Unlock()
				if len(got) > 0 {
					if got[0] != "300" {
						t.Fatalf("voicemail opened box %q, want 300", got[0])
					}
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("voicemail application never looked up a box")
				}
				time.Sleep(10 * time.Millisecond)
			}
			hangup(t, r.dcs)
			pbx.nextCDR(t)
		})
	}
}

// TestGroupFailoverToGroup fails if a failure destination that is another
// ring group does not ring that group's members, for a ring-all group and
// for a hunt group.
func TestGroupFailoverToGroup(t *testing.T) {
	for _, hunt := range []bool{false, true} {
		t.Run(fmt.Sprintf("hunt=%v", hunt), func(t *testing.T) {
			pbx := startPBX(t, threeDevices(), shortRing(300*time.Millisecond),
				withGroups(
					failoverGroup{name: "700", members: []string{"200"}, kind: "ring_group", target: "800"},
					failoverGroup{name: "800", hunt: hunt, strategy: "sequential", members: []string{"300"}, kind: "none"},
				))
			a := newPhone(t, pbx, "a1", "pa")
			b := newPhone(t, pbx, "b1", "pb1")
			c := newPhone(t, pbx, "c1", "pc")
			for _, p := range []*phone{a, b, c} {
				p.register(t)
			}
			b.setCallee(ringForever())
			r := waitCall(t, dial(t.Context(), a, "700"))
			if r.err != nil {
				t.Fatalf("group call: %v", r.err)
			}
			waitReq(t, b.invites, "INVITE to the first group's member")
			waitReq(t, c.invites, "INVITE to the second group's member")
			hangup(t, r.dcs)
			rec := pbx.nextCDR(t)
			if got := traceText(rec.Trace); !strings.Contains(got, "fails over to group 800") {
				t.Fatalf("trace lacks the failover:\n%s", got)
			}
		})
	}
}

// TestGroupFailoverOwnFailure fails if the group a call failed over to does
// not apply its own failure destination when its members give up too.
func TestGroupFailoverOwnFailure(t *testing.T) {
	pbx := startPBX(t, threeDevices(), shortRing(300*time.Millisecond),
		withGroups(
			failoverGroup{name: "700", members: []string{"200"}, kind: "ring_group", target: "800"},
			failoverGroup{name: "800", members: []string{"300"}, kind: "extension", target: "210"},
		))
	a := newPhone(t, pbx, "a1", "pa")
	b := newPhone(t, pbx, "b1", "pb1")
	c := newPhone(t, pbx, "c1", "pc")
	d := newPhone(t, pbx, "d1", "pd")
	for _, p := range []*phone{a, b, c, d} {
		p.register(t)
	}
	b.setCallee(ringForever())
	c.setCallee(ringForever())
	r := waitCall(t, dial(t.Context(), a, "700"))
	if r.err != nil {
		t.Fatalf("group call: %v", r.err)
	}
	waitReq(t, d.invites, "INVITE to the extension of the second group's failure")
	hangup(t, r.dcs)
	pbx.nextCDR(t)
}

// TestGroupFailoverLoop fails if groups that fail over to each other (or to
// themselves) ring forever instead of being cut with 408, and if a chain
// longer than the depth bound is not cut either.
func TestGroupFailoverLoop(t *testing.T) {
	t.Run("two groups", func(t *testing.T) {
		pbx := startPBX(t, threeDevices(), shortRing(200*time.Millisecond),
			withGroups(
				failoverGroup{name: "700", members: []string{"200"}, kind: "ring_group", target: "800"},
				failoverGroup{name: "800", members: []string{"300"}, kind: "ring_group", target: "700"},
			))
		a := newPhone(t, pbx, "a1", "pa")
		b := newPhone(t, pbx, "b1", "pb1")
		c := newPhone(t, pbx, "c1", "pc")
		for _, p := range []*phone{a, b, c} {
			p.register(t)
		}
		b.setCallee(ringForever())
		c.setCallee(ringForever())
		r := waitCall(t, dial(t.Context(), a, "700"))
		if code := responseCode(r.err); r.err == nil || code != 408 {
			t.Fatalf("failover loop = %v, want 408", r.err)
		}
		rec := pbx.nextCDR(t)
		if rec.FinalStatus != 408 || !strings.Contains(traceText(rec.Trace), "failover loop detected") {
			t.Fatalf("CDR %d, trace:\n%s", rec.FinalStatus, traceText(rec.Trace))
		}
		if v := pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "loop"}); v != 1 {
			t.Fatalf("forwarded{loop} = %v", v)
		}
	})
	t.Run("a group to itself", func(t *testing.T) {
		pbx := startPBX(t, callerDevices(), shortRing(200*time.Millisecond),
			withGroups(failoverGroup{name: "700", members: []string{"200"}, kind: "ring_group", target: "700"}))
		a := newPhone(t, pbx, "a1", "pa")
		b := newPhone(t, pbx, "b1", "pb1")
		a.register(t)
		b.register(t)
		b.setCallee(ringForever())
		r := waitCall(t, dial(t.Context(), a, "700"))
		if r.err == nil || responseCode(r.err) != 408 {
			t.Fatalf("self failover = %v, want 408", r.err)
		}
		pbx.nextCDR(t)
	})
	t.Run("extension already rung", func(t *testing.T) {
		// 200 forwards always to group 700, whose failure is extension
		// 200 again: the forward chain already rang it.
		pbx := startPBX(t, threeDevices(), shortRing(200*time.Millisecond),
			withFeatures(func(s *snapshot.Snapshot) {
				s.WithExtensionData([]snapshot.Extension{{Number: "200", ForwardAlways: "700"}})
			}),
			withGroups(failoverGroup{name: "700", members: []string{"300"}, kind: "extension", target: "200"}))
		a := newPhone(t, pbx, "a1", "pa")
		b := newPhone(t, pbx, "b1", "pb1")
		c := newPhone(t, pbx, "c1", "pc")
		for _, p := range []*phone{a, b, c} {
			p.register(t)
		}
		c.setCallee(ringForever())
		r := waitCall(t, dial(t.Context(), a, "200"))
		if r.err == nil || responseCode(r.err) != 408 {
			t.Fatalf("failover to a rung extension = %v, want 408", r.err)
		}
		noReq(t, b.invites, 100*time.Millisecond, "INVITE to the extension the chain already rang")
		pbx.nextCDR(t)
	})
	t.Run("depth bound", func(t *testing.T) {
		// A chain of distinct groups without a registered member fails over
		// at once, group after group; the bound cuts it.
		var gs []failoverGroup
		n := groupFailMaxDepth + 4
		for i := 0; i < n; i++ {
			g := failoverGroup{name: fmt.Sprintf("7%02d", i), members: []string{"250"}, kind: "ring_group"}
			if i+1 < n {
				g.target = fmt.Sprintf("7%02d", i+1)
			} else {
				g.kind = "none"
			}
			gs = append(gs, g)
		}
		pbx := startPBX(t, callerDevices(), shortRing(200*time.Millisecond), withGroups(gs...))
		a := newPhone(t, pbx, "a1", "pa")
		a.register(t)
		r := waitCall(t, dial(t.Context(), a, "700"))
		if r.err == nil || responseCode(r.err) != 408 {
			t.Fatalf("long chain = %v, want 408", r.err)
		}
		rec := pbx.nextCDR(t)
		if rec.FinalStatus != 408 {
			t.Fatalf("CDR %d", rec.FinalStatus)
		}
	})
}

// TestIVRGroupFailover fails if a ring group a menu key dials does not fall
// back to its extension or other group for the menu's caller, or if a
// failover loop through the menu's path is not cut.
func TestIVRGroupFailover(t *testing.T) {
	group := func(name, kind, target string) snapshot.RingGroup {
		return snapshot.RingGroup{Name: name, Strategy: "ring-all", RingTimeout: 1, MemberDelay: 1, FailureKind: kind, FailureTarget: target}
	}
	run := func(t *testing.T, groups []snapshot.RingGroup, members [][]snapshot.RingGroupMember) (*testPBX, *phone, *phone, *phone, callResult) {
		pbx, _ := ivrPBX(t, 1, ivrMenu("main", "800", map[string]snapshot.IVRDest{
			"1": {Kind: snapshot.IVRRingGroup, Target: "700"},
		}))
		pbx.snaps.p.Load().WithRingGroups(groups, members)
		a, b, c := registerABC(t, pbx)
		b.setCallee(ringForever())
		r := waitCall(t, dial(t.Context(), a, "800"))
		if r.err != nil {
			t.Fatal(r.err)
		}
		seq := r.dcs.InviteRequest.CSeq().SeqNo + 10
		sendDigits(t, a, r.dcs, "1", &seq)
		return pbx, a, b, c, r
	}
	m200 := []snapshot.RingGroupMember{{ExtensionID: 2, Extension: "200", Position: 1}}
	m300 := []snapshot.RingGroupMember{{ExtensionID: 3, Extension: "300", Position: 1}}
	t.Run("extension", func(t *testing.T) {
		_, _, _, c, r := run(t,
			[]snapshot.RingGroup{group("700", "extension", "300")},
			[][]snapshot.RingGroupMember{m200})
		waitReq(t, c.invites, "INVITE to the failover extension")
		_ = r.dcs.Bye(t.Context())
	})
	t.Run("group", func(t *testing.T) {
		_, _, _, c, r := run(t,
			[]snapshot.RingGroup{group("700", "ring_group", "710"), group("710", "none", "")},
			[][]snapshot.RingGroupMember{m200, m300})
		waitReq(t, c.invites, "INVITE to the failover group's member")
		_ = r.dcs.Bye(t.Context())
	})
	t.Run("loop", func(t *testing.T) {
		pbx, _, b, _, r := run(t,
			[]snapshot.RingGroup{group("700", "ring_group", "710"), group("710", "ring_group", "700")},
			[][]snapshot.RingGroupMember{m200, m200})
		waitReq(t, b.invites, "INVITE to the member")
		eventually(t, "the loop is cut", func() bool {
			return pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "loop"}) == 1
		})
		_ = r.dcs.Bye(t.Context())
	})
}
