package sip

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// TestParseFeatureBody fails if a phone's SetDoNotDisturb / SetForwarding
// (or the matching event) document is read as anything but the change it
// asks for, or if a malformed one is accepted.
func TestParseFeatureBody(t *testing.T) {
	const ns = ` xmlns="http://www.ecma-international.org/standards/ecma-323/csta/ed3"`
	tests := []struct {
		name    string
		body    string
		wantErr bool
		check   func(t *testing.T, c []featChange)
	}{
		{name: "empty body has no change", body: "  ", check: func(t *testing.T, c []featChange) {
			if len(c) != 0 {
				t.Fatalf("changes = %v", c)
			}
		}},
		{name: "dnd on", body: `<?xml version="1.0"?><SetDoNotDisturb` + ns + `><device>sip:200@hello.test</device><doNotDisturbOn>true</doNotDisturbOn></SetDoNotDisturb>`,
			check: func(t *testing.T, c []featChange) {
				if len(c) != 1 || c[0].dnd == nil || !*c[0].dnd || c[0].device != "sip:200@hello.test" {
					t.Fatalf("changes = %+v", c)
				}
			}},
		{name: "dnd event off", body: `<DoNotDisturbEvent><device>200</device><doNotDisturbOn>false</doNotDisturbOn></DoNotDisturbEvent>`,
			check: func(t *testing.T, c []featChange) {
				if len(c) != 1 || c[0].dnd == nil || *c[0].dnd {
					t.Fatalf("changes = %+v", c)
				}
			}},
		{name: "forward always set from a sip uri", body: `<SetForwarding><device>200</device><forwardingType>forwardImmediate</forwardingType><activateForward>true</activateForward><forwardDN>sip:+3212345@hello.test;user=phone</forwardDN></SetForwarding>`,
			check: func(t *testing.T, c []featChange) {
				if len(c) != 1 || c[0].fwd != fwdImmediate || c[0].target == nil || *c[0].target != "+3212345" {
					t.Fatalf("changes = %+v", c)
				}
			}},
		{name: "forward busy cleared keeps no target", body: `<SetForwarding><device>200</device><forwardingType>forwardBusy</forwardingType><activateForward>false</activateForward><forwardDN>300</forwardDN></SetForwarding>`,
			check: func(t *testing.T, c []featChange) {
				if len(c) != 1 || c[0].fwd != fwdBusy || c[0].target == nil || *c[0].target != "" {
					t.Fatalf("changes = %+v", c)
				}
			}},
		{name: "no answer with ring count (event form)", body: `<CallForwardingEvent><device>200</device><forwardingType>forwardNoAns</forwardingType><forwardStatus>true</forwardStatus><forwardTo>300</forwardTo><ringCount>4</ringCount></CallForwardingEvent>`,
			check: func(t *testing.T, c []featChange) {
				if len(c) != 1 || c[0].fwd != fwdNoAnswer || *c[0].target != "300" || c[0].rings == nil || *c[0].rings != 4 {
					t.Fatalf("changes = %+v", c)
				}
			}},
		{name: "several documents and an unknown root", body: `<?xml version="1.0"?><Other/><SetDoNotDisturb><doNotDisturbOn>1</doNotDisturbOn></SetDoNotDisturb><SetForwarding><forwardingType>forwardBusy</forwardingType><forwardDN>301</forwardDN></SetForwarding>`,
			check: func(t *testing.T, c []featChange) {
				if len(c) != 2 || *c[0].dnd != true || c[1].fwd != fwdBusy || *c[1].target != "301" {
					t.Fatalf("changes = %+v", c)
				}
			}},
		{name: "malformed xml", body: `<SetDoNotDisturb><doNotDisturbOn>true`, wantErr: true},
		{name: "dnd without state", body: `<SetDoNotDisturb><device>200</device></SetDoNotDisturb>`, wantErr: true},
		{name: "unknown forwarding type", body: `<SetForwarding><forwardingType>forwardSometimes</forwardingType><forwardDN>300</forwardDN></SetForwarding>`, wantErr: true},
		{name: "activate without a target", body: `<SetForwarding><forwardingType>forwardImmediate</forwardingType><activateForward>true</activateForward></SetForwarding>`, wantErr: true},
		{name: "target with letters", body: `<SetForwarding><forwardingType>forwardImmediate</forwardingType><forwardDN>evil</forwardDN></SetForwarding>`, wantErr: true},
		{name: "ring count out of range", body: `<SetForwarding><forwardingType>forwardNoAns</forwardingType><forwardDN>300</forwardDN><ringCount>99</ringCount></SetForwarding>`, wantErr: true},
		{name: "oversized", body: `<SetDoNotDisturb><doNotDisturbOn>true</doNotDisturbOn></SetDoNotDisturb>` + strings.Repeat(" ", maxFeatureBody), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c, err := parseFeatureBody([]byte(tc.body))
			if tc.wantErr {
				if !errors.Is(err, errFeatureBody) {
					t.Fatalf("err = %v, want errFeatureBody", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			tc.check(t, c)
		})
	}
}

// TestFeatureEvents fails if the first NOTIFY does not carry every feature,
// if a later one carries more than what changed, or if the documents do not
// have the ECMA-323 shape.
func TestFeatureEvents(t *testing.T) {
	cur := featState{DND: true, Always: "300", NoAns: "301", Rings: 4}
	all := featureEvents("200", nil, cur)
	if len(all) != 4 {
		t.Fatalf("full state = %d documents, want 4", len(all))
	}
	text := ""
	for _, d := range all {
		text += string(d)
	}
	for _, want := range []string{
		"<DoNotDisturbEvent", "<doNotDisturbOn>true</doNotDisturbOn>", "<ForwardingEvent", "<device>200</device>",
		"<forwardingType>forwardImmediate</forwardingType><forwardStatus>true</forwardStatus><forwardTo>300</forwardTo>",
		"<forwardingType>forwardBusy</forwardingType><forwardStatus>false</forwardStatus><forwardTo></forwardTo>",
		"<forwardingType>forwardNoAns</forwardingType><forwardStatus>true</forwardStatus><forwardTo>301</forwardTo><ringCount>4</ringCount>",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("documents lack %q:\n%s", want, text)
		}
	}
	if d := featureEvents("200", &cur, cur); len(d) != 0 {
		t.Fatalf("unchanged state sent %d documents", len(d))
	}
	next := cur
	next.Busy = "302"
	if d := featureEvents("200", &cur, next); len(d) != 1 || !strings.Contains(string(d[0]), "forwardBusy") {
		t.Fatalf("busy change = %q", d)
	}
	next = cur
	next.Rings = 6
	if d := featureEvents("200", &cur, next); len(d) != 1 || !strings.Contains(string(d[0]), "<ringCount>6</ringCount>") {
		t.Fatalf("ring change = %q", d)
	}
}

// TestRingTimeoutFromRingCount fails if a call to an extension with its own
// ring count does not ring that long, or if one without it stops using the
// system timeout.
func TestRingTimeoutFromRingCount(t *testing.T) {
	srv := &Server{cfg: Config{RingTimeout: 30 * time.Second}}
	c := &call{s: srv}
	if got := c.ringTimeout(); got != 30*time.Second {
		t.Fatalf("no stages = %v", got)
	}
	c.stages = &callStages{feats: snapshot.Extension{Number: "200"}}
	if got := c.ringTimeout(); got != 30*time.Second {
		t.Fatalf("default = %v", got)
	}
	c.stages.feats.NoAnswerRings = 3
	if got := c.ringTimeout(); got != 18*time.Second {
		t.Fatalf("3 rings = %v, want 18s", got)
	}
	if st := srv.featStateOf(snapshot.Extension{}); st.Rings != 5 {
		t.Fatalf("default rings reported = %d, want 5", st.Rings)
	}
}

// featureSub sends a feature-event SUBSCRIBE (digest-answered) with an
// optional body. callID and toTag make it an in-dialog refresh when set.
func (p *phone) featureSub(t *testing.T, ruriUser, body string, expires int, callID, toTag string) (*sip.Response, string) {
	t.Helper()
	req := sip.NewRequest(sip.SUBSCRIBE, sip.Uri{Scheme: "sip", User: ruriUser, Host: testDomain})
	req.AppendHeader(&sip.FromHeader{Address: p.aorURI(), Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	to := &sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: ruriUser, Host: testDomain}}
	if toTag != "" {
		to.Params = sip.HeaderParams{{K: "tag", V: toTag}}
	}
	req.AppendHeader(to)
	if callID == "" {
		callID = newID()
	}
	cid := sip.CallIDHeader(callID)
	req.AppendHeader(&cid)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: sip.SUBSCRIBE})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.NewHeader("Event", eventFeature))
	req.AppendHeader(sip.NewHeader("Accept", featureContentType))
	req.AppendHeader(sip.HeaderClone(&p.contact))
	req.AppendHeader(sip.NewHeader("Expires", strconv.Itoa(expires)))
	if body != "" {
		req.AppendHeader(sip.NewHeader("Content-Type", featureContentType))
		req.SetBody([]byte(body))
	}
	req.SetDestination(p.pbx)
	return p.authDo(req, AlgSHA256, p.pass), callID
}

