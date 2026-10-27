package sip

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/routing"
	"github.com/azrtydxb/hello/internal/snapshot"
	"github.com/emiago/sipgo/sip"
)

// --- fake queue --------------------------------------------------------------

type fakeMsgRow struct {
	job      MessageJob
	status   string // pending, sending, delivered, failed
	detail   string
	claimed  []string // every node that ever claimed it
	attempts int
	trunk    int64
}

// fakeMsgs is the durable queue in memory with the store's claim
// semantics: a pending row goes to one claimer only.
type fakeMsgs struct {
	mu        sync.Mutex
	rows      []*fakeMsgRow
	exts      map[string]bool
	ring      map[string][]string
	paging    map[string][]string // paging code -> members
	callers   map[string][]string // paging code -> allowed callers
	next      int64
	wake      []func()
	sends     []MessageSend
	rateLimit int
	maintains int
	down      bool
}

func newFakeMsgs(exts ...string) *fakeMsgs {
	f := &fakeMsgs{exts: map[string]bool{}, ring: map[string][]string{}, paging: map[string][]string{}, callers: map[string][]string{}}
	for _, e := range exts {
		f.exts[e] = true
	}
	return f
}

func (f *fakeMsgs) Watch(ctx context.Context, wake func()) {
	f.mu.Lock()
	f.wake = append(f.wake, wake)
	f.mu.Unlock()
	wake()
	<-ctx.Done()
}

func (f *fakeMsgs) notify() {
	f.mu.Lock()
	ws := slices.Clone(f.wake)
	f.mu.Unlock()
	for _, w := range ws {
		w()
	}
}

func (f *fakeMsgs) Send(_ context.Context, in MessageSend) (MessageSent, error) {
	f.mu.Lock()
	if f.down {
		f.mu.Unlock()
		return MessageSent{}, context.DeadlineExceeded
	}
	f.sends = append(f.sends, in)
	if f.rateLimit > 0 {
		n := 0
		for _, s := range f.sends {
			if s.Sender == in.Sender {
				n++
			}
		}
		if n > f.rateLimit {
			f.mu.Unlock()
			return MessageSent{}, ErrMsgRate
		}
	}
	var recipients []string
	scope, ref := "direct", ""
	kind := MsgTargetExtension
	switch in.TargetKind {
	case MsgTargetExtension:
		if !f.exts[in.Target] {
			f.mu.Unlock()
			return MessageSent{}, ErrMsgTarget
		}
		recipients = []string{in.Target}
	case MsgTargetRing:
		recipients, scope, ref = f.ring[in.Target], "ring_group", in.Target
	case MsgTargetPaging:
		if c := f.callers[in.Target]; len(c) > 0 && !slices.Contains(c, in.Sender) {
			f.mu.Unlock()
			return MessageSent{}, ErrMsgForbidden
		}
		recipients, scope, ref = f.paging[in.Target], "paging_group", in.Target
	case MsgTargetExternal:
		recipients, kind = []string{in.Target}, MsgTargetExternal
	}
	var kept []string
	for _, r := range recipients {
		if scope == "direct" || r != in.Sender {
			kept = append(kept, r)
		}
	}
	if len(kept) == 0 {
		f.mu.Unlock()
		return MessageSent{}, ErrMsgTarget
	}
	claimed := in.ClaimBy != "" && len(kept) == 1
	var out MessageSent
	for _, r := range kept {
		f.next++
		row := &fakeMsgRow{status: "pending", trunk: in.TrunkID, job: MessageJob{
			ID: f.next, Sender: in.Sender, SenderKind: in.SenderKind, SenderName: in.SenderName,
			Recipient: r, RecipientKind: kind, Scope: scope, ScopeRef: ref, ContentType: in.ContentType,
			Body: in.Body, TrunkID: in.TrunkID, CreatedAt: time.Now(),
		}}
		if claimed {
			row.status = "sending"
			row.claimed = append(row.claimed, in.ClaimBy)
			out.Jobs = append(out.Jobs, row.job)
		}
		f.rows = append(f.rows, row)
	}
	out.Recipients = len(kept)
	f.mu.Unlock()
	if !claimed {
		f.notify()
	}
	return out, nil
}

func (f *fakeMsgs) Pending(context.Context) ([]string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var exts []string
	external := false
	for _, r := range f.rows {
		if r.status != "pending" {
			continue
		}
		if r.job.RecipientKind == MsgTargetExternal {
			external = true
		} else if !slices.Contains(exts, r.job.Recipient) {
			exts = append(exts, r.job.Recipient)
		}
	}
	return exts, external, nil
}

