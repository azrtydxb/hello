package messaging_test

// The wiring between the messaging items against PostgreSQL: the router's
// assignments reach the desk phone (item 3 through item 5), the router's
// automatic texts go out on a chat site and a carrier channel (items 4 and 2
// through item 5), a chat site's availability follows the staffing of where
// its chats go (item 4 through item 5), and a chat site's opening hours are
// its address's schedule. They skip without HELLO_TEST_DATABASE_URL.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/pressly/goose/v3"

	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/provider"
	_ "github.com/azrtydxb/hello/internal/messaging/provider/twilio" // registers the twilio adapter
	"github.com/azrtydxb/hello/internal/messaging/router"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/store"
	"github.com/azrtydxb/hello/migrations/legacy"
)

// tickWith runs one router sweep on st at now with the agents' state.
func tickWith(t *testing.T, st *store.Store, live router.Live, now time.Time) {
	t.Helper()
	if err := (&router.Router{Store: st.MessageRouter(), Live: live}).Tick(context.Background(), now); err != nil {
		t.Fatal(err)
	}
}

func noLive() *liveStub {
	return &liveStub{states: map[string]queue.AgentState{}, busy: map[string]bool{}}
}

// textQueue makes a text queue of the extensions (static members) with the
// strategy.
func textQueue(t *testing.T, st *store.Store, name, strategy string, exts ...int64) store.Queue {
	t.Helper()
	var members []store.QueueMember
	for i, e := range exts {
		members = append(members, store.QueueMember{ExtensionID: e, Position: i + 1})
	}
	hang := store.IVRDestination{Kind: store.IVRHangup}
	q, err := st.CreateQueue(context.Background(), "test", store.QueueInput{
		Name: name, Enabled: true, Strategy: strategy, AgentTimeout: 20, RetryDelay: 5, MaxWait: 600, JoinEmpty: true,
		Timeout: hang, Full: hang, NoAgents: hang, Members: members,
		TextEnabled: true, TextCapacity: 5, TextReassignSeconds: 0,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// routeAddress sets an address's route, schedule and after-hours action.
func routeAddress(t *testing.T, st *store.Store, addressID int64, kind, target string, sched messaging.Schedule, ah store.AfterHours, first string) {
	t.Helper()
	if ah.Action == "" {
		ah = store.AfterHours{Action: messaging.AfterNone, RouteKind: messaging.RouteInbox}
	}
	if _, err := st.PutMsgRouting(context.Background(), "test", addressID, store.MsgRoutingInput{
		RouteKind: kind, RouteTarget: target, Schedule: sched, AfterHours: ah, FirstContactText: first}); err != nil {
		t.Fatal(err)
	}
}

// TestRouterDeliversToDeskPhone fails if a conversation the router gives to
// a user, by a direct route or by a text queue's strategy, does not queue the
// customer's latest text for the user's desk phone exactly as a manual
// assignment does, or if a shared pool (ring_all) that assigns nobody queues
// one.
func TestRouterDeliversToDeskPhone(t *testing.T) {
	ctx := context.Background()
	monday := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

	t.Run("direct route", func(t *testing.T) {
		d := newDeskRig(t)
		routeAddress(t, d.st, d.addr.ID, messaging.RouteUser, strconv.FormatInt(d.agent, 10), messaging.Schedule{}, store.AfterHours{}, "")
		conv := d.ingest(customer, "routed to you").ConversationID
		if n := d.queued(d.ext); n != 0 {
			t.Fatalf("%d texts queued before the router placed the conversation", n)
		}
		tickWith(t, d.st, noLive(), monday)
		var assignee int64
		if err := d.db.QueryRowContext(ctx, `SELECT coalesce(assignee_user_id, 0) FROM conversations WHERE id = $1`, conv).Scan(&assignee); err != nil || assignee != d.agent {
			t.Fatalf("setup: the router assigned %d, %v", assignee, err)
		}
		if n := d.count(`SELECT count(*) FROM messages WHERE recipient = $1 AND body_text = 'routed to you' AND conversation_message_id IS NOT NULL`, d.ext); n != 1 {
			t.Fatalf("the router's assignment queued %d texts for the desk phone, want 1", n)
		}
		// A second sweep places nothing again and so sends nothing again.
		tickWith(t, d.st, noLive(), monday.Add(time.Minute))
		if n := d.queued(d.ext); n != 1 {
			t.Fatalf("%d texts after a second sweep, want 1", n)
		}
	})

	t.Run("queue strategy", func(t *testing.T) {
		d := newDeskRig(t)
		q := textQueue(t, d.st, "desk", queue.Linear, d.extID)
		routeAddress(t, d.st, d.addr.ID, messaging.RouteQueue, strconv.FormatInt(q.ID, 10), messaging.Schedule{}, store.AfterHours{}, "")
		d.ingest(customer, "first text")
		d.ingest(customer, "latest text")
		tickWith(t, d.st, noLive(), monday)
		if n := d.count(`SELECT count(*) FROM messages WHERE recipient = $1 AND body_text = 'latest text'`, d.ext); n != 1 {
			t.Fatalf("the queue's assignment queued the latest text %d times, want 1", n)
		}
		if n := d.queued(d.ext); n != 1 {
			t.Fatalf("the queue's assignment queued %d texts, want only the latest", n)
		}
		// The customer's next text follows the conversation as for a manual
		// assignment.
		d.ingest(customer, "one more")
		if n := d.queued(d.ext); n != 2 {
			t.Fatalf("a later customer text queued %d in all, want 2", n)
		}
	})

	t.Run("shared pool", func(t *testing.T) {
		d := newDeskRig(t)
		q := textQueue(t, d.st, "shared", queue.RingAll, d.extID)
		routeAddress(t, d.st, d.addr.ID, messaging.RouteQueue, strconv.FormatInt(q.ID, 10), messaging.Schedule{}, store.AfterHours{}, "")
		d.ingest(customer, "for whoever")
		tickWith(t, d.st, noLive(), monday)
		if n := d.queued(d.ext); n != 0 {
			t.Fatalf("a shared pool that assigned nobody queued %d texts", n)
		}
	})
}

// TestSystemTextChat fails if the router's first-contact text or
// closed-hours reply on a chat site is not shown in the visitor's widget
// (history and stream) as a line from the agent side, is sent twice, or if
// an automatic text counts as an agent's answer for the visitor's cap.
func TestSystemTextChat(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	monday := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	sunday := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	hours := messaging.Schedule{Zone: "UTC", Days: []messaging.ScheduleDay{{Day: 1, From: "09:00", To: "17:00"}}}
	auto := func(conv int64) int {
		return r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND direction = 'out' AND kind = 'message' AND sender_kind = 'system' AND status = 'sent'`, conv)
	}
	seen := func(visitor int64, text string) {
		t.Helper()
		lines, err := r.st.ChatHistory(ctx, visitor, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, l := range lines {
			if l.Text == text && l.From == "agent" {
				found = true
			}
		}
		if !found {
			t.Fatalf("the visitor's history %+v lacks %q from the agent side", lines, text)
		}
		evs, err := r.st.ChatEvents(ctx, visitor, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		for _, e := range evs {
			if e.Type == "message" && e.Line != nil && e.Line.Text == text {
				return
			}
		}
		t.Fatalf("the visitor's stream did not carry %q", text)
	}

	site := r.site("Welcome", nil)
	routeAddress(t, r.st, site.AddressID, messaging.RouteInbox, "", hours,
		store.AfterHours{Action: messaging.AfterReply, Text: "We are closed; leave a message", RouteKind: messaging.RouteInbox},
		"Welcome, someone will be with you shortly")
	v, _ := r.visitor(site, "", "")
	conv := r.say(site, v, "hello").ConversationID
	tickWith(t, r.st, noLive(), monday)
	if n := auto(conv); n != 1 {
		t.Fatalf("%d automatic texts on a first chat, want 1", n)
	}
	seen(v, "Welcome, someone will be with you shortly")
	tickWith(t, r.st, noLive(), monday.Add(time.Minute))
	if n := auto(conv); n != 1 {
		t.Fatalf("%d automatic texts after a second sweep, want 1", n)
	}
	var last int64
	if err := r.db.QueryRowContext(ctx, `SELECT coalesce(last_message_id, 0) FROM conversations WHERE id = $1`, conv).Scan(&last); err != nil || last == 0 {
		t.Fatalf("the conversation's last message was not moved: %d %v", last, err)
	}

	// Outside the hours the closed-hours reply goes instead of the
	// first-contact text.
	v2, _ := r.visitor(site, "", "")
	conv2 := r.say(site, v2, "anyone?").ConversationID
	tickWith(t, r.st, noLive(), sunday)
	if n := auto(conv2); n != 1 {
		t.Fatalf("%d automatic texts outside the hours, want 1", n)
	}
	seen(v2, "We are closed; leave a message")
	if n := r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND kind = 'system' AND body LIKE '%was not sent%'`, conv2); n != 0 {
		t.Fatal("a chat site wrote that its automatic text was not sent")
	}

	// The automatic text is not an answer: the visitor's cap of messages
	// before any agent reply still counts from their first message.
	for i := 1; i < messaging.MaxChatUnanswered; i++ {
		if _, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v2, Text: "more", ClientMsgID: fmt.Sprintf("cap-%d", i)}); err != nil {
			t.Fatalf("message %d of %d: %v", i+1, messaging.MaxChatUnanswered, err)
		}
	}
	if _, err := r.st.ChatSend(ctx, store.ChatSendInput{Site: site, VisitorID: v2, Text: "more", ClientMsgID: "cap-over"}); !errors.Is(err, store.ErrChatLimit) {
		t.Fatalf("a message over the cap after only an automatic text: %v; want ErrChatLimit", err)
	}
}

// TestSystemTextCarrier fails if the router's automatic text on a carrier
// (Twilio) number is not queued for the send worker, if the worker does not
// claim it for the customer, or if a channel whose credentials were refused
// or a customer address the carrier cannot text queues one instead of
// writing why it was not sent.
func TestSystemTextCarrier(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	if !provider.Known("twilio") {
		t.Fatal("setup: the twilio adapter is not registered")
	}
	ch, err := r.st.CreateMsgChannel(ctx, "test", store.MsgChannelInput{Kind: "sms", Provider: "twilio", Name: "Twilio", Enabled: true,
		Credentials: &provider.Config{AccountSID: "AC" + strings.Repeat("ab", 16), AuthToken: "test-token"}})
	if err != nil {
		t.Fatal(err)
	}
	number := "+3270999000"
	key, _ := phonebook.Key(number)
	suffix, _ := phonebook.Suffix(key)
	addr, err := r.st.CreateMsgAddress(ctx, "test", store.MsgAddressInput{ChannelID: ch.ID, Address: number, Key: key, Suffix: suffix, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	routeAddress(t, r.st, addr.ID, messaging.RouteInbox, "", messaging.Schedule{}, store.AfterHours{}, "Thanks for texting us")
	// No carrier delivers inbound texts on the LAN yet, so the conversation
	// is made as an inbound one would be: unrouted and unassigned.
	newConv := func(peer string) int64 {
		t.Helper()
		var id int64
		if err := r.db.QueryRowContext(ctx, `
			INSERT INTO conversations (channel_id, address_id, peer_key, peer_kind, last_inbound_at) VALUES ($1, $2, $3, 'phone', now()) RETURNING id`,
			ch.ID, addr.ID, peer).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	queued := func(conv int64) int {
		return r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND direction = 'out' AND sender_kind = 'system' AND status = 'queued'`, conv)
	}
	notSent := func(conv int64, why string) int {
		return r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND kind = 'system' AND body LIKE $2`, conv, "%first-contact text was not sent: "+why+"%")
	}
	now := time.Now()

	conv := newConv("+32475000111")
	tickWith(t, r.st, noLive(), now)
	if n := queued(conv); n != 1 {
		t.Fatalf("%d automatic texts queued for the carrier, want 1", n)
	}
	jobs, err := r.st.ClaimOutbound(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 || jobs[0].ConversationID != conv || jobs[0].To != "+32475000111" || jobs[0].From != number || jobs[0].Body != "Thanks for texting us" {
		t.Fatalf("claimed %+v", jobs)
	}

	if _, err := r.db.ExecContext(ctx, `UPDATE msg_channels SET status = 'credentials_invalid' WHERE id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	refused := newConv("+32475000222")
	tickWith(t, r.st, noLive(), now)
	if queued(refused) != 0 || notSent(refused, "the carrier refused") != 1 {
		t.Fatal("a channel with refused credentials queued the text or did not say why it was not sent")
	}

	if _, err := r.db.ExecContext(ctx, `UPDATE msg_channels SET status = 'ok' WHERE id = $1`, ch.ID); err != nil {
		t.Fatal(err)
	}
	local := newConv("0475000333")
	tickWith(t, r.st, noLive(), now)
	if queued(local) != 0 || notSent(local, "the carrier cannot text") != 1 {
		t.Fatal("a number the carrier cannot text queued the text or did not say why it was not sent")
	}
}

// TestChatAvailabilityStaffing fails if a chat site routed to a text queue is
// online while no member is staffed (logged out, paused) or offline while
// one is, if a supervisor's presence stands in for the queue's agents, if an
// unreadable agent state does not fall back to the static members, if the
// after-hours route is not the one judged outside the hours, if a direct
// user route does not follow that user's presence, or if a queue that stopped
// taking texts does not fall back to the supervisors' pool.
func TestChatAvailabilityStaffing(t *testing.T) {
	r := newRouteRig(t, 2, queue.Linear, 3)
	ctx := context.Background()
	monday := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	sunday := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	hours := messaging.Schedule{Zone: "UTC", Days: []messaging.ScheduleDay{{Day: 1, From: "09:00", To: "17:00"}}}
	if _, err := r.db.ExecContext(ctx, `UPDATE queue_members SET dynamic = TRUE WHERE queue_id = $1`, r.queue.ID); err != nil {
		t.Fatal(err)
	}
	site := r.site("Queue chat", nil)
	qid := strconv.FormatInt(r.queue.ID, 10)
	routeAddress(t, r.st, site.AddressID, messaging.RouteQueue, qid, hours, store.AfterHours{}, "")
	site, err := r.st.GetChatSite(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	check := func(name string, now time.Time, states map[string]queue.AgentState, wantOn bool, wantReason string) {
		t.Helper()
		on, reason, err := r.st.ChatAvailability(ctx, site, now, states)
		if err != nil || on != wantOn || reason != wantReason {
			t.Fatalf("%s: online %v reason %q err %v; want %v %q", name, on, reason, err, wantOn, wantReason)
		}
	}
	a, b := r.numbers[0], r.numbers[1]
	if err := r.st.TouchInboxPresence(ctx, r.admin); err != nil {
		t.Fatal(err)
	}
	check("nobody logged in", monday, map[string]queue.AgentState{}, false, store.ChatOfflineNoAgent)
	check("logged in", monday, map[string]queue.AgentState{a: {Ext: a, LoggedIn: true}}, true, "")
	check("logged in to another queue", monday, map[string]queue.AgentState{a: {Ext: a, LoggedIn: true, Queues: []string{"other"}}}, false, store.ChatOfflineNoAgent)
	check("paused", monday, map[string]queue.AgentState{a: {Ext: a, LoggedIn: true, Paused: true}}, false, store.ChatOfflineNoAgent)
	check("state unreadable, dynamic members only", monday, nil, false, store.ChatOfflineNoAgent)
	if _, err := r.db.ExecContext(ctx, `UPDATE queue_members SET dynamic = FALSE WHERE queue_id = $1 AND extension_id = $2`, r.queue.ID, r.exts[1]); err != nil {
		t.Fatal(err)
	}
	check("state unreadable, a static member", monday, nil, true, "")
	check("static member paused", monday, map[string]queue.AgentState{b: {Ext: b, Paused: true}}, false, store.ChatOfflineNoAgent)
	check("outside the hours", sunday, map[string]queue.AgentState{a: {Ext: a, LoggedIn: true}}, false, store.ChatOfflineHours)

	// Outside the hours an after-hours route is judged instead: the pool,
	// answered while a supervisor has the inbox open.
	routeAddress(t, r.st, site.AddressID, messaging.RouteQueue, qid, hours, store.AfterHours{Action: messaging.AfterRoute, RouteKind: messaging.RouteInbox}, "")
	check("after-hours route to a staffed pool", sunday, map[string]queue.AgentState{}, true, "")
	if _, err := r.db.ExecContext(ctx, `DELETE FROM inbox_presence`); err != nil {
		t.Fatal(err)
	}
	check("after-hours route to an empty pool", sunday, map[string]queue.AgentState{}, false, store.ChatOfflineNoAgent)

	// A direct user route follows the user's own presence.
	routeAddress(t, r.st, site.AddressID, messaging.RouteUser, strconv.FormatInt(r.users[0], 10), messaging.Schedule{}, store.AfterHours{}, "")
	check("user away", monday, nil, false, store.ChatOfflineNoAgent)
	if err := r.st.TouchInboxPresence(ctx, r.users[0]); err != nil {
		t.Fatal(err)
	}
	check("user present", monday, nil, true, "")

	// A queue that stops taking texts sends chats to the pool, and the
	// pool's supervisors decide.
	routeAddress(t, r.st, site.AddressID, messaging.RouteQueue, qid, messaging.Schedule{}, store.AfterHours{}, "")
	if _, err := r.db.ExecContext(ctx, `UPDATE queues SET text_enabled = FALSE WHERE id = $1`, r.queue.ID); err != nil {
		t.Fatal(err)
	}
	check("queue off, no supervisor", monday, map[string]queue.AgentState{a: {Ext: a, LoggedIn: true}}, false, store.ChatOfflineNoAgent)
	if err := r.st.TouchInboxPresence(ctx, r.admin); err != nil {
		t.Fatal(err)
	}
	check("queue off, a supervisor", monday, nil, true, "")
}

// TestChatSiteHoursAreRouting fails if a chat site's opening hours and its
// address's routing schedule can differ: hours set on the site must be the
// routing's schedule, a schedule set through the routing must be the site's
// hours, and clearing either clears both.
func TestChatSiteHoursAreRouting(t *testing.T) {
	r := newRig(t)
	ctx := context.Background()
	hours := messaging.Schedule{Zone: "Europe/Brussels", Days: []messaging.ScheduleDay{{Day: 2, From: "08:00", To: "12:00"}}}
	site := r.site("One schedule", func(in *store.ChatSiteInput) { in.Hours = hours })
	rt, err := r.st.GetMsgRouting(ctx, site.AddressID)
	if err != nil {
		t.Fatal(err)
	}
	if rt.Schedule.Zone != "Europe/Brussels" || len(rt.Schedule.Days) != 1 || rt.Schedule.Days[0] != hours.Days[0] {
		t.Fatalf("the site's hours are not its routing schedule: %+v", rt.Schedule)
	}
	other := messaging.Schedule{Zone: "UTC", Days: []messaging.ScheduleDay{{Day: 5, From: "13:00", To: "18:00"}, {Day: 6, From: "10:00", To: "12:00"}}}
	routeAddress(t, r.st, site.AddressID, messaging.RouteInbox, "", other, store.AfterHours{}, "")
	got, err := r.st.GetChatSite(ctx, site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Hours.Zone != "UTC" || len(got.Hours.Days) != 2 {
		t.Fatalf("a routing schedule did not become the site's hours: %+v", got.Hours)
	}
	in := store.ChatSiteInput{Name: got.Name, Enabled: true, AllowedOrigins: got.AllowedOrigins, Accent: got.Accent, Position: got.Position,
		Collect: got.Collect, OfflineMode: got.OfflineMode, Challenge: got.Challenge, PowBits: got.PowBits, MaxOpenUnassigned: got.MaxOpenUnassigned}
	if _, err := r.st.UpdateChatSite(ctx, "test", site.ID, in); err != nil {
		t.Fatal(err)
	}
	if rt, err = r.st.GetMsgRouting(ctx, site.AddressID); err != nil || len(rt.Schedule.Days) != 0 {
		t.Fatalf("clearing the site's hours left the routing schedule %+v, %v", rt.Schedule, err)
	}
}

// oneScheduleVersion is the version of the migration that made a chat
// site's hours its address's schedule, found by name so a renumbering does
// not break the test.
func oneScheduleVersion(t *testing.T) int64 {
	t.Helper()
	names, err := fs.Glob(legacy.FS, "*_messaging_one_schedule.sql")
	if err != nil || len(names) != 1 {
		t.Fatalf("the one-schedule migration: %v %v", names, err)
	}
	v, err := strconv.ParseInt(strings.SplitN(names[0], "_", 2)[0], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// TestOneScheduleMigration fails if the migration that drops a chat site's
// own hours loses them: hours a site had must become its address's schedule,
// a site without hours must keep the schedule its address had, and rolling
// back must give each site its address's schedule as hours again.
func TestOneScheduleMigration(t *testing.T) {
	r := newLegacyRig(t)
	ctx := context.Background()
	v := oneScheduleVersion(t)
	p, err := goose.NewProvider(goose.DialectPostgres, r.db, legacy.FS)
	if err != nil {
		t.Fatal(err)
	}
	withHours := r.site("Had hours", nil)
	without := r.site("No hours", nil)
	if _, err := p.DownTo(ctx, v-1); err != nil {
		t.Fatalf("roll back below %d: %v", v, err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE chat_sites SET hours = $2::jsonb WHERE id = $1`, withHours.ID,
		`{"zone":"UTC","days":[{"day":1,"from":"09:00","to":"17:00"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE msg_addresses SET schedule = $2::jsonb WHERE id = $1`, without.AddressID,
		`{"zone":"UTC","days":[{"day":3,"from":"10:00","to":"11:00"}]}`); err != nil {
		t.Fatal(err)
	}
	if _, err := p.UpTo(ctx, v); err != nil {
		t.Fatal(err)
	}
	day := func(addressID int64) string {
		var d string
		if err := r.db.QueryRowContext(ctx, `SELECT coalesce(schedule #>> '{days,0,day}', '') FROM msg_addresses WHERE id = $1`, addressID).Scan(&d); err != nil {
			t.Fatal(err)
		}
		return d
	}
	if got := day(withHours.AddressID); got != "1" {
		t.Fatalf("a site's hours did not become its address's schedule: day %q", got)
	}
	if got := day(without.AddressID); got != "3" {
		t.Fatalf("a site without hours lost its address's schedule: day %q", got)
	}
	if _, err := p.DownTo(ctx, v-1); err != nil {
		t.Fatal(err)
	}
	var hrs string
	if err := r.db.QueryRowContext(ctx, `SELECT coalesce(hours #>> '{days,0,day}', '') FROM chat_sites WHERE id = $1`, without.ID).Scan(&hrs); err != nil || hrs != "3" {
		t.Fatalf("rolling back did not restore the hours from the schedule: %q %v", hrs, err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatal(err)
	}
}
