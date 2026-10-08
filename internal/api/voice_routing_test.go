package api

import (
	"context"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/store"
)

// The voice_agent destination (spec voice-agents S-10 to S-13): ring group
// members and failure targets, and the routing test endpoint's trace. The
// compile and Decide halves live in internal/routing.

const testVoiceAddr = "192.0.2.10:5060"

// newVoiceEnv is an env with the talking-agent address configured and two
// agents in the registry: support (enabled, extension 700) and after-hours
// (disabled). The registry API is Task 2's, so the rows go in by SQL.
func newVoiceEnv(t *testing.T, voiceAddr string) *env {
	t.Helper()
	e := newEnvConfig(t, Config{VoiceSIPAddress: voiceAddr}, nil)
	c := e.login()
	e.ext101 = c.must(http.StatusCreated, "POST", "/api/v1/extensions",
		map[string]string{"number": "101", "name": "Sales"}).json(t)
	ctx := context.Background()
	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO voice_agents (name, sip_user, enabled, extension) VALUES ('support', '00000001', true, '700')`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.ExecContext(ctx,
		`INSERT INTO voice_agents (name, sip_user, enabled) VALUES ('after-hours', '00000002', false)`); err != nil {
		t.Fatal(err)
	}
	return e
}

// agentMember is a member body naming the agent with id.
func agentMember(id int64, pos int) map[string]any {
	return map[string]any{"voiceAgentId": id, "position": pos}
}

// voiceAgentID reads the enabled agent's id.
func voiceAgentID(t *testing.T, e *env, name string) int64 {
	t.Helper()
	var id int64
	if err := e.db.QueryRowContext(context.Background(),
		`SELECT id FROM voice_agents WHERE name = $1`, name).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// TestVoiceAgentRingGroupValidation is the pure half of the ring group
// story: the stage 1 rules that need no database. It fails if an agent
// member of a non-sequential strategy, a member with both or neither
// target, or a failure kind outside the enum is accepted.
func TestVoiceAgentRingGroupValidation(t *testing.T) {
	base := store.RingGroupInput{
		Name: "sales", Strategy: "sequential", RingTimeout: 25,
		FailureKind: "voice_agent", FailureTarget: "support",
		Members: []store.RingGroupMember{
			{ExtensionID: 1, Position: 1, Weight: 1},
			{VoiceAgentID: 2, Position: 2, Weight: 1},
		},
	}
	fields := func(f fieldErrs) string {
		out := make([]string, len(f))
		for i, e := range f {
			out[i] = e.Path
		}
		return strings.Join(out, ",")
	}
	cases := []struct {
		name     string
		mut      func(*store.RingGroupInput)
		wantPath string
	}{
		{"ring-all with an agent member", func(in *store.RingGroupInput) {
			in.Strategy = "ring-all"
		}, "members[1].voiceAgentId"},
		{"longest-idle with an agent member", func(in *store.RingGroupInput) {
			in.Strategy = "longest-idle"
		}, "members[1].voiceAgentId"},
		{"round-robin with an agent member", func(in *store.RingGroupInput) {
			in.Strategy = "round-robin"
		}, "members[1].voiceAgentId"},
		{"weighted with an agent member", func(in *store.RingGroupInput) {
			in.Strategy = "weighted"
		}, "members[1].voiceAgentId"},
		{"member with both targets", func(in *store.RingGroupInput) {
			in.Members[1].ExtensionID = 1
		}, "members[1]"},
		{"member with neither target", func(in *store.RingGroupInput) {
			in.Members[1].VoiceAgentID = 0
		}, "members[1]"},
		{"agent member twice", func(in *store.RingGroupInput) {
			in.Members[1].ExtensionID = 0
			in.Members = append(in.Members, store.RingGroupMember{VoiceAgentID: 2, Position: 3})
		}, "members[2].voiceAgentId"},
		{"unknown failure kind", func(in *store.RingGroupInput) {
			in.FailureKind = "announcement"
		}, "failureKind"},
		{"failure target empty", func(in *store.RingGroupInput) {
			in.FailureTarget = ""
		}, "failureTarget"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base
			in.Members = slices.Clone(base.Members)
			tc.mut(&in)
			f := validateRingGroup(&in)
			if !strings.Contains(fields(f), tc.wantPath) {
				t.Fatalf("fields = %v, want one on %s", f, tc.wantPath)
			}
		})
	}
	// The unmutated base is accepted.
	if f := validateRingGroup(&base); len(f) != 0 {
		t.Fatalf("base input rejected: %v", f)
	}
}

func TestVoiceAgentRingGroup(t *testing.T) {
	e := newVoiceEnv(t, testVoiceAddr)
	c := e.login()
	support := voiceAgentID(t, e, "support")
	extID := e.ext101["id"]

	group := func(members []map[string]any) map[string]any {
		return map[string]any{
			"name": "sales", "strategy": "sequential", "ringTimeout": 25,
			"failureKind": "voice_agent", "failureTarget": "support",
			"members": members,
		}
	}

	// An agent member is refused in every strategy but sequential (S-12: an
	// agent answers instantly and would always win).
	for _, strategy := range []string{"ring-all", "round-robin", "longest-idle", "weighted"} {
		b := group([]map[string]any{agentMember(support, 1)})
		b["strategy"] = strategy
		r := c.do("POST", "/api/v1/ring-groups", b)
		if r.code != http.StatusBadRequest || !strings.Contains(string(r.body), "voiceAgentId") {
			t.Fatalf("%s with an agent member = %d %s, want 400 naming voiceAgentId", strategy, r.code, r.body)
		}
	}

	// A member is an extension or an agent, never both, never neither.
	bad := group([]map[string]any{{"extensionId": extID, "voiceAgentId": support, "position": 1}})
	if r := c.do("POST", "/api/v1/ring-groups", bad); r.code != http.StatusBadRequest {
		t.Fatalf("member with both targets = %d, want 400", r.code)
	}
	bad = group([]map[string]any{{"position": 1}})
	if r := c.do("POST", "/api/v1/ring-groups", bad); r.code != http.StatusBadRequest {
		t.Fatalf("member with neither target = %d, want 400", r.code)
	}

	// The references must exist and be enabled, as member and as target.
	bad = group([]map[string]any{agentMember(999999, 1)})
	if r := c.do("POST", "/api/v1/ring-groups", bad); r.code != http.StatusBadRequest || !strings.Contains(string(r.body), "existing voice agent") {
		t.Fatalf("unknown agent member = %d %s, want 400", r.code, r.body)
	}
	bad = group([]map[string]any{agentMember(voiceAgentID(t, e, "after-hours"), 1)})
	if r := c.do("POST", "/api/v1/ring-groups", bad); r.code != http.StatusBadRequest || !strings.Contains(string(r.body), "disabled") {
		t.Fatalf("disabled agent member = %d %s, want 400", r.code, r.body)
	}
	bad = group([]map[string]any{agentMember(support, 1)})
	bad["failureTarget"] = "no-such-agent"
	if r := c.do("POST", "/api/v1/ring-groups", bad); r.code != http.StatusBadRequest || !strings.Contains(string(r.body), "does not exist") {
		t.Fatalf("missing failure target = %d %s, want 400", r.code, r.body)
	}
	bad["failureTarget"] = "after-hours"
	if r := c.do("POST", "/api/v1/ring-groups", bad); r.code != http.StatusBadRequest || !strings.Contains(string(r.body), "disabled") {
		t.Fatalf("disabled failure target = %d %s, want 400", r.code, r.body)
	}

	// The sequential group with the agent member and failure target stores.
	created := c.must(http.StatusCreated, "POST", "/api/v1/ring-groups", group([]map[string]any{
		{"extensionId": extID, "position": 1},
		agentMember(support, 2),
	})).json(t)
	members := created["members"].([]any)
	last := members[1].(map[string]any)
	if last["voiceAgentId"] == nil || last["name"] != "support" || last["number"] != "700" {
		t.Fatalf("agent member = %v, want voiceAgentId, name support and number 700", last)
	}
	first := members[0].(map[string]any)
	if first["voiceAgentId"] != nil || first["number"] != "101" {
		t.Fatalf("extension member = %v, want no voiceAgentId", first)
	}

	// GET shows the same shape.
	got := c.must(http.StatusOK, "GET", "/api/v1/ring-groups/"+jsonID(created["id"]), nil).json(t)
	if len(got["members"].([]any)) != 2 {
		t.Fatalf("get = %v", got)
	}

	// A PATCH back to extensions-only works, and the failure kind accepts
	// voice_agent on the way back.
	patched := c.must(http.StatusOK, "PATCH", "/api/v1/ring-groups/"+jsonID(created["id"]),
		map[string]any{"members": []map[string]any{{"extensionId": extID, "position": 1}}}).json(t)
	if len(patched["members"].([]any)) != 1 {
		t.Fatalf("patched members = %v", patched["members"])
	}
}

func TestVoiceAgentRoutingEndpoint(t *testing.T) {
	e := newVoiceEnv(t, testVoiceAddr)
	c := e.login()

	// A route naming a missing agent is refused with the field named, and
	// voice_not_configured without the address (S-17).
	bad := map[string]any{
		"name": "Agent DID", "didKind": "exact", "did": "97142221000",
		"destinationKind": "voice_agent", "destination": "no-such-agent",
	}
	r := c.do("POST", "/api/v1/inbound-routes", bad)
	if r.code != http.StatusBadRequest || !strings.Contains(string(r.body), "does not exist") {
		t.Fatalf("route to missing agent = %d %s, want 400", r.code, r.body)
	}

	e2 := newVoiceEnv(t, "")
	r2 := e2.login()
	ok := map[string]any{
		"name": "Agent DID", "didKind": "exact", "did": "97142221000",
		"destinationKind": "voice_agent", "destination": "support",
	}
	if r := r2.do("POST", "/api/v1/inbound-routes", ok); r.code != http.StatusBadRequest || !strings.Contains(string(r.body), "not configured") {
		t.Fatalf("route to agent without an address = %d %s, want 400 not configured", r.code, r.body)
	}

	// The valid route stores, and the test endpoint shows the agent step.
	c.must(http.StatusCreated, "POST", "/api/v1/trunks", map[string]any{
		"name": "peer", "mode": "ip", "destinations": []map[string]any{{"host": "10.0.0.5"}},
	})
	c.must(http.StatusCreated, "POST", "/api/v1/inbound-routes", ok)
	out := c.must(http.StatusOK, "POST", "/api/v1/routing/test",
		map[string]any{"from": "trunk:1", "number": "97142221000"}).json(t)
	decision := out["decision"].(map[string]any)
	if decision["voiceAgent"] != "support" || decision["sipUri"] != "sip:00000001@"+testVoiceAddr {
		t.Fatalf("decision = %v, want the agent and its sip_user URI", decision)
	}
	trace := out["trace"].([]any)
	found := false
	for _, s := range trace {
		if strings.Contains(s.(map[string]any)["text"].(string), `Destination: voice agent "support" (sip:00000001@`) {
			found = true
		}
	}
	if !found {
		t.Fatalf("trace = %v, want the voice agent step", trace)
	}

	// Dialling the agent's extension resolves to the agent too (S-13).
	out = c.must(http.StatusOK, "POST", "/api/v1/routing/test",
		map[string]any{"from": "101", "number": "700"}).json(t)
	decision = out["decision"].(map[string]any)
	if decision["voiceAgent"] != "support" {
		t.Fatalf("extension dial decision = %v, want the agent", decision)
	}
}

// jsonID renders a JSON id (float64) as a path fragment.
func jsonID(v any) string {
	return strconv.FormatInt(int64(v.(float64)), 10)
}
