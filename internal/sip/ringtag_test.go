package sip

import (
	"net/netip"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/ringtag"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func tagDevices() []snapshot.Device {
	return append(callerDevices(), dev(3, "c1", "300", "pc"))
}

// withRingTags installs a ring tag configuration and two groups: 700 keeps
// the ringgroup tag, 710 overrides it with "vip". Both ring extension 200.
func withRingTags(d snapshot.RingTagData) pbxOpt {
	return withFeatures(func(s *snapshot.Snapshot) {
		g700 := snapshot.RingGroup{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 30}
		g710 := snapshot.RingGroup{ID: 2, Name: "710", Strategy: "ring-all", RingTimeout: 30, RingTag: "vip"}
		m := func() []snapshot.RingGroupMember {
			return []snapshot.RingGroupMember{{ExtensionID: 2, Extension: "200", Position: 1, Weight: 1}}
		}
		s.WithRingGroups([]snapshot.RingGroup{g700, g710}, [][]snapshot.RingGroupMember{m(), m()})
		s.WithRingTags(d)
	})
}

func testRingTags() snapshot.RingTagData {
	return snapshot.RingTagData{
		Tags: []ringtag.Tag{
			{Name: "vip", BellcoreSlot: 4}, {Name: "late-shift", BellcoreSlot: 2},
			{Name: "carrier", BellcoreSlot: 5},
		},
		Rules: []ringtag.Rule{
			{ID: 1, Position: 1, Name: "night desk", Match: ringtag.Match{CallerPattern: "300"}, Tag: "late-shift"},
			{ID: 2, Position: 2, Name: "main carrier", Match: ringtag.Match{Trunk: "carrier-in"}, Tag: "carrier"},
		},
	}
}

// ringCall places a call from p to ext, waits for the callee's INVITE and CDR,
// and returns both.
func ringCall(t *testing.T, pbx *testPBX, from, to *phone, ext string) (*sip.Request, cdr.Record) {
	t.Helper()
	r := waitCall(t, dial(t.Context(), from, ext))
	if r.err != nil {
		t.Fatalf("call %s: %v", ext, r.err)
	}
	inv := waitReq(t, to.invites, "INVITE to "+ext)
	hangup(t, r.dcs)
	return inv, pbx.nextCDR(t)
}

func alertInfos(req *sip.Request) []string {
	var out []string
	for _, h := range req.GetHeaders("Alert-Info") {
		out = append(out, h.Value())
	}
	return out
}