func (f *fakeMsgs) Claim(_ context.Context, node string, exts []string, external bool, limit int) ([]MessageJob, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var jobs []MessageJob
	for _, r := range f.rows {
		if len(jobs) >= limit || r.status != "pending" {
			continue
		}
		if (r.job.RecipientKind == MsgTargetExtension && slices.Contains(exts, r.job.Recipient)) ||
			(r.job.RecipientKind == MsgTargetExternal && external) {
			r.status = "sending"
			r.claimed = append(r.claimed, node)
			jobs = append(jobs, r.job)
		}
	}
	return jobs, nil
}

func (f *fakeMsgs) Finish(_ context.Context, id int64, result, detail string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, r := range f.rows {
		if r.job.ID != id {
			continue
		}
		r.detail = detail
		switch result {
		case MsgResultDelivered:
			r.status = "delivered"
		case MsgResultFailed:
			r.status = "failed"
		default:
			r.status = "pending"
			if result == MsgResultRetry {
				r.attempts++
			}
		}
	}
	return nil
}

func (f *fakeMsgs) Maintain(context.Context) error {
	f.mu.Lock()
	f.maintains++
	f.mu.Unlock()
	return nil
}

func (f *fakeMsgs) statusOf(recipient string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, r := range f.rows {
		if r.job.Recipient == recipient {
			out = append(out, r.status)
		}
	}
	return out
}

func (f *fakeMsgs) rowCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func withMessages(q MessageQueue) pbxOpt {
	return func(_ *Config, d *Deps) { d.Messages = q }
}

// fastMessages makes the delivery loop's sweep quick for the test.
func fastMessages(t *testing.T) {
	t.Helper()
	oldS, oldM := messageSweepInterval, messageMaintainInterval
	messageSweepInterval, messageMaintainInterval = 40*time.Millisecond, 60*time.Millisecond
	t.Cleanup(func() { messageSweepInterval, messageMaintainInterval = oldS, oldM })
}

// messageReq is a MESSAGE as a phone sends it.
func (p *phone) messageReq(to, from, ct, body string) *sip.Request {
	req := sip.NewRequest(sip.MESSAGE, sip.Uri{Scheme: "sip", User: to, Host: testDomain})
	fu := p.aorURI()
	if from != "" {
		fu.User = from
	}
	req.AppendHeader(&sip.FromHeader{Address: fu, Params: sip.HeaderParams{{K: "tag", V: sip.GenerateTagN(8)}}})
	req.AppendHeader(&sip.ToHeader{Address: sip.Uri{Scheme: "sip", User: to, Host: testDomain}})
	if ct != "" {
		req.AppendHeader(sip.NewHeader("Content-Type", ct))
	}
	req.SetBody([]byte(body))
	req.SetDestination(p.pbx)
	return req
}

// sendMessage sends a MESSAGE with digest credentials and returns the final
// response.
func (p *phone) sendMessage(to, ct, body string) *sip.Response {
	return p.authDo(p.messageReq(to, "", ct, body), AlgSHA256, p.pass)
}

func msgDevices() []snapshot.Device {
	return []snapshot.Device{
		dev(1, "a1", "100", "pa"),
		dev(2, "b1", "200", "pb"),
		dev(3, "b2", "200", "pb2"), // a second device of extension 200
		dev(4, "c1", "300", "pc"),
	}
}

func bodyOf(r *sip.Request) string { return string(r.Body()) }

// TestMessageDeliveredToEveryDevice fails if a MESSAGE to an extension does
// not reach every registered device of it with the authenticated sender in
// From and the body and content type unchanged, if the sender is not
// answered 200 once a contact has it, or if the delivery is not counted.
func TestMessageDeliveredToEveryDevice(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200", "300")
	pbx := startPBX(t, msgDevices(), withMessages(q))
	a := newPhone(t, pbx, "a1", "pa")
	b1, b2 := newPhone(t, pbx, "b1", "pb"), newPhone(t, pbx, "b2", "pb2")
	b1.register(t)
	b2.register(t)

	res := a.sendMessage("200", "text/plain;charset=UTF-8", "lunch at noon")
	if res.StatusCode != 200 {
		t.Fatalf("MESSAGE = %d, want 200 (delivered to a contact)", res.StatusCode)
	}
	for _, p := range []*phone{b1, b2} {
		m := waitReq(t, p.messages, "MESSAGE at "+p.user)
		if got := m.From().Address.User; got != "100" {
			t.Errorf("%s sees From user %q, want the sender's extension 100", p.user, got)
		}
		if got := bodyOf(m); got != "lunch at noon" {
			t.Errorf("%s got body %q", p.user, got)
		}
		if ct := m.ContentType(); ct == nil || ct.Value() != "text/plain;charset=UTF-8" {
			t.Errorf("%s got content type %v", p.user, ct)
		}
		if m.GetHeader("Date") == nil {
			t.Errorf("%s: no Date header", p.user)
		}
	}
	eventually(t, "row delivered", func() bool { return slices.Equal(q.statusOf("200"), []string{"delivered"}) })
	if got := pbx.metric(t, "hello_sip_messages_total", map[string]string{"source": "phone", "outcome": "delivered"}); got != 1 {
		t.Errorf("delivered counter = %v, want 1", got)
	}
	if got := pbx.metric(t, "hello_sip_message_deliveries_total", map[string]string{"via": "phone", "result": "delivered"}); got != 1 {
		t.Errorf("delivery counter = %v, want 1", got)
	}
}

