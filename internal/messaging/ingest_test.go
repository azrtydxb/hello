package messaging_test

// The inbox against PostgreSQL: the ingest the SIP nodes drive, opt-outs,
// sending through the SIP text queue, assignment, the live-update log,
// retention and erase. They skip without HELLO_TEST_DATABASE_URL.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/migrate"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/secret"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

const (
	did      = "+3270123456"
	customer = "+32475123456"
)

type rig struct {
	t       *testing.T
	dsn     string
	db      *sql.DB
	st      *store.Store
	trunk   int64
	channel store.MsgChannel
	addr    store.MsgAddress
	admin   int64
	agent   int64
	other   int64
}

func (r *rig) admAcc() store.InboxAccess {
	return store.InboxAccess{UserID: r.admin, All: true, Actor: "user:admin", Name: "admin"}
}
func (r *rig) agentAcc() store.InboxAccess {
	return store.InboxAccess{UserID: r.agent, Actor: "user:agent", Name: "agent"}
}
func (r *rig) otherAcc() store.InboxAccess {
	return store.InboxAccess{UserID: r.other, Actor: "user:other", Name: "other"}
}

// newStoreOn opens another store (another "pod") on the same database.
func (r *rig) newStoreOn() *store.Store {
	r.t.Helper()
	db, err := sql.Open("pgx", r.dsn)
	if err != nil {
		r.t.Fatal(err)
	}
	r.t.Cleanup(func() { _ = db.Close() })
	return store.New(db).WithSecretBox(testBox(r.t))
}

func testBox(t *testing.T) *secret.Box {
	t.Helper()
	b, err := secret.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newRig migrates a scratch database and sets up a trunk with SMS over SIP,
// a channel with one number and three users (an admin, an agent, another).
func newRig(t *testing.T) *rig {
	t.Helper()
	base := os.Getenv("HELLO_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("HELLO_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	cfg, err := pgx.ParseConfig(base)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*cfg)
	t.Cleanup(func() { _ = admin.Close() })
	name := fmt.Sprintf("hello_msg_%d", time.Now().UnixNano())
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.ExecContext(context.Background(), "DROP DATABASE "+name+" WITH (FORCE)") })
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	r := &rig{t: t, dsn: u.String()}
	if r.db, err = sql.Open("pgx", r.dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.db.Close() })
	if _, err := migrate.Up(ctx, r.db); err != nil {
		t.Fatal(err)
	}
	r.st = store.New(r.db).WithSecretBox(testBox(t))
	if r.admin, err = r.st.CreateUser(ctx, "test", "admin", "x", auth.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if r.agent, err = r.st.CreateUser(ctx, "test", "agent", "x", auth.RoleUser); err != nil {
		t.Fatal(err)
	}
	if r.other, err = r.st.CreateUser(ctx, "test", "other", "x", auth.RoleUser); err != nil {
		t.Fatal(err)
	}
	if err := r.db.QueryRowContext(ctx, `INSERT INTO trunks (name, mode, messaging) VALUES ('carrier', 'ip', TRUE) RETURNING id`).Scan(&r.trunk); err != nil {
		t.Fatal(err)
	}
	if r.channel, err = r.st.CreateMsgChannel(ctx, "test", store.MsgChannelInput{
		Kind: "sms", Provider: "sip_trunk", Name: "Main", Enabled: true, TrunkID: &r.trunk}); err != nil {
		t.Fatal(err)
	}
	r.addr = r.address(did)
	return r
}

func (r *rig) address(number string) store.MsgAddress {
	r.t.Helper()
	key, _ := phonebook.Key(number)
	suffix, _ := phonebook.Suffix(key)
	a, err := r.st.CreateMsgAddress(context.Background(), "test", store.MsgAddressInput{
		ChannelID: r.channel.ID, Address: number, Key: key, Suffix: suffix, Enabled: true})
	if err != nil {
		r.t.Fatal(err)
	}
	return a
}

var eventSeq atomic.Int64

// text is one trunk delivery with a fresh event key.
func (r *rig) text(from, body string) store.TrunkText {
	return r.textKey(from, body, fmt.Sprintf("ev-%d", eventSeq.Add(1)))
}

func (r *rig) textKey(from, body, key string) store.TrunkText {
	toKey, _ := phonebook.Key(did)
	suffix, _ := phonebook.Suffix(toKey)
	fromKey, _ := phonebook.Key(from)
	return store.TrunkText{TrunkID: r.trunk, ToKey: toKey, ToSuffix: suffix, FromKey: fromKey, Body: body, EventKey: key}
}

// ingest delivers a text and fails the test if the ingest errors.
func (r *rig) ingest(from, body string) store.IngestResult {
	r.t.Helper()
	res, err := r.st.IngestTrunkText(context.Background(), r.text(from, body))
	if err != nil {
		r.t.Fatal(err)
	}
	return res
}

