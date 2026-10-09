package store

// The store half of the voice runtime (plan Task 5): the view snapshot, the
// ack singleton, the idempotent call report, the advisory lock and the
// prune. They need the real schema, so they skip unless
// HELLO_TEST_DATABASE_URL is set.

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// seedRuntimeAgent inserts one enabled agent with one attached server and
// returns the agent's name.
func seedRuntimeAgent(t *testing.T, s *Store) string {
	t.Helper()
	ctx := context.Background()
	cred, err := s.box.Seal("plain-cred", "voice_mcp:1:cred")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO voice_mcp_servers (id, name, url, auth, credential, timeout_ms, enabled)
		VALUES (1, 'srv', 'http://127.0.0.1:1/mcp', 'bearer', $1, 5000, true)`, cred); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO voice_agents (name, enabled, sip_user, prompt, greeting, caller_verification, pin_hash)
		VALUES ('support', true, '2001', 'p', 'g', 'none', 'hash')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO voice_agent_mcp (agent_id, server_id, enabled, tools)
		SELECT a.id, 1, true, '[{"name":"lookup","confirm":true,"write":false}]'
		FROM voice_agents a WHERE a.name = 'support'`); err != nil {
		t.Fatal(err)
	}
	return "support"
}

// TestVoiceRuntimeSnapshot reads the view inputs back: only enabled agents,
// with the sealed credential and the attachment's tools, under the global
// revision.
func TestVoiceRuntimeSnapshot(t *testing.T) {
	s := scratchStore(t).WithSecretBox(voiceBox(t))
	ctx := context.Background()
	seedRuntimeAgent(t, s)

	rev, agents, err := s.VoiceRuntimeSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rev < 0 || len(agents) != 1 {
		t.Fatalf("rev=%d agents=%d", rev, len(agents))
	}
	a := agents[0]
	if a.Name != "support" || a.SIPUser != "2001" || a.Prompt != "p" || len(a.Servers) != 1 {
		t.Fatalf("agent = %+v", a)
	}
	sv := a.Servers[0]
	if sv.Name != "srv" || sv.Auth != "bearer" || sv.TimeoutMS != 5000 || len(sv.Tools) != 1 || sv.Tools[0].Name != "lookup" {
		t.Fatalf("server = %+v", sv)
	}
	plain, err := s.box.Open(sv.Credential, "voice_mcp:1:cred")
	if err != nil || plain != "plain-cred" {
		t.Fatalf("credential does not open: %v %q", err, plain)
	}

	if _, err := s.db.ExecContext(ctx, `UPDATE voice_agents SET enabled = false WHERE name = 'support'`); err != nil {
		t.Fatal(err)
	}
	_, agents, err = s.VoiceRuntimeSnapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(agents) != 0 {
		t.Fatalf("disabled agent still in the view: %+v", agents)
	}
}