// TestRingTagChoice fails if the order of S-40 is broken (a rule beats the
// ring group's tag, a group's override beats ringgroup, a trunk call is not
// external, an internal call is not internal), if a ring group's own tag is
// ignored, or if the chosen tag and what chose it are missing from the CDR
// and the trace. The choice reads the snapshot only: the test has no database.
func TestRingTagChoice(t *testing.T) {
	r := trunkTagRouter()
	pbx := startPBX(t, tagDevices(), trunkCfgForTags(), withRouter(r, nil), withRingTags(testRingTags()))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	c := newPhone(t, pbx, "c1", "pc")
	c.register(t)

	for _, tc := range []struct {
		name      string
		from      *phone
		ext       string
		tag, why  string
		alertInfo string
	}{
		{"internal default", a, "200", "internal", "default", "<http://127.0.0.1/Bellcore-dr1>;info=hello-internal"},
		{"group without override", a, "700", "ringgroup", "group", "<http://127.0.0.1/Bellcore-dr3>;info=hello-ringgroup"},
		{"group override", a, "710", "vip", "group", "<http://127.0.0.1/Bellcore-dr4>;info=hello-vip"},
		{"rule beats internal", c, "200", "late-shift", "rule:1 night desk", "<http://127.0.0.1/Bellcore-dr2>;info=hello-late-shift"},
		{"rule beats group override", c, "710", "late-shift", "rule:1 night desk", "<http://127.0.0.1/Bellcore-dr2>;info=hello-late-shift"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inv, cd := ringCall(t, pbx, tc.from, b, tc.ext)
			if got := alertInfos(inv); len(got) != 1 || got[0] != tc.alertInfo {
				t.Errorf("Alert-Info = %q, want %q", got, tc.alertInfo)
			}
			if cd.RingTag != tc.tag || cd.RingRule != tc.why {
				t.Errorf("CDR ring tag = %q (%q), want %q (%q)", cd.RingTag, cd.RingRule, tc.tag, tc.why)
			}
			wantTrace(t, cd.Trace, "Ring tag "+tc.tag+" ("+tc.why+")")
		})
	}
	if v := pbx.metric(t, "hello_ring_tag_total", map[string]string{"tag": "vip"}); v != 1 {
		t.Errorf("hello_ring_tag_total{vip} = %v, want 1", v)
	}

	// A call that entered on a trunk: the trunk rule wins over external.
	in, cd := trunkRingCall(t, pbx, r, b)
	if got := alertInfos(in); len(got) != 1 || got[0] != "<http://127.0.0.1/Bellcore-dr5>;info=hello-carrier" {
		t.Errorf("trunk call Alert-Info = %q", got)
	}
	if cd.Direction != "inbound" || cd.RingTag != "carrier" || cd.RingRule != "rule:2 main carrier" {
		t.Fatalf("trunk CDR = %+v", cd)
	}
}

// TestRingTagExternal fails if a call that entered on a trunk, and matched no
// rule, is not external with the external slot's Alert-Info.
func TestRingTagExternal(t *testing.T) {
	r := trunkTagRouter()
	pbx := startPBX(t, tagDevices(), trunkCfgForTags(), withRouter(r, nil),
		withRingTags(snapshot.RingTagData{Tags: testRingTags().Tags}))
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	in, cd := trunkRingCall(t, pbx, r, b)
	if got := alertInfos(in); len(got) != 1 || got[0] != "<http://127.0.0.1/Bellcore-dr2>;info=hello-external" {
		t.Errorf("trunk call Alert-Info = %q", got)
	}
	if cd.RingTag != "external" || cd.RingRule != "trunk" {
		t.Fatalf("trunk CDR = %+v", cd)
	}
	wantTrace(t, cd.Trace, "Ring tag external (trunk)")
}

func trunkTagRouter() *fakeRouter {
	r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true}, sources: map[netip.Addr]int64{},
		trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true}}}
	r.decide = func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
		d := routing.Decision{Kind: routing.KindInbound, Extension: "200", Route: "Main DID", CallerID: "+971555000111"}
		d.Trace.Add(`Inbound route "Main DID" matched (exact ` + c.Number + `)`)
		return d
	}
	return r
}