func (r *rig) count(q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// TestIngestIdempotentAcrossPods fails if the same event delivered at once
// to two ingest instances creates two messages or two conversations, if
// either returns an error for the duplicate, if texts of one new peer that
// arrive at once make more than one conversation, or if a number that is no
// channel address is handled.
func TestIngestIdempotentAcrossPods(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	pods := []*store.Store{r.st, r.newStoreOn()}

	t.Run("same event twice at once", func(t *testing.T) {
		in := r.textKey(customer, "hello there", "dup-1")
		var wg sync.WaitGroup
		results := make([]store.IngestResult, 8)
		errs := make([]error, 8)
		for i := range results {
			wg.Go(func() { results[i], errs[i] = pods[i%2].IngestTrunkText(ctx, in) })
		}
		wg.Wait()
		stored := 0
		for i, err := range errs {
			if err != nil {
				t.Fatalf("delivery %d: %v", i, err)
			}
			if !results[i].Handled {
				t.Errorf("delivery %d not handled", i)
			}
			if !results[i].Duplicate {
				stored++
			}
		}
		if stored != 1 {
			t.Errorf("%d deliveries stored, want 1", stored)
		}
		if n := r.count(`SELECT count(*) FROM conversation_messages WHERE body = 'hello there'`); n != 1 {
			t.Errorf("%d messages, want 1", n)
		}
		if n := r.count(`SELECT count(*) FROM conversations`); n != 1 {
			t.Errorf("%d conversations, want 1", n)
		}
	})

	t.Run("a new peer's texts at once", func(t *testing.T) {
		var wg sync.WaitGroup
		for i := range 12 {
			wg.Go(func() {
				if _, err := pods[i%2].IngestTrunkText(ctx, r.text("+32475999000", fmt.Sprintf("text %d", i))); err != nil {
					t.Error(err)
				}
			})
		}
		wg.Wait()
		if n := r.count(`SELECT count(*) FROM conversations WHERE peer_key = '+32475999000'`); n != 1 {
			t.Errorf("%d conversations for one peer, want 1", n)
		}
		if n := r.count(`SELECT count(*) FROM conversation_messages m JOIN conversations c ON c.id = m.conversation_id
			WHERE c.peer_key = '+32475999000' AND m.kind = 'message'`); n != 12 {
			t.Errorf("%d messages, want 12", n)
		}
	})

	t.Run("a number that is not a channel address", func(t *testing.T) {
		in := r.text(customer, "x")
		in.ToKey, in.ToSuffix = "+3299999999", "299999999"
		before := r.count(`SELECT count(*) FROM conversation_messages`)
		res, err := r.st.IngestTrunkText(ctx, in)
		if err != nil || res.Handled {
			t.Fatalf("res = %+v, err = %v; want not handled", res, err)
		}
		if n := r.count(`SELECT count(*) FROM conversation_messages`); n != before {
			t.Error("a text to an unknown number was stored")
		}
		// A disabled number, a disabled channel and another trunk are no
		// channel address either.
		in = r.text(customer, "y")
		in.TrunkID = r.trunk + 1000
		if res, _ := r.st.IngestTrunkText(ctx, in); res.Handled {
			t.Error("a text from another trunk was handled")
		}
	})

	t.Run("the number matches with or without the plus", func(t *testing.T) {
		in := r.text(customer, "no plus")
		in.ToKey = "3270123456"
		in.ToSuffix = "270123456"
		res, err := r.st.IngestTrunkText(ctx, in)
		if err != nil || !res.Handled || res.Duplicate {
			t.Fatalf("res = %+v, err = %v", res, err)
		}
	})
}

// TestIngestFailureRetries fails if an ingest whose transaction fails
// reports success, or leaves its event in the ledger so the carrier's retry
// is taken for a duplicate and lost.
func TestIngestFailureRetries(t *testing.T) {
	r := newRig(t)
	in := r.textKey(customer, "will retry", "retry-1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.st.IngestTrunkText(ctx, in); err == nil {
		t.Fatal("an ingest with a dead context succeeded")
	}
	if n := r.count(`SELECT count(*) FROM messaging_events`); n != 0 {
		t.Fatalf("%d ledger rows after a failed ingest, want 0", n)
	}
	res, err := r.st.IngestTrunkText(context.Background(), in)
	if err != nil || res.Duplicate || res.MessageID == 0 {
		t.Fatalf("the retry: %+v, %v; want it stored", res, err)
	}
}

// TestOptOutEnforcement fails if STOP in any case does not opt the customer
// out, a sentence containing stop does, an agent can still text an opted-out
// customer, START does not clear it, the confirmation is sent more than
// once, or an erased customer who opted out can be texted again.
func TestOptOutEnforcement(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, err := r.st.IngestTrunkText(ctx, r.text(customer, "please stop calling me at 5"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Replies) != 0 || r.count(`SELECT count(*) FROM sms_opt_outs`) != 0 {
		t.Fatal("a sentence containing stop opted the customer out")
	}
	conv := res.ConversationID
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "sorry about that"}); err != nil {
		t.Fatalf("sending before the opt-out: %v", err)
	}

	res, err = r.st.IngestTrunkText(ctx, r.text(customer, "STOP"))
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Replies) != 1 || res.Replies[0].To != customer || res.Replies[0].From != did || res.Replies[0].Body == "" {
		t.Fatalf("replies = %+v, want one confirmation", res.Replies)
	}
	again, _ := r.st.IngestTrunkText(ctx, r.text(customer, "stop."))
	if len(again.Replies) != 0 {
		t.Error("the confirmation was sent twice")
	}
	_, _, err = r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "one more"})
	if !errors.Is(err, store.ErrOptedOut) {
		t.Fatalf("send to an opted-out customer = %v, want ErrOptedOut", err)
	}
	if n := r.count(`SELECT count(*) FROM messages WHERE body_text = 'one more'`); n != 0 {
		t.Error("a refused send reached the SIP queue")
	}
	c, err := r.st.GetConversation(ctx, r.admAcc(), conv)
	if err != nil || !c.OptedOut {
		t.Fatalf("conversation = %+v, %v; want optedOut", c, err)
	}
	if opts, _ := r.st.ListOptOuts(ctx, store.OptOutFilter{}, 10); len(opts) != 1 || opts[0].Peer != customer {
		t.Errorf("opt-outs = %+v", opts)
	}

	// START clears it.
	if _, err := r.st.IngestTrunkText(ctx, r.text(customer, "Start")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "welcome back"}); err != nil {
		t.Fatalf("send after START: %v", err)
	}

	// An administrator records an opt-out by hand and, later, re-consent.
	o, err := r.st.RecordOptOut(ctx, "user:admin", r.addr.ID, customer, "asked by phone")
	if err != nil || o.State != "out" || o.Source != "admin" {
		t.Fatalf("RecordOptOut = %+v, %v", o, err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "no"}); !errors.Is(err, store.ErrOptedOut) {
		t.Fatalf("after a hand-recorded opt-out: %v", err)
	}
	if _, err := r.st.Reconsent(ctx, "user:admin", o.ID, "verbal, 2026-10-26"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "yes"}); err != nil {
		t.Fatalf("after re-consent: %v", err)
	}

	// Opt out again, erase the customer: the hash alone remains and still
	// blocks a send into the conversation they start next.
	if _, err := r.st.IngestTrunkText(ctx, r.text(customer, "unsubscribe")); err != nil {
		t.Fatal(err)
	}
	rep, err := r.st.ErasePeer(ctx, "user:admin", customer)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Conversations != 1 || rep.OptOutsKept != 1 || len(rep.Providers) != 1 {
		t.Fatalf("erase report = %+v", rep)
	}
	if n := r.count(`SELECT count(*) FROM conversations`) + r.count(`SELECT count(*) FROM conversation_messages`); n != 0 {
		t.Errorf("%d rows left after an erase", n)
	}
	if n := r.count(`SELECT count(*) FROM sms_opt_outs WHERE peer_key IS NOT NULL`); n != 0 {
		t.Error("an opt-out still names the erased number")
	}
	res, err = r.st.IngestTrunkText(ctx, r.text(customer, "hello again"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: res.ConversationID, Body: "hi"}); !errors.Is(err, store.ErrOptedOut) {
		t.Fatalf("a send after the erase = %v, want ErrOptedOut: the hash must still block", err)
	}
	// The erase left no number or text in the audit trail.
	for _, bad := range []string{customer, "STOP", "hello again"} {
		if n := r.count(`SELECT count(*) FROM audit_events WHERE action LIKE '%' || $1 || '%' OR resource_id LIKE '%' || $1 || '%'`, bad); n != 0 {
			t.Errorf("%q appears in %d audit rows", bad, n)
		}
	}
}

