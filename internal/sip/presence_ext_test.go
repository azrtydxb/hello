package sip

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/livestate"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// memPublished is an in-memory PublishedPresence with the semantics of the
// Valkey one; two servers sharing one stand for the two hello-sip nodes.
type memPublished struct {
	mu   sync.Mutex
	docs map[string]memDoc // ext + "|" + etag
}

type memDoc struct {
	d   livestate.PresenceDoc
	exp time.Time
}

func newMemPublished() *memPublished { return &memPublished{docs: map[string]memDoc{}} }

func (m *memPublished) PresencePut(_ context.Context, d livestate.PresenceDoc, replaces string, ttl time.Duration) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if replaces != "" {
		old, ok := m.docs[d.Extension+"|"+replaces]
		if !ok || !old.exp.After(time.Now()) {
			return false, nil
		}
		delete(m.docs, d.Extension+"|"+replaces)
	}
	m.docs[d.Extension+"|"+d.ETag] = memDoc{d: d, exp: time.Now().Add(ttl)}
	return true, nil
}

func (m *memPublished) PresenceDelete(_ context.Context, ext, etag string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.docs[ext+"|"+etag]
	delete(m.docs, ext+"|"+etag)
	return ok, nil
}

func (m *memPublished) PresenceDocs(_ context.Context, ext string) ([]livestate.PresenceDoc, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []livestate.PresenceDoc
	for _, v := range m.docs {
		if v.d.Extension == ext && v.exp.After(time.Now()) {
			out = append(out, v.d)
		}
	}
	livestate.SortPresenceDocs(out)
	return out, nil
}

func withPublished(p PublishedPresence) pbxOpt {
	return func(_ *Config, d *Deps) { d.Published = p }
}

// send builds and sends a request from p to user with the given headers.
func (p *phone) send(t *testing.T, method sip.RequestMethod, user string, hdrs map[string]string, body string) *sip.Response {
	t.Helper()
	req := sip.NewRequest(method, sip.Uri{Scheme: "sip", User: user, Host: testDomain})
	req.AppendHeader(&sip.FromHeader{Address: p.aorURI(), Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: user, Host: testDomain}})
	callID := sip.CallIDHeader(newID())
	req.AppendHeader(&callID)
	req.AppendHeader(&sip.CSeqHeader{SeqNo: 1, MethodName: method})
	req.AppendHeader(sip.NewHeader("Max-Forwards", "70"))
	req.AppendHeader(sip.HeaderClone(&p.contact))
	for k, v := range hdrs {
		req.AppendHeader(sip.NewHeader(k, v))
	}
	if body != "" {
		req.SetBody([]byte(body))
	}
	req.SetDestination(p.pbx)
	return p.authDo(req, AlgSHA256, p.pass)
}

func pidf(entity, basic, note string) string {
	s := `<?xml version="1.0"?><presence xmlns="urn:ietf:params:xml:ns:pidf" entity="` + entity + `">` +
		`<tuple id="t1"><status><basic>` + basic + `</basic></status></tuple>`
	if note != "" {
		s += `<note>` + note + `</note>`
	}
	return s + `</presence>`
}

func (p *phone) publish(t *testing.T, user string, expires int, ifMatch, ctype, body string) *sip.Response {
	h := map[string]string{"Event": "presence", "Expires": strconv.Itoa(expires)}
	if ifMatch != "" {
		h["SIP-If-Match"] = ifMatch
	}
	if body != "" {
		h["Content-Type"] = ctype
	}
	return p.send(t, sip.PUBLISH, user, h, body)
}

// waitNotify returns the first NOTIFY of event on ch whose body contains want.
func waitNotify(t *testing.T, ch chan *sip.Request, event, want string) *sip.Request {
	t.Helper()
	deadline := time.After(10 * time.Second)
	for {
		select {
		case r := <-ch:
			if h := r.GetHeader("Event"); h != nil && h.Value() == event && strings.Contains(string(r.Body()), want) {
				return r
			}
		case <-deadline:
			t.Fatalf("timed out waiting for a %s NOTIFY containing %q", event, want)
		}
	}
}

func drainNotifies(ch chan *sip.Request) {
	for {
		select {
		case <-ch:
		default:
			return
		}
	}
}