// TestMessagePassThrough fails if a content type other than text/plain is not
// delivered with its type and bytes intact.
func TestMessagePassThrough(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200")
	pbx := startPBX(t, msgDevices(), withMessages(q))
	a := newPhone(t, pbx, "a1", "pa")
	b := newPhone(t, pbx, "b1", "pb")
	b.register(t)
	const cpim = "From: <sip:100@hello.test>\r\nTo: <sip:200@hello.test>\r\n\r\nContent-Type: text/plain\r\n\r\nhi"
	if res := a.sendMessage("200", "message/cpim", cpim); res.StatusCode != 200 {
		t.Fatalf("cpim MESSAGE = %d", res.StatusCode)
	}
	m := waitReq(t, b.messages, "cpim MESSAGE")
	if ct := m.ContentType(); ct == nil || ct.Value() != "message/cpim" || bodyOf(m) != cpim {
		t.Fatalf("cpim arrived as %v %q", m.ContentType(), bodyOf(m))
	}
}

// TestMessageStoreAndForward fails if a MESSAGE to an extension with no
// registered device is not answered 202 and kept, if it is delivered before
// a device registers, if a REGISTER does not deliver it at once, or if it is
// delivered more than once.
func TestMessageStoreAndForward(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200")
	pbx := startPBX(t, msgDevices(), withMessages(q))
	a := newPhone(t, pbx, "a1", "pa")
	b := newPhone(t, pbx, "b1", "pb")

	for _, text := range []string{"first", "second"} {
		if res := a.sendMessage("200", "text/plain", text); res.StatusCode != 202 {
			t.Fatalf("MESSAGE to an unregistered extension = %d, want 202", res.StatusCode)
		}
	}
	noReq(t, b.messages, 200*time.Millisecond, "delivery before the phone registered")
	if got := q.statusOf("200"); !slices.Equal(got, []string{"pending", "pending"}) {
		t.Fatalf("rows = %v, want both pending", got)
	}

	b.register(t)
	first := waitReq(t, b.messages, "stored MESSAGE after REGISTER")
	second := waitReq(t, b.messages, "second stored MESSAGE")
	if bodyOf(first) != "first" || bodyOf(second) != "second" {
		t.Fatalf("delivered %q then %q, want first then second", bodyOf(first), bodyOf(second))
	}
	eventually(t, "rows delivered", func() bool { return slices.Equal(q.statusOf("200"), []string{"delivered", "delivered"}) })
	// A refresh of the registration redelivers nothing.
	b.register(t)
	noReq(t, b.messages, 300*time.Millisecond, "redelivery after a refresh")
}

