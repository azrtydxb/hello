package sip

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/cdr"
	"github.com/azrtydxb/hello/internal/location"
	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func fp(v float64) *float64 { return &v }

// emergencyPlace is the stored location of device a1 in these tests.
var emergencyPlace = location.Place{
	Path: "HQ / 3", Country: "NL", City: "Amsterdam", Street: "Damrak", HouseNumber: "1", PostalCode: "1012LG", Floor: "3",
	Latitude: fp(52.3), Longitude: fp(4.9),
}

// speakerLog records what the PBX asked to be spoken and refuses, so the
// callers fall back to their tone.
type speakerLog struct {
	mu    sync.Mutex
	texts []string
}

func (s *speakerLog) Speak(_ context.Context, text string) ([]byte, error) {
	s.mu.Lock()
	s.texts = append(s.texts, text)
	s.mu.Unlock()
	return nil, errors.New("no voice in the test")
}

func (s *speakerLog) said(sub string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.texts {
		if strings.Contains(t, sub) {
			return true
		}
	}
	return false
}

// emergencySetup is a PBX with phones a1 (100, at emergencyPlace, DND on),
// b1 (200, DND on) and c1 (300); two carriers, the first flagged
// geolocation; 112 is an emergency route over the first, 113 over the second;
// a call from a trunk goes to extension 100.
type emergencySetup struct {
	pbx     *testPBX
	flagged *carrier
	plain   *carrier
	rt      *fakeRouter
	a, b, c *phone
	speaker *speakerLog
}

func newEmergencySetup(t *testing.T, e snapshot.EmergencyData, groups bool) *emergencySetup {
	t.Helper()
	flagged, plain := newCarrier(t, "acct1", "pw1"), newCarrier(t, "acct2", "pw2")
	t1 := flagged.trunk(1, "carrier-geo", "ip")
	t1.Geolocation = true
	t2 := plain.trunk(2, "carrier-plain", "ip")
	rt := &fakeRouter{extensions: map[string]bool{"100": true, "200": true, "300": true}, sources: map[netip.Addr]int64{},
		trunks: []routing.Trunk{t1, t2, {ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true}}}
	rt.decide = func(c routing.Call, usable routing.TrunkUsability) routing.Decision {
		if c.FromTrunk != 0 {
			d := routing.Decision{Kind: routing.KindInbound, Extension: "100", Route: "Main DID", CallerID: "+971555000111"}
			d.Trace.Add(`Inbound route "Main DID" matched (exact ` + c.Number + `)`)
			return d
		}
		if c.Number == "113" {
			return outboundVia(rt, "113", "+97140000100", true, nil, 2)(c, usable)
		}
		return outboundVia(rt, "112", "+97140000100", true, nil, 1)(c, usable)
	}
	sp := &speakerLog{}
	devs := []snapshot.Device{dev(1, "a1", "100", "pa"), dev(2, "b1", "200", "pb"), dev(3, "c1", "300", "pc")}
	pbx := startPBX(t, devs, trunkCfg(newFakeTrunkState()), withRouter(rt, nil), withSpeech(sp), withFeatures(func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{
			{Number: "100", Name: "Ext 100", DND: true}, {Number: "200", Name: "Ext 200", DND: true}, {Number: "300", Name: "Ext 300"}})
		if groups {
			s.WithRingGroups([]snapshot.RingGroup{{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 30}},
				[][]snapshot.RingGroupMember{{{ExtensionID: 2, Extension: "200", Position: 1, Weight: 1}}})
			s.WithRingGroups([]snapshot.RingGroup{{ID: 1, Name: "700", Strategy: "ring-all", RingTimeout: 30}, {ID: 2, Name: "900", Strategy: "ring-all", RingTimeout: 30}},
				[][]snapshot.RingGroupMember{{{ExtensionID: 2, Extension: "200", Position: 1, Weight: 1}}, {{ExtensionID: 3, Extension: "300", Position: 1, Weight: 1}}})
		}
		s.WithEmergency(e)
	}))
	es := &emergencySetup{pbx: pbx, flagged: flagged, plain: plain, rt: rt, speaker: sp}
	es.a = newPhone(t, pbx, "a1", "pa")
	es.a.register(t)
	es.b = newPhone(t, pbx, "b1", "pb")
	es.b.register(t)
	es.c = newPhone(t, pbx, "c1", "pc")
	es.c.register(t)
	return es
}