// TestHelpKeyword fails if HELP does not answer with the configured text, or
// answers when none is configured.
func TestHelpKeyword(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, _ := r.st.IngestTrunkText(ctx, r.text(customer, "help"))
	if len(res.Replies) != 0 {
		t.Fatal("HELP answered with no help text configured")
	}
	if _, err := r.st.UpdateMsgAddress(ctx, "test", r.addr.ID, store.MsgAddressInput{Enabled: true, HelpText: "Call 0800 1234"}); err != nil {
		t.Fatal(err)
	}
	res, _ = r.st.IngestTrunkText(ctx, r.text(customer, "HELP"))
	if len(res.Replies) != 1 || res.Replies[0].Body != "Call 0800 1234" {
		t.Fatalf("replies = %+v", res.Replies)
	}
}

// TestSendThroughTheQueue fails if an inbox reply does not reach the SIP
// text queue from the customer's number through the channel's trunk, if a
// retried send with the same clientMsgId sends twice, if a long reply is not
// split, or if the queue's result does not reach the message.
func TestSendThroughTheQueue(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, err := r.st.IngestTrunkText(ctx, r.text(customer, "I need help"))
	if err != nil {
		t.Fatal(err)
	}
	conv := res.ConversationID

	if _, _, err := r.st.SendConversationText(ctx, r.agentAcc(), store.SendInput{ConversationID: conv, Body: "on it", ClientMsgID: "c1"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an agent replied to a conversation that is not theirs: %v", err)
	}
	if _, err := r.st.ClaimConversation(ctx, r.admAcc(), conv); err != nil {
		t.Fatal(err)
	}
	m, created, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "on it", ClientMsgID: "c1"})
	if err != nil || !created || m.Status != "sending" || m.SenderName != "admin" {
		t.Fatalf("send = %+v, %v, %v", m, created, err)
	}
	again, created, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "on it", ClientMsgID: "c1"})
	if err != nil || created || again.ID != m.ID {
		t.Fatalf("retried send = %+v, %v, %v; want the first message", again, created, err)
	}
	var recipient, sender, kind string
	var trunk int64
	if err := r.db.QueryRowContext(ctx, `SELECT recipient, sender, sender_kind, trunk_id FROM messages WHERE conversation_message_id = $1`, m.ID).
		Scan(&recipient, &sender, &kind, &trunk); err != nil {
		t.Fatal(err)
	}
	if recipient != customer || sender != did || kind != "system" || trunk != r.trunk {
		t.Errorf("queued as %s from %s (%s) via trunk %d", recipient, sender, kind, trunk)
	}
	if n := r.count(`SELECT count(*) FROM messages WHERE conversation_message_id = $1`, m.ID); n != 1 {
		t.Fatalf("a retried send queued %d rows, want 1", n)
	}

	// The trunk accepts it: sent, never delivered.
	jobs, err := r.st.ClaimMessages(ctx, "n1", nil, true, 10)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("claim = %v, %v", jobs, err)
	}
	if err := r.st.FinishMessage(ctx, jobs[0].ID, "delivered", "accepted by trunk carrier"); err != nil {
		t.Fatal(err)
	}
	got := mustMessage(t, r, m.ID)
	if got.Status != "sent" || got.SentAt == nil {
		t.Fatalf("after the trunk accepted it: %+v", got)
	}

	// A long reply is split into parts that fit one SIP MESSAGE; one part
	// failing fails the message.
	long := strings.Repeat("word ", 400)
	lm, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: long})
	if err != nil {
		t.Fatal(err)
	}
	if n := r.count(`SELECT count(*) FROM messages WHERE conversation_message_id = $1`, lm.ID); n < 3 {
		t.Fatalf("a %d byte reply made %d parts, want 3 or more", len(long), n)
	}
	jobs, _ = r.st.ClaimMessages(ctx, "n1", nil, true, 10)
	for i, j := range jobs {
		result := "delivered"
		if i == len(jobs)-1 {
			result = "failed"
		}
		if err := r.st.FinishMessage(ctx, j.ID, result, "trunk carrier refused: Forbidden"); err != nil {
			t.Fatal(err)
		}
	}
	if got := mustMessage(t, r, lm.ID); got.Status != "failed" || !strings.Contains(got.ErrorCode, "refused") {
		t.Fatalf("a refused part: %+v", got)
	}

	// Replying to a disabled number is refused before anything is queued.
	if _, err := r.st.UpdateMsgAddress(ctx, "test", r.addr.ID, store.MsgAddressInput{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	before := r.count(`SELECT count(*) FROM messages`)
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "x"}); !errors.Is(err, store.ErrChannelUnavailable) {
		t.Fatalf("send on a disabled number = %v", err)
	}
	if r.count(`SELECT count(*) FROM messages`) != before {
		t.Error("a refused send reached the queue")
	}
}