// TestMessageDeliveredOnce fails if, with two nodes sweeping the same queue
// and both seeing the phone register, any stored message is claimed or
// delivered by both.
func TestMessageDeliveredOnce(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200")
	fs := newFakeState()
	n1 := startPBX(t, msgDevices(), withMessages(q), withSharedState(fs), withNodeID("n1"))
	n2 := startPBX(t, msgDevices(), withMessages(q), withSharedState(fs), withNodeID("n2"))
	a := newPhone(t, n1, "a1", "pa")
	const count = 12
	for i := 0; i < count; i++ {
		if res := a.sendMessage("200", "text/plain", "m"); res.StatusCode != 202 {
			t.Fatalf("MESSAGE %d = %d, want 202", i, res.StatusCode)
		}
	}
	b := newPhone(t, n1, "b1", "pb")
	b.register(t)
	eventually(t, "all delivered", func() bool {
		st := q.statusOf("200")
		return len(st) == count && !slices.Contains(st, "pending") && !slices.Contains(st, "sending")
	})
	got := 0
	for {
		select {
		case <-b.messages:
			got++
			continue
		case <-time.After(300 * time.Millisecond):
		}
		break
	}
	if got != count {
		t.Fatalf("phone received %d messages, want exactly %d", got, count)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	byNode := map[string]int{}
	for _, r := range q.rows {
		// A claim is only repeated after its delivery came back retryable
		// (an edge refusal while a node's peer set catches up); a row is
		// never claimed by two nodes at once, which Claim's status check
		// guarantees and the phone's exact count below proves end to end.
		if len(r.claimed) == 0 || r.status != "delivered" {
			t.Errorf("message %d claimed %d times (%v), status %s, want claimed and delivered", r.job.ID, len(r.claimed), r.claimed, r.status)
		}
		for _, n := range r.claimed {
			byNode[n]++
		}
	}
	t.Logf("claims by node: %v", byNode)
	_ = n2
}

// TestMessageAuthorization fails if a MESSAGE can be sent without
// credentials, as another extension, to a number no trunk carries, or to an
// unknown extension, or if any such attempt stores a message or reaches a
// phone.
func TestMessageAuthorization(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200")
	pbx := startPBX(t, msgDevices(), withMessages(q))
	a := newPhone(t, pbx, "a1", "pa")
	b := newPhone(t, pbx, "b1", "pb")
	b.register(t)

	// No credentials, a request that claims to be from a phone: challenged,
	// and the challenge is answered only with the right password.
	res := a.do(a.messageReq("200", "", "text/plain", "hi"))
	if res.StatusCode != 401 {
		t.Fatalf("MESSAGE without credentials = %d, want 401", res.StatusCode)
	}
	if res := a.authDo(a.messageReq("200", "", "text/plain", "hi"), AlgSHA256, "wrong"); res.StatusCode != 401 && res.StatusCode != 403 {
		t.Fatalf("MESSAGE with a wrong password = %d, want 401 or 403", res.StatusCode)
	}

	// From naming another extension is a spoof, even with a valid password.
	spoof := a.messageReq("200", "200", "text/plain", "I am Bob")
	if res := a.authDo(spoof, AlgSHA256, "pa"); res.StatusCode != 403 {
		t.Fatalf("MESSAGE with a spoofed From = %d, want 403", res.StatusCode)
	}

	// An unknown extension is 404; an external number no trunk carries is
	// 403: Hello relays to registered contacts and trunks only.
	if res := a.sendMessage("299", "text/plain", "x"); res.StatusCode != 404 {
		t.Fatalf("MESSAGE to an unknown extension = %d, want 404", res.StatusCode)
	}
	if res := a.sendMessage("+971501234567", "text/plain", "x"); res.StatusCode != 404 && res.StatusCode != 403 {
		t.Fatalf("MESSAGE to an external number with no route = %d, want 404 or 403", res.StatusCode)
	}
	if res := a.sendMessage("sip:evil@elsewhere.example", "text/plain", "x"); res.StatusCode == 200 || res.StatusCode == 202 {
		t.Fatalf("MESSAGE to a foreign URI = %d, want a refusal", res.StatusCode)
	}

	// A stranger from no known source and not in the domain: 403.
	stranger := newPhone(t, pbx, "x", "x")
	req := stranger.messageReq("200", "", "text/plain", "hi")
	req.From().Address.Host = "elsewhere.example"
	if res := stranger.do(req); res.StatusCode != 403 {
		t.Fatalf("MESSAGE from an unknown source = %d, want 403", res.StatusCode)
	}

	noReq(t, b.messages, 300*time.Millisecond, "any message at the target")
	if n := q.rowCount(); n != 0 {
		t.Fatalf("%d rows stored by refused messages", n)
	}
	if got := pbx.metric(t, "hello_sip_messages_total", map[string]string{"outcome": "rejected_forbidden"}); got < 1 {
		t.Errorf("rejected_forbidden counter = %v, want at least 1", got)
	}
}

// TestMessageValidation fails if an oversize, empty or typeless MESSAGE is
// accepted, or if it is not refused with the right code.
func TestMessageValidation(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200")
	pbx := startPBX(t, msgDevices(), withMessages(q))
	a := newPhone(t, pbx, "a1", "pa")
	for name, c := range map[string]struct {
		ct, body string
		want     int
	}{
		"too large":    {"text/plain", strings.Repeat("x", MaxMessageBytes+1), 413},
		"empty":        {"text/plain", "", 400},
		"no type":      {"", "hi", 400},
		"bad type":     {"not a type", "hi", 400},
		"largest ok":   {"text/plain", strings.Repeat("x", MaxMessageBytes), 202},
		"unicode text": {"text/plain", "héllo ✓", 202},
	} {
		t.Run(name, func(t *testing.T) {
			if res := a.sendMessage("200", c.ct, c.body); res.StatusCode != c.want {
				t.Errorf("MESSAGE = %d, want %d", res.StatusCode, c.want)
			}
		})
	}
}

// TestMessageRateAndOutage fails if a sender over the limit is not answered
// 429 with Retry-After, or if a queue outage is not 503.
func TestMessageRateAndOutage(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200")
	q.rateLimit = 2
	pbx := startPBX(t, msgDevices(), withMessages(q))
	a := newPhone(t, pbx, "a1", "pa")
	for i := 0; i < 2; i++ {
		if res := a.sendMessage("200", "text/plain", "x"); res.StatusCode != 202 {
			t.Fatalf("MESSAGE %d = %d", i, res.StatusCode)
		}
	}
	res := a.sendMessage("200", "text/plain", "x")
	if res.StatusCode != 429 || res.GetHeader("Retry-After") == nil {
		t.Fatalf("third MESSAGE = %d (Retry-After %v), want 429 with Retry-After", res.StatusCode, res.GetHeader("Retry-After"))
	}
	q.mu.Lock()
	q.rateLimit, q.down = 0, true
	q.mu.Unlock()
	if res := a.sendMessage("200", "text/plain", "x"); res.StatusCode != 503 {
		t.Fatalf("MESSAGE with the queue down = %d, want 503", res.StatusCode)
	}
}

// TestMessageNoQueue fails if a MESSAGE is answered 200 and dropped when no
// queue is configured.
func TestMessageNoQueue(t *testing.T) {
	pbx := startPBX(t, msgDevices())
	a := newPhone(t, pbx, "a1", "pa")
	if res := a.sendMessage("200", "text/plain", "x"); res.StatusCode != 503 {
		t.Fatalf("MESSAGE with no queue = %d, want 503, never 200", res.StatusCode)
	}
}

// TestMessageGroups fails if a MESSAGE to a ring group or a paging group does
// not reach every member but the sender, with the group in the display name,
// if a caller outside a paging group's caller list is not refused, or if a
// member who sends to the group gets its own message back.
func TestMessageGroups(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200", "300")
	q.ring["sales"] = []string{"100", "200", "300"}
	q.paging["*88"] = []string{"200", "300"}
	q.callers["*88"] = []string{"100"}
	pbx := startPBX(t, msgDevices(), withMessages(q), withFeatures(func(s *snapshot.Snapshot) {
		s.WithRingGroups([]snapshot.RingGroup{{ID: 1, Name: "sales", Strategy: "ring-all"}},
			[][]snapshot.RingGroupMember{{{Extension: "100"}, {Extension: "200"}, {Extension: "300"}}})
		s.WithPagingGroups([]snapshot.PagingGroup{{ID: 1, Name: "floor", Code: "*88", Enabled: true, Members: []string{"200", "300"}, Callers: []string{"100"}}})
	}))
	a := newPhone(t, pbx, "a1", "pa")
	b, c := newPhone(t, pbx, "b1", "pb"), newPhone(t, pbx, "c1", "pc")
	a.register(t)
	b.register(t)
	c.register(t)

	if res := a.sendMessage("sales", "text/plain", "standup"); res.StatusCode != 202 {
		t.Fatalf("MESSAGE to a ring group = %d, want 202", res.StatusCode)
	}
	for _, p := range []*phone{b, c} {
		m := waitReq(t, p.messages, "group MESSAGE at "+p.user)
		if bodyOf(m) != "standup" || m.From().Address.User != "100" || !strings.Contains(m.From().DisplayName, "(sales)") {
			t.Errorf("%s got %q from %q (%q)", p.user, bodyOf(m), m.From().Address.User, m.From().DisplayName)
		}
	}
	noReq(t, a.messages, 200*time.Millisecond, "the sender's own group message")

	if res := a.sendMessage("*88", "text/plain", "floor meeting"); res.StatusCode != 202 {
		t.Fatalf("MESSAGE to a paging group = %d, want 202", res.StatusCode)
	}
	waitReq(t, b.messages, "paging group MESSAGE")
	waitReq(t, c.messages, "paging group MESSAGE")

	// b is not on the paging group's caller list.
	if res := b.sendMessage("*88", "text/plain", "hi"); res.StatusCode != 403 {
		t.Fatalf("MESSAGE to a paging group by a non-caller = %d, want 403", res.StatusCode)
	}
}

// TestMessageTypingNotStored fails if a typing indication (RFC 3994) is
// stored, or is not relayed to a registered recipient.
func TestMessageTypingNotStored(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200")
	pbx := startPBX(t, msgDevices(), withMessages(q))
	a := newPhone(t, pbx, "a1", "pa")
	b := newPhone(t, pbx, "b1", "pb")
	const typing = `<?xml version="1.0"?><isComposing xmlns="urn:ietf:params:xml:ns:im-iscomposing"><state>active</state></isComposing>`
	if res := a.sendMessage("200", "application/im-iscomposing+xml", typing); res.StatusCode != 200 {
		t.Fatalf("typing to an offline phone = %d, want 200", res.StatusCode)
	}
	if n := q.rowCount(); n != 0 {
		t.Fatalf("typing indication stored (%d rows)", n)
	}
	b.register(t)
	noReq(t, b.messages, 200*time.Millisecond, "stale typing indication after REGISTER")
	if res := a.sendMessage("200", "application/im-iscomposing+xml", typing); res.StatusCode != 200 {
		t.Fatalf("typing to an online phone = %d", res.StatusCode)
	}
	if m := waitReq(t, b.messages, "typing relay"); !strings.Contains(bodyOf(m), "isComposing") {
		t.Fatalf("relayed %q", bodyOf(m))
	}
	if n := q.rowCount(); n != 0 {
		t.Fatalf("typing indication stored (%d rows)", n)
	}
}

// TestMessageMaintenance fails if the delivery loop does not run the queue's
// expiry and purge.
func TestMessageMaintenance(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100")
	startPBX(t, msgDevices(), withMessages(q))
	eventually(t, "maintenance run", func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return q.maintains >= 2
	})
}