// TestVoiceRuntimeAckRoundTrip stores an ack and reads the state back with
// the current revision and the enabled-agents flag.
func TestVoiceRuntimeAckRoundTrip(t *testing.T) {
	s := scratchStore(t).WithSecretBox(voiceBox(t))
	ctx := context.Background()
	seedRuntimeAgent(t, s)

	rev, err := s.VoiceRevision(ctx)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Truncate(time.Millisecond)
	loaded, _ := json.Marshal([]map[string]any{{"name": "support", "state": "loaded", "revision": rev}})
	if err := s.SaveVoiceRuntimeAck(ctx, 0, at, rev, "talking-agent 1.0", loaded); err != nil {
		t.Fatal(err)
	}
	st, err := s.VoiceRuntimeState(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if st.Revision != rev || st.AckRevision != rev || st.Version != "talking-agent 1.0" ||
		st.LastSeen == nil || !st.HasEnabledAgents || len(st.Loaded) == 0 {
		t.Fatalf("state = %+v", st)
	}
}

// TestVoiceAgentPolicyRead is ErrNotFound for an unknown agent, false/true
// per the record_transcript flag.
func TestVoiceAgentPolicyRead(t *testing.T) {
	s := scratchStore(t).WithSecretBox(voiceBox(t))
	ctx := context.Background()
	seedRuntimeAgent(t, s)

	if rt, err := s.VoiceAgentPolicy(ctx, "support"); err != nil || rt {
		t.Fatalf("policy = %v, %v", rt, err)
	}
	if _, err := s.db.ExecContext(ctx, `UPDATE voice_agents SET record_transcript = true WHERE name = 'support'`); err != nil {
		t.Fatal(err)
	}
	if rt, err := s.VoiceAgentPolicy(ctx, "support"); err != nil || !rt {
		t.Fatalf("policy = %v, %v", rt, err)
	}
	if _, err := s.VoiceAgentPolicy(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown agent: %v", err)
	}
}

// TestVoiceAgentCallIdempotent: the first report is stored, the repeat is
// not, and the read-back carries the transcript flag but never the
// transcript itself.
func TestVoiceAgentCallIdempotent(t *testing.T) {
	s := scratchStore(t).WithSecretBox(voiceBox(t))
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Millisecond)

	tools := []byte(`[{"tool":"lookup","ok":true,"arguments":{"q":"x"}}]`)
	saved, err := s.SaveVoiceAgentCall(ctx, "corr-1", "support", "answered", "done", tools, 11, 7, strPtr("t"), at)
	if err != nil || !saved {
		t.Fatalf("saved=%v err=%v", saved, err)
	}
	if saved, err := s.SaveVoiceAgentCall(ctx, "corr-1", "support", "failed", "other", nil, 0, 0, nil, at); err != nil || saved {
		t.Fatalf("repeat saved=%v err=%v", saved, err)
	}
	c, err := s.VoiceAgentCallByCorrelation(ctx, "corr-1")
	if err != nil {
		t.Fatal(err)
	}
	if c.AgentName != "support" || c.Outcome != "answered" || c.Summary != "done" ||
		c.TokensIn != 11 || c.TokensOut != 7 || len(c.ToolCalls) != 1 || c.ToolCalls[0].Tool != "lookup" ||
		!c.TranscriptPresent {
		t.Fatalf("call = %+v", c)
	}
	if _, err := s.VoiceAgentCallByCorrelation(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown correlation: %v", err)
	}
}

// TestVoicePruneRetention: transcripts clear at the agent's retention, the
// report row itself at the CDR retention.
func TestVoicePruneRetention(t *testing.T) {
	s := scratchStore(t).WithSecretBox(voiceBox(t))
	ctx := context.Background()
	seedRuntimeAgent(t, s)
	// One day past the agent's 30-day transcript retention, but well inside
	// the CDR retention the test uses below.
	old := time.Now().Add(-31 * 24 * time.Hour)

	if _, err := s.SaveVoiceAgentCall(ctx, "corr-old", "support", "answered", "done",
		[]byte(`[]`), 0, 0, strPtr("transcript"), old); err != nil {
		t.Fatal(err)
	}
	n, err := s.VoicePrune(ctx, time.Now(), 90*24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 { // the transcript cleared, the row itself is inside retention
		t.Fatalf("pruned %d rows, want 1", n)
	}
	c, err := s.VoiceAgentCallByCorrelation(ctx, "corr-old")
	if err != nil || c.TranscriptPresent {
		t.Fatalf("transcript survived the prune: %+v, %v", c, err)
	}

	// 61 days later the row itself is past the 90-day CDR retention.
	if n, err := s.VoicePrune(ctx, time.Now().Add(61*24*time.Hour), 90*24*time.Hour); err != nil || n != 1 {
		t.Fatalf("pruned %d rows, err %v", n, err)
	}
	if _, err := s.VoiceAgentCallByCorrelation(ctx, "corr-old"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("row survived the prune: %v", err)
	}
}

// TestTryVoiceLockHeld: a second lock of the same key is refused until the
// unlock runs.
func TestTryVoiceLockHeld(t *testing.T) {
	s := scratchStore(t)
	ctx := context.Background()

	unlock, ok, err := s.TryVoiceLock(ctx, "test:voice:lock")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.TryVoiceLock(ctx, "test:voice:lock"); err != nil || ok {
		t.Fatalf("second lock ok=%v err=%v", ok, err)
	}
	unlock()
	if _, ok, err := s.TryVoiceLock(ctx, "test:voice:lock"); err != nil || !ok {
		t.Fatalf("after unlock ok=%v err=%v", ok, err)
	}
}

func strPtr(v string) *string { return &v }
