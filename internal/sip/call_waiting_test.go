package sip

import (
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// cwPBX starts a PBX with the four test phones registered (a1=100, b1=200,
// c1=300, d1=210) and the given extension rows and ring groups.
func cwPBX(t *testing.T, groups bool, rows ...snapshot.Extension) (*testPBX, map[string]*phone) {
	t.Helper()
	feats := func(s *snapshot.Snapshot) {
		s.WithExtensionData(rows)
		if groups {
			g := snapshot.RingGroup{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 30}
			s.WithRingGroups([]snapshot.RingGroup{g}, [][]snapshot.RingGroupMember{
				{{ExtensionID: 2, Extension: "200", Position: 1, Weight: 1}, {ExtensionID: 3, Extension: "300", Position: 2, Weight: 1}},
			})
		}
	}
	pbx := startPBX(t, threeDevices(), shortRing(400*time.Millisecond), withFeatures(feats))
	ph := map[string]*phone{}
	for name, pw := range map[string]string{"a1": "pa", "b1": "pb1", "c1": "pc", "d1": "pd"} {
		ph[name] = newPhone(t, pbx, name, pw)
		ph[name].register(t)
	}
	return pbx, ph
}

// markBusy publishes a connected live call to ext, as another node would.
func markBusy(t *testing.T, pbx *testPBX, id, ext string) {
	t.Helper()
	if err := pbx.fake.PutCall(t.Context(), livestate.Call{ID: id, From: "999", To: ext, State: "connected"}, time.Minute); err != nil {
		t.Fatal(err)
	}
}

// TestCallWaitingFlag fails if a busy extension with call waiting on is not
// rung, or if one with it off is rung instead of getting busy or its
// forward-busy target (spec S-18).
func TestCallWaitingFlag(t *testing.T) {
	t.Run("on rings the busy extension", func(t *testing.T) {
		pbx, ph := cwPBX(t, false, snapshot.Extension{Number: "200"})
		markBusy(t, pbx, "other", "200")
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call to a busy extension with call waiting on: %v", r.err)
		}
		waitReq(t, ph["b1"].invites, "call-waiting INVITE to 200")
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})

	t.Run("off and busy gives 486", func(t *testing.T) {
		pbx, ph := cwPBX(t, false, snapshot.Extension{Number: "200", NoCallWaiting: true})
		markBusy(t, pbx, "other", "200")
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if code := responseCode(r.err); code != 486 {
			t.Fatalf("call to a busy extension with call waiting off = %v, want 486", r.err)
		}
		cdr := pbx.nextCDR(t)
		wantTrace(t, cdr.Trace, "Extension 200 is busy and has call waiting off: busy")
		noReq(t, ph["b1"].invites, 200*time.Millisecond, "INVITE to a busy extension with call waiting off")
	})

	t.Run("off and busy follows forward-busy", func(t *testing.T) {
		pbx, ph := cwPBX(t, false, snapshot.Extension{Number: "200", NoCallWaiting: true, ForwardBusy: "300"})
		markBusy(t, pbx, "other", "200")
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("forward-busy call: %v", r.err)
		}
		waitReq(t, ph["c1"].invites, "forward-busy INVITE to 300")
		noReq(t, ph["b1"].invites, 200*time.Millisecond, "INVITE to a busy extension with call waiting off")
		hangup(t, r.dcs)
		pbx.nextCDR(t)
		if v := pbx.metric(t, "hello_forwarded_calls_total", map[string]string{"kind": "busy"}); v != 1 {
			t.Fatalf("forwarded{busy} = %v, want 1", v)
		}
	})

	t.Run("off but idle rings", func(t *testing.T) {
		pbx, ph := cwPBX(t, false, snapshot.Extension{Number: "200", NoCallWaiting: true})
		r := waitCall(t, dial(t.Context(), ph["a1"], "200"))
		if r.err != nil {
			t.Fatalf("call to an idle extension with call waiting off: %v", r.err)
		}
		waitReq(t, ph["b1"].invites, "INVITE to the idle extension")
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})

	t.Run("a ring group skips a busy member with call waiting off", func(t *testing.T) {
		pbx, ph := cwPBX(t, true, snapshot.Extension{Number: "200", NoCallWaiting: true}, snapshot.Extension{Number: "300"})
		markBusy(t, pbx, "other", "200")
		markBusy(t, pbx, "other2", "300") // busy, but call waiting on: still rung
		r := waitCall(t, dial(t.Context(), ph["a1"], "700"))
		if r.err != nil {
			t.Fatalf("group call: %v", r.err)
		}
		waitReq(t, ph["c1"].invites, "group INVITE to the call-waiting member")
		noReq(t, ph["b1"].invites, 200*time.Millisecond, "group INVITE to the busy member with call waiting off")
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	})
}

