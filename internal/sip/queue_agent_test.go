package sip

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/queue"
	"github.com/azrtydxb/hello/internal/snapshot"
)

// TestApplyAgentCode covers the pure part of the agent codes.
func TestApplyAgentCode(t *testing.T) {
	snap := snapshot.New(1, testDomain, nil).WithQueues([]snapshot.Queue{
		testQueue("sales", "600", queue.RingAll, dynamic("200", 1)),
		testQueue("support", "601", queue.RingAll, dynamic("200", 1)),
	})
	now := time.Now()
	mine := []string{"sales", "support"}
	st, err := applyAgentCode(queue.AgentState{}, "200", snapshot.FeatureCode{Action: ActionQueueLogin}, "601", mine, snap, now)
	if err != nil || !st.LoggedIn || !slices.Equal(st.Queues, []string{"support"}) {
		t.Fatalf("login to one queue = %+v, %v", st, err)
	}
	if _, err := applyAgentCode(st, "200", snapshot.FeatureCode{Action: ActionQueueLogin}, "999", mine, snap, now); err == nil {
		t.Fatal("login to an unknown queue was accepted")
	}
	st, _ = applyAgentCode(st, "200", snapshot.FeatureCode{Action: ActionQueuePause, Argument: "lunch"}, "", mine, snap, now)
	if !st.Paused || st.Reason != "lunch" || !st.LoggedIn {
		t.Fatalf("pause = %+v", st)
	}
	st, _ = applyAgentCode(st, "200", snapshot.FeatureCode{Action: ActionQueueLogout}, "", mine, snap, now)
	if st.LoggedIn || st.Paused || st.Reason != "" || len(st.Queues) != 0 {
		t.Fatalf("logout = %+v", st)
	}
}

// TestQueueLamp: the login and pause lamps follow the agent's state, and a
// subscription target is recognised only for an agent of a queue.
func TestQueueLamp(t *testing.T) {
	snap := snapshot.New(1, testDomain, threeDevices()).WithQueues([]snapshot.Queue{
		testQueue("sales", "600", queue.RingAll, dynamic("200", 1), static("300", 0, 2)),
	}).WithFeatureCodes([]snapshot.FeatureCode{
		{Code: "*45", Action: ActionQueueLogin}, {Code: "*47", Action: ActionQueuePause},
	})
	if a, agent, ok := queueBLFTarget(snap, "*45200"); !ok || a != ActionQueueLogin || agent != "200" {
		t.Fatalf("target = %q %q %v", a, agent, ok)
	}
	if _, _, ok := queueBLFTarget(snap, "*45100"); ok {
		t.Fatal("a lamp for an extension that is no agent")
	}
	if _, _, ok := queueBLFTarget(snap, "*99200"); ok {
		t.Fatal("a lamp for an unknown code")
	}
	for _, tc := range []struct {
		action string
		st     queue.AgentState
		known  bool
		static bool
		want   string
	}{
		{ActionQueueLogin, queue.AgentState{}, false, false, StateIdle},
		{ActionQueueLogin, queue.AgentState{}, false, true, StateOnCall},
		{ActionQueueLogin, queue.AgentState{LoggedIn: true}, true, false, StateOnCall},
		{ActionQueueLogin, queue.AgentState{LoggedIn: false}, true, true, StateIdle},
		{ActionQueuePause, queue.AgentState{LoggedIn: true}, true, false, StateIdle},
		{ActionQueuePause, queue.AgentState{LoggedIn: true, Paused: true}, true, false, StateOnCall},
	} {
		if got := queueLampState(tc.action, tc.st, tc.known, tc.static); got != tc.want {
			t.Errorf("%s %+v known=%v static=%v = %s, want %s", tc.action, tc.st, tc.known, tc.static, got, tc.want)
		}
	}
}

// TestQueueLampNotify: a phone subscribed to an agent's login lamp is
// notified when the state changes through the shared store (the API, another
// node or a feature code). It fails if the initial NOTIFY or the change is
// missing.
func TestQueueLampNotify(t *testing.T) {
	st := queue.NewMemStore()
	pbx, _ := queuePBX(t, st, nil, testQueue("sales", "600", queue.RingAll, dynamic("200", 1)))
	a, _, _ := registerABC(t, pbx)
	if res := a.subscribe(t, "*45200", 600); res.StatusCode != 200 {
		t.Fatalf("SUBSCRIBE to the lamp = %d", res.StatusCode)
	}
	first := waitReq(t, a.notifies, "initial NOTIFY")
	if !containsState(first.Body(), "terminated") {
		t.Fatalf("initial lamp = %s", first.Body())
	}
	if err := st.SetAgent(t.Context(), queue.AgentState{Ext: "200", LoggedIn: true}); err != nil {
		t.Fatal(err)
	}
	for {
		n := waitReq(t, a.notifies, "NOTIFY after the login")
		if containsState(n.Body(), "confirmed") {
			return
		}
	}
}

func containsState(body []byte, state string) bool {
	return strings.Contains(string(body), "<state>"+state+"</state>")
}