// TestPresenceExtensions fails if Event: reg is refused to an extension's own
// device or served to another extension's, if PUBLISH presence is refused,
// stored for another extension, or its refresh and removal do not follow the
// entity-tag, if a presence SUBSCRIBE does not return the published document
// with the dialog state folded in, if a resource-list SUBSCRIBE is refused
// to the phone's own device, served to another phone, or does not carry its
// BLF keys and their changes, or if a publication on one node does not reach a
// subscriber on the other.
func TestPresenceExtensions(t *testing.T) {
	old := presencePollInterval
	presencePollInterval = 50 * time.Millisecond
	t.Cleanup(func() { presencePollInterval = old })

	pub := newMemPublished()
	feats := func(s *snapshot.Snapshot) {
		s.WithBLFLists(map[string][]string{"b1": {"100", "300", "999"}})
	}
	fs := newFakeState()
	pbx := startPBX(t, threeDevices(), withSharedState(fs), withPublished(pub), withFeatures(feats))
	a, b := newPhone(t, pbx, "a1", "pa"), newPhone(t, pbx, "b1", "pb1")
	a.register(t)
	b.register(t)
	drainNotifies(b.notifies)
	drainNotifies(a.notifies)

	t.Run("reg", func(t *testing.T) {
		if res := a.subscribeEvent(t, "200", "reg", 120); res.StatusCode != 403 {
			t.Fatalf("another extension's reg SUBSCRIBE = %d, want 403", res.StatusCode)
		}
		res := b.subscribeEvent(t, "200", "reg", 120)
		if res.StatusCode != 200 {
			t.Fatalf("own reg SUBSCRIBE = %d", res.StatusCode)
		}
		first := waitNotify(t, b.notifies, "reg", "registered")
		if ct := first.GetHeader("Content-Type"); ct == nil || ct.Value() != "application/reginfo+xml" {
			t.Fatalf("Content-Type = %v", ct)
		}
		body := string(first.Body())
		if !strings.Contains(body, `aor="sip:200@`+testDomain+`"`) || !strings.Contains(body, `state="active"`) {
			t.Fatalf("reginfo = %s", body)
		}
		// A second binding of the extension changes the document.
		b2 := newPhone(t, pbx, "b1", "pb1")
		b2.register(t)
		waitNotify(t, b.notifies, "reg", b2.contact.Address.String())
		// Removing every binding ends the registration.
		drainNotifies(b.notifies)
		if res := b2.authDo(b2.registerReq(0, "*"), AlgSHA256, b2.pass); res.StatusCode != 200 {
			t.Fatalf("unregister = %d", res.StatusCode)
		}
		waitNotify(t, b.notifies, "reg", `state="terminated"`)
	})

	t.Run("publish", func(t *testing.T) {
		a.register(t)
		own := pidf("sip:100@"+testDomain, "open", "At my desk")
		if res := a.publish(t, "200", 300, "", pidfType, own); res.StatusCode != 403 {
			t.Fatalf("PUBLISH for another extension = %d, want 403", res.StatusCode)
		}
		if res := a.publish(t, "100", 300, "", pidfType, pidf("sip:200@"+testDomain, "open", "")); res.StatusCode != 403 {
			t.Fatalf("PUBLISH naming another entity = %d, want 403", res.StatusCode)
		}
		if res := a.publish(t, "100", 300, "", "text/plain", own); res.StatusCode != 415 {
			t.Fatalf("PUBLISH of text/plain = %d, want 415", res.StatusCode)
		}
		if res := a.publish(t, "100", 300, "", pidfType, "<presence"); res.StatusCode != 400 {
			t.Fatalf("malformed PUBLISH = %d, want 400", res.StatusCode)
		}
		if res := a.publish(t, "100", 300, "", "", ""); res.StatusCode != 400 {
			t.Fatalf("empty initial PUBLISH = %d, want 400", res.StatusCode)
		}
		if res := a.publish(t, "100", 300, "nope", pidfType, own); res.StatusCode != 412 {
			t.Fatalf("PUBLISH with an unknown etag = %d, want 412", res.StatusCode)
		}
		res := a.publish(t, "100", 300, "", pidfType, own)
		if res.StatusCode != 200 {
			t.Fatalf("PUBLISH = %d", res.StatusCode)
		}
		etag := res.GetHeader("SIP-ETag")
		if etag == nil || etag.Value() == "" {
			t.Fatal("no SIP-ETag")
		}
		docs, _ := pub.PresenceDocs(context.Background(), "100")
		if len(docs) != 1 || docs[0].Basic != "open" || docs[0].Note != "At my desk" {
			t.Fatalf("stored = %+v", docs)
		}
		if other, _ := pub.PresenceDocs(context.Background(), "200"); len(other) != 0 {
			t.Fatalf("another extension has a publication: %+v", other)
		}
		// A refresh without a body keeps the document under a new tag.
		res = a.publish(t, "100", 300, etag.Value(), "", "")
		if res.StatusCode != 200 {
			t.Fatalf("refresh = %d", res.StatusCode)
		}
		refreshed := res.GetHeader("SIP-ETag").Value()
		if res := a.publish(t, "100", 300, etag.Value(), "", ""); res.StatusCode != 412 {
			t.Fatalf("refresh of a replaced etag = %d, want 412", res.StatusCode)
		}
		// Another extension cannot touch it with the tag.
		if res := b.publish(t, "200", 0, refreshed, "", ""); res.StatusCode != 412 {
			t.Fatalf("removal through another extension = %d, want 412", res.StatusCode)
		}
		if res := a.publish(t, "100", 0, refreshed, "", ""); res.StatusCode != 200 {
			t.Fatalf("removal = %d", res.StatusCode)
		}
		if docs, _ := pub.PresenceDocs(context.Background(), "100"); len(docs) != 0 {
			t.Fatalf("after removal: %+v", docs)
		}
		if res := a.publish(t, "100", 0, refreshed, "", ""); res.StatusCode != 412 {
			t.Fatalf("second removal = %d, want 412", res.StatusCode)
		}
		if res := a.send(t, sip.PUBLISH, "100", map[string]string{"Event": "vq-rtcpxr", "Expires": "0"}, ""); res.StatusCode != 489 {
			t.Fatalf("PUBLISH of an unknown event = %d, want 489", res.StatusCode)
		}
	})

	t.Run("presence subscribe", func(t *testing.T) {
		drainNotifies(b.notifies)
		if res := b.subscribeEvent(t, "555", "presence", 120); res.StatusCode != 404 {
			t.Fatalf("presence SUBSCRIBE to an unknown extension = %d, want 404", res.StatusCode)
		}
		if res := b.subscribeEvent(t, "100", "presence", 120); res.StatusCode != 200 {
			t.Fatalf("presence SUBSCRIBE = %d", res.StatusCode)
		}
		first := waitNotify(t, b.notifies, "presence", "<basic>open</basic>") // registered, nothing published
		if ct := first.GetHeader("Content-Type"); ct == nil || ct.Value() != pidfType {
			t.Fatalf("Content-Type = %v", ct)
		}
		res := a.publish(t, "100", 300, "", pidfType, pidf("sip:100@"+testDomain, "closed", "In a meeting"))
		if res.StatusCode != 200 {
			t.Fatalf("PUBLISH = %d", res.StatusCode)
		}
		n := waitNotify(t, b.notifies, "presence", "In a meeting")
		if !strings.Contains(string(n.Body()), "<basic>closed</basic>") {
			t.Fatalf("published document = %s", n.Body())
		}
		// The dialog state is folded in.
		drainNotifies(b.notifies)
		pbx.srv.notifyExtension("100", StateOnCall)
		n = waitNotify(t, b.notifies, "presence", "on-the-phone")
		if !strings.Contains(string(n.Body()), "In a meeting") {
			t.Fatalf("folded document lost the published note: %s", n.Body())
		}
	})

	t.Run("eventlist", func(t *testing.T) {
		drainNotifies(b.notifies)
		list := "blf-b1"
		hdr := map[string]string{"Event": "dialog", "Expires": "120", "Accept": "application/rlmi+xml, multipart/related"}
		if res := b.send(t, sip.SUBSCRIBE, list, hdr, ""); res.StatusCode != 421 {
			t.Fatalf("list SUBSCRIBE without Supported: eventlist = %d, want 421", res.StatusCode)
		}
		hdr["Supported"] = "eventlist"
		if res := a.send(t, sip.SUBSCRIBE, list, hdr, ""); res.StatusCode != 403 {
			t.Fatalf("another phone's list = %d, want 403", res.StatusCode)
		}
		if res := a.send(t, sip.SUBSCRIBE, "blf-a1", hdr, ""); res.StatusCode != 404 {
			t.Fatalf("a phone without BLF keys = %d, want 404", res.StatusCode)
		}
		res := b.send(t, sip.SUBSCRIBE, list, hdr, "")
		if res.StatusCode != 200 {
			t.Fatalf("list SUBSCRIBE = %d", res.StatusCode)
		}
		// The list arrives in datagram-sized NOTIFYs: the first is full state,
		// the rest partial.
		n := waitNotify(t, b.notifies, "dialog", "application/rlmi+xml")
		if r := n.GetHeader("Require"); r == nil || r.Value() != "eventlist" {
			t.Fatalf("NOTIFY Require = %v", r)
		}
		if ct := n.GetHeader("Content-Type"); ct == nil || !strings.HasPrefix(ct.Value(), "multipart/related") {
			t.Fatalf("Content-Type = %v", ct)
		}
		body := string(n.Body())
		if !strings.Contains(body, `fullState="true"`) || !strings.Contains(body, `uri="sip:blf-b1@`+testDomain+`"`) {
			t.Fatalf("first list NOTIFY:\n%s", body)
		}
		n2 := waitNotify(t, b.notifies, "dialog", "application/rlmi+xml")
		body2 := string(n2.Body())
		if !strings.Contains(body2, `fullState="false"`) {
			t.Fatalf("second list NOTIFY is not partial:\n%s", body2)
		}
		all := body + body2
		for _, want := range []string{`<resource uri="sip:100@` + testDomain + `">`, `<resource uri="sip:300@` + testDomain + `">`,
			`entity="sip:100@` + testDomain + `"`, `entity="sip:300@` + testDomain + `"`} {
			if !strings.Contains(all, want) {
				t.Fatalf("list lacks %q:\n%s", want, all)
			}
		}
		if strings.Contains(all, "999") {
			t.Fatalf("list carries a number that is no extension:\n%s", all)
		}
		if strings.Contains(all, "sip:200@") {
			t.Fatalf("list carries an extension that is not on the keys:\n%s", all)
		}
		// One member ringing re-sends the list.
		drainNotifies(b.notifies)
		pbx.srv.notifyExtension("300", StateRinging)
		n = waitNotify(t, b.notifies, "dialog", "<state>early</state>")
		if !strings.Contains(string(n.Body()), `entity="sip:300@`+testDomain+`"`) || strings.Contains(string(n.Body()), `fullState="true"`) {
			t.Fatalf("a change must be a partial NOTIFY of the changed member:\n%s", n.Body())
		}
		// A change to an extension that is on no key sends nothing.
		drainNotifies(b.notifies)
		pbx.srv.notifyExtension("200", StateOnCall)
		select {
		case r := <-b.notifies:
			t.Fatalf("NOTIFY for an extension outside the list: %s", r.Body())
		case <-time.After(300 * time.Millisecond):
		}
	})
}