// TestCallWaitingBusyRelease fails if an extension stays busy after the call
// that made it busy ended (BYE from either side), or if the extension an
// answered ring group call landed on is not counted busy.
func TestCallWaitingBusyRelease(t *testing.T) {
	pbx, ph := cwPBX(t, true, snapshot.Extension{Number: "200", NoCallWaiting: true}, snapshot.Extension{Number: "300", NoCallWaiting: true})
	a, b, c, d := ph["a1"], ph["b1"], ph["c1"], ph["d1"]

	// a calls c and talks: 300 is busy, so d's call to it is refused.
	first := waitCall(t, dial(t.Context(), a, "300"))
	if first.err != nil {
		t.Fatalf("first call: %v", first.err)
	}
	waitReq(t, c.invites, "INVITE to 300")
	second := waitCall(t, dial(t.Context(), d, "300"))
	if code := responseCode(second.err); code != 486 {
		t.Fatalf("call to 300 during a call = %v, want 486", second.err)
	}
	pbx.nextCDR(t)
	noReq(t, c.invites, 200*time.Millisecond, "INVITE to the busy extension")

	// The caller hangs up: the count is released, 300 rings again.
	hangup(t, first.dcs)
	pbx.nextCDR(t)
	eventually(t, "300 idle again", func() bool { return !pbx.srv.busyExtensions()["300"] })
	third := waitCall(t, dial(t.Context(), d, "300"))
	if third.err != nil {
		t.Fatalf("call to 300 after the first ended: %v", third.err)
	}
	waitReq(t, c.invites, "INVITE to 300 after release")
	hangup(t, third.dcs)
	pbx.nextCDR(t)
	eventually(t, "300 idle after the second call", func() bool { return !pbx.srv.busyExtensions()["300"] })

	c.setCallee(ringForever()) // member 200 answers the group call, 300 keeps ringing
	// A group call answered by member 200 makes 200 busy, not just the group.
	grp := waitCall(t, dial(t.Context(), a, "700"))
	if grp.err != nil {
		t.Fatalf("group call: %v", grp.err)
	}
	waitReq(t, b.invites, "group INVITE to 200")
	eventually(t, "200 busy through the group call", func() bool { return pbx.srv.busyExtensions()["200"] })
	hangup(t, grp.dcs)
	pbx.nextCDR(t)
	eventually(t, "200 idle after the group call", func() bool { return !pbx.srv.busyExtensions()["200"] })
}

// TestCallWaitingFeatureCodes fails if the call waiting codes do not queue
// the flag change for the dialling extension.
func TestCallWaitingFeatureCodes(t *testing.T) {
	settings := &fakeSettings{}
	feats := func(s *snapshot.Snapshot) {
		s.WithFeatureCodes([]snapshot.FeatureCode{
			{Code: "*70", Action: ActionCallWaitingOff},
			{Code: "*71", Action: ActionCallWaitingOn},
		})
	}
	pbx := startPBX(t, callerDevices(), withFeatures(feats), func(_ *Config, d *Deps) { d.Settings = settings })
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	for _, tc := range []struct {
		code string
		want bool
	}{{"*70", false}, {"*71", true}} {
		r := waitCall(t, dial(t.Context(), a, tc.code))
		if r.err != nil {
			t.Fatalf("%s: %v", tc.code, r.err)
		}
		hangup(t, r.dcs)
		pbx.nextCDR(t)
	}
	ups := settings.got()
	if len(ups) != 2 {
		t.Fatalf("updates = %+v, want 2", ups)
	}
	for i, want := range []bool{false, true} {
		u := ups[i]
		if u.Extension != "100" || u.CallWaiting == nil || *u.CallWaiting != want || u.DND != nil {
			t.Errorf("update %d = %+v, want call waiting %v for 100 only", i, u, want)
		}
	}
}