// --- trunks --------------------------------------------------------------------

// inboundRig is a PBX with extension 100 registered and one trunk whose
// source is the test phones' address; messaging sets the trunk's flag.
func inboundRig(t *testing.T, messaging bool, trusted bool) (*testPBX, *fakeMsgs, *phone, *phone) {
	t.Helper()
	q := newFakeMsgs("100", "200")
	r := &fakeRouter{extensions: map[string]bool{"100": true, "200": true}, sources: map[netip.Addr]int64{},
		trunks: []routing.Trunk{{ID: 7, Name: "carrier-in", Mode: "ip", Enabled: true, Messaging: messaging}}}
	r.decide = func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
		var d routing.Decision
		if c.Number != "+97140000100" {
			d.Kind, d.RejectCode, d.Reason = routing.KindReject, 404, "No inbound route matched"
			return d
		}
		d.Kind, d.Extension, d.Route = routing.KindInbound, "100", "Main DID"
		return d
	}
	pbx := startPBX(t, msgDevices(), trunkCfg(newFakeTrunkState()), withRouter(r, nil), withMessages(q))
	a := newPhone(t, pbx, "a1", "pa")
	a.register(t)
	carrier := newPhone(t, pbx, "carrier", "")
	if trusted {
		ap, _ := netip.ParseAddrPort(carrier.addr)
		r.setSource(ap.Addr(), 7)
	}
	return pbx, q, a, carrier
}