func mustMessage(t *testing.T, r *rig, id int64) store.ConvMessage {
	t.Helper()
	var convID int64
	if err := r.db.QueryRowContext(context.Background(), `SELECT conversation_id FROM conversation_messages WHERE id = $1`, id).Scan(&convID); err != nil {
		t.Fatal(err)
	}
	ms, _, err := r.st.ListConversationMessages(context.Background(), r.admAcc(), convID, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range ms {
		if m.ID == id {
			return m
		}
	}
	t.Fatalf("message %d not listed", id)
	return store.ConvMessage{}
}

// TestUnknownOutcome fails if a reply whose queue rows are gone is not
// marked unknown after five minutes.
func TestUnknownOutcome(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, _ := r.st.IngestTrunkText(ctx, r.text(customer, "hi"))
	m, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: res.ConversationID, Body: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `DELETE FROM messages`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversation_messages SET created_at = now() - interval '6 minutes' WHERE id = $1`, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.MaintainMessages(ctx); err != nil {
		t.Fatal(err)
	}
	if got := mustMessage(t, r, m.ID); got.Status != "unknown" {
		t.Fatalf("status = %q, want unknown", got.Status)
	}
}

// TestAssignmentClaim fails if two agents can both claim a conversation, a
// claim of an assigned one succeeds, a user reaches a conversation that is
// not theirs, an assignment to a user who cannot take it is accepted, or a
// conversation handed over stays visible to its old owner.
func TestAssignmentClaim(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, _ := r.st.IngestTrunkText(ctx, r.text(customer, "hi"))
	conv := res.ConversationID

	if _, err := r.st.GetConversation(ctx, r.agentAcc(), conv); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an agent saw an unassigned conversation: %v", err)
	}
	// Two supervisors claim at once: one wins.
	var wins, losses atomic.Int32
	var wg sync.WaitGroup
	for _, acc := range []store.InboxAccess{r.admAcc(), {UserID: r.agent, All: true, Actor: "user:agent", Name: "agent"}} {
		for range 3 {
			wg.Go(func() {
				pod := r.newStoreOn()
				switch _, err := pod.ClaimConversation(ctx, acc, conv); {
				case err == nil:
					wins.Add(1)
				case errors.Is(err, store.ErrConversationState):
					losses.Add(1)
				default:
					t.Errorf("claim: %v", err)
				}
			})
		}
	}
	wg.Wait()
	if wins.Load() != 1 || losses.Load() != 5 {
		t.Fatalf("%d wins, %d losses; want 1 and 5", wins.Load(), losses.Load())
	}

	c, _ := r.st.GetConversation(ctx, r.admAcc(), conv)
	if c.AssigneeUserID == nil {
		t.Fatal("nobody holds the conversation")
	}
	// A supervisor hands it to the agent, who then reaches it and nobody
	// else (not being a supervisor) does.
	if _, err := r.st.AssignConversation(ctx, r.admAcc(), conv, r.agent); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.GetConversation(ctx, r.agentAcc(), conv); err != nil {
		t.Fatalf("the assignee cannot read their conversation: %v", err)
	}
	if _, err := r.st.GetConversation(ctx, r.otherAcc(), conv); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("another agent read it: %v", err)
	}
	if _, err := r.st.AssignConversation(ctx, r.otherAcc(), conv, r.other); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a stranger assigned it: %v", err)
	}
	if _, err := r.st.AssignConversation(ctx, r.agentAcc(), conv, 99999); !errors.Is(err, store.ErrNotAgent) {
		t.Fatalf("assigned to a user that does not exist: %v", err)
	}
	if _, err := r.st.AssignConversation(ctx, r.agentAcc(), conv, 0); !errors.Is(err, store.ErrNotAgent) {
		t.Fatalf("an agent returned it to the pool: %v", err)
	}
	// The assignee hands it over; the old owner no longer reaches it.
	moved, err := r.st.AssignConversation(ctx, r.agentAcc(), conv, r.other)
	if err != nil || moved.AssigneeUserID == nil || *moved.AssigneeUserID != r.other {
		t.Fatalf("hand over = %+v, %v", moved, err)
	}
	if _, err := r.st.GetConversation(ctx, r.agentAcc(), conv); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the old owner still reaches it: %v", err)
	}
	// A supervisor returns it to the pool; it can be claimed again.
	if _, err := r.st.AssignConversation(ctx, r.admAcc(), conv, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.ClaimConversation(ctx, r.otherAcc(), conv); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("an agent claimed from a pool they cannot see: %v", err)
	}
	if n := r.count(`SELECT count(*) FROM conversation_assignments WHERE conversation_id = $1`, conv); n < 4 {
		t.Errorf("%d assignment rows, want the history", n)
	}
	// A viewer cannot be assigned a conversation.
	var viewer int64
	if err := r.db.QueryRowContext(ctx, `INSERT INTO users (username, password_hash, role) VALUES ('vera', 'x', 'viewer') RETURNING id`).Scan(&viewer); err != nil {
		t.Fatal(err)
	}
	if _, err := r.st.AssignConversation(ctx, r.admAcc(), conv, viewer); !errors.Is(err, store.ErrNotAgent) {
		t.Fatalf("assigned to a viewer: %v", err)
	}
}

// TestReplyClaimsAndReopens fails if replying to an unassigned conversation
// does not claim it, replying to a resolved one does not reopen it, a new
// text on a resolved conversation inside the reopen window does not reopen
// it, or one outside the window does not start a new conversation.
func TestReplyClaimsAndReopens(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, _ := r.st.IngestTrunkText(ctx, r.text(customer, "hi"))
	conv := res.ConversationID
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "hello"}); err != nil {
		t.Fatal(err)
	}
	c, _ := r.st.GetConversation(ctx, r.admAcc(), conv)
	if c.AssigneeUserID == nil || *c.AssigneeUserID != r.admin || c.FirstResponseAt == nil {
		t.Fatalf("after the first reply: %+v", c)
	}
	if c, err := r.st.SetConversationStatus(ctx, r.admAcc(), conv, "resolved", nil); err != nil || c.ResolvedAt == nil {
		t.Fatalf("resolve = %+v, %v", c, err)
	}
	// Inside the window, a new text reopens the same conversation.
	res2, err := r.st.IngestTrunkText(ctx, r.text(customer, "one more thing"))
	if err != nil || res2.ConversationID != conv {
		t.Fatalf("a text after resolve: %+v, %v; want conversation %d", res2, err, conv)
	}
	c, _ = r.st.GetConversation(ctx, r.admAcc(), conv)
	if c.Status != "open" || c.ResolvedAt != nil {
		t.Fatalf("after the text: status %q resolvedAt %v", c.Status, c.ResolvedAt)
	}
	// Resolved again, then a reply reopens it.
	if _, err := r.st.SetConversationStatus(ctx, r.admAcc(), conv, "resolved", nil); err != nil {
		t.Fatal(err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "reopening"}); err != nil {
		t.Fatal(err)
	}
	if c, _ := r.st.GetConversation(ctx, r.admAcc(), conv); c.Status != "open" {
		t.Fatalf("status after a reply = %q", c.Status)
	}
	// Outside the window a new conversation starts, and the old one cannot
	// be reopened beside it.
	if _, err := r.st.SetConversationStatus(ctx, r.admAcc(), conv, "resolved", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET last_message_at = now() - interval '8 days', resolved_at = now() - interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	res3, err := r.st.IngestTrunkText(ctx, r.text(customer, "a new matter"))
	if err != nil || res3.ConversationID == conv {
		t.Fatalf("a text after the window: %+v, %v; want a new conversation", res3, err)
	}
	if _, err := r.st.SetConversationStatus(ctx, r.admAcc(), conv, "open", nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("reopening beside an open conversation = %v, want ErrConflict", err)
	}
}

// TestInboxEvents fails if an event does not reach the supervisors while a
// conversation is unassigned and its assignee afterwards, if a user gets
// another user's event, if the ids are not increasing, or if a stream
// resuming after an id repeats or skips events.
func TestInboxEvents(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, _ := r.st.IngestTrunkText(ctx, r.text(customer, "hi"))
	conv := res.ConversationID

	ev := func(user int64, after int64) []store.InboxEvent {
		t.Helper()
		out, err := r.st.ListInboxEvents(ctx, user, after, 100)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	adminEvents := ev(r.admin, 0)
	types := map[string]bool{}
	for _, e := range adminEvents {
		types[e.Type] = true
		if e.ConversationID != conv {
			t.Errorf("event for conversation %d, want %d", e.ConversationID, conv)
		}
	}
	if !types["conversation.new"] || !types["message.in"] {
		t.Fatalf("a supervisor's events = %v", types)
	}
	if len(ev(r.agent, 0)) != 0 || len(ev(r.other, 0)) != 0 {
		t.Fatal("an agent was told about an unassigned conversation")
	}
	last, _ := r.st.LatestInboxEventID(ctx, r.admin)
	if last != adminEvents[len(adminEvents)-1].ID {
		t.Fatalf("latest id %d, want %d", last, adminEvents[len(adminEvents)-1].ID)
	}

	if _, err := r.st.AssignConversation(ctx, r.admAcc(), conv, r.agent); err != nil {
		t.Fatal(err)
	}
	if got := ev(r.agent, 0); len(got) == 0 || got[0].Type != "conversation.assigned" {
		t.Fatalf("the new assignee's events = %v", got)
	}
	res, _ = r.st.IngestTrunkText(ctx, r.text(customer, "more"))
	mine := ev(r.agent, 0)
	if mine[len(mine)-1].Type != "message.in" || mine[len(mine)-1].MessageID != res.MessageID {
		t.Fatalf("the assignee's last event = %+v", mine[len(mine)-1])
	}
	if got := ev(r.other, 0); len(got) != 0 {
		t.Fatalf("another agent received %v", got)
	}
	// Resume: nothing repeats, nothing is skipped.
	after := ev(r.agent, mine[0].ID)
	if len(after) != len(mine)-1 || after[0].ID != mine[1].ID {
		t.Fatalf("resume after %d = %v, want %d events", mine[0].ID, after, len(mine)-1)
	}
	for i := 1; i < len(mine); i++ {
		if mine[i].ID <= mine[i-1].ID {
			t.Fatal("event ids do not increase")
		}
	}
}

// TestRetentionAndErase fails if an open conversation is purged by age, a
// resolved one past its retention is kept, one inside it is purged, a purge
// leaves a message row, or an audit row carries a number or text.
func TestRetentionAndErase(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	old, _ := r.st.IngestTrunkText(ctx, r.text(customer, "old open one"))
	resolvedOld, _ := r.st.IngestTrunkText(ctx, r.text("+32475000001", "old resolved one"))
	resolvedNew, _ := r.st.IngestTrunkText(ctx, r.text("+32475000002", "recent resolved one"))
	for _, id := range []int64{resolvedOld.ConversationID, resolvedNew.ConversationID} {
		if _, err := r.st.SetConversationStatus(ctx, r.admAcc(), id, "resolved", nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET created_at = now() - interval '800 days', last_message_at = now() - interval '800 days'
		WHERE id = $1`, old.ConversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET resolved_at = now() - interval '400 days', last_message_at = now() - interval '400 days'
		WHERE id = $1`, resolvedOld.ConversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET resolved_at = now() - interval '100 days', last_message_at = now() - interval '100 days'
		WHERE id = $1`, resolvedNew.ConversationID); err != nil {
		t.Fatal(err)
	}
	m, err := r.st.MaintainMessaging(ctx)
	if err != nil || m.Conversations != 1 {
		t.Fatalf("maintenance = %+v, %v; want one conversation purged", m, err)
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1`, old.ConversationID); n != 1 {
		t.Error("an open conversation was purged by age")
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1`, resolvedOld.ConversationID); n != 0 {
		t.Error("a resolved conversation past retention was kept")
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE id = $1`, resolvedNew.ConversationID); n != 1 {
		t.Error("a resolved conversation inside retention was purged")
	}
	if n := r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1`, resolvedOld.ConversationID); n != 0 {
		t.Error("a purge left message rows")
	}
	// A shorter retention applies at the next run.
	if _, err := r.st.PutMessagingSettings(ctx, "user:admin", store.MessagingSettings{RetentionSMSDays: 30, RetentionChatDays: 30, ReopenDays: 7, EmailNotify: true}); err != nil {
		t.Fatal(err)
	}
	if m, err := r.st.MaintainMessaging(ctx); err != nil || m.Conversations != 1 {
		t.Fatalf("second run = %+v, %v", m, err)
	}
	for _, bad := range []string{"+32475", "recent resolved", "old open"} {
		if n := r.count(`SELECT count(*) FROM audit_events WHERE action LIKE '%' || $1 || '%' OR resource_id LIKE '%' || $1 || '%'`, bad); n != 0 {
			t.Errorf("%q appears in %d audit rows", bad, n)
		}
	}
}

