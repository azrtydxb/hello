package store

// The store half of the voice registry's persistence (plan Task 2): the
// sealed credential's additional-data binding, the change transaction's
// revision bumps, the version prune and the restore. They need the real
// schema, so they skip unless HELLO_TEST_DATABASE_URL is set.

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/secret"
)

func voiceBox(t *testing.T) *secret.Box {
	t.Helper()
	box, err := secret.New(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("v", 32))))
	if err != nil {
		t.Fatal(err)
	}
	return box
}

func voiceAgentIn(name string) VoiceAgentInput {
	return VoiceAgentInput{Name: name, Enabled: true, Prompt: "prompt " + name, Greeting: "hello",
		CallerVerification: "none"}
}

// TestVoiceMCPServers is the internal/store half (acceptance S-5): the
// credential is sealed under its own row's additional data, a PUT without a
// credential keeps it, and every change bumps the global voice revision in
// its own transaction. Mutation check: removing the bumpVoiceRevision call
// from CreateVoiceAgent (or the COALESCE keeping the credential in
// UpdateVoiceMCPServer) fails the revision and keep assertions.
func TestVoiceMCPServers(t *testing.T) {
	s := scratchStore(t).WithSecretBox(voiceBox(t))
	ctx := context.Background()

	rev := func() int64 {
		var r int64
		if err := s.db.QueryRowContext(ctx, `SELECT revision FROM voice_revision WHERE id = 1`).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	rev0 := rev()

	created, err := s.CreateVoiceMCPServer(ctx, "test", NewVoiceMCPServer{
		Name: "srv", URL: "http://127.0.0.1:1/mcp", Auth: "bearer", Credential: "plain-secret",
		TimeoutMS: 5000, Enabled: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !created.CredentialSet || created.LastCheckStatus != "" {
		t.Fatalf("created = %+v", created)
	}
	if rev() != rev0+1 {
		t.Fatalf("voice revision after create = %d, want %d", rev(), rev0+1)
	}

	// The seal opens under the row's additional data only.
	plain, err := s.VoiceMCPServerCredential(ctx, created.ID)
	if err != nil || plain != "plain-secret" {
		t.Fatalf("credential = %q, %v", plain, err)
	}

	// A PUT without a credential keeps the stored one; the voice revision
	// still moves (the URL an agent calls changed).
	updated, err := s.UpdateVoiceMCPServer(ctx, "test", created.ID, NewVoiceMCPServer{
		Name: "srv", URL: "http://127.0.0.1:2/mcp", Auth: "bearer", TimeoutMS: 5000, Enabled: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !updated.CredentialSet {
		t.Fatal("the PUT lost the credential")
	}
	if plain, err := s.VoiceMCPServerCredential(ctx, created.ID); err != nil || plain != "plain-secret" {
		t.Fatalf("credential after PUT = %q, %v", plain, err)
	}
	if rev() != rev0+2 {
		t.Fatalf("voice revision after update = %d, want %d", rev(), rev0+2)
	}

	// The check result stores the status and the time, never a body.
	if err := s.SetVoiceMCPCheck(ctx, created.ID, "ok"); err != nil {
		t.Fatal(err)
	}
	got, _, err := s.GetVoiceMCPServer(ctx, created.ID)
	if err != nil || got.LastCheckStatus != "ok" || got.LastCheckAt == nil {
		t.Fatalf("server after check = %+v (%v)", got, err)
	}
}

// TestVoiceAgentRevisionBump fails if a persona save, an attachment change
// or a delete does not move the global voice revision, or if the per-agent
// revision or the version prune drift from the spec's 10 kept versions
// (spec S-4).
func TestVoiceAgentRevisionBump(t *testing.T) {
	s := scratchStore(t).WithSecretBox(voiceBox(t))
	ctx := context.Background()
	rev := func() int64 {
		var r int64
		if err := s.db.QueryRowContext(ctx, `SELECT revision FROM voice_revision WHERE id = 1`).Scan(&r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	a, err := s.CreateVoiceAgent(ctx, "test", voiceAgentIn("one"), 50, nil)
	if err != nil {
		t.Fatal(err)
	}
	if a.Revision != 1 {
		t.Fatalf("created revision = %d", a.Revision)
	}
	// The save with the same persona still bumps the revision but adds no
	// version row.
	same := voiceAgentIn("one")
	same.Prompt = a.Prompt
	if _, err := s.UpdateVoiceAgent(ctx, "test", a.ID, same, nil); err != nil {
		t.Fatal(err)
	}
	n := func() int {
		var n int
		if err := s.db.QueryRowContext(ctx,
			`SELECT count(*) FROM voice_agent_versions WHERE agent_id = $1`, a.ID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n() != 1 {
		t.Fatalf("unchanged persona added a version row: %d", n())
	}
	// Ten persona changes: 11 versions total, 10 kept, newest revision
	// 12.
	for i := 0; i < 10; i++ {
		in := voiceAgentIn("one")
		in.Prompt = "prompt v" + string(rune('a'+i))
		if _, err := s.UpdateVoiceAgent(ctx, "test", a.ID, in, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n() != 10 {
		t.Fatalf("versions kept = %d, want 10", n())
	}
	got, err := s.GetVoiceAgent(ctx, a.ID)
	if err != nil || got.Revision != 12 {
		t.Fatalf("agent revision = %d (%v)", got.Revision, err)
	}
	// Restore the oldest kept revision (3): a new revision 13, history
	// intact.
	restored, err := s.RestoreVoiceAgentVersion(ctx, "test", a.ID, 3, nil)
	if err != nil {
		t.Fatal(err)
	}
	if restored.Revision != 13 || restored.Prompt != "prompt v"+"a" {
		t.Fatalf("restored = revision %d prompt %q", restored.Revision, restored.Prompt)
	}
	if n() != 10 {
		t.Fatalf("versions after restore = %d, want 10", n())
	}
	var keep int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM voice_agent_versions WHERE agent_id = $1 AND revision = 3`, a.ID).Scan(&keep); err != nil || keep != 1 {
		t.Fatalf("source version survived = %d (%v)", keep, err)
	}

	// A delete moves the counter and removes the versions (cascade).
	prev := rev()
	if err := s.DeleteVoiceAgent(ctx, "test", a.ID, nil); err != nil {
		t.Fatal(err)
	}
	if rev() != prev+1 {
		t.Fatal("a delete did not bump the voice revision")
	}
	if n() != 0 {
		t.Fatal("the versions survived the delete")
	}
}