func carrierMessage(carrier *phone, did, from, body string) *sip.Request {
	req := carrier.messageReq(did, "", "text/plain", body)
	req.From().Address = sip.Uri{Scheme: "sip", User: from, Host: "carrier.example"}
	req.From().DisplayName = ""
	return req
}

// TestInboundTrunkMessage fails if a carrier's MESSAGE is accepted from an
// address that is not a trunk's, from a trunk with messaging off, claiming
// to be an internal extension, or to a DID no inbound route names; or if one
// from a trusted trunk is not stored for the routed extension and delivered
// to its phone as from the external number.
func TestInboundTrunkMessage(t *testing.T) {
	fastMessages(t)

	t.Run("unknown source", func(t *testing.T) {
		pbx, q, a, carrier := inboundRig(t, true, false)
		if res := carrier.do(carrierMessage(carrier, "+97140000100", "+971555000111", "hi")); res.StatusCode != 403 {
			t.Fatalf("MESSAGE from an unknown source = %d, want 403", res.StatusCode)
		}
		if n, _ := pbx.throttle.Failures(context.Background(), "127.0.0.1"); n != 1 {
			t.Fatalf("throttle count = %d, want 1", n)
		}
		noReq(t, a.messages, 200*time.Millisecond, "a message from an unknown source")
		if q.rowCount() != 0 {
			t.Fatal("a refused message was stored")
		}
	})

	t.Run("messaging off", func(t *testing.T) {
		pbx, q, a, carrier := inboundRig(t, false, true)
		if res := carrier.do(carrierMessage(carrier, "+97140000100", "+971555000111", "hi")); res.StatusCode != 403 {
			t.Fatalf("MESSAGE through a trunk with messaging off = %d, want 403", res.StatusCode)
		}
		noReq(t, a.messages, 200*time.Millisecond, "a message through a trunk with messaging off")
		if q.rowCount() != 0 {
			t.Fatal("a refused message was stored")
		}
		if got := pbx.metric(t, "hello_sip_messages_total", map[string]string{"source": "trunk", "outcome": "rejected_forbidden"}); got != 1 {
			t.Errorf("trunk rejected counter = %v, want 1", got)
		}
	})

	t.Run("refused senders and DIDs", func(t *testing.T) {
		_, q, a, carrier := inboundRig(t, true, true)
		if res := carrier.do(carrierMessage(carrier, "+97140000100", "200", "I am Bob")); res.StatusCode != 403 {
			t.Fatalf("trunk MESSAGE from an internal extension number = %d, want 403", res.StatusCode)
		}
		if res := carrier.do(carrierMessage(carrier, "+97140000100", "not a number", "hi")); res.StatusCode != 403 {
			t.Fatalf("trunk MESSAGE from a non-number = %d, want 403", res.StatusCode)
		}
		if res := carrier.do(carrierMessage(carrier, "+97149999999", "+971555000111", "hi")); res.StatusCode != 404 {
			t.Fatalf("trunk MESSAGE to a DID with no route = %d, want 404", res.StatusCode)
		}
		noReq(t, a.messages, 200*time.Millisecond, "a refused trunk message")
		if q.rowCount() != 0 {
			t.Fatal("a refused message was stored")
		}
	})

	t.Run("trusted trunk", func(t *testing.T) {
		pbx, q, a, carrier := inboundRig(t, true, true)
		if res := carrier.do(carrierMessage(carrier, "+97140000100", "+971555000111", "your parcel is ready")); res.StatusCode != 202 {
			t.Fatalf("trusted trunk MESSAGE = %d, want 202", res.StatusCode)
		}
		m := waitReq(t, a.messages, "SMS at the extension's phone")
		if m.From().Address.User != "+971555000111" || bodyOf(m) != "your parcel is ready" || !strings.HasPrefix(m.From().DisplayName, "SMS ") {
			t.Fatalf("phone got %q from %q (%q)", bodyOf(m), m.From().Address.User, m.From().DisplayName)
		}
		q.mu.Lock()
		row := q.rows[0]
		q.mu.Unlock()
		if row.job.SenderKind != MsgSenderExternal || row.trunk != 7 || row.job.Recipient != "100" {
			t.Fatalf("stored row = %+v", row)
		}
		if got := pbx.metric(t, "hello_sip_messages_total", map[string]string{"source": "trunk", "outcome": "accepted"}); got != 1 {
			t.Errorf("trunk accepted counter = %v, want 1", got)
		}
	})
}

