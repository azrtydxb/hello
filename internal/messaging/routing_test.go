package messaging_test

// Routing and ACD against PostgreSQL (spec messaging-channels M-13, M-14):
// where a number's new conversations go, how a text queue hands them to its
// agents, and how they come back. Skipped without HELLO_TEST_DATABASE_URL.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/messaging"
	"github.com/azrtydxb/hello/internal/messaging/router"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/store"
)

// liveStub is the call queues' shared state in memory.
type liveStub struct {
	mu     sync.Mutex
	states map[string]queue.AgentState
	busy   map[string]bool
}

func (l *liveStub) Agents(context.Context) (map[string]queue.AgentState, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]queue.AgentState{}
	for k, v := range l.states {
		out[k] = v
	}
	return out, nil
}

func (l *liveStub) BusyExtensions(context.Context) (map[string]bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := map[string]bool{}
	for k, v := range l.busy {
		out[k] = v
	}
	return out, nil
}

var peerSeq atomic.Int64

// routeRig is the inbox rig with agents (each linked to an extension), a text
// queue and a router.
type routeRig struct {
	*rig
	live    *liveStub
	users   []int64 // the agents, in queue order
	exts    []int64
	numbers []string
	queue   store.Queue
}

// newRouteRig makes n agents (users with extension 101, 102, ...) and a text
// queue of them with the strategy and capacity.
func newRouteRig(t *testing.T, n int, strategy string, capacity int) *routeRig {
	t.Helper()
	r := &routeRig{rig: newRig(t), live: &liveStub{states: map[string]queue.AgentState{}, busy: map[string]bool{}}}
	ctx := context.Background()
	var members []store.QueueMember
	for i := 0; i < n; i++ {
		num := strconv.Itoa(101 + i)
		ext, err := r.st.CreateExtension(ctx, "test", num, "Ext "+num, "", nil)
		if err != nil {
			t.Fatal(err)
		}
		uid, err := r.st.CreateUser(ctx, "test", fmt.Sprintf("agent%d", i), "x", auth.RoleUser)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.SetUserExtension(ctx, "test", uid, &ext.ID); err != nil {
			t.Fatal(err)
		}
		r.users, r.exts, r.numbers = append(r.users, uid), append(r.exts, ext.ID), append(r.numbers, num)
		members = append(members, store.QueueMember{ExtensionID: ext.ID, Position: i + 1})
	}
	hang := store.IVRDestination{Kind: store.IVRHangup}
	in := store.QueueInput{
		Name: "support", Enabled: true, Strategy: strategy, AgentTimeout: 20, RetryDelay: 5, MaxWait: 600, JoinEmpty: true,
		Timeout: hang, Full: hang, NoAgents: hang, Members: members,
		TextEnabled: true, TextCapacity: capacity, TextReassignSeconds: 60,
	}
	var err error
	if r.queue, err = r.st.CreateQueue(ctx, "test", in, nil); err != nil {
		t.Fatal(err)
	}
	r.route(messaging.RouteQueue, strconv.FormatInt(r.queue.ID, 10))
	return r
}

// route sets the number's route with no schedule.
func (r *routeRig) route(kind, target string) {
	r.t.Helper()
	_, err := r.st.PutMsgRouting(context.Background(), "test", r.addr.ID, store.MsgRoutingInput{
		RouteKind: kind, RouteTarget: target, AfterHours: store.AfterHours{Action: messaging.AfterNone, RouteKind: messaging.RouteInbox}})
	if err != nil {
		r.t.Fatal(err)
	}
}

func (r *routeRig) router(st *store.Store) *router.Router {
	return &router.Router{Store: st.MessageRouter(), Live: r.live}
}

func (r *routeRig) tick(now time.Time) {
	r.t.Helper()
	if err := r.router(r.st).Tick(context.Background(), now); err != nil {
		r.t.Fatal(err)
	}
}