// featureNotifies collects n as-feature-event NOTIFY bodies.
func featureNotifies(t *testing.T, ch chan *sip.Request, n int) []string {
	t.Helper()
	var out []string
	deadline := time.After(10 * time.Second)
	for len(out) < n {
		select {
		case r := <-ch:
			if h := r.GetHeader("Event"); h != nil && h.Value() == eventFeature {
				if ct := r.GetHeader("Content-Type"); ct == nil || ct.Value() != featureContentType {
					t.Fatalf("Content-Type = %v", ct)
				}
				out = append(out, string(r.Body()))
			}
		case <-deadline:
			t.Fatalf("timed out after %d of %d NOTIFYs: %q", len(out), n, out)
		}
	}
	return out
}

func drainReqs(ch chan *sip.Request) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// reloadWith installs a new snapshot revision and runs the reload hook, as
// the snapshot watcher does after a configuration change.
func reloadWith(pbx *testPBX, fn func(s *snapshot.Snapshot)) {
	old := pbx.snaps.Current()
	cur := snapshot.New(old.Revision+1, testDomain, callerDevices()).WithExtensions(bareExtension)
	fn(cur)
	pbx.snaps.p.Store(cur)
	pbx.srv.SnapshotChanged(old, cur)
}

const setDND = `<?xml version="1.0" encoding="UTF-8"?><SetDoNotDisturb xmlns="http://www.ecma-international.org/standards/ecma-323/csta/ed3"><device>%s</device><doNotDisturbOn>%s</doNotDisturbOn></SetDoNotDisturb>`