// TestSnoozeWakes fails if a snoozed conversation does not come back when
// its time passes, or is woken early.
func TestSnoozeWakes(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	res, _ := r.st.IngestTrunkText(ctx, r.text(customer, "hi"))
	until := time.Now().Add(time.Hour)
	c, err := r.st.SetConversationStatus(ctx, r.admAcc(), res.ConversationID, "snoozed", &until)
	if err != nil || c.Status != "snoozed" || c.SnoozedUntil == nil {
		t.Fatalf("snooze = %+v, %v", c, err)
	}
	if m, _ := r.st.MaintainMessaging(ctx); m.Woken != 0 {
		t.Fatal("woken early")
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET snoozed_until = now() - interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	if m, _ := r.st.MaintainMessaging(ctx); m.Woken != 1 {
		t.Fatalf("woken = %d, want 1", m.Woken)
	}
	if c, _ := r.st.GetConversation(ctx, r.admAcc(), res.ConversationID); c.Status != "open" {
		t.Fatalf("status = %q", c.Status)
	}
}

// TestInboxNotices fails if an agent without an open inbox is not emailed
// after two minutes, one with a stream is, a second text within 15 minutes
// emails again, an event is looked at twice, or no email goes out while the
// setting is off.
func TestInboxNotices(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.db.ExecContext(ctx, `UPDATE users SET email = 'admin@example.com' WHERE id = $1`, r.admin); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE users SET email = 'agent@example.com' WHERE id = $1`, r.agent); err != nil {
		t.Fatal(err)
	}
	age := func() {
		if _, err := r.db.ExecContext(ctx, `UPDATE conversation_events SET at = now() - interval '3 minutes' WHERE NOT notified`); err != nil {
			t.Fatal(err)
		}
	}
	res, _ := r.st.IngestTrunkText(ctx, r.text(customer, "hi"))
	if got, err := r.st.ClaimInboxNotices(ctx, 50); err != nil || len(got) != 0 {
		t.Fatalf("an event under two minutes old was noticed: %v, %v", got, err)
	}
	age()
	if err := r.st.TouchInboxPresence(ctx, r.admin); err != nil { // the admin has the inbox open
		t.Fatal(err)
	}
	got, err := r.st.ClaimInboxNotices(ctx, 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("notices = %+v: the admin is present and nobody else was told", got)
	}
	// Assign to the agent, who has no inbox open.
	if _, err := r.st.AssignConversation(ctx, r.admAcc(), res.ConversationID, r.agent); err != nil {
		t.Fatal(err)
	}
	r.ingest(customer, "second")
	age()
	got, _ = r.st.ClaimInboxNotices(ctx, 50)
	if len(got) != 1 || got[0].To != "agent@example.com" || got[0].ChannelName != "Main" || got[0].ConversationID != res.ConversationID {
		t.Fatalf("notices = %+v, want one to the agent", got)
	}
	if again, _ := r.st.ClaimInboxNotices(ctx, 50); len(again) != 0 {
		t.Fatal("an event was noticed twice")
	}
	// Throttled for 15 minutes.
	r.ingest(customer, "third")
	age()
	if got, _ := r.st.ClaimInboxNotices(ctx, 50); len(got) != 0 {
		t.Fatalf("notices = %+v inside the throttle", got)
	}
	// Off by setting.
	if _, err := r.db.ExecContext(ctx, `UPDATE conversation_notify SET last_email_at = now() - interval '1 hour'`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE messaging_settings SET email_notify = FALSE`); err != nil {
		t.Fatal(err)
	}
	r.ingest(customer, "fourth")
	age()
	if got, _ := r.st.ClaimInboxNotices(ctx, 50); len(got) != 0 {
		t.Fatalf("notices = %+v with the setting off", got)
	}
}

