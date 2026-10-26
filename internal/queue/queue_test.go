package queue

import (
	"reflect"
	"testing"
	"time"
)

func cand(ext string, penalty, pos, calls int, lastEnd time.Time) Candidate {
	return Candidate{Ext: ext, Penalty: penalty, Position: pos, Calls: calls, LastEnd: lastEnd}
}

// TestPlanStrategies covers the six ring strategies on one agent set. It
// fails if an order differs from the strategy's definition.
func TestPlanStrategies(t *testing.T) {
	base := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	cands := []Candidate{
		cand("101", 0, 1, 5, base.Add(3*time.Minute)),
		cand("102", 0, 2, 2, base.Add(1*time.Minute)),
		cand("103", 0, 3, 2, time.Time{}), // never had a call
		cand("104", 0, 4, 9, base.Add(2*time.Minute)),
	}
	flat := func(rounds [][]string) []string {
		var out []string
		for _, r := range rounds {
			out = append(out, r...)
		}
		return out
	}
	tests := []struct {
		name     string
		strategy string
		rr       uint64
		want     [][]string
	}{
		{"ring all rings the tier together", RingAll, 0, [][]string{{"101", "102", "103", "104"}}},
		{"linear keeps the configured order", Linear, 7, [][]string{{"101"}, {"102"}, {"103"}, {"104"}}},
		{"longest idle puts never-called first, then the oldest end", LongestIdle, 0, [][]string{{"103"}, {"102"}, {"104"}, {"101"}}},
		{"fewest calls, ties by idle time", FewestCalls, 0, [][]string{{"103"}, {"102"}, {"101"}, {"104"}}},
		{"round robin starts at the rotation", RoundRobin, 2, [][]string{{"103"}, {"104"}, {"101"}, {"102"}}},
		{"round robin wraps the rotation", RoundRobin, 5, [][]string{{"102"}, {"103"}, {"104"}, {"101"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Plan(tc.strategy, cands, tc.rr, 1); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Plan = %v, want %v", got, tc.want)
			}
		})
	}
	t.Run("random is a permutation, stable per seed, varied across seeds", func(t *testing.T) {
		a := flat(Plan(Random, cands, 0, 42))
		if b := flat(Plan(Random, cands, 0, 42)); !reflect.DeepEqual(a, b) {
			t.Fatalf("same seed ordered %v then %v", a, b)
		}
		seen := map[string]bool{}
		for _, e := range a {
			seen[e] = true
		}
		if len(a) != 4 || len(seen) != 4 {
			t.Fatalf("random order %v is not a permutation", a)
		}
		differs := false
		for seed := uint64(0); seed < 50 && !differs; seed++ {
			differs = !reflect.DeepEqual(flat(Plan(Random, cands, 0, seed)), a)
		}
		if !differs {
			t.Fatal("50 seeds all ordered the agents the same")
		}
	})
	t.Run("no candidates", func(t *testing.T) {
		if got := Plan(RingAll, nil, 0, 0); len(got) != 0 {
			t.Fatalf("Plan(nil) = %v", got)
		}
	})
}