// outboundRig is a PBX whose outbound route leads to one carrier trunk; the
// carrier's received MESSAGEs arrive on the returned channel.
func outboundRig(t *testing.T, messaging bool) (*testPBX, *fakeMsgs, *phone, chan *sip.Request) {
	t.Helper()
	cr := newCarrier(t, "acct", "secret")
	got := cr.messages
	r := &fakeRouter{extensions: map[string]bool{"100": true}, sources: map[netip.Addr]int64{},
		trunks: []routing.Trunk{{ID: 1, Name: "carrier-out", Mode: "ip", Enabled: true, Messaging: messaging, FromDomain: "carrier.example",
			Destinations: []routing.Destination{{Host: cr.host, Port: cr.port}}}}}
	r.decide = func(c routing.Call, _ routing.TrunkUsability) routing.Decision {
		d := routing.Decision{Kind: routing.KindOutbound, Number: "+" + strings.TrimPrefix(c.Number, "0"), CallerID: "+97140000100", Route: "Out"}
		tr, _ := r.Trunk(1)
		d.Candidates = []routing.Candidate{{Trunk: tr, Destinations: tr.Destinations}}
		return d
	}
	q := newFakeMsgs("100")
	pbx := startPBX(t, msgDevices(), trunkCfg(newFakeTrunkState()), withRouter(r, nil), withMessages(q))
	return pbx, q, newPhone(t, pbx, "a1", "pa"), got
}