// TestNoContentInAudit fails if a message, a number or a note appears in an
// audit row after a full round of inbox work.
func TestNoContentInAudit(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	conv := r.ingest(customer, "my secret account is 99887766").ConversationID
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := r.st.ClaimConversation(ctx, r.admAcc(), conv)
	must(err)
	_, err = r.st.AddNote(ctx, r.admAcc(), conv, "internal remark about the account")
	must(err)
	_, _, err = r.st.SendConversationText(ctx, r.admAcc(), store.SendInput{ConversationID: conv, Body: "your account is fine"})
	must(err)
	_, err = r.st.SetConversationTags(ctx, r.admAcc(), conv, []string{"billing"})
	must(err)
	_, err = r.st.GetConversation(ctx, store.InboxAccess{UserID: r.other, All: true, Actor: "user:other"}, conv)
	must(err)
	_, err = r.st.BulkConversations(ctx, r.admAcc(), []int64{conv}, "resolve", 0)
	must(err)
	_, err = r.st.RecordOptOut(ctx, "user:admin", r.addr.ID, customer, "a note about the customer")
	must(err)
	rows, err := r.db.QueryContext(ctx, `SELECT actor || ' ' || action || ' ' || resource || ' ' || resource_id FROM audit_events`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	seen := 0
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		seen++
		for _, bad := range []string{"99887766", "internal remark", "your account", "32475", "a note about the customer"} {
			if strings.Contains(line, bad) {
				t.Errorf("audit row %q carries %q", line, bad)
			}
		}
	}
	if seen == 0 {
		t.Fatal("no audit rows at all")
	}
	// The supervisor's view of a conversation that is not theirs is audited.
	if n := r.count(`SELECT count(*) FROM audit_events WHERE action = 'supervisor_view' AND actor = 'user:other'`); n != 1 {
		t.Errorf("%d supervisor_view rows, want 1", n)
	}
}

