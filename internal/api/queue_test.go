package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/store"
)

func TestValidateQueue(t *testing.T) {
	good := func() store.QueueInput {
		hang := store.IVRDestination{Kind: "hangup"}
		return store.QueueInput{
			Name: "sales", Number: "600", Enabled: true, Strategy: "ring_all", AgentTimeout: 20, RetryDelay: 5, MaxWait: 600,
			Timeout: hang, Full: hang, NoAgents: hang,
			Members: []store.QueueMember{{ExtensionID: 1, Penalty: 0, Position: 1}, {ExtensionID: 2, Dynamic: true, Penalty: 5, Position: 2}},
		}
	}
	if f := validateQueue(ptr(good())); len(f) > 0 {
		t.Fatalf("a good queue failed: %v", f)
	}
	cases := map[string]func(in *store.QueueInput){
		"bad name":          func(in *store.QueueInput) { in.Name = "a b" },
		"bad number":        func(in *store.QueueInput) { in.Number = "6x" },
		"bad strategy":      func(in *store.QueueInput) { in.Strategy = "fastest" },
		"agent timeout low": func(in *store.QueueInput) { in.AgentTimeout = 1 },
		"retry delay":       func(in *store.QueueInput) { in.RetryDelay = 121 },
		"wrap up":           func(in *store.QueueInput) { in.WrapUp = 601 },
		"max wait":          func(in *store.QueueInput) { in.MaxWait = 86401 },
		"bad destination":   func(in *store.QueueInput) { in.Full = store.IVRDestination{Kind: "fax"} },
		"queue dest shape":  func(in *store.QueueInput) { in.Timeout = store.IVRDestination{Kind: "queue", Target: "a b"} },
		"penalty":           func(in *store.QueueInput) { in.Members[0].Penalty = 100 },
		"twice position":    func(in *store.QueueInput) { in.Members[1].Position = 1 },
		"breakout key only": func(in *store.QueueInput) { in.BreakoutKey = "1" },
		"announce position needs prompts": func(in *store.QueueInput) {
			in.AnnounceFrequency, in.AnnouncePosition = 30, true
		},
		"announce needs frequency": func(in *store.QueueInput) { in.AnnounceWait = true },
		"bad moh class":            func(in *store.QueueInput) { in.MOHClassID = ptr(int64(-1)) },
	}
	for name, mut := range cases {
		in := good()
		mut(&in)
		if f := validateQueue(&in); len(f) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}

// TestQueueCRUD: create, read, replace and delete a queue; references must
// exist, failover loops are refused, a queue something names cannot be
// renamed or deleted, and the destination pickers (inbound routes, ring group
// failover, IVR options, feature codes) accept a queue. It fails if an
// unknown reference is stored, a loop is accepted or a referenced queue can
// be deleted.
func TestQueueCRUD(t *testing.T) {
	e := newEnvConfig(t, Config{Objects: newMemObjects(), Queues: queue.NewMemStore()}, nil)
	c := e.login()
	ctx := context.Background()
	e.ext101 = c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	e.ext102 = c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"}).json(t)
	greet, err := e.st.CreateAnnouncement(ctx, "test", "greet", "ann/greet.wav", nil)
	if err != nil {
		t.Fatal(err)
	}
	members := []map[string]any{
		{"extensionId": e.ext101["id"], "penalty": 0},
		{"extensionId": e.ext102["id"], "dynamic": true, "penalty": 3},
	}
	good := map[string]any{"name": "sales", "number": "600", "strategy": "longest_idle", "wrapUp": 10, "members": members,
		"announceFrequency": 30, "announcePosition": true, "positionPromptId": greet.ID, "digitsPrefix": "d"}
	created := c.must(http.StatusCreated, "POST", "/api/v1/queues", good).json(t)
	id := fmt.Sprint(created["id"])
	if created["strategy"] != "longest_idle" || created["agentTimeout"] != float64(20) || created["maxWait"] != float64(600) ||
		created["joinEmpty"] != true || created["positionPromptName"] != "greet" {
		t.Fatalf("created = %v", created)
	}
	ms := created["members"].([]any)
	if len(ms) != 2 || ms[0].(map[string]any)["number"] != "101" || ms[1].(map[string]any)["dynamic"] != true {
		t.Fatalf("members = %v", ms)
	}

	if l := c.must(http.StatusOK, "GET", "/api/v1/queues", nil).json(t)["items"].([]any); len(l) != 1 {
		t.Fatalf("list = %v", l)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/queues", good)
	c.must(http.StatusConflict, "POST", "/api/v1/queues", map[string]any{"name": "other", "number": "600"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/queues", map[string]any{"name": "x", "number": "101"})
	for name, body := range map[string]map[string]any{
		"unknown extension": {"name": "a", "members": []map[string]any{{"extensionId": 99999}}},
		"unknown prompt":    {"name": "a", "announceFrequency": 5, "announcePosition": true, "positionPromptId": 99999, "digitsPrefix": "d"},
		"unknown dest":      {"name": "a", "timeout": map[string]any{"kind": "queue", "target": "ghost"}},
		"self dest":         {"name": "a", "timeout": map[string]any{"kind": "queue", "target": "a"}},
		"unknown property":  {"name": "a", "extra": 1},
		"missing name":      {"number": "610"},
		"duplicate member":  {"name": "a", "members": []map[string]any{{"extensionId": e.ext101["id"]}, {"extensionId": e.ext101["id"], "position": 2}}},
		"breakout no box":   {"name": "a", "breakoutKey": "1", "breakoutVoicemail": "999"},
	} {
		if r := c.do("POST", "/api/v1/queues", body); r.code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, r.code)
		}
	}

	// Failover to another queue; a loop through failover destinations is refused.
	spill := c.must(http.StatusCreated, "POST", "/api/v1/queues", map[string]any{
		"name": "spill", "timeout": map[string]any{"kind": "queue", "target": "sales"}}).json(t)
	put := map[string]any{"name": "sales", "number": "600", "members": members, "timeout": map[string]any{"kind": "queue", "target": "spill"}}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/queues/"+id, put)
	put["timeout"] = map[string]any{"kind": "hangup", "target": ""}
	put["strategy"] = "random"
	got := c.must(http.StatusOK, "PUT", "/api/v1/queues/"+id, put).json(t)
	if got["strategy"] != "random" || got["announceFrequency"] != float64(0) {
		t.Fatalf("replaced = %v", got)
	}
	c.must(http.StatusNotFound, "PUT", "/api/v1/queues/987654", good)
	c.must(http.StatusNotFound, "GET", "/api/v1/queues/987654", nil)
	c.must(http.StatusOK, "GET", "/api/v1/queues/"+id, nil)

	// A queue something names cannot be deleted or renamed.
	c.must(http.StatusConflict, "DELETE", "/api/v1/queues/"+id, nil)
	put["name"] = "sales2"
	c.must(http.StatusConflict, "PUT", "/api/v1/queues/"+id, put)
	put["name"] = "sales"

	// The destination pickers take a queue.
	c.must(http.StatusBadRequest, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "in", "didKind": "any", "destinationKind": "queue", "destination": "ghost"})
	in := c.must(http.StatusCreated, "POST", "/api/v1/routes/inbound", map[string]any{
		"name": "in", "didKind": "any", "destinationKind": "queue", "destination": "sales"}).json(t)
	member := []map[string]any{{"extensionId": e.ext101["id"], "position": 1}}
	c.must(http.StatusBadRequest, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "g", "members": member, "failureKind": "queue", "failureTarget": "ghost"})
	rg := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", map[string]any{
		"name": "g", "members": member, "failureKind": "queue", "failureTarget": "sales"}).json(t)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/ring-groups/%v", rg["id"]), nil)
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/routes/inbound/%v", in["id"]), nil)
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*45", "action": "queue_login"}, {"code": "*46", "action": "queue_logout"},
		{"code": "*47", "action": "queue_pause", "argument": "break"}, {"code": "*48", "action": "queue_unpause"}}})
	// An agent code that collides with another code or a page code is refused.
	c.must(http.StatusBadRequest, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{
		{"code": "*45", "action": "queue_login"}, {"code": "*45", "action": "queue_logout"}}})
	c.must(http.StatusOK, "PUT", "/api/v1/feature-codes", map[string]any{"items": []map[string]any{}})

	// Deleting: spill points at sales, sales points at nothing.
	c.must(http.StatusNoContent, "DELETE", fmt.Sprintf("/api/v1/queues/%v", spill["id"]), nil)
	c.must(http.StatusNoContent, "DELETE", "/api/v1/queues/"+id, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/queues/"+id, nil)
}

