package store

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
)

// messageRig is a store with extensions 101 (Sales), 102 and 103, each with
// an enabled device.
func messageRig(t *testing.T) (*Store, func(string) int) {
	t.Helper()
	st, db, _ := provStore(t)
	ctx := context.Background()
	for _, n := range []string{"102", "103"} {
		if _, err := st.CreateExtension(ctx, "test", n, "Ext "+n, "", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO devices (extension_id, sip_username, realm, ha1_md5, ha1_sha256)
		SELECT id, 'dev' || number, 'hello.test', 'x', 'x' FROM extensions`); err != nil {
		t.Fatal(err)
	}
	return st, func(q string) int {
		var n int
		if err := db.QueryRowContext(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
}

func direct(to, body string) MessageInput {
	return MessageInput{Sender: "101", SenderKind: MsgKindExtension, SenderName: "Sales",
		TargetKind: MsgKindExtension, Target: to, ContentType: "text/plain", Body: []byte(body)}
}

// TestMessageClaimOnce fails if a pending message can be claimed by more
// than one node, however many claim at once.
func TestMessageClaimOnce(t *testing.T) {
	st, _ := messageRig(t)
	ctx := context.Background()
	const total = 40
	for i := 0; i < total; i++ {
		if _, err := st.SendMessage(ctx, direct("102", "m")); err != nil {
			t.Fatal(err)
		}
	}
	var (
		mu   sync.Mutex
		seen = map[int64]string{}
		wg   sync.WaitGroup
	)
	for _, node := range []string{"n1", "n2", "n3", "n4"} {
		wg.Go(func() {
			for {
				jobs, err := st.ClaimMessages(ctx, node, []string{"102"}, false, 5)
				if err != nil {
					t.Error(err)
					return
				}
				if len(jobs) == 0 {
					return
				}
				mu.Lock()
				for _, j := range jobs {
					if prev, dup := seen[j.ID]; dup {
						t.Errorf("message %d claimed by %s and %s", j.ID, prev, node)
					}
					seen[j.ID] = node
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(seen) != total {
		t.Fatalf("claimed %d of %d messages", len(seen), total)
	}
}

// TestMessageLifecycle fails if a message is claimable for an extension that
// is not online, if a result does not move it correctly (delivered, not
// registered, retry with back-off then failure), or if a claimed message
// is claimable again.
func TestMessageLifecycle(t *testing.T) {
	st, n := messageRig(t)
	ctx := context.Background()
	sent, err := st.SendMessage(ctx, direct("102", "hi"))
	if err != nil || sent.Recipients != 1 || len(sent.Jobs) != 0 {
		t.Fatalf("send = %+v %v", sent, err)
	}
	if exts, ext, err := st.MessagePending(ctx); err != nil || !slices.Equal(exts, []string{"102"}) || ext {
		t.Fatalf("pending = %v %v %v", exts, ext, err)
	}
	if jobs, _ := st.ClaimMessages(ctx, "n1", []string{"103"}, false, 10); len(jobs) != 0 {
		t.Fatalf("claimed %d messages for an extension they are not for", len(jobs))
	}
	jobs, err := st.ClaimMessages(ctx, "n1", []string{"102"}, false, 10)
	if err != nil || len(jobs) != 1 || string(jobs[0].Body) != "hi" || jobs[0].Sender != "101" {
		t.Fatalf("claim = %+v %v", jobs, err)
	}
	if again, _ := st.ClaimMessages(ctx, "n2", []string{"102"}, false, 10); len(again) != 0 {
		t.Fatal("a claimed message was claimed again")
	}
	id := jobs[0].ID
	if err := st.FinishMessage(ctx, id, MsgResultNotRegistered, "not registered"); err != nil {
		t.Fatal(err)
	}
	if n("SELECT count(*) FROM messages WHERE status = 'pending' AND attempts = 0") != 1 {
		t.Fatal("not_registered must requeue without using an attempt")
	}
	jobs, _ = st.ClaimMessages(ctx, "n1", []string{"102"}, false, 10)
	if err := st.FinishMessage(ctx, jobs[0].ID, MsgResultRetry, "timeout"); err != nil {
		t.Fatal(err)
	}
	if n("SELECT count(*) FROM messages WHERE status = 'pending' AND attempts = 1 AND next_attempt_at > now()") != 1 {
		t.Fatal("a retry must wait out a back-off")
	}
	if again, _ := st.ClaimMessages(ctx, "n1", []string{"102"}, false, 10); len(again) != 0 {
		t.Fatal("a message inside its back-off was claimed")
	}
	// After MaxMessageAttempts failures it fails for good.
	if _, err := st.db.ExecContext(ctx, `UPDATE messages SET attempts = $1, next_attempt_at = now()`, MaxMessageAttempts-1); err != nil {
		t.Fatal(err)
	}
	jobs, _ = st.ClaimMessages(ctx, "n1", []string{"102"}, false, 10)
	if err := st.FinishMessage(ctx, jobs[0].ID, MsgResultRetry, "timeout"); err != nil {
		t.Fatal(err)
	}
	if n("SELECT count(*) FROM messages WHERE status = 'failed'") != 1 {
		t.Fatal("the last attempt must fail the message")
	}
	// Finishing an unclaimed message is refused.
	if err := st.FinishMessage(ctx, jobs[0].ID, MsgResultDelivered, ""); !errors.Is(err, ErrNotFound) {
		t.Fatalf("finish of a failed message = %v, want ErrNotFound", err)
	}
}

// TestMessageSendClaimed fails if a single-recipient send with ClaimBy is not
// stored already claimed (and so not claimable by another node).
func TestMessageSendClaimed(t *testing.T) {
	st, n := messageRig(t)
	ctx := context.Background()
	in := direct("102", "now")
	in.ClaimBy = "n1"
	sent, err := st.SendMessage(ctx, in)
	if err != nil || len(sent.Jobs) != 1 || string(sent.Jobs[0].Body) != "now" {
		t.Fatalf("send = %+v %v", sent, err)
	}
	if jobs, _ := st.ClaimMessages(ctx, "n2", []string{"102"}, false, 10); len(jobs) != 0 {
		t.Fatal("another node claimed a message stored claimed")
	}
	if err := st.FinishMessage(ctx, sent.Jobs[0].ID, MsgResultDelivered, "delivered to 1 of 1 contacts"); err != nil {
		t.Fatal(err)
	}
	if n("SELECT count(*) FROM messages WHERE status = 'delivered' AND delivered_at IS NOT NULL") != 1 {
		t.Fatal("not delivered")
	}
}

// TestMessageTargets fails if a send does not resolve groups, broadcasts and
// paging caller lists as documented, or if the sender gets its own group
// message.
func TestMessageTargets(t *testing.T) {
	st, n := messageRig(t)
	ctx := context.Background()
	if _, err := st.db.ExecContext(ctx, `
		INSERT INTO ring_groups (name, strategy) VALUES ('sales', 'ring-all');
		INSERT INTO ring_group_members (group_id, extension_id, position) SELECT (SELECT id FROM ring_groups), id, row_number() OVER (ORDER BY number) FROM extensions;
		INSERT INTO paging_groups (name, code) VALUES ('floor', '*88');
		INSERT INTO paging_group_members (group_id, extension_id) SELECT (SELECT id FROM paging_groups), id FROM extensions WHERE number <> '101';
		INSERT INTO paging_group_callers (group_id, extension_id) SELECT (SELECT id FROM paging_groups), id FROM extensions WHERE number = '102'`); err != nil {
		t.Fatal(err)
	}
	send := func(kind, target, sender string) (MessageSent, error) {
		in := direct(target, "x")
		in.TargetKind, in.Sender = kind, sender
		return st.SendMessage(ctx, in)
	}
	if s, err := send(MsgScopeRingGroup, "sales", "101"); err != nil || s.Recipients != 2 || s.Scope != MsgScopeRingGroup {
		t.Fatalf("ring group send = %+v %v, want 2 recipients without the sender", s, err)
	}
	if _, err := send(MsgScopePagingGroup, "*88", "101"); !errors.Is(err, ErrMessageForbidden) {
		t.Fatalf("paging by a non-caller = %v, want ErrMessageForbidden", err)
	}
	if s, err := send(MsgScopePagingGroup, "floor", "102"); err != nil || s.Recipients != 1 {
		t.Fatalf("paging by a caller = %+v %v, want 1 recipient (103)", s, err)
	}
	if _, err := send(MsgKindExtension, "999", "101"); !errors.Is(err, ErrMessageTarget) {
		t.Fatalf("unknown extension = %v", err)
	}
	if _, err := send(MsgScopeRingGroup, "none", "101"); !errors.Is(err, ErrMessageTarget) {
		t.Fatalf("unknown group = %v", err)
	}
	b := MessageInput{SenderKind: MsgSenderSystem, TargetKind: MsgScopeBroadcast, ContentType: "text/plain", Body: []byte("Fire drill"), Audit: true, Actor: "user:admin"}
	if s, err := st.SendMessage(ctx, b); err != nil || s.Recipients != 3 {
		t.Fatalf("broadcast = %+v %v, want 3", s, err)
	}
	if n("SELECT count(*) FROM audit_events WHERE action = 'send_broadcast' AND resource_id NOT LIKE '%Fire%'") != 1 {
		t.Fatal("broadcast not audited without its text")
	}
	// External numbers need a trunk with messaging on.
	e := MessageInput{SenderKind: MsgKindExtension, Sender: "101", TargetKind: MsgKindExternal, Target: "+32475000000", Body: []byte("x")}
	if _, err := st.SendMessage(ctx, e); !errors.Is(err, ErrNoExternalTrunk) {
		t.Fatalf("external without a trunk = %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO trunks (name, mode, enabled, messaging) VALUES ('c', 'ip', true, true)`); err != nil {
		t.Fatal(err)
	}
	if s, err := st.SendMessage(ctx, e); err != nil || s.Recipients != 1 {
		t.Fatalf("external = %+v %v", s, err)
	}
	if _, ext, _ := st.MessagePending(ctx); !ext {
		t.Fatal("external message not pending")
	}
}

// TestMessageLimits fails if the per-sender rate, the per-extension queue
// bound, the TTL expiry, the retention purge, the off switch or a body in
// binary are not honoured.
func TestMessageLimits(t *testing.T) {
	st, n := messageRig(t)
	ctx := context.Background()
	set, err := st.GetMessageSettings(ctx)
	if err != nil || !set.Enabled || set.TTLHours != 24 || set.RetentionDays != 30 {
		t.Fatalf("defaults = %+v %v", set, err)
	}
	set.RatePerMinute, set.MaxQueuedPerExtension = 3, 2
	if _, err := st.PutMessageSettings(ctx, "test", set); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if _, err := st.SendMessage(ctx, direct("102", "m")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.SendMessage(ctx, direct("102", "m")); !errors.Is(err, ErrMessageRate) {
		t.Fatalf("fourth send in a minute = %v, want ErrMessageRate", err)
	}
	if n("SELECT count(*) FROM messages WHERE status = 'pending'") != 2 || n("SELECT count(*) FROM messages WHERE status = 'failed' AND detail LIKE 'queue full%'") != 1 {
		t.Fatal("the queue bound must drop the oldest waiting message")
	}

	// Expiry, a lost node and retention.
	if _, err := st.db.ExecContext(ctx, `UPDATE messages SET expires_at = now() - interval '1 minute' WHERE status = 'pending'`); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := st.ClaimMessages(ctx, "n1", []string{"102"}, false, 10); len(jobs) != 0 {
		t.Fatal("an expired message was claimed")
	}
	expired, _, err := st.MaintainMessages(ctx)
	if err != nil || expired != 2 {
		t.Fatalf("maintain expired %d (%v), want 2", expired, err)
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE messages SET created_at = now() - interval '31 days'`); err != nil {
		t.Fatal(err)
	}
	if _, purged, err := st.MaintainMessages(ctx); err != nil || purged != 3 {
		t.Fatalf("maintain purged %d (%v), want all 3 finished messages", purged, err)
	}

	// A claimed message whose node died returns to the queue.
	set.RatePerMinute, set.MaxQueuedPerExtension = 30, 100
	if _, err := st.PutMessageSettings(ctx, "test", set); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SendMessage(ctx, direct("103", "m")); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := st.ClaimMessages(ctx, "dead", []string{"103"}, false, 10); len(jobs) != 1 {
		t.Fatal("claim")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE messages SET claimed_at = now() - interval '10 minutes' WHERE status = 'sending'`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := st.MaintainMessages(ctx); err != nil {
		t.Fatal(err)
	}
	if jobs, _ := st.ClaimMessages(ctx, "alive", []string{"103"}, false, 10); len(jobs) != 1 {
		t.Fatal("a message of a dead node was not returned to the queue")
	}

	// Off refuses, a purge keeps waiting messages.
	set.Enabled = false
	if _, err := st.PutMessageSettings(ctx, "test", set); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SendMessage(ctx, direct("102", "m")); !errors.Is(err, ErrMessagesDisabled) {
		t.Fatalf("send while off = %v", err)
	}
	if _, err := st.SendMessage(ctx, MessageInput{SenderKind: MsgKindExtension, Sender: "101", TargetKind: MsgKindExtension, Target: "102",
		Body: make([]byte, MaxMessageBytes+1)}); !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("oversize = %v", err)
	}
}

// TestMessageListing fails if a group send is not one history entry with its
// recipient counts, if conversations are not keyed by the two parties, if
// search misses the text, or if the extension filter shows a stranger's
// messages.
func TestMessageListing(t *testing.T) {
	st, _ := messageRig(t)
	ctx := context.Background()
	for _, in := range []MessageInput{direct("102", "lunch at noon"), {
		SenderKind: MsgSenderSystem, TargetKind: MsgScopeBroadcast, ContentType: "text/plain", Body: []byte("Fire drill at 3pm")}} {
		if _, err := st.SendMessage(ctx, in); err != nil {
			t.Fatal(err)
		}
	}
	// 103 sends to 102 as well.
	other := direct("102", "private to 103")
	other.Sender = "103"
	if _, err := st.SendMessage(ctx, other); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.ClaimMessages(ctx, "n", []string{"101"}, false, 10)
	if len(jobs) != 1 {
		t.Fatalf("claimed %d", len(jobs))
	}
	if err := st.FinishMessage(ctx, jobs[0].ID, MsgResultDelivered, ""); err != nil {
		t.Fatal(err)
	}

	all, err := st.ListMessages(ctx, MessageFilter{}, 0, 50)
	if err != nil || len(all) != 3 {
		t.Fatalf("history = %d entries (%v), want 3 sends", len(all), err)
	}
	var drill *Message
	for i := range all {
		if all[i].Scope == MsgScopeBroadcast {
			drill = &all[i]
		}
	}
	if drill == nil || drill.Recipients != 3 || drill.Delivered != 1 || drill.Pending != 2 || drill.Status != MsgPending {
		t.Fatalf("broadcast entry = %+v", drill)
	}
	if hits, _ := st.ListMessages(ctx, MessageFilter{Query: "FIRE DRILL"}, 0, 50); len(hits) != 1 {
		t.Fatalf("search hits = %d, want 1", len(hits))
	}
	if hits, _ := st.ListMessages(ctx, MessageFilter{Query: "%"}, 0, 50); len(hits) != 0 {
		t.Fatal("a wildcard matched everything")
	}
	convs, err := st.ListMessageConversations(ctx, MessageFilter{}, 50)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, c := range convs {
		keys[c.Key] = true
	}
	for _, want := range []string{"101>102", "102>103", "broadcast"} {
		if !keys[want] {
			t.Errorf("conversations %v lack %s", keys, want)
		}
	}
	mine, _ := st.ListMessages(ctx, MessageFilter{Extension: "103"}, 0, 50)
	for _, m := range mine {
		if m.Sender != "103" && m.Recipient != "103" {
			t.Errorf("103's view holds %+v", m)
		}
	}
	thread, _ := st.ListMessages(ctx, MessageFilter{Extension: "103", Conversation: "101>102"}, 0, 50)
	if len(thread) != 0 {
		t.Fatalf("a conversation key reached another pair's messages: %+v", thread)
	}
	// Paging by cursor.
	first, _ := st.ListMessages(ctx, MessageFilter{}, 0, 1)
	rest, _ := st.ListMessages(ctx, MessageFilter{}, first[0].ID, 50)
	if len(first) != 1 || len(rest) != 2 {
		t.Fatalf("pages = %d + %d", len(first), len(rest))
	}
}

// TestMessageBinaryBody fails if a non-text body does not round-trip byte for
// byte, or a text body is not kept as text.
func TestMessageBinaryBody(t *testing.T) {
	st, _ := messageRig(t)
	ctx := context.Background()
	raw := []byte{0, 1, 2, 0xff, 0xfe}
	in := direct("102", "")
	in.ContentType, in.Body = "application/octet-stream", raw
	if _, err := st.SendMessage(ctx, in); err != nil {
		t.Fatal(err)
	}
	text := direct("102", "héllo")
	if _, err := st.SendMessage(ctx, text); err != nil {
		t.Fatal(err)
	}
	jobs, _ := st.ClaimMessages(ctx, "n", []string{"102"}, false, 10)
	if len(jobs) != 2 || string(jobs[0].Body) != string(raw) || string(jobs[1].Body) != "héllo" {
		t.Fatalf("jobs = %+v", jobs)
	}
	list, _ := st.ListMessages(ctx, MessageFilter{}, 0, 10)
	for _, m := range list {
		if m.ContentType == "application/octet-stream" && (m.Body != "" || m.BodyBytes != len(raw)) {
			t.Errorf("binary entry = %+v", m)
		}
	}
}