// TestChannelRules fails if a channel with no trunk, a duplicate name, or a
// delete of a channel or number that has conversations is accepted.
func TestChannelRules(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if _, err := r.st.CreateMsgChannel(ctx, "t", store.MsgChannelInput{Kind: "sms", Provider: "sip_trunk", Name: "X", Enabled: true}); err == nil {
		t.Error("a SIP trunk channel with no trunk was accepted")
	}
	bad := int64(424242)
	if _, err := r.st.CreateMsgChannel(ctx, "t", store.MsgChannelInput{Kind: "sms", Provider: "sip_trunk", Name: "X", Enabled: true, TrunkID: &bad}); err == nil {
		t.Error("a channel with a trunk that does not exist was accepted")
	}
	if _, err := r.st.CreateMsgChannel(ctx, "t", store.MsgChannelInput{Kind: "sms", Provider: "sip_trunk", Name: "main", Enabled: true, TrunkID: &r.trunk}); !errors.Is(err, store.ErrConflict) {
		t.Errorf("a duplicate name (case-insensitive) = %v, want ErrConflict", err)
	}
	if ok, _ := r.st.HasMsgChannel(ctx); !ok {
		t.Error("HasMsgChannel = false with a channel and a number")
	}
	res, _ := r.st.IngestTrunkText(ctx, r.text(customer, "hi"))
	var inUse *store.InUseError
	if err := r.st.DeleteMsgAddress(ctx, "t", r.addr.ID); !errors.As(err, &inUse) {
		t.Errorf("deleting a number with conversations = %v", err)
	}
	if err := r.st.DeleteMsgChannel(ctx, "t", r.channel.ID); !errors.As(err, &inUse) {
		t.Errorf("deleting a channel with conversations = %v", err)
	}
	if _, err := r.st.ErasePeer(ctx, "t", customer); err != nil {
		t.Fatal(err)
	}
	_ = res
	if err := r.st.DeleteMsgAddress(ctx, "t", r.addr.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.st.DeleteMsgChannel(ctx, "t", r.channel.ID); err != nil {
		t.Fatal(err)
	}
	if ok, _ := r.st.HasMsgChannel(ctx); ok {
		t.Error("HasMsgChannel = true with no channel")
	}
}

// TestConversationList fails if the list does not honour the view, the
// status, the search, the cursor and the caller's reach.
func TestConversationList(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	var ids []int64
	for i, who := range []string{"+32475000011", "+32475000012", "+32475000013"} {
		res, err := r.st.IngestTrunkText(ctx, r.text(who, fmt.Sprintf("question about invoice %d", i)))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, res.ConversationID)
		time.Sleep(5 * time.Millisecond)
	}
	for _, err := range []error{
		func() error { _, err := r.st.AssignConversation(ctx, r.admAcc(), ids[0], r.agent); return err }(),
		func() error {
			_, err := r.st.SetConversationStatus(ctx, r.admAcc(), ids[2], "resolved", nil)
			return err
		}(),
	} {
		if err != nil {
			t.Fatal(err)
		}
	}

	list := func(acc store.InboxAccess, f store.ConvFilter, limit int) ([]store.Conversation, string) {
		t.Helper()
		cs, next, err := r.st.ListConversations(ctx, acc, f, limit)
		if err != nil {
			t.Fatal(err)
		}
		return cs, next
	}
	if cs, _ := list(r.admAcc(), store.ConvFilter{}, 10); len(cs) != 2 {
		t.Errorf("the active conversations = %d, want 2", len(cs))
	}
	if cs, _ := list(r.admAcc(), store.ConvFilter{Status: "any"}, 10); len(cs) != 3 {
		t.Errorf("every conversation = %d, want 3", len(cs))
	}
	if cs, _ := list(r.admAcc(), store.ConvFilter{Status: "resolved"}, 10); len(cs) != 1 || cs[0].ID != ids[2] {
		t.Errorf("resolved = %v", cs)
	}
	if cs, _ := list(r.admAcc(), store.ConvFilter{View: "unassigned"}, 10); len(cs) != 1 || cs[0].ID != ids[1] {
		t.Errorf("unassigned = %v", cs)
	}
	if cs, _ := list(r.agentAcc(), store.ConvFilter{View: "mine"}, 10); len(cs) != 1 || cs[0].ID != ids[0] || cs[0].Unread != 1 {
		t.Errorf("an agent's own = %+v", cs)
	}
	if cs, _ := list(r.agentAcc(), store.ConvFilter{View: "all", Status: "any"}, 10); len(cs) != 1 {
		t.Errorf("an agent reached %d conversations, want only their own", len(cs))
	}
	if cs, _ := list(r.otherAcc(), store.ConvFilter{Status: "any"}, 10); len(cs) != 0 {
		t.Errorf("another agent reached %d conversations", len(cs))
	}
	if cs, _ := list(r.admAcc(), store.ConvFilter{Q: "invoice 1"}, 10); len(cs) != 1 || cs[0].ID != ids[1] {
		t.Errorf("a search by text = %v", cs)
	}
	if cs, _ := list(r.admAcc(), store.ConvFilter{Q: "000012"}, 10); len(cs) != 1 || cs[0].ID != ids[1] {
		t.Errorf("a search by number = %v", cs)
	}
	if cs, _ := list(r.admAcc(), store.ConvFilter{Q: "nothing like this"}, 10); len(cs) != 0 {
		t.Errorf("a search with no hit = %v", cs)
	}
	// Paging.
	page1, next := list(r.admAcc(), store.ConvFilter{Status: "any"}, 2)
	if len(page1) != 2 || next == "" {
		t.Fatalf("page 1 = %d, next %q", len(page1), next)
	}
	cur, ok := store.ParseConvCursor(next)
	if !ok {
		t.Fatalf("cursor %q does not parse", next)
	}
	page2, next2 := list(r.admAcc(), store.ConvFilter{Status: "any", After: &cur}, 2)
	if len(page2) != 1 || next2 != "" || page2[0].ID == page1[0].ID || page2[0].ID == page1[1].ID {
		t.Fatalf("page 2 = %v, next %q", page2, next2)
	}
	// Reading clears the unread count.
	if err := r.st.MarkConversationRead(ctx, r.agentAcc(), ids[0]); err != nil {
		t.Fatal(err)
	}
	if c, _ := r.st.GetConversation(ctx, r.agentAcc(), ids[0]); c.Unread != 0 {
		t.Errorf("unread after reading = %d", c.Unread)
	}
	if st, err := r.st.GetInboxStatus(ctx, r.agentAcc()); err != nil || !st.Enabled || st.Unread != 0 {
		t.Errorf("status = %+v, %v", st, err)
	}
	if st, err := r.st.GetInboxStatus(ctx, r.admAcc()); err != nil || st.Unassigned != 1 {
		t.Errorf("supervisor status = %+v, %v", st, err)
	}
	stats, err := r.st.GetInboxStats(ctx)
	if err != nil || stats.Open != 2 || stats.Unassigned != 1 || stats.ResolvedToday != 1 {
		t.Errorf("stats = %+v, %v", stats, err)
	}
}