func emergencyData() snapshot.EmergencyData {
	return snapshot.EmergencyData{
		Alarm:  map[string]string{"a1": "200", "c1": ""},
		Places: map[string]location.Place{"a1": emergencyPlace},
	}
}

// fromTrunk places a call from the carrier to the DID, which routes to 100.
func (es *emergencySetup) fromTrunk(t *testing.T) callResult {
	t.Helper()
	carrier := newPhone(t, es.pbx, "carrier", "")
	ap, _ := netip.ParseAddrPort(carrier.addr)
	es.rt.setSource(ap.Addr(), 7)
	defer func() {
		es.rt.mu.Lock()
		delete(es.rt.sources, ap.Addr())
		es.rt.mu.Unlock()
	}()
	dcs, err := carrier.dua.WriteInvite(t.Context(), carrierInvite(carrier, es.pbx, "+97140000100"))
	if err != nil {
		t.Fatal(err)
	}
	if err := dcs.WaitAnswer(t.Context(), sipgo.AnswerOptions{}); err != nil {
		return callResult{err: err}
	}
	_ = dcs.Ack(t.Context())
	return callResult{dcs: dcs}
}

func hasHeader(req *sip.Request, name string) bool { return req.GetHeader(name) != nil }

// TestEmergencyCalling covers what an emergency call does beyond ordinary
// outbound routing (spec S-28). It fails if a trunk flagged geolocation does
// not get the stored PIDF-LO and a Geolocation header for an emergency call,
// if an unflagged trunk gets either, if a location the phone sent is dropped
// (or replaced by the stored one), if a call that is not an emergency call
// carries a location, if a callback within the window does not ring through
// DND with the emergency-callback tag, if a callback without an emergency call
// is not declined by DND, or if the notify group's phones are not paged with
// the caller's identity.
func TestEmergencyCalling(t *testing.T) {
	t.Run("stored location on a flagged trunk", func(t *testing.T) {
		es := newEmergencySetup(t, emergencyData(), false)
		r := waitCall(t, dial(t.Context(), es.a, "112"))
		if r.err != nil {
			t.Fatalf("emergency call: %v", r.err)
		}
		inv := waitReq(t, es.flagged.invites, "INVITE to the flagged trunk")
		geo := inv.GetHeader("Geolocation")
		if geo == nil || !strings.HasPrefix(geo.Value(), "<cid:") {
			t.Fatalf("Geolocation = %v", geo)
		}
		ct := inv.ContentType()
		body := string(inv.Body())
		if ct == nil || !strings.HasPrefix(ct.Value(), "multipart/mixed;boundary=") {
			t.Fatalf("Content-Type = %v", ct)
		}
		cid := strings.TrimSuffix(strings.TrimPrefix(geo.Value(), "<cid:"), ">")
		for _, want := range []string{"Content-Type: application/sdp", "Content-Type: application/pidf+xml", "Content-ID: <" + cid + ">",
			"<ca:A3>Amsterdam</ca:A3>", "<ca:A6>Damrak</ca:A6>", "<ca:FLR>3</ca:FLR>", "52.3 4.9", `entity="pres:100@` + testDomain + `"`} {
			if !strings.Contains(body, want) {
				t.Errorf("body lacks %q:\n%s", want, body)
			}
		}
		hangup(t, r.dcs)
		cd := es.pbx.nextCDR(t)
		wantTrace(t, cd.Trace, "Emergency call to trunk carrier-geo: sent Geolocation (the stored location HQ / 3)")
	})

	t.Run("an unflagged trunk gets no location", func(t *testing.T) {
		es := newEmergencySetup(t, emergencyData(), false)
		r := waitCall(t, dial(t.Context(), es.a, "113"))
		if r.err != nil {
			t.Fatalf("emergency call: %v", r.err)
		}
		inv := waitReq(t, es.plain.invites, "INVITE to the plain trunk")
		if hasHeader(inv, "Geolocation") || strings.Contains(string(inv.Body()), "pidf") {
			t.Fatalf("an unflagged trunk got a location: %v\n%s", inv.GetHeader("Geolocation"), inv.Body())
		}
		anchoredSDP(t, es.pbx, inv.Body())
		hangup(t, r.dcs)
	})

	t.Run("a call that is not an emergency call carries no location", func(t *testing.T) {
		es := newEmergencySetup(t, emergencyData(), false)
		es.rt.setDecide(outboundVia(es.rt, "+971501234567", "+97140000100", false, nil, 1))
		r := waitCall(t, dial(t.Context(), es.a, "0501234567"))
		if r.err != nil {
			t.Fatalf("call: %v", r.err)
		}
		inv := waitReq(t, es.flagged.invites, "INVITE to the flagged trunk")
		if hasHeader(inv, "Geolocation") || strings.Contains(string(inv.Body()), "pidf") {
			t.Fatalf("an ordinary call carried a location: %v", inv.GetHeader("Geolocation"))
		}
		hangup(t, r.dcs)
	})

	t.Run("the phone's own location is forwarded", func(t *testing.T) {
		es := newEmergencySetup(t, emergencyData(), false)
		const pidf = `<?xml version="1.0"?><presence xmlns="urn:ietf:params:xml:ns:pidf" xmlns:ca="urn:ietf:params:xml:ns:pidf:geopriv10:civicAddr" entity="pres:a"><tuple id="x"><status><geopriv xmlns="urn:ietf:params:xml:ns:pidf:geopriv10"><location-info><ca:civicAddress><ca:country>NL</ca:country><ca:A3>PhoneTown</ca:A3></ca:civicAddress></location-info></geopriv></status></tuple></presence>`
		req := es.a.inviteReq("112")
		req.RemoveHeader("Content-Type")
		req.AppendHeader(sip.NewHeader("Content-Type", "multipart/mixed;boundary=phoneb"))
		req.AppendHeader(sip.NewHeader("Geolocation", "<cid:phone-loc@a1>, <https://lis.example/loc/1>"))
		req.AppendHeader(sip.NewHeader("P-Emergency-Info", "emerg-1"))
		req.SetBody([]byte("--phoneb\r\nContent-Type: application/sdp\r\n\r\n" + es.a.sdp +
			"\r\n--phoneb\r\nContent-Type: application/pidf+xml\r\nContent-ID: <phone-loc@a1>\r\n\r\n" + pidf + "\r\n--phoneb--\r\n"))
		dcs, err := es.a.callReq(t.Context(), req)
		if err != nil {
			t.Fatalf("emergency call with a location: %v", err)
		}
		inv := waitReq(t, es.flagged.invites, "INVITE to the flagged trunk")
		body := string(inv.Body())
		if !strings.Contains(body, "PhoneTown") || strings.Contains(body, "Amsterdam") {
			t.Fatalf("the phone's location was not forwarded in place of the stored one:\n%s", body)
		}
		geo := inv.GetHeader("Geolocation").Value()
		if !strings.HasPrefix(geo, "<cid:") || strings.Contains(geo, "phone-loc@a1") || !strings.Contains(geo, "<https://lis.example/loc/1>") {
			t.Fatalf("Geolocation = %q: want our cid and the phone's reference", geo)
		}
		if got := inv.GetHeader("P-Emergency-Info"); got == nil || got.Value() != "emerg-1" {
			t.Fatalf("P-Emergency-Info = %v", got)
		}
		anchoredSDP(t, es.pbx, []byte(sdpPartOf(t, body)))
		hangup(t, dcs)
		cd := es.pbx.nextCDR(t)
		wantTrace(t, cd.Trace, "Emergency call to trunk carrier-geo: sent Geolocation (the phone's own location)")
	})

	t.Run("a malformed location from the phone is not forwarded", func(t *testing.T) {
		es := newEmergencySetup(t, emergencyData(), false)
		req := es.a.inviteReq("112")
		req.RemoveHeader("Content-Type")
		req.AppendHeader(sip.NewHeader("Content-Type", "multipart/mixed;boundary=phoneb"))
		req.AppendHeader(sip.NewHeader("Geolocation", "<cid:phone-loc@a1>"))
		req.SetBody([]byte("--phoneb\r\nContent-Type: application/sdp\r\n\r\n" + es.a.sdp +
			"\r\n--phoneb\r\nContent-Type: application/pidf+xml\r\nContent-ID: <phone-loc@a1>\r\n\r\n" +
			`<!DOCTYPE x [<!ENTITY a "b">]><presence xmlns="urn:ietf:params:xml:ns:pidf">&a;PhoneTown</presence>` + "\r\n--phoneb--\r\n"))
		dcs, err := es.a.callReq(t.Context(), req)
		if err != nil {
			t.Fatalf("emergency call: %v", err)
		}
		body := string(waitReq(t, es.flagged.invites, "INVITE to the flagged trunk").Body())
		if strings.Contains(body, "PhoneTown") || strings.Contains(body, "DOCTYPE") || !strings.Contains(body, "Amsterdam") {
			t.Fatalf("a malformed location was forwarded, or the stored one not used:\n%s", body)
		}
		hangup(t, dcs)
	})

	t.Run("a callback within the window ignores DND", func(t *testing.T) {
		es := newEmergencySetup(t, emergencyData(), false)
		// Control: with no emergency call, DND declines the call from the trunk.
		if r := es.fromTrunk(t); r.err == nil || responseCode(r.err) != 603 {
			t.Fatalf("a call to a DND extension = %v, want 603", r.err)
		}
		noReq(t, es.a.invites, 200*time.Millisecond, "INVITE to a DND extension")
		es.pbx.nextCDR(t)

		r := waitCall(t, dial(t.Context(), es.a, "112"))
		if r.err != nil {
			t.Fatalf("emergency call: %v", r.err)
		}
		hangup(t, r.dcs)
		es.pbx.nextCDR(t)

		back := es.fromTrunk(t)
		if back.err != nil {
			t.Fatalf("the callback was refused: %v", back.err)
		}
		inv := waitReq(t, es.a.invites, "INVITE of the callback")
		if got := alertInfos(inv); len(got) != 1 || got[0] != "<http://127.0.0.1/Bellcore-dr1>;info=hello-emergency-callback" {
			t.Errorf("Alert-Info = %q", got)
		}
		hangup(t, back.dcs)
		cd := es.pbx.nextCDR(t)
		if cd.RingTag != "emergency-callback" || cd.RingRule != "kind:emergency-callback" || cd.Kind != "" && cd.Kind != cdr.KindCall {
			t.Fatalf("CDR ring tag %q (%q) kind %q", cd.RingTag, cd.RingRule, cd.Kind)
		}
		wantTrace(t, cd.Trace, "Callback to extension 100, which placed an emergency call within 30 minutes: ring tag emergency-callback, DND ignored")
	})

	t.Run("the notify group is paged", func(t *testing.T) {
		d := emergencyData()
		d.NotifyGroup = "900"
		es := newEmergencySetup(t, d, true)
		r := waitCall(t, dial(t.Context(), es.a, "112"))
		if r.err != nil {
			t.Fatalf("emergency call: %v", r.err)
		}
		inv := waitReq(t, es.c.invites, "the notice INVITE to the notify group's phone")
		if inv.From().Address.User != "100" || inv.From().DisplayName != "Emergency call" {
			t.Errorf("notice From = %s %q", inv.From().Address.User, inv.From().DisplayName)
		}
		if !hasHeader(inv, "Call-Info") || !hasHeader(inv, "Alert-Info") {
			t.Errorf("the notice lacks the auto-answer headers: %v", inv.Headers())
		}
		eventually(t, "the notice named the extension", func() bool { return es.speaker.said("Emergency call from Ext 100, extension 1, 0, 0") })
		waitReq(t, es.c.byes, "BYE ending the notice")
		noReq(t, es.b.invites, 200*time.Millisecond, "INVITE to a phone outside the notify group")
		hangup(t, r.dcs)
	})
}