// TestQueueAgentsAndStatus: agents log in and out and pause with a reason
// through the API, an unknown extension or a bad reason is refused, and the
// wallboard feed shows the agents' states and the waiting callers. It fails
// if a state is not stored, a pause survives a logout, or the feed lacks a
// waiting caller.
func TestQueueAgentsAndStatus(t *testing.T) {
	qs := queue.NewMemStore()
	e := newEnvConfig(t, Config{Objects: newMemObjects(), Queues: qs}, nil)
	c := e.login()
	ctx := context.Background()
	ext := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Other"})
	c.must(http.StatusCreated, "POST", "/api/v1/queues", map[string]any{"name": "sales", "number": "600",
		"members": []map[string]any{{"extensionId": ext["id"], "dynamic": true}}})

	agents := c.must(http.StatusOK, "GET", "/api/v1/queue-agents", nil).json(t)["items"].([]any)
	if len(agents) != 1 || agents[0].(map[string]any)["loggedIn"] != false {
		t.Fatalf("agents = %v", agents)
	}
	got := c.must(http.StatusOK, "PUT", "/api/v1/queue-agents/101", map[string]any{"loggedIn": true}).json(t)
	if got["loggedIn"] != true || got["paused"] != false {
		t.Fatalf("login = %v", got)
	}
	got = c.must(http.StatusOK, "PUT", "/api/v1/queue-agents/101", map[string]any{"paused": true, "reason": "lunch"}).json(t)
	if got["paused"] != true || got["reason"] != "lunch" || got["loggedIn"] != true {
		t.Fatalf("pause = %v", got)
	}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/queue-agents/101", map[string]any{"reason": "bad;reason"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/queue-agents/101", map[string]any{"queues": []string{"ghost"}})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/queue-agents/101", map[string]any{"extra": true})
	c.must(http.StatusNotFound, "PUT", "/api/v1/queue-agents/102", map[string]any{"loggedIn": true})
	c.must(http.StatusNotFound, "PUT", "/api/v1/queue-agents/x", map[string]any{"loggedIn": true})

	if _, err := qs.Join(ctx, queue.Entry{CallID: "call-1", Queue: "sales", Caller: "555", Node: "sip-1"}, 0); err != nil {
		t.Fatal(err)
	}
	status := c.must(http.StatusOK, "GET", "/api/v1/queues/status", nil).json(t)["items"].([]any)
	if len(status) != 1 {
		t.Fatalf("status = %v", status)
	}
	s := status[0].(map[string]any)
	if w := s["waiting"].([]any); len(w) != 1 || w[0].(map[string]any)["position"] != float64(1) {
		t.Fatalf("waiting = %v", s["waiting"])
	}
	if a := s["agents"].([]any)[0].(map[string]any); a["state"] != "paused" || s["staffed"] != float64(0) {
		t.Fatalf("agent state = %v, staffed %v", a, s["staffed"])
	}
	c.must(http.StatusOK, "PUT", "/api/v1/queue-agents/101", map[string]any{"paused": false})
	status = c.must(http.StatusOK, "GET", "/api/v1/queues/status", nil).json(t)["items"].([]any)
	if a := status[0].(map[string]any)["agents"].([]any)[0].(map[string]any); a["state"] != "available" {
		t.Fatalf("agent after unpause = %v", a)
	}
	got = c.must(http.StatusOK, "PUT", "/api/v1/queue-agents/101", map[string]any{"paused": true, "loggedIn": false}).json(t)
	if got["loggedIn"] != false {
		t.Fatalf("logout = %v", got)
	}
	if st, _, _ := qs.Agent(ctx, "101"); st.LoggedIn {
		t.Fatalf("state after logout = %+v", st)
	}
}