// TestBulk fails if a bulk action changes a conversation it does not fit, or
// miscounts.
func TestBulk(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	a, _ := r.st.IngestTrunkText(ctx, r.text("+32475000021", "a"))
	b, _ := r.st.IngestTrunkText(ctx, r.text("+32475000022", "b"))
	res, err := r.st.BulkConversations(ctx, r.admAcc(), []int64{a.ConversationID, b.ConversationID, 999999}, "resolve", 0)
	if err != nil || res.Done != 2 || res.Failed != 1 {
		t.Fatalf("bulk resolve = %+v, %v", res, err)
	}
	res, _ = r.st.BulkConversations(ctx, r.admAcc(), []int64{a.ConversationID, b.ConversationID}, "reopen", 0)
	if res.Done != 2 {
		t.Fatalf("bulk reopen = %+v", res)
	}
	res, _ = r.st.BulkConversations(ctx, r.admAcc(), []int64{a.ConversationID, b.ConversationID}, "assign", r.agent)
	if res.Done != 2 {
		t.Fatalf("bulk assign = %+v", res)
	}
	if n := r.count(`SELECT count(*) FROM conversations WHERE assignee_user_id = $1`, r.agent); n != 2 {
		t.Errorf("%d assigned, want 2", n)
	}
}

// TestMessagingTables fails if the migration did not make a table the inbox
// needs. The reversal is exercised by test/integration's migration test.
func TestMessagingTables(t *testing.T) {
	r := newRig(t)
	for _, tbl := range []string{"msg_channels", "msg_addresses", "conversations", "conversation_messages", "messaging_events",
		"sms_opt_outs", "conversation_reads", "conversation_assignments", "conversation_events", "inbox_presence", "conversation_notify", "messaging_settings"} {
		if n := r.count(`SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1`, tbl); n != 1 {
			t.Errorf("table %s missing", tbl)
		}
	}
}