// sdpPartOf returns the SDP part of a multipart body.
func sdpPartOf(t *testing.T, body string) string {
	t.Helper()
	_, rest, ok := strings.Cut(body, "Content-Type: application/sdp\r\n\r\n")
	if !ok {
		t.Fatalf("no SDP part in:\n%s", body)
	}
	sdp, _, _ := strings.Cut(rest, "\r\n--")
	return sdp
}

// TestEmergencyMarkWindow fails if a mark outlives the callback window or an
// unmarked extension counts as recent.
func TestEmergencyMarkWindow(t *testing.T) {
	var m emergencyMarks
	now := time.Now()
	if m.recent("100", now) {
		t.Fatal("an unmarked extension is recent")
	}
	m.set("100", now.Add(EmergencyCallbackWindow))
	if !m.recent("100", now.Add(EmergencyCallbackWindow-time.Second)) {
		t.Error("a mark inside the window is not recent")
	}
	if m.recent("100", now.Add(EmergencyCallbackWindow)) {
		t.Error("a mark at the end of the window is still recent")
	}
	if m.recent("101", now) {
		t.Error("another extension is recent")
	}
	if EmergencyCallbackWindow != 30*time.Minute {
		t.Errorf("window = %v, want 30 minutes (spec S-28 f)", EmergencyCallbackWindow)
	}
}