// TestFeatureKeySyncBothWays fails if the phone's DND key does not reach the
// settings sink as a feature-code style update, if the answering NOTIFY
// flaps back to the old state, or if a change made elsewhere (UI, API, AI or
// feature code, all of which reload the snapshot) does not NOTIFY every
// subscribed device exactly once with just what changed.
func TestFeatureKeySyncBothWays(t *testing.T) {
	settings := &fakeSettings{}
	pbx := startPBX(t, callerDevices(), withFeatures(func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", ForwardNoAnswer: "301", NoAnswerRings: 4}})
	}), func(_ *Config, d *Deps) { d.Settings = settings })
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	drainReqs(b.notifies)

	// Subscribe: 200 and the full state, DND plus the three forwards.
	res, _ := b.featureSub(t, "200", "", 300, "", "")
	if res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE = %d", res.StatusCode)
	}
	if h := res.GetHeader("Expires"); h == nil || h.Value() != "300" {
		t.Fatalf("Expires = %v", h)
	}
	docs := strings.Join(featureNotifies(t, b.notifies, 4), "\n")
	for _, want := range []string{"<doNotDisturbOn>false<", "forwardImmediate</forwardingType><forwardStatus>false",
		"forwardNoAns</forwardingType><forwardStatus>true</forwardStatus><forwardTo>301</forwardTo><ringCount>4</ringCount>"} {
		if !strings.Contains(docs, want) {
			t.Errorf("initial state lacks %q:\n%s", want, docs)
		}
	}
	if len(settings.got()) != 0 {
		t.Fatalf("subscribing changed settings: %+v", settings.got())
	}

	// Phone to Hello: DND key pressed. Queued like a feature code; the NOTIFY
	// that answers already shows DND on, and the reload that follows adds none.
	res, _ = b.featureSub(t, "200", strings.Replace(strings.Replace(setDND, "%s", "200", 1), "%s", "true", 1), 300, "", "")
	if res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE with DND = %d", res.StatusCode)
	}
	if got := settings.got(); len(got) != 1 || got[0].Extension != "200" || got[0].DND == nil || !*got[0].DND ||
		got[0].ForwardAlways != nil || got[0].ForwardBusy != nil || got[0].ForwardNoAnswer != nil {
		t.Fatalf("settings = %+v", got)
	}
	// The second dialog is new and gets the full state.
	docs = strings.Join(featureNotifies(t, b.notifies, 4), "\n")
	if !strings.Contains(docs, "<doNotDisturbOn>true<") {
		t.Fatalf("answering NOTIFY does not show DND on:\n%s", docs)
	}
	drainReqs(b.notifies)
	reloadWith(pbx, func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", DND: true, ForwardNoAnswer: "301", NoAnswerRings: 4}})
	})
	// The first dialog never heard DND on: exactly its one document. The
	// second already did and gets nothing.
	got := featureNotifies(t, b.notifies, 1)
	if !strings.Contains(got[0], "<doNotDisturbOn>true<") {
		t.Fatalf("reload NOTIFY = %s", got[0])
	}
	noReq(t, b.notifies, 300*time.Millisecond, "NOTIFY for an unchanged state")

	// Hello to phone: forward-always set from the UI, ring count changed.
	reloadWith(pbx, func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", DND: true, ForwardAlways: "300", ForwardNoAnswer: "301", NoAnswerRings: 6}})
	})
	// 2 dialogs x (always + no-answer ring count).
	text := strings.Join(featureNotifies(t, b.notifies, 4), "\n")
	if strings.Count(text, "forwardImmediate</forwardingType><forwardStatus>true</forwardStatus><forwardTo>300<") != 2 ||
		strings.Count(text, "<ringCount>6</ringCount>") != 2 || strings.Contains(text, "DoNotDisturbEvent") {
		t.Fatalf("change NOTIFYs:\n%s", text)
	}
	noReq(t, b.notifies, 300*time.Millisecond, "extra NOTIFY")

	// Another extension's change does not reach this phone.
	reloadWith(pbx, func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{
			{Number: "200", DND: true, ForwardAlways: "300", ForwardNoAnswer: "301", NoAnswerRings: 6},
			{Number: "100", DND: true},
		})
	})
	noReq(t, b.notifies, 300*time.Millisecond, "NOTIFY for another extension")
}