// TestPresenceTwoNodes fails if a publication made through one node is not
// served to a subscriber of the other, or a node cannot refresh or remove
// what the other stored.
func TestPresenceTwoNodes(t *testing.T) {
	old := presencePollInterval
	presencePollInterval = 50 * time.Millisecond
	t.Cleanup(func() { presencePollInterval = old })

	pub, fs := newMemPublished(), newFakeState()
	n1 := startPBX(t, threeDevices(), withNodeID("sip-1"), withSharedState(fs), withPublished(pub))
	n2 := startPBX(t, threeDevices(), withNodeID("sip-2"), withSharedState(fs), withPublished(pub))
	a, b := newPhone(t, n2, "a1", "pa"), newPhone(t, n1, "b1", "pb1")
	a.register(t)
	b.register(t)

	if res := b.subscribeEvent(t, "100", "presence", 120); res.StatusCode != 200 {
		t.Fatalf("presence SUBSCRIBE on node 1 = %d", res.StatusCode)
	}
	waitNotify(t, b.notifies, "presence", "<basic>open</basic>")

	res := a.publish(t, "100", 300, "", pidfType, pidf("sip:100@"+testDomain, "closed", "Out"))
	if res.StatusCode != 200 {
		t.Fatalf("PUBLISH on node 2 = %d", res.StatusCode)
	}
	waitNotify(t, b.notifies, "presence", "<basic>closed</basic>")

	// The other node (and phone) can refresh what node 2 stored: the event
	// state is shared, not per node.
	a1 := newPhone(t, n1, "a1", "pa")
	a1.register(t)
	etag := res.GetHeader("SIP-ETag").Value()
	res = a1.publish(t, "100", 300, etag, "", "")
	if res.StatusCode != 200 {
		t.Fatalf("refresh on node 1 of a publication made on node 2 = %d", res.StatusCode)
	}
	if res := a1.publish(t, "100", 0, res.GetHeader("SIP-ETag").Value(), "", ""); res.StatusCode != 200 {
		t.Fatalf("removal on node 1 = %d", res.StatusCode)
	}
	waitNotify(t, b.notifies, "presence", "<basic>open</basic>")
}

// TestListNotifyFitsDatagram fails if a resource-list NOTIFY for the longest
// member number and list URI does not fit the UDP datagram sipgo allows, which
// would drop the whole list silently.
func TestListNotifyFitsDatagram(t *testing.T) {
	long := strings.Repeat("9", 4) // BLF keys name extensions, a handful of digits
	body := listBody("hello.example.com", "sip:blf-"+strings.Repeat("u", 10)+"@hello.example.com",
		"rlAbCdEfGh", "rAbCdEfGh@rl", 123456, true, []string{long}, map[string]string{long: StateDND})
	if len(body) > listNotifyBudget {
		t.Fatalf("one member's body is %d bytes, budget %d", len(body), listNotifyBudget)
	}
	// The NOTIFY's own headers measured 520 bytes; allow a Route header.
	if listNotifyBudget+700 > sip.UDPMTUSize-200 {
		t.Fatalf("budget %d leaves no room for headers in %d bytes", listNotifyBudget, sip.UDPMTUSize-200)
	}
}