// trunkRingCall places a call from the trunk to extension 200 (b) and returns
// the INVITE b got and the call's CDR.
func trunkRingCall(t *testing.T, pbx *testPBX, r *fakeRouter, b *phone) (*sip.Request, cdr.Record) {
	t.Helper()
	carrier := newPhone(t, pbx, "carrier", "")
	ap, _ := netip.ParseAddrPort(carrier.addr)
	r.setSource(ap.Addr(), 7)
	dcs, err := carrier.dua.WriteInvite(t.Context(), carrierInvite(carrier, pbx, "+97140000200"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		t.Fatalf("INVITE from the trunk: %v", err)
	}
	_ = dcs.Ack(t.Context())
	in := waitReq(t, b.invites, "INVITE from the trunk")
	hangup(t, dcs)
	return in, pbx.nextCDR(t)
}

func trunkCfgForTags() pbxOpt { return trunkCfg(newFakeTrunkState()) }

// TestAlertInfoEmission fails if a received Alert-Info reaches a device, if a
// device leg lacks the canonical header or the vendor form, if a trunk leg
// carries any Alert-Info, or if the Bellcore slot differs from the tag's. The
// page and intercom auto-answer header keeping its place is covered by
// TestAutoAnswer*.
func TestAlertInfoEmission(t *testing.T) {
	devs := []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "snom200", "200", "pb"), dev(3, "b1", "300", "pc")}
	carrierA, carrierB := newCarrier(t, "acct1", "pw1"), newCarrier(t, "acct2", "pw2")
	rt := &fakeRouter{extensions: map[string]bool{"100": true, "200": true, "300": true},
		trunks: []routing.Trunk{carrierA.trunk(1, "carrier-primary", "ip"), carrierB.trunk(2, "carrier-backup", "ip")}}
	rt.decide = outboundVia(rt, "+971501234567", "+97140000100", false, nil, 1)
	st := newFakeTrunkState()
	pbx := startPBX(t, devs, trunkCfg(st), withRouter(rt, nil), withRingTags(snapshot.RingTagData{
		Tags: []ringtag.Tag{{Name: "vip", BellcoreSlot: 4}},
	}))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	snom := newPhone(t, pbx, "snom200", "pb")
	reg := snom.registerReq(300)
	reg.AppendHeader(sip.NewHeader("User-Agent", "snom D785-10.1.150"))
	if res := snom.authDo(reg, AlgSHA256, snom.pass); res.StatusCode != 200 {
		t.Fatalf("snom register = %d", res.StatusCode)
	}
	plain := newPhone(t, pbx, "b1", "pc")
	plain.register(t)

	// A caller's own Alert-Info, in any spelling, never reaches the device;
	// the device gets exactly one header, the canonical one.
	req := a.inviteReq("300")
	req.AppendHeader(sip.NewHeader("Alert-Info", "<http://evil.example/x.wav>;info=alert-autoanswer"))
	req.AppendHeader(sip.NewHeader("Alert-Info", "<sip:evil>;info=hello-alarm"))
	dcs, err := a.dua.WriteInvite(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{Username: a.user, Password: a.pass}); err != nil {
		t.Fatalf("call: %v", err)
	}
	_ = dcs.Ack(t.Context())
	got := alertInfos(waitReq(t, plain.invites, "INVITE to 300"))
	if len(got) != 1 || got[0] != "<http://127.0.0.1/Bellcore-dr1>;info=hello-internal" {
		t.Fatalf("Alert-Info reaching the device = %q", got)
	}
	hangup(t, dcs)
	pbx.nextCDR(t)

	// A Snom phone gets its own form for the three built-in classes (S-41).
	inv, _ := ringCall(t, pbx, a, snom, "200")
	if got := alertInfos(inv); len(got) != 1 || got[0] != "<http://127.0.0.1/Bellcore-dr1>;info=alert-internal" {
		t.Fatalf("Snom Alert-Info = %q", got)
	}

	// A trunk leg carries no Alert-Info, whatever the caller sent.
	req = a.inviteReq("0501234567")
	req.AppendHeader(sip.NewHeader("Alert-Info", "<sip:evil>;info=hello-alarm"))
	dcs, err = a.dua.WriteInvite(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{Username: a.user, Password: a.pass}); err != nil {
		t.Fatalf("outbound call: %v", err)
	}
	_ = dcs.Ack(t.Context())
	out := waitReq(t, carrierA.invites, "INVITE to the carrier")
	if h := alertInfos(out); len(h) != 0 {
		t.Fatalf("trunk leg carries Alert-Info %q", h)
	}
	hangup(t, dcs)
	cd := pbx.nextCDR(t)
	if cd.RingTag != "" {
		t.Errorf("a call that rang no internal device has ring tag %q", cd.RingTag)
	}
	if strings.Contains(traceText(cd.Trace), "Ring tag") {
		t.Errorf("trunk call trace names a ring tag:\n%s", traceText(cd.Trace))
	}
}