// TestFeatureKeyAuthorization fails if anyone but the extension's own
// authenticated device can change or watch its features.
func TestFeatureKeyAuthorization(t *testing.T) {
	settings := &fakeSettings{}
	pbx := startPBX(t, callerDevices(), withFeatures(func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "100"}, {Number: "200"}})
	}), func(_ *Config, d *Deps) { d.Settings = settings })
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)

	// a1 (100) addresses 200: refused, nothing queued.
	if res, _ := a.featureSub(t, "200", "", 300, "", ""); res.StatusCode != 403 {
		t.Fatalf("watching another extension = %d, want 403", res.StatusCode)
	}
	if res, _ := a.featureSub(t, "200", strings.Replace(strings.Replace(setDND, "%s", "200", 1), "%s", "true", 1), 300, "", ""); res.StatusCode != 403 {
		t.Fatalf("changing another extension = %d, want 403", res.StatusCode)
	}
	// a1 addresses its own extension but the document names 200: refused.
	if res, _ := a.featureSub(t, "100", strings.Replace(strings.Replace(setDND, "%s", "200", 1), "%s", "true", 1), 300, "", ""); res.StatusCode != 403 {
		t.Fatalf("document naming another device = %d, want 403", res.StatusCode)
	}
	// Wrong password: no change, no subscription.
	bad := newPhone(t, pbx, "b1", "wrong")
	if res, _ := bad.featureSub(t, "200", strings.Replace(strings.Replace(setDND, "%s", "200", 1), "%s", "true", 1), 300, "", ""); res.StatusCode == 200 {
		t.Fatalf("wrong password accepted")
	}
	// Malformed document is a 400 and changes nothing.
	if res, _ := b.featureSub(t, "200", "<SetDoNotDisturb>", 300, "", ""); res.StatusCode != 400 {
		t.Fatalf("malformed = %d, want 400", res.StatusCode)
	}
	if got := settings.got(); len(got) != 0 {
		t.Fatalf("refused requests queued %+v", got)
	}

	// The device's own extension, by number or by its username, works.
	if res, _ := b.featureSub(t, "b1", strings.Replace(strings.Replace(setDND, "%s", "sip:200@"+testDomain, 1), "%s", "true", 1), 300, "", ""); res.StatusCode != 200 {
		t.Fatalf("own extension by username = %d", res.StatusCode)
	}
	if got := settings.got(); len(got) != 1 || got[0].Extension != "200" {
		t.Fatalf("settings = %+v", got)
	}
	featureNotifies(t, b.notifies, 4) // let the state NOTIFYs land before the phone goes away

	// A settings store that is not taking changes is a 503, not a silent drop.
	settings.mu.Lock()
	settings.down = true
	settings.mu.Unlock()
	if res, _ := b.featureSub(t, "200", strings.Replace(strings.Replace(setDND, "%s", "200", 1), "%s", "false", 1), 300, "", ""); res.StatusCode != 503 {
		t.Fatalf("settings down = %d, want 503", res.StatusCode)
	}
}