// TestAlarmCall covers *55 (spec S-29). It fails if *55 does not reach the
// alarm destination of the calling device's group through the destination's
// DND, if the ring tag is not alarm, the CDR kind not alarm, or the answering
// party is not told who the alarm is from; if *55 from a phone without a
// destination (or whose destination is gone) is not 404; or if a plain call
// to the same DND extension is not declined.
func TestAlarmCall(t *testing.T) {
	t.Run("an extension in DND", func(t *testing.T) {
		es := newEmergencySetup(t, emergencyData(), false)
		// Control: an ordinary call to the DND extension is declined.
		if r := waitCall(t, dial(t.Context(), es.c, "200")); responseCode(r.err) != 603 {
			t.Fatalf("a plain call to a DND extension = %v, want 603", r.err)
		}
		noReq(t, es.b.invites, 200*time.Millisecond, "INVITE to a DND extension")
		es.pbx.nextCDR(t)

		r := waitCall(t, dial(t.Context(), es.a, AlarmCode))
		if r.err != nil {
			t.Fatalf("alarm call: %v", r.err)
		}
		inv := waitReq(t, es.b.invites, "INVITE of the alarm")
		if got := alertInfos(inv); len(got) != 1 || got[0] != "<http://127.0.0.1/Bellcore-dr5>;info=hello-alarm" {
			t.Errorf("Alert-Info = %q", got)
		}
		if inv.From().Address.User != "100" {
			t.Errorf("alarm From = %s", inv.From().Address.User)
		}
		eventually(t, "the alarm announcement", func() bool { return es.speaker.said("Panic alarm from Ext 100.") })
		hangup(t, r.dcs)
		cd := es.pbx.nextCDR(t)
		if cd.Kind != cdr.KindAlarm || cd.RingTag != "alarm" || cd.RingRule != "kind:alarm" || cd.FinalStatus != 200 || cd.RewrittenDestination != "200" {
			t.Fatalf("CDR = kind %q tag %q (%q) status %d to %q", cd.Kind, cd.RingTag, cd.RingRule, cd.FinalStatus, cd.RewrittenDestination)
		}
		wantTrace(t, cd.Trace, "Alarm call from 100 to 200: DND ignored, ring tag alarm")
	})

	t.Run("a ring group whose member is in DND", func(t *testing.T) {
		d := emergencyData()
		d.Alarm["a1"] = "700"
		es := newEmergencySetup(t, d, true)
		r := waitCall(t, dial(t.Context(), es.a, AlarmCode))
		if r.err != nil {
			t.Fatalf("alarm call to the group: %v", r.err)
		}
		waitReq(t, es.b.invites, "INVITE of the alarm to the group member")
		hangup(t, r.dcs)
		if cd := es.pbx.nextCDR(t); cd.Kind != cdr.KindAlarm || cd.RingTag != "alarm" {
			t.Fatalf("CDR = kind %q tag %q", cd.Kind, cd.RingTag)
		}
	})

	t.Run("no destination", func(t *testing.T) {
		es := newEmergencySetup(t, emergencyData(), false)
		r := waitCall(t, dial(t.Context(), es.c, AlarmCode)) // c1's group has none
		if responseCode(r.err) != 404 {
			t.Fatalf("*55 without a destination = %v, want 404", r.err)
		}
		cd := es.pbx.nextCDR(t)
		if cd.Kind != cdr.KindAlarm || cd.FinalStatus != 404 {
			t.Fatalf("CDR = kind %q status %d", cd.Kind, cd.FinalStatus)
		}
		noReq(t, es.b.invites, 100*time.Millisecond, "INVITE with no destination")
	})

	t.Run("destination gone", func(t *testing.T) {
		d := emergencyData()
		d.Alarm["a1"] = "555"
		es := newEmergencySetup(t, d, false)
		if r := waitCall(t, dial(t.Context(), es.a, AlarmCode)); responseCode(r.err) != 404 {
			t.Fatalf("*55 to a deleted destination = %v, want 404", r.err)
		}
	})
}