// customerText delivers a text from a new peer and returns the conversation.
func (r *routeRig) customerText(body string) int64 {
	r.t.Helper()
	peer := fmt.Sprintf("+3247511%04d", peerSeq.Add(1))
	return r.ingest(peer, body).ConversationID
}

func (r *routeRig) assignee(conv int64) int64 {
	r.t.Helper()
	var u *int64
	if err := r.db.QueryRowContext(context.Background(), `SELECT assignee_user_id FROM conversations WHERE id = $1`, conv).Scan(&u); err != nil {
		r.t.Fatal(err)
	}
	if u == nil {
		return 0
	}
	return *u
}

func (r *routeRig) accOf(user int64) store.InboxAccess {
	return store.InboxAccess{UserID: user, Actor: fmt.Sprintf("user:%d", user), Name: fmt.Sprintf("agent %d", user)}
}

// TestPerDIDRouting fails if a number routes to a different target than
// configured, an unknown number creates a conversation, a target that is gone
// does not fall back to the pool, or the after-hours action is not applied.
func TestPerDIDRouting(t *testing.T) {
	r := newRouteRig(t, 2, queue.Linear, 5)
	ctx := context.Background()
	monday := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)

	t.Run("an unknown number creates nothing", func(t *testing.T) {
		toKey, _ := phonebook.Key("+3270999999")
		suffix, _ := phonebook.Suffix(toKey)
		fromKey, _ := phonebook.Key(customer)
		res, err := r.st.IngestTrunkText(ctx, store.TrunkText{TrunkID: r.trunk, ToKey: toKey, ToSuffix: suffix, FromKey: fromKey, Body: "x", EventKey: "unknown-1"})
		if err != nil || res.Handled {
			t.Fatalf("ingest = %+v, %v; want unhandled", res, err)
		}
		if n := r.count(`SELECT count(*) FROM conversations`); n != 0 {
			t.Fatalf("%d conversations", n)
		}
	})

	t.Run("the pool is the default", func(t *testing.T) {
		r.route(messaging.RouteInbox, "")
		conv := r.customerText("hello")
		r.tick(monday)
		if r.assignee(conv) != 0 || r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND routed_at IS NOT NULL AND queue_id IS NULL`, conv) != 1 {
			t.Fatal("a pool conversation was placed")
		}
	})

	t.Run("a user", func(t *testing.T) {
		r.route(messaging.RouteUser, strconv.FormatInt(r.users[1], 10))
		conv := r.customerText("hello")
		r.tick(monday)
		if got := r.assignee(conv); got != r.users[1] {
			t.Fatalf("assignee = %d, want %d", got, r.users[1])
		}
		if r.count(`SELECT count(*) FROM conversation_assignments WHERE conversation_id = $1 AND reason = 'route'`, conv) != 1 {
			t.Fatal("no history row")
		}
	})

	t.Run("an extension reaches its user", func(t *testing.T) {
		r.route(messaging.RouteExtension, strconv.FormatInt(r.exts[0], 10))
		conv := r.customerText("hello")
		r.tick(monday)
		if got := r.assignee(conv); got != r.users[0] {
			t.Fatalf("assignee = %d, want %d", got, r.users[0])
		}
	})

	t.Run("a queue", func(t *testing.T) {
		r.route(messaging.RouteQueue, strconv.FormatInt(r.queue.ID, 10))
		conv := r.customerText("hello")
		r.tick(monday)
		if got := r.assignee(conv); got != r.users[0] && got != r.users[1] {
			t.Fatalf("assignee = %d, want one of the queue's agents", got)
		}
		if r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND queue_id = $2`, conv, r.queue.ID) != 1 {
			t.Fatal("the conversation does not name its queue")
		}
	})

	t.Run("a ring group is a shared pool", func(t *testing.T) {
		g, err := r.st.CreateRingGroup(ctx, "test", store.RingGroupInput{Name: "sales", Strategy: "ring-all", RingTimeout: 20, FailureKind: "none",
			Members: []store.RingGroupMember{{ExtensionID: r.exts[1], Position: 1, Weight: 1}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		r.route(messaging.RouteRingGroup, strconv.FormatInt(g.ID, 10))
		conv := r.customerText("hello")
		r.tick(monday)
		if r.assignee(conv) != 0 {
			t.Fatal("a shared pool assigned")
		}
		if _, err := r.st.GetConversation(ctx, r.accOf(r.users[1]), conv); err != nil {
			t.Fatalf("a group member cannot see the pool conversation: %v", err)
		}
		if _, err := r.st.GetConversation(ctx, r.accOf(r.users[0]), conv); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("a non-member saw it: %v", err)
		}
		if r.count(`SELECT count(*) FROM conversation_events WHERE conversation_id = $1 AND user_id = $2`, conv, r.users[1]) < 1 {
			t.Fatal("the member was not told")
		}
		if _, err := r.st.ClaimConversation(ctx, r.accOf(r.users[1]), conv); err != nil {
			t.Fatalf("a member could not claim: %v", err)
		}
	})

	t.Run("a target that is gone falls back to the pool", func(t *testing.T) {
		r.route(messaging.RouteUser, strconv.FormatInt(r.users[0], 10))
		if _, err := r.db.ExecContext(ctx, `UPDATE users SET enabled = FALSE WHERE id = $1`, r.users[0]); err != nil {
			t.Fatal(err)
		}
		conv := r.customerText("hello")
		r.tick(monday)
		if r.assignee(conv) != 0 {
			t.Fatal("assigned to a disabled user")
		}
		if _, err := r.db.ExecContext(ctx, `UPDATE users SET enabled = TRUE WHERE id = $1`, r.users[0]); err != nil {
			t.Fatal(err)
		}
		// A queue that stopped taking texts too.
		r.route(messaging.RouteQueue, strconv.FormatInt(r.queue.ID, 10))
		in := store.QueueInput{Name: "support", Enabled: true, Strategy: queue.Linear, AgentTimeout: 20, RetryDelay: 5, MaxWait: 600, JoinEmpty: true,
			Timeout: store.IVRDestination{Kind: store.IVRHangup}, Full: store.IVRDestination{Kind: store.IVRHangup}, NoAgents: store.IVRDestination{Kind: store.IVRHangup},
			Members: r.queue.Members}
		if _, err := r.st.UpdateQueue(ctx, "test", r.queue.ID, in, nil); err != nil {
			t.Fatal(err)
		}
		conv = r.customerText("hello")
		r.tick(monday)
		if r.assignee(conv) != 0 || r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND queue_id IS NULL`, conv) != 1 {
			t.Fatal("a queue that takes no texts took one")
		}
	})
}

// TestAfterHours fails if a conversation that starts outside the schedule is
// not given the after-hours action, one inside it is, the reply goes to a
// customer who opted out, or the reply is sent twice.
func TestAfterHours(t *testing.T) {
	r := newRouteRig(t, 1, queue.Linear, 5)
	ctx := context.Background()
	sched := messaging.Schedule{Zone: "UTC", Days: []messaging.ScheduleDay{{Day: 1, From: "09:00", To: "17:00"}}}
	monday := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	sunday := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	set := func(a store.AfterHours) {
		t.Helper()
		if _, err := r.st.PutMsgRouting(ctx, "test", r.addr.ID, store.MsgRoutingInput{
			RouteKind: messaging.RouteQueue, RouteTarget: strconv.FormatInt(r.queue.ID, 10), Schedule: sched, AfterHours: a}); err != nil {
			t.Fatal(err)
		}
	}
	replies := func(conv int64) int {
		return r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND direction = 'out' AND sender_kind = 'system' AND kind = 'message'`, conv)
	}

	set(store.AfterHours{Action: messaging.AfterReply, Text: "We are closed", RouteKind: messaging.RouteInbox})
	conv := r.customerText("hello")
	r.tick(sunday)
	if replies(conv) != 1 || r.count(`SELECT count(*) FROM messages WHERE conversation_message_id IN
		(SELECT id FROM conversation_messages WHERE conversation_id = $1 AND sender_kind = 'system')`, conv) != 1 {
		t.Fatal("the closed-hours reply was not queued once")
	}
	if r.assignee(conv) == 0 {
		t.Fatal("a reply must still route the conversation")
	}
	r.tick(sunday.Add(time.Minute))
	if replies(conv) != 1 {
		t.Fatalf("%d replies after a second sweep", replies(conv))
	}

	inside := r.customerText("hello")
	r.tick(monday)
	if replies(inside) != 0 {
		t.Fatal("a reply inside opening hours")
	}

	set(store.AfterHours{Action: messaging.AfterRoute, RouteKind: messaging.RouteInbox})
	conv = r.customerText("hello")
	r.tick(sunday)
	if r.assignee(conv) != 0 || r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND queue_id IS NULL`, conv) != 1 {
		t.Fatal("the after-hours route was not used")
	}
	inside = r.customerText("hello")
	r.tick(monday)
	if r.assignee(inside) == 0 {
		t.Fatal("the normal route was not used inside hours")
	}

	set(store.AfterHours{Action: messaging.AfterReply, Text: "We are closed", RouteKind: messaging.RouteInbox})
	peer := fmt.Sprintf("+3247512%04d", peerSeq.Add(1))
	conv = r.ingest(peer, "STOP").ConversationID
	r.tick(sunday)
	if replies(conv) != 0 {
		t.Fatal("the closed-hours reply went to a customer who opted out")
	}
	none := store.AfterHours{Action: messaging.AfterNone, RouteKind: messaging.RouteInbox}
	set(none)
	conv = r.customerText("hello")
	r.tick(sunday)
	if replies(conv) != 0 || r.assignee(conv) == 0 {
		t.Fatal("the action none must route as usual")
	}
}

// TestAssignmentACD fails if two concurrent claims both succeed, an agent
// gets more than the capacity, an unstaffed, paused or unlinked agent is
// chosen, a waiting conversation is not assigned when an agent frees up, a
// ring_all conversation is assigned, or one conversation is given twice by
// pods sweeping at once.
func TestAssignmentACD(t *testing.T) {
	ctx := context.Background()
	now := time.Now()

	t.Run("capacity and concurrent sweeps", func(t *testing.T) {
		r := newRouteRig(t, 3, queue.Linear, 2)
		var convs []int64
		for range 10 {
			convs = append(convs, r.customerText("hello"))
		}
		var wg sync.WaitGroup
		for range 4 {
			pod := r.newStoreOn()
			wg.Go(func() {
				if err := r.router(pod).Tick(ctx, now); err != nil {
					t.Errorf("tick: %v", err)
				}
			})
		}
		wg.Wait()
		r.tick(now)
		held := map[int64]int{}
		for _, c := range convs {
			if a := r.assignee(c); a != 0 {
				held[a]++
			}
			if n := r.count(`SELECT count(*) FROM conversation_assignments WHERE conversation_id = $1 AND reason = 'acd'`, c); n > 1 {
				t.Errorf("conversation %d was assigned %d times", c, n)
			}
		}
		total := 0
		for u, n := range held {
			if n > 2 {
				t.Errorf("user %d holds %d, capacity is 2", u, n)
			}
			total += n
		}
		if total != 6 {
			t.Errorf("%d assigned, want 6 (3 agents x 2) with the rest waiting", total)
		}
		// An agent frees up: the next waiting conversation goes to them.
		var held0 int64
		for _, c := range convs {
			if r.assignee(c) == r.users[0] {
				held0 = c
				break
			}
		}
		if _, err := r.st.SetConversationStatus(ctx, r.accOf(r.users[0]), held0, messaging.StatusResolved, nil); err != nil {
			t.Fatal(err)
		}
		r.tick(now)
		if n := r.count(`SELECT count(*) FROM conversations WHERE assignee_user_id = $1 AND status = 'open'`, r.users[0]); n != 2 {
			t.Fatalf("agent 0 holds %d after one was resolved, want 2", n)
		}
	})

	t.Run("two claims at once", func(t *testing.T) {
		r := newRouteRig(t, 2, queue.Linear, 5)
		conv := r.customerText("hello")
		r.live.states["101"], r.live.states["102"] = queue.AgentState{Ext: "101", Paused: true}, queue.AgentState{Ext: "102", Paused: true}
		r.tick(now)
		if r.assignee(conv) != 0 {
			t.Fatal("assigned to a paused agent")
		}
		var done, other atomic.Int32
		var wg sync.WaitGroup
		for _, u := range r.users {
			for range 3 {
				pod := r.newStoreOn()
				wg.Go(func() {
					out, err := pod.MessageRouter().Assign(ctx, conv, u, router.Queue{ID: r.queue.ID, Name: "support", Capacity: 5})
					switch {
					case err != nil:
						t.Errorf("assign: %v", err)
					case out == messaging.AssignDone:
						done.Add(1)
					default:
						other.Add(1)
					}
				})
			}
		}
		wg.Wait()
		if done.Load() != 1 || other.Load() != 5 {
			t.Fatalf("%d done, %d refused; want 1 and 5", done.Load(), other.Load())
		}
	})

	t.Run("who is staffed", func(t *testing.T) {
		r := newRouteRig(t, 3, queue.Linear, 5)
		r.live.states["101"] = queue.AgentState{Ext: "101", Paused: true}
		r.live.busy["102"] = true
		conv := r.customerText("hello")
		if _, err := r.db.ExecContext(ctx, `UPDATE queues SET text_requires_idle_voice = TRUE WHERE id = $1`, r.queue.ID); err != nil {
			t.Fatal(err)
		}
		r.tick(now)
		if got := r.assignee(conv); got != r.users[2] {
			t.Fatalf("assignee = %d, want the only free agent %d", got, r.users[2])
		}
		// An agent whose extension was unlinked is not a candidate.
		if _, err := r.st.SetUserExtension(ctx, "test", r.users[2], nil); err != nil {
			t.Fatal(err)
		}
		conv = r.customerText("again")
		r.tick(now)
		if got := r.assignee(conv); got != 0 {
			t.Fatalf("assignee = %d: an unlinked agent, a paused one and one on a call were chosen", got)
		}
	})

	t.Run("ring_all shares the pool", func(t *testing.T) {
		r := newRouteRig(t, 2, queue.RingAll, 5)
		conv := r.customerText("hello")
		r.tick(now)
		if r.assignee(conv) != 0 {
			t.Fatal("ring_all assigned")
		}
		for _, u := range r.users {
			if _, err := r.st.GetConversation(ctx, r.accOf(u), conv); err != nil {
				t.Fatalf("a queue member cannot see the shared pool: %v", err)
			}
		}
		var wins atomic.Int32
		var wg sync.WaitGroup
		for _, u := range r.users {
			wg.Go(func() {
				if _, err := r.newStoreOn().ClaimConversation(ctx, r.accOf(u), conv); err == nil {
					wins.Add(1)
				}
			})
		}
		wg.Wait()
		if wins.Load() != 1 {
			t.Fatalf("%d agents claimed the shared conversation", wins.Load())
		}
	})

	t.Run("strategy order", func(t *testing.T) {
		r := newRouteRig(t, 3, queue.FewestCalls, 5)
		// Agent 0 has had two, agent 1 one: the next goes to agent 2, then 1.
		if _, err := r.db.ExecContext(ctx, `INSERT INTO msg_agent_stats (user_id, queue_id, text_answered, last_end) VALUES
			($1, $3, 2, now()), ($2, $3, 1, now())`, r.users[0], r.users[1], r.queue.ID); err != nil {
			t.Fatal(err)
		}
		var got []int64
		for range 3 {
			c := r.customerText("hello")
			r.tick(now)
			got = append(got, r.assignee(c))
		}
		if want := []int64{r.users[2], r.users[1], r.users[2]}; !slices.Equal(got, want) {
			t.Fatalf("assigned %v, want %v (fewest calls)", got, want)
		}
	})
}

// TestReassign fails if an unanswered conversation does not return to the
// pool after the queue's time, goes back to the agent who let it lapse, comes
// back when an agent had answered or a person had taken it, or stays with an
// agent who left the queue.
func TestReassign(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	later := now.Add(2 * time.Minute) // the queue's time is 60 s

	t.Run("unanswered", func(t *testing.T) {
		r := newRouteRig(t, 2, queue.Linear, 5)
		conv := r.customerText("hello")
		r.tick(now)
		first := r.assignee(conv)
		if first != r.users[0] {
			t.Fatalf("assignee = %d", first)
		}
		r.tick(now.Add(30 * time.Second))
		if r.assignee(conv) != first {
			t.Fatal("returned before the time")
		}
		r.tick(later)
		if got := r.assignee(conv); got != r.users[1] {
			t.Fatalf("assignee = %d, want the next agent %d (not the one who let it lapse)", got, r.users[1])
		}
		if r.count(`SELECT count(*) FROM conversation_assignments WHERE conversation_id = $1 AND reason = 'acd_timeout' AND from_user_id = $2`, conv, first) != 1 {
			t.Fatal("no history row for the return")
		}
		// With nobody else it waits in the pool.
		if _, err := r.db.ExecContext(ctx, `UPDATE users SET enabled = FALSE WHERE id = $1`, r.users[0]); err != nil {
			t.Fatal(err)
		}
		r.tick(later.Add(4 * time.Minute))
		r.tick(later.Add(4 * time.Minute))
		if got := r.assignee(conv); got != 0 {
			t.Fatalf("assignee = %d, want it waiting: the other agent is disabled and the first just let it lapse", got)
		}
	})

	t.Run("answered, manual and off", func(t *testing.T) {
		r := newRouteRig(t, 2, queue.Linear, 5)
		answered, manual := r.customerText("a"), r.customerText("b")
		r.tick(now)
		if _, _, err := r.st.SendConversationText(ctx, r.accOf(r.assignee(answered)), store.SendInput{ConversationID: answered, Body: "on it"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.st.AssignConversation(ctx, r.admAcc(), manual, r.users[1]); err != nil {
			t.Fatal(err)
		}
		r.tick(later)
		if r.assignee(answered) == 0 || r.assignee(manual) != r.users[1] {
			t.Fatal("an answered or manually assigned conversation was taken back")
		}
		if _, err := r.db.ExecContext(ctx, `UPDATE queues SET text_reassign_seconds = 0 WHERE id = $1`, r.queue.ID); err != nil {
			t.Fatal(err)
		}
		fresh := r.customerText("c")
		r.tick(later)
		r.tick(later.Add(time.Hour))
		if r.assignee(fresh) == 0 {
			t.Fatal("reassign 0 must never return a conversation")
		}
	})

	t.Run("an agent leaves the queue", func(t *testing.T) {
		r := newRouteRig(t, 2, queue.Linear, 5)
		conv := r.customerText("hello")
		r.tick(now)
		if r.assignee(conv) != r.users[0] {
			t.Fatal("setup")
		}
		if _, err := r.st.SetUserExtension(ctx, "test", r.users[0], nil); err != nil {
			t.Fatal(err)
		}
		r.tick(now)
		if got := r.assignee(conv); got != r.users[1] {
			t.Fatalf("assignee = %d, want %d after the first was unlinked", got, r.users[1])
		}
	})

	t.Run("a queue stops taking texts", func(t *testing.T) {
		r := newRouteRig(t, 1, queue.Linear, 1)
		held, waiting := r.customerText("a"), r.customerText("b")
		r.tick(now)
		if r.assignee(held) == 0 || r.assignee(waiting) != 0 {
			t.Fatal("setup")
		}
		if _, err := r.db.ExecContext(ctx, `UPDATE queues SET text_enabled = FALSE WHERE id = $1`, r.queue.ID); err != nil {
			t.Fatal(err)
		}
		r.tick(now)
		if r.count(`SELECT count(*) FROM conversations WHERE id = $1 AND queue_id IS NULL`, waiting) != 1 {
			t.Fatal("a waiting conversation stayed in a queue that takes no texts")
		}
		if r.assignee(held) == 0 {
			t.Fatal("an assigned conversation was taken from its agent")
		}
	})
}

// TestQueueTextDelete fails if a queue that a number routes to, or that holds
// an open conversation, can be deleted.
func TestQueueTextDelete(t *testing.T) {
	r := newRouteRig(t, 1, queue.Linear, 5)
	ctx := context.Background()
	var inUse *store.InUseError
	if err := r.st.DeleteQueue(ctx, "test", r.queue.ID, nil); !errors.As(err, &inUse) {
		t.Fatalf("a queue a number routes to was deleted: %v", err)
	}
	r.route(messaging.RouteInbox, "")
	conv := r.customerText("hello")
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET queue_id = $2, routed_at = now() WHERE id = $1`, conv, r.queue.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.st.DeleteQueue(ctx, "test", r.queue.ID, nil); !errors.As(err, &inUse) {
		t.Fatalf("a queue with an open conversation was deleted: %v", err)
	}
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET status = 'resolved' WHERE id = $1`, conv); err != nil {
		t.Fatal(err)
	}
	if err := r.st.DeleteQueue(ctx, "test", r.queue.ID, nil); err != nil {
		t.Fatalf("delete: %v", err)
	}
}

// TestFirstResponseCounted fails if a first response is counted twice or not
// at all.
func TestFirstResponseCounted(t *testing.T) {
	r := newRouteRig(t, 1, queue.Linear, 5)
	ctx := context.Background()
	conv := r.customerText("hello")
	r.tick(time.Now())
	rs := r.st.MessageRouter()
	if d, err := rs.CountFirstResponses(ctx); err != nil || len(d) != 0 {
		t.Fatalf("before a reply: %v, %v", d, err)
	}
	if _, _, err := r.st.SendConversationText(ctx, r.accOf(r.users[0]), store.SendInput{ConversationID: conv, Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if d, err := rs.CountFirstResponses(ctx); err != nil || len(d) != 1 || d[0] < 0 {
		t.Fatalf("after a reply: %v, %v", d, err)
	}
	if d, err := rs.CountFirstResponses(ctx); err != nil || len(d) != 0 {
		t.Fatalf("counted twice: %v, %v", d, err)
	}
}

// TestRoutingValidation fails if a route that cannot work is accepted.
func TestRoutingValidation(t *testing.T) {
	r := newRouteRig(t, 1, queue.Linear, 5)
	ctx := context.Background()
	bad := map[string]store.MsgRoutingInput{
		"unknown queue":      {RouteKind: messaging.RouteQueue, RouteTarget: "99999"},
		"queue not id":       {RouteKind: messaging.RouteQueue, RouteTarget: "support"},
		"pool with target":   {RouteKind: messaging.RouteInbox, RouteTarget: "1"},
		"unknown user":       {RouteKind: messaging.RouteUser, RouteTarget: "99999"},
		"unknown ring group": {RouteKind: messaging.RouteRingGroup, RouteTarget: "99999"},
		"unknown extension":  {RouteKind: messaging.RouteExtension, RouteTarget: "99999"},
		"after hours target": {RouteKind: messaging.RouteInbox, AfterHours: store.AfterHours{Action: messaging.AfterRoute, RouteKind: messaging.RouteQueue, RouteTarget: "99999"}},
	}
	for name, in := range bad {
		if in.AfterHours.Action == "" {
			in.AfterHours = store.AfterHours{Action: messaging.AfterNone, RouteKind: messaging.RouteInbox}
		}
		var ve *store.ValidationError
		if _, err := r.st.PutMsgRouting(ctx, "test", r.addr.ID, in); !errors.As(err, &ve) {
			t.Errorf("%s: %v, want a validation error", name, err)
		}
	}
	// An extension nobody is linked to cannot answer.
	free, err := r.st.CreateExtension(ctx, "test", "190", "Free", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	var ve *store.ValidationError
	if _, err := r.st.PutMsgRouting(ctx, "test", r.addr.ID, store.MsgRoutingInput{RouteKind: messaging.RouteExtension, RouteTarget: strconv.FormatInt(free.ID, 10),
		AfterHours: store.AfterHours{Action: messaging.AfterNone, RouteKind: messaging.RouteInbox}}); !errors.As(err, &ve) {
		t.Errorf("an unlinked extension: %v", err)
	}
	if _, err := r.st.PutMsgRouting(ctx, "test", 99999, store.MsgRoutingInput{RouteKind: messaging.RouteInbox,
		AfterHours: store.AfterHours{Action: messaging.AfterNone, RouteKind: messaging.RouteInbox}}); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("an unknown number: %v", err)
	}
}

// TestFirstContactText fails if the first text from a customer does not get
// the first-contact text once, a later conversation of the same customer gets
// it again, it is sent with the closed-hours reply, or it goes to a customer
// who opted out.
func TestFirstContactText(t *testing.T) {
	r := newRouteRig(t, 1, queue.Linear, 5)
	ctx := context.Background()
	monday := time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)
	sunday := time.Date(2026, 11, 8, 10, 0, 0, 0, time.UTC)
	set := func(a store.AfterHours) {
		t.Helper()
		if _, err := r.st.PutMsgRouting(ctx, "test", r.addr.ID, store.MsgRoutingInput{
			RouteKind: messaging.RouteQueue, RouteTarget: strconv.FormatInt(r.queue.ID, 10), FirstContactText: "Thanks, an agent will answer",
			Schedule:   messaging.Schedule{Zone: "UTC", Days: []messaging.ScheduleDay{{Day: 1, From: "09:00", To: "17:00"}}},
			AfterHours: a}); err != nil {
			t.Fatal(err)
		}
	}
	sent := func(conv int64) int {
		return r.count(`SELECT count(*) FROM conversation_messages WHERE conversation_id = $1 AND direction = 'out' AND sender_kind = 'system' AND kind = 'message'`, conv)
	}
	set(store.AfterHours{Action: messaging.AfterNone, RouteKind: messaging.RouteInbox})
	peer := fmt.Sprintf("+3247513%04d", peerSeq.Add(1))
	first := r.ingest(peer, "hello").ConversationID
	r.tick(monday)
	if sent(first) != 1 {
		t.Fatalf("%d first-contact texts, want 1", sent(first))
	}
	// The conversation ends; the same customer starts another a month later.
	if _, err := r.db.ExecContext(ctx, `UPDATE conversations SET status = 'resolved', resolved_at = now() - interval '30 days', last_message_at = now() - interval '30 days' WHERE id = $1`, first); err != nil {
		t.Fatal(err)
	}
	second := r.ingest(peer, "hello again").ConversationID
	if second == first {
		t.Fatal("setup: the conversation was reopened")
	}
	r.tick(monday)
	if sent(second) != 0 {
		t.Fatal("a returning customer got the first-contact text again")
	}

	// Outside the hours the closed-hours reply goes instead, not both.
	set(store.AfterHours{Action: messaging.AfterReply, Text: "We are closed", RouteKind: messaging.RouteInbox})
	conv := r.customerText("hello")
	r.tick(sunday)
	if sent(conv) != 1 {
		t.Fatalf("%d automatic texts outside the hours, want 1", sent(conv))
	}
	// A customer who opts out first gets none.
	conv = r.ingest(fmt.Sprintf("+3247514%04d", peerSeq.Add(1)), "STOP").ConversationID
	r.tick(monday)
	if sent(conv) != 0 {
		t.Fatal("the first-contact text went to a customer who opted out")
	}
}