// TestFeatureSubscriptionLifecycle fails if a refresh the node has never
// seen (failover) is not re-established with the full state, if an
// unsubscribe is not ended with a terminating NOTIFY, or if the event is
// not advertised.
func TestFeatureSubscriptionLifecycle(t *testing.T) {
	pbx := startPBX(t, callerDevices(), withFeatures(func(s *snapshot.Snapshot) {
		s.WithExtensionData([]snapshot.Extension{{Number: "200", DND: true}})
	}))
	b := newPhone(t, pbx, "b1", "pb1")
	b.register(t)
	drainReqs(b.notifies)

	if res := b.subscribeEvent(t, "200", "x-unknown", 60); res.StatusCode != 489 ||
		!strings.Contains(res.GetHeader("Allow-Events").Value(), eventFeature) {
		t.Fatalf("489 / Allow-Events = %d", res.StatusCode)
	}

	// An in-dialog refresh of a dialog this node never accepted.
	res, callID := b.featureSub(t, "200", "", 300, "node-a-call-id", "tag-from-node-a")
	if res.StatusCode != 200 {
		t.Fatalf("adopting refresh = %d, want 200", res.StatusCode)
	}
	docs := featureNotifies(t, b.notifies, 4)
	if !strings.Contains(strings.Join(docs, ""), "<doNotDisturbOn>true<") {
		t.Fatalf("adopted subscription not sent the state: %q", docs)
	}
	if pbx.srv.subCount() != 1 {
		t.Fatalf("subscriptions = %d", pbx.srv.subCount())
	}
	pbx.srv.subsMu.Lock()
	sub := pbx.srv.subs[callID]
	localTag, cseq := sub.localTag, sub.cseq
	pbx.srv.subsMu.Unlock()
	if localTag != "tag-from-node-a" || cseq < 1_000_000 {
		t.Fatalf("adopted dialog tag=%q cseq=%d", localTag, cseq)
	}

	// Unsubscribe ends it with a terminated NOTIFY.
	res, _ = b.featureSub(t, "200", "", 0, callID, "tag-from-node-a")
	if res.StatusCode != 200 {
		t.Fatalf("unsubscribe = %d", res.StatusCode)
	}
	select {
	case r := <-b.notifies:
		if st := r.GetHeader("Subscription-State"); st == nil || !strings.HasPrefix(st.Value(), "terminated") {
			t.Fatalf("Subscription-State = %v", st)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no terminating NOTIFY")
	}
	if pbx.srv.subCount() != 0 {
		t.Fatalf("subscriptions after unsubscribe = %d", pbx.srv.subCount())
	}
}