// TestPlanPenalties: a lower penalty is tried first, a higher one only after
// every agent below it, under every strategy. It fails if a high-penalty
// agent is rung before a low-penalty one.
func TestPlanPenalties(t *testing.T) {
	cands := []Candidate{
		cand("201", 5, 1, 0, time.Time{}),
		cand("202", 0, 2, 0, time.Time{}),
		cand("203", 5, 3, 0, time.Time{}),
		cand("204", 0, 4, 0, time.Time{}),
		cand("205", 9, 5, 0, time.Time{}),
	}
	for _, s := range Strategies {
		rounds := Plan(s, cands, 3, 11)
		order := map[string]int{}
		for i, r := range rounds {
			for _, e := range r {
				order[e] = i
			}
		}
		if len(order) != 5 {
			t.Fatalf("%s: %v lost an agent", s, rounds)
		}
		for _, low := range []string{"202", "204"} {
			for _, mid := range []string{"201", "203"} {
				if order[low] >= order[mid] {
					t.Errorf("%s: penalty 0 agent %s is not before penalty 5 agent %s: %v", s, low, mid, rounds)
				}
				if order[mid] >= order["205"] {
					t.Errorf("%s: penalty 5 agent %s is not before penalty 9 agent 205: %v", s, mid, rounds)
				}
			}
		}
	}
	got := Plan(RingAll, cands, 0, 0)
	want := [][]string{{"202", "204"}, {"201", "203"}, {"205"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ring_all tiers = %v, want %v", got, want)
	}
}

// TestMemberStaffed: static members are always staffed, dynamic ones only
// while logged in to the queue, and a pause removes either.
func TestMemberStaffed(t *testing.T) {
	static := Member{Ext: "101"}
	dyn := Member{Ext: "102", Dynamic: true}
	cases := []struct {
		name  string
		m     Member
		st    AgentState
		known bool
		want  bool
	}{
		{"static, no state", static, AgentState{}, false, true},
		{"static, paused", static, AgentState{Paused: true}, true, false},
		{"dynamic, never logged in", dyn, AgentState{}, false, false},
		{"dynamic, logged out", dyn, AgentState{LoggedIn: false}, true, false},
		{"dynamic, logged in to all", dyn, AgentState{LoggedIn: true}, true, true},
		{"dynamic, logged in to this queue", dyn, AgentState{LoggedIn: true, Queues: []string{"sales"}}, true, true},
		{"dynamic, logged in to another queue", dyn, AgentState{LoggedIn: true, Queues: []string{"support"}}, true, false},
		{"dynamic, logged in but paused", dyn, AgentState{LoggedIn: true, Paused: true}, true, false},
	}
	for _, tc := range cases {
		if got := tc.m.Staffed("sales", tc.st, tc.known); got != tc.want {
			t.Errorf("%s: Staffed = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestWrapUp: an agent is wrapping until the wrap-up time has passed.
func TestWrapUp(t *testing.T) {
	now := time.Date(2026, 10, 25, 9, 0, 0, 0, time.UTC)
	s := AgentStats{LastEndMs: now.UnixMilli(), WrapUntilMs: now.Add(30 * time.Second).UnixMilli()}
	if !s.Wrapping(now.Add(29 * time.Second)) {
		t.Error("not wrapping 1 s before the end")
	}
	if s.Wrapping(now.Add(30 * time.Second)) {
		t.Error("still wrapping at the end")
	}
	if (AgentStats{}).Wrapping(now) {
		t.Error("an agent who never had a call is wrapping")
	}
	if !(AgentStats{}).LastEnd().IsZero() {
		t.Error("LastEnd of a new agent is not zero")
	}
}

// TestEstimatedWait covers the position and wait-time numbers announced.
func TestEstimatedWait(t *testing.T) {
	cases := []struct {
		pos, staffed int
		avg, want    time.Duration
	}{
		{1, 3, 4 * time.Minute, 0},
		{4, 1, 2 * time.Minute, 6 * time.Minute},
		{4, 3, 3 * time.Minute, 3 * time.Minute},
		{3, 2, 0, 3 * time.Minute}, // unmeasured: the default handling time
		{3, 0, time.Minute, 0},     // nobody to serve
		{0, 2, time.Minute, 0},
	}
	for _, tc := range cases {
		if got := EstimatedWait(tc.pos, tc.avg, tc.staffed); got != tc.want {
			t.Errorf("EstimatedWait(%d, %v, %d) = %v, want %v", tc.pos, tc.avg, tc.staffed, got, tc.want)
		}
	}
	for d, want := range map[time.Duration]int{0: 1, time.Second: 1, time.Minute: 1, time.Minute + 1: 2, 5*time.Minute - time.Second: 5} {
		if got := Minutes(d); got != want {
			t.Errorf("Minutes(%v) = %d, want %d", d, got, want)
		}
	}
}

// TestPositionsAndNextInLine: callers are ordered by when they joined, and a
// caller may be offered only when every caller ahead is already being
// served.
func TestPositionsAndNextInLine(t *testing.T) {
	es := []Entry{
		{CallID: "c", EnteredMs: 300},
		{CallID: "a", EnteredMs: 100},
		{CallID: "b", EnteredMs: 100},
	}
	Sort(es)
	if es[0].CallID != "a" || es[1].CallID != "b" || es[2].CallID != "c" {
		t.Fatalf("order = %v", es)
	}
	if Position(es, "c") != 3 || Position(es, "a") != 1 || Position(es, "zz") != 0 {
		t.Fatalf("positions wrong: %d %d %d", Position(es, "c"), Position(es, "a"), Position(es, "zz"))
	}
	if !NextInLine(es, "a") || NextInLine(es, "b") || NextInLine(es, "c") {
		t.Fatal("only the head is next in line while nobody is being served")
	}
	es[0].Offering = "101"
	if !NextInLine(es, "b") || NextInLine(es, "c") {
		t.Fatal("with the head being served the second is next in line, the third is not")
	}
	es[1].Offering = "102"
	if !NextInLine(es, "c") {
		t.Fatal("with both ahead served the third is next in line")
	}
	if NextInLine(es, "gone") {
		t.Fatal("a call that is not queued is next in line")
	}
}
