package api

// Routing and ACD over the API (spec messaging-channels M-13, M-14): a
// number's route, the queues' text settings, the supervisors' queue view and
// an agent's login and pause. Every documented status of each new operation
// is produced here or by TestMessagingRBAC.

import (
	"context"
	"encoding/base64"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/messaging/router"
	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/secret"
)

// newRoutingRig is the inbox rig with the call queues' shared state, which
// the agent's login and pause need.
func newRoutingRig(t *testing.T) (*inboxRig, *queue.MemStore) {
	t.Helper()
	box, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	mem := queue.NewMemStore()
	e := newEnvConfig(t, Config{Live: noLive{}, Queues: mem}, box)
	r := &inboxRig{env: e}
	ctx := context.Background()
	r.admin = e.login()
	if err := e.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = $1`, testUser).Scan(&r.adminID); err != nil {
		t.Fatal(err)
	}
	r.operator, _ = e.asRole("otto", auth.RoleOperator)
	r.agent, r.agentID = e.asRole("agnes", auth.RoleUser)
	r.other, r.otherID = e.asRole("olga", auth.RoleUser)
	r.viewer, r.viewerID = e.asRole("vera", auth.RoleViewer)
	if err := e.db.QueryRowContext(ctx, `INSERT INTO trunks (name, mode, messaging) VALUES ('carrier', 'ip', TRUE) RETURNING id`).Scan(&r.trunk); err != nil {
		t.Fatal(err)
	}
	ch := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/channels",
		map[string]any{"kind": "sms", "provider": "sip_trunk", "name": "Main", "trunkId": r.trunk}).json(t)
	r.channel = int64(ch["id"].(float64))
	ad := r.admin.must(http.StatusCreated, "POST", "/api/v1/messaging/addresses",
		map[string]any{"channelId": r.channel, "address": inboxDID, "label": "Main line"}).json(t)
	r.address = int64(ad["id"].(float64))
	return r, mem
}

// TestRoutingAPI fails if the routing, the queues' text settings, the queue
// view or the agent availability routes do not answer each documented status,
// store a route that cannot work, or let a conversation reach someone outside
// its queue.
func TestRoutingAPI(t *testing.T) {
	r, mem := newRoutingRig(t)
	ctx := context.Background()
	admin := r.admin
	routing := idPath("/api/v1/messaging/addresses", r.address, "/routing")

	// agnes answers the queue through extension 101 (a dynamic member);
	// olga has an extension that is in no queue.
	ext := func(number string, user int64) int64 {
		var id int64
		if err := r.db.QueryRowContext(ctx, `INSERT INTO extensions (number, name) VALUES ($1, $2) RETURNING id`, number, "Ext "+number).Scan(&id); err != nil {
			t.Fatal(err)
		}
		if _, err := r.db.ExecContext(ctx, `INSERT INTO user_extensions (user_id, extension_id) VALUES ($1, $2)`, user, id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	agnesExt := ext("101", r.agentID)
	ext("102", r.otherID)

	t.Run("a number starts in the pool", func(t *testing.T) {
		got := admin.must(http.StatusOK, "GET", routing, nil).json(t)
		ah := got["afterHours"].(map[string]any)
		if got["routeKind"] != "inbox" || got["routeTarget"] != "" || ah["action"] != "none" || len(got["schedule"].(map[string]any)["days"].([]any)) != 0 {
			t.Fatalf("routing = %v", got)
		}
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/addresses/99999/routing", nil)
		admin.must(http.StatusNotFound, "GET", "/api/v1/messaging/addresses/zero/routing", nil)
		admin.must(http.StatusNotFound, "PUT", "/api/v1/messaging/addresses/99999/routing", map[string]any{"routeKind": "inbox"})
	})

	t.Run("queue text settings", func(t *testing.T) {
		base := map[string]any{"name": "support", "members": []map[string]any{{"extensionId": agnesExt, "dynamic": true}}}
		q := admin.must(http.StatusCreated, "POST", "/api/v1/queues", base).json(t)
		if q["textEnabled"] != false || q["textCapacity"] != float64(3) || q["textReassignSeconds"] != float64(300) || q["textRequiresIdleVoice"] != false {
			t.Fatalf("a new queue's text settings = %v", q)
		}
		id := int64(q["id"].(float64))
		for _, body := range map[string]map[string]any{
			"capacity low":   {"textCapacity": -1},
			"capacity high":  {"textCapacity": 51},
			"reassign high":  {"textReassignSeconds": 86401},
			"reassign minus": {"textReassignSeconds": -1},
		} {
			body["name"] = "support"
			admin.must(http.StatusBadRequest, "PUT", idPath("/api/v1/queues", id, ""), body)
		}
		got := admin.must(http.StatusOK, "PUT", idPath("/api/v1/queues", id, ""), map[string]any{
			"name": "support", "members": base["members"], "textEnabled": true, "textCapacity": 2, "textReassignSeconds": 120, "textRequiresIdleVoice": true}).json(t)
		if got["textEnabled"] != true || got["textCapacity"] != float64(2) || got["textReassignSeconds"] != float64(120) || got["textRequiresIdleVoice"] != true {
			t.Fatalf("queue = %v", got)
		}
		// A queue that does not take texts cannot be a route.
		off := admin.must(http.StatusCreated, "POST", "/api/v1/queues", map[string]any{"name": "voiceonly"}).json(t)
		admin.must(http.StatusBadRequest, "PUT", routing, map[string]any{"routeKind": "queue", "routeTarget": strconv.Itoa(int(off["id"].(float64)))})
	})

	var queueID int64
	if err := r.db.QueryRowContext(ctx, `SELECT id FROM queues WHERE name = 'support'`).Scan(&queueID); err != nil {
		t.Fatal(err)
	}

	t.Run("setting a route", func(t *testing.T) {
		for name, body := range map[string]map[string]any{
			"unknown kind":         {"routeKind": "carrier-pigeon"},
			"queue with no target": {"routeKind": "queue"},
			"queue not an id":      {"routeKind": "queue", "routeTarget": "support"},
			"unknown queue":        {"routeKind": "queue", "routeTarget": "99999"},
			"unknown user":         {"routeKind": "user", "routeTarget": "99999"},
			"a viewer":             {"routeKind": "user", "routeTarget": strconv.FormatInt(r.viewerID, 10)},
			"unknown ring group":   {"routeKind": "ring_group", "routeTarget": "99999"},
			"unknown extension":    {"routeKind": "extension", "routeTarget": "99999"},
			"zone missing":         {"schedule": map[string]any{"days": []map[string]any{{"day": 1, "from": "09:00", "to": "17:00"}}}},
			"backwards window":     {"schedule": map[string]any{"zone": "UTC", "days": []map[string]any{{"day": 1, "from": "17:00", "to": "09:00"}}}},
			"reply with no text":   {"afterHours": map[string]any{"action": "reply"}},
			"unknown action":       {"afterHours": map[string]any{"action": "ignore"}},
			"after-hours target":   {"afterHours": map[string]any{"action": "route", "routeKind": "queue", "routeTarget": "99999"}},
			"unknown field":        {"surprise": true},
			"control characters":   {"afterHours": map[string]any{"action": "reply", "text": "closed\x01"}},
		} {
			if got := admin.do("PUT", routing, body); got.code != http.StatusBadRequest {
				t.Errorf("%s: %d %s, want 400", name, got.code, got.body)
			}
		}
		got := admin.must(http.StatusOK, "PUT", routing, map[string]any{
			"routeKind": "queue", "routeTarget": strconv.FormatInt(queueID, 10),
			"schedule":         map[string]any{"zone": "Europe/Brussels", "days": []map[string]any{{"day": 1, "from": "09:00", "to": "17:00"}}},
			"afterHours":       map[string]any{"action": "reply", "text": "We open at nine"},
			"firstContactText": "Thanks for texting us",
		}).json(t)
		if got["routeKind"] != "queue" || got["routeName"] != "support" || got["afterHours"].(map[string]any)["text"] != "We open at nine" ||
			got["firstContactText"] != "Thanks for texting us" {
			t.Fatalf("routing = %v", got)
		}
		// What a request leaves out keeps its value.
		got = admin.must(http.StatusOK, "PUT", routing, map[string]any{"afterHours": map[string]any{"action": "none"}}).json(t)
		if got["routeKind"] != "queue" || len(got["schedule"].(map[string]any)["days"].([]any)) != 1 || got["afterHours"].(map[string]any)["text"] != "We open at nine" ||
			got["firstContactText"] != "Thanks for texting us" {
			t.Fatalf("a partial change lost a field: %v", got)
		}
		// The queue is in use now.
		admin.must(http.StatusConflict, "DELETE", idPath("/api/v1/queues", queueID, ""), nil)
		// The route shows on the address list too.
		for _, a := range listItems(t, admin.must(http.StatusOK, "GET", "/api/v1/messaging/addresses", nil)) {
			if a.(map[string]any)["id"] == float64(r.address) && a.(map[string]any)["routeKind"] != "queue" {
				t.Fatalf("the number lists as %v", a)
			}
		}
	})

	t.Run("agents answer, others do not see the pool", func(t *testing.T) {
		rt := &router.Router{Store: r.st.MessageRouter(), Live: apiLive{mem}}
		conv := r.text(peerNumber(), "hello")
		if err := rt.Tick(ctx, time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)); err != nil { // inside the hours
			t.Fatal(err)
		}
		// agnes is a dynamic member and has not logged in: the conversation waits.
		q := r.operator.must(http.StatusOK, "GET", "/api/v1/inbox/queues", nil)
		qs := listItems(t, q)
		if len(qs) != 1 {
			t.Fatalf("queues = %v", qs)
		}
		first := qs[0].(map[string]any)
		if first["waitingTotal"] != float64(1) || first["agents"].([]any)[0].(map[string]any)["state"] != "logged_out" {
			t.Fatalf("queue view = %v", first)
		}
		if w := first["waiting"].([]any)[0].(map[string]any); w["conversationId"] != float64(conv) || w["position"] != float64(1) {
			t.Fatalf("waiting = %v", w)
		}
		seen := false
		for _, c := range listItems(t, r.agent.must(http.StatusOK, "GET", "/api/v1/me/inbox/conversations?view=unassigned", nil)) {
			seen = seen || c.(map[string]any)["id"] == float64(conv)
		}
		if !seen {
			t.Fatal("a queue member does not see the waiting conversation")
		}
		r.other.must(http.StatusNotFound, "GET", idPath("/api/v1/me/inbox/conversations", conv, ""), nil)

		// agnes logs in, pauses and resumes.
		got := r.agent.must(http.StatusOK, "GET", "/api/v1/me/inbox/agent", nil).json(t)
		if got["member"] != true || got["needsLogin"] != true || got["loggedIn"] != false || got["paused"] != false {
			t.Fatalf("agent = %v", got)
		}
		got = r.agent.must(http.StatusOK, "POST", "/api/v1/me/inbox/agent", map[string]any{"loggedIn": true}).json(t)
		if got["loggedIn"] != true {
			t.Fatalf("after login = %v", got)
		}
		if st, known, _ := mem.Agent(ctx, "101"); !known || !st.LoggedIn {
			t.Fatalf("the shared state = %+v, %v", st, known)
		}
		got = r.agent.must(http.StatusOK, "POST", "/api/v1/me/inbox/agent", map[string]any{"paused": true, "reason": "lunch"}).json(t)
		if got["paused"] != true || got["reason"] != "lunch" {
			t.Fatalf("after pause = %v", got)
		}
		if err := rt.Tick(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		if n := routingCount(r, `SELECT count(*) FROM conversations WHERE id = $1 AND assignee_user_id IS NOT NULL`, conv); n != 0 {
			t.Fatal("a paused agent was given a conversation")
		}
		r.agent.must(http.StatusOK, "POST", "/api/v1/me/inbox/agent", map[string]any{"paused": false})
		if err := rt.Tick(ctx, time.Now()); err != nil {
			t.Fatal(err)
		}
		if n := routingCount(r, `SELECT count(*) FROM conversations WHERE id = $1 AND assignee_user_id = $2`, conv, r.agentID); n != 1 {
			t.Fatal("the waiting conversation was not assigned when the agent resumed")
		}
		r.agent.must(http.StatusOK, "GET", idPath("/api/v1/me/inbox/conversations", conv, ""), nil)
		cs := listItems(t, r.operator.must(http.StatusOK, "GET", "/api/v1/inbox/conversations?queueId="+strconv.FormatInt(queueID, 10), nil))
		if len(cs) != 1 || cs[0].(map[string]any)["queueName"] != "support" {
			t.Fatalf("the queue's conversations = %v", cs)
		}
		r.operator.must(http.StatusBadRequest, "GET", "/api/v1/inbox/conversations?queueId=x", nil)
		r.agent.must(http.StatusBadRequest, "GET", "/api/v1/me/inbox/conversations?queueId=0", nil)

		// What the agent may not do.
		for _, body := range []map[string]any{{}, {"reason": "x"}, {"paused": true, "reason": "bad;reason"}, {"loggedIn": true, "queues": []string{"support"}}, {"surprise": 1}} {
			r.agent.must(http.StatusBadRequest, "POST", "/api/v1/me/inbox/agent", body)
		}
		got = r.other.must(http.StatusOK, "GET", "/api/v1/me/inbox/agent", nil).json(t)
		if got["member"] != false || len(got["extensions"].([]any)) != 0 {
			t.Fatalf("a non-member's availability = %v", got)
		}
		r.other.must(http.StatusNotFound, "POST", "/api/v1/me/inbox/agent", map[string]any{"paused": true})
		if st, known, _ := mem.Agent(ctx, "102"); known && st.Paused {
			t.Fatal("a non-member paused an extension")
		}
	})
}

func routingCount(r *inboxRig, q string, args ...any) int {
	r.t.Helper()
	var n int
	if err := r.db.QueryRowContext(context.Background(), q, args...).Scan(&n); err != nil {
		r.t.Fatal(err)
	}
	return n
}

// apiLive reads the call queues' state for the router the way hello-control does.
type apiLive struct{ q queue.Store }

func (l apiLive) Agents(ctx context.Context) (map[string]queue.AgentState, error) {
	return l.q.Agents(ctx)
}
func (apiLive) BusyExtensions(context.Context) (map[string]bool, error) { return nil, nil }
