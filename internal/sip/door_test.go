package sip

import (
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
)

const doorbellAlert = "<http://127.0.0.1/Bellcore-dr4>;info=hello-doorbell"

// doorFeatures installs a door station (900) that may call extensions 200 and
// 300, a Door ring group (800) and an ordinary group (810) the station does
// not list. Extension 300 forwards every call to an outside number.
func doorFeatures() pbxOpt {
	return withFeatures(func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{
			{Number: "100", Name: "Ext 100"},
			{Number: "200", Name: "Ext 200"},
			{Number: "300", Name: "Ext 300", ForwardAlways: "0501234567"},
			{Number: "900", Name: "Front door", Door: true, DoorExtensions: []string{"200", "300"}},
		})
		m := []snapshot.RingGroupMember{{ExtensionID: 2, Extension: "200", Position: 1, Weight: 1}}
		s.WithRingGroups([]snapshot.RingGroup{
			{ID: 1, Name: "800", Strategy: "ring-all", RingTimeout: 30, IsDoor: true, RingTag: "doorbell"},
			{ID: 2, Name: "810", Strategy: "ring-all", RingTimeout: 30},
		}, [][]snapshot.RingGroupMember{m, m})
	})
}

func doorDevices() []snapshot.Device {
	return []snapshot.Device{
		dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb1"), dev(3, "c1", "300", "pc"), dev(4, "d1", "900", "pd"),
	}
}

// TestDoorExtension fails if a door extension can call outside its targets or
// a trunk, can receive or send MESSAGE, or if its call, or a call to the Door
// group, lacks the doorbell Alert-Info, the ring tag doorbell or the CDR kind
// door (spec S-45 and S-46).
func TestDoorExtension(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200", "300", "900")
	carrierA := newCarrier(t, "acct1", "pw1")
	rt := &fakeRouter{extensions: map[string]bool{"100": true, "200": true, "300": true, "900": true},
		trunks: []routing.Trunk{carrierA.trunk(1, "carrier-primary", "ip")}}
	rt.decide = outboundVia(rt, "+971501234567", "+97140000100", false, nil, 1)
	pbx := startPBX(t, doorDevices(), trunkCfg(newFakeTrunkState()), withRouter(rt, nil), doorFeatures(), withMessages(q))
	user := newPhone(t, pbx, "a1", "pa")
	user.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	door := newPhone(t, pbx, "d1", "pd")
	door.register(t)

	noCarrierInvite := func(t *testing.T) {
		t.Helper()
		select {
		case <-carrierA.invites:
			t.Fatal("the carrier got an INVITE from a door station's call")
		case <-time.After(300 * time.Millisecond):
		}
	}

	for _, tc := range []struct {
		name string
		from *phone
		dial string
	}{
		{"door to a listed extension", door, "200"},
		{"door to the Door group", door, "800"},
		{"user to the Door group", user, "800"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, cd := ringCall(t, pbx, tc.from, b, tc.dial)
			if got := alertInfos(inv); len(got) != 1 || got[0] != doorbellAlert {
				t.Errorf("Alert-Info = %q, want %q", got, doorbellAlert)
			}
			if cd.Kind != cdr.KindDoor || cd.RingTag != "doorbell" || cd.RingRule != "kind:door" {
				t.Errorf("CDR kind %q ring tag %q (%q), want door doorbell (kind:door)", cd.Kind, cd.RingTag, cd.RingRule)
			}
		})
	}

	for _, dialled := range []string{"100", "810", "0501234567", "*97", "900", "9999"} {
		t.Run("door refused "+dialled, func(t *testing.T) {
			r := waitCall(t, dial(t.Context(), door, dialled))
			if got := responseCode(r.err); got != 403 {
				t.Fatalf("door station call to %s = %d (%v), want 403", dialled, got, r.err)
			}
			cd := pbx.nextCDR(t)
			if cd.Kind != cdr.KindDoor || cd.FinalStatus != 403 {
				t.Errorf("CDR kind %q status %d, want door 403", cd.Kind, cd.FinalStatus)
			}
			wantTrace(t, cd.Trace, "Door station 900 may not call "+dialled)
			noCarrierInvite(t)
		})
	}

	t.Run("forwarding never leaves on a trunk", func(t *testing.T) {
		r := waitCall(t, dial(t.Context(), door, "300"))
		if r.err == nil {
			hangup(t, r.dcs)
			t.Fatal("a door station's call was forwarded out on a trunk and answered")
		}
		pbx.nextCDR(t)
		noCarrierInvite(t)
	})

	t.Run("no MESSAGE", func(t *testing.T) {
		if res := user.sendMessage("900", "text/plain", "hi"); res.StatusCode != 403 {
			t.Errorf("MESSAGE to a door station = %d, want 403", res.StatusCode)
		}
		if res := door.sendMessage("200", "text/plain", "hi"); res.StatusCode != 403 {
			t.Errorf("MESSAGE from a door station = %d, want 403", res.StatusCode)
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		if len(q.sends) != 0 {
			t.Errorf("a MESSAGE to or from a door station was queued: %d sends", len(q.sends))
		}
	})
}