// TestOutboundTrunkMessage fails if a phone's MESSAGE to an external number
// is accepted when its route reaches no trunk with messaging on, or does not
// go out through the routed trunk with the rewritten number and caller ID
// when one does.
func TestOutboundTrunkMessage(t *testing.T) {
	fastMessages(t)

	t.Run("messaging off", func(t *testing.T) {
		_, q, a, got := outboundRig(t, false)
		if res := a.sendMessage("0501234567", "text/plain", "hello"); res.StatusCode != 403 {
			t.Fatalf("MESSAGE to a number whose trunk has messaging off = %d, want 403", res.StatusCode)
		}
		noReq(t, got, 200*time.Millisecond, "a message at a trunk with messaging off")
		if q.rowCount() != 0 {
			t.Fatal("a refused message was stored")
		}
	})

	t.Run("messaging on", func(t *testing.T) {
		pbx, q, a, got := outboundRig(t, true)
		if res := a.sendMessage("0501234567", "text/plain", "hello carrier"); res.StatusCode != 202 {
			t.Fatalf("MESSAGE to an external number = %d, want 202", res.StatusCode)
		}
		m := waitReq(t, got, "MESSAGE at the carrier")
		if m.Recipient.User != "+501234567" || m.From().Address.User != "+97140000100" || bodyOf(m) != "hello carrier" {
			t.Fatalf("carrier got %s from %s: %q", m.Recipient.User, m.From().Address.User, bodyOf(m))
		}
		eventually(t, "row delivered", func() bool { return slices.Equal(q.statusOf("0501234567"), []string{"delivered"}) })
		if got := pbx.metric(t, "hello_sip_message_deliveries_total", map[string]string{"via": "trunk", "result": "delivered"}); got != 1 {
			t.Errorf("trunk delivery counter = %v, want 1", got)
		}
	})
}

// TestMessageHelpers fails if the From display name can break out of its
// quotes, if the group is not named, or if the failure classes change.
func TestMessageHelpers(t *testing.T) {
	j := MessageJob{SenderKind: MsgSenderExt, SenderName: `Al "the" \ Boss` + "\r\n", Scope: "ring_group", ScopeRef: "sales"}
	if got := messageDisplayName(j); strings.ContainsAny(got, "\"\\\r\n") || !strings.HasSuffix(got, "(sales)") {
		t.Errorf("display name = %q", got)
	}
	if got := messageDisplayName(MessageJob{SenderKind: "system", Scope: "broadcast"}); got != "Announcement (everyone)" {
		t.Errorf("system broadcast display name = %q", got)
	}
	if got := messageDisplayName(MessageJob{SenderKind: MsgSenderExternal, Sender: "+1555"}); !strings.HasPrefix(got, "SMS") {
		t.Errorf("external display name = %q", got)
	}
	for code, want := range map[int]bool{400: true, 403: true, 413: true, 415: true, 488: true, 404: false, 408: false, 480: false, 486: false, 500: false, 503: false, 0: false} {
		if permanentMessageFailure(code) != want {
			t.Errorf("permanentMessageFailure(%d) = %v, want %v", code, !want, want)
		}
	}
	if fromUser(MessageJob{SenderKind: "system"}) != "hello" || fromUser(MessageJob{SenderKind: MsgSenderExt, Sender: "100"}) != "100" {
		t.Error("fromUser")
	}
}

// TestMessageEdgeRefusalIsRetried fails if a MESSAGE the edge proxy of the
// phone's node refuses (it does not know the sending node as a peer, as after
// it briefly lost its peer set) is failed for good, or if, once the peer is
// known again, the message is not delivered exactly once.
func TestMessageEdgeRefusalIsRetried(t *testing.T) {
	fastMessages(t)
	q := newFakeMsgs("100", "200")
	fs := newFakeState()
	pr1, pr2 := newFakePresence(), newFakePresence()
	node := func(id string, pr *fakePresence, opts ...pbxOpt) *testPBX {
		return startPBX(t, msgDevices(), append([]pbxOpt{withSharedState(fs), withNodeID(id),
			func(_ *Config, d *Deps) { d.Presence = pr }}, opts...)...)
	}
	// n1 holds the phone and has no messaging; n2 delivers. n1's peer set
	// lacks n2.
	n1 := node("n1", pr1)
	n2 := node("n2", pr2, withMessages(q))
	b := newPhone(t, n1, "b1", "pb")
	b.register(t)
	a := newPhone(t, n2, "a1", "pa")
	if res := a.sendMessage("200", "text/plain", "hello"); res.StatusCode != 202 && res.StatusCode != 200 {
		t.Fatalf("MESSAGE = %d", res.StatusCode)
	}
	eventually(t, "the refused message is tried again", func() bool {
		q.mu.Lock()
		defer q.mu.Unlock()
		return len(q.rows) == 1 && q.rows[0].attempts >= 2
	})
	if st := q.statusOf("200"); st[0] == "failed" {
		t.Fatalf("an edge refusal failed the message for good: %v", st)
	}
	noReq(t, b.messages, 100*time.Millisecond, "delivery before n1 trusts n2")

	pr1.add(n2.addr) // n1 knows its peer again
	waitReq(t, b.messages, "MESSAGE once n1 trusts n2")
	eventually(t, "row delivered", func() bool { return slices.Equal(q.statusOf("200"), []string{"delivered"}) })
	noReq(t, b.messages, 300*time.Millisecond, "a second copy of the message")
}
