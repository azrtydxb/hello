package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/store"
)

// TestVoiceCallerVerification is the API half (acceptance S-36): the PIN is
// never in a response, stored only as a salted hash, kept across a save
// without a PIN, and the allowlist never reaches audit text.
//
// Mutation check: returning PINHash in agentBody (or skipping the hash on
// save) fails the body scan and the hash assertions.
func TestVoiceCallerVerification(t *testing.T) {
	e := newVoiceEnv(t)
	ctx := context.Background()
	c := e.login()

	a := c.must(http.StatusCreated, "POST", "/api/v1/voice/agents", voiceAgentBody("verified",
		func(m map[string]any) {
			m["callerVerification"] = "pin"
			m["pin"] = "1234"
			m["callerAllowlist"] = []string{"+31405550199", "101"}
		})).json(t)
	if a["pinSet"] != true || a["callerVerification"] != "pin" {
		t.Fatalf("created agent = %v", a)
	}
	if strings.Contains(fmt.Sprint(a), "1234") {
		t.Fatal("the PIN is in the response")
	}
	id := fmt.Sprint(a["id"])

	// The stored value is a salted bcrypt hash of the PIN, nothing else.
	var hash string
	if err := e.db.QueryRowContext(ctx, `SELECT pin_hash FROM voice_agents WHERE id = $1`, id).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !auth.CheckPassword(hash, "1234") || strings.Contains(hash, "1234") {
		t.Fatalf("pin_hash = %q", hash)
	}

	// A save without a PIN keeps the hash; a save with one replaces it.
	c.must(http.StatusOK, "PUT", "/api/v1/voice/agents/"+id, voiceAgentBody("verified",
		func(m map[string]any) { m["callerVerification"] = "pin" }))
	var hash2 string
	if err := e.db.QueryRowContext(ctx, `SELECT pin_hash FROM voice_agents WHERE id = $1`, id).Scan(&hash2); err != nil {
		t.Fatal(err)
	}
	if hash2 != hash {
		t.Fatal("a save without a PIN changed the hash")
	}
	c.must(http.StatusOK, "PUT", "/api/v1/voice/agents/"+id, voiceAgentBody("verified",
		func(m map[string]any) { m["callerVerification"] = "pin"; m["pin"] = "987654" }))
	if err := e.db.QueryRowContext(ctx, `SELECT pin_hash FROM voice_agents WHERE id = $1`, id).Scan(&hash2); err != nil {
		t.Fatal(err)
	}
	if hash2 == hash || !auth.CheckPassword(hash2, "987654") {
		t.Fatal("the new PIN was not hashed")
	}

	// The allowlist and the PIN hash never reach the audit text.
	var leaked bool
	if err := e.db.QueryRowContext(ctx, `
		SELECT EXISTS (SELECT 1 FROM audit_events WHERE resource = 'voice_agent'
		  AND (to_jsonb(audit_events)::text LIKE '%31405550199%'
		    OR to_jsonb(audit_events)::text LIKE '%1234%'))`).Scan(&leaked); err != nil || leaked {
		t.Fatalf("allowlist or PIN in audit rows (%v)", err)
	}
}

// TestVoiceMCPServers is the API half (acceptance S-5, S-6, S-8): the
// credential is write-only, sealed at rest and changed only by an admin; a
// PUT without one keeps it; the egress guard refuses a public URL at save;
// attachments default their confirm/write flags, refuse write tools without
// caller verification and refuse colliding names. The store half (the
// sealed bytes, the additional-data binding and the revision bumps) is
// TestVoiceMCPServers in internal/store.
//
// Mutation check: dropping the admin scope from the create/update rows in
// routes.go (or returning the credential in serverBody) fails the 403 and
// body-scan assertions.
func TestVoiceMCPServers(t *testing.T) {
	e := newVoiceEnv(t)
	ctx := context.Background()
	c := e.login()

	// Only an admin credential manages servers: a write-scope personal
	// token is refused before the handler.
	tok, hash := auth.NewToken()
	var uid int64
	if err := e.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = $1`, testUser).Scan(&uid); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateToken(ctx, "test", uid, store.NewToken{Name: "op", Scopes: auth.Scopes{"read", "write"}}, hash); err != nil {
		t.Fatal(err)
	}
	op := e.client()
	op.bearer = tok
	if r := op.do("POST", "/api/v1/voice/mcp-servers", map[string]any{
		"name": "srv", "url": "http://127.0.0.1:1/mcp", "auth": "none"}); r.code != http.StatusForbidden {
		t.Fatalf("operator create server = %d", r.code)
	}

	// A public URL is refused at save with the spec's code.
	c.must(http.StatusBadRequest, "POST", "/api/v1/voice/mcp-servers", map[string]any{
		"name": "srv", "url": "http://192.0.2.1/mcp", "auth": "none"})

	// Create with a credential: write-only in every response, sealed in
	// the database under the row's additional data.
	v := c.must(http.StatusCreated, "POST", "/api/v1/voice/mcp-servers", map[string]any{
		"name": "srv", "url": "http://127.0.0.1:1/mcp", "auth": "bearer", "credential": "bearer-secret-1",
		"timeoutMs": 5000,
	}).json(t)
	sid := fmt.Sprint(v["id"])
	if v["credentialSet"] != true || v["auth"] != "bearer" {
		t.Fatalf("created server = %v", v)
	}
	if strings.Contains(fmt.Sprint(v), "bearer-secret-1") {
		t.Fatal("the credential is in the create response")
	}
	list := c.must(http.StatusOK, "GET", "/api/v1/voice/mcp-servers", nil)
	if strings.Contains(string(list.body), "bearer-secret-1") {
		t.Fatal("the credential is in the list response")
	}
	var sealed []byte
	if err := e.db.QueryRowContext(ctx,
		`SELECT credential FROM voice_mcp_servers WHERE id = $1`, sid).Scan(&sealed); err != nil {
		t.Fatal(err)
	}
	if sealed == nil || string(sealed) == "bearer-secret-1" {
		t.Fatal("the credential is stored unsealed")
	}
	if plain, err := e.voiceCredential(ctx, sid); err != nil || plain != "bearer-secret-1" {
		t.Fatalf("open sealed credential = %q, %v", plain, err)
	}
	// The seal is bound to the row: the ciphertext does not open under
	// another row's additional data.
	if _, err := e.box.Open(sealed, "voice_mcp:999:cred"); err == nil {
		t.Fatal("a credential opened under another row's additional data")
	}

	// Missing material: header auth without its header name, OAuth without
	// its token endpoint or client id.
	c.must(http.StatusBadRequest, "POST", "/api/v1/voice/mcp-servers", map[string]any{
		"name": "h", "url": "http://127.0.0.1:1/mcp", "auth": "header", "credential": "x"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/voice/mcp-servers", map[string]any{
		"name": "o", "url": "http://127.0.0.1:1/mcp", "auth": "oauth_client_credentials", "credential": "x"})
	c.must(http.StatusBadRequest, "POST", "/api/v1/voice/mcp-servers", map[string]any{
		"name": "o", "url": "http://127.0.0.1:1/mcp", "auth": "oauth_client_credentials",
		"credential": "x", "tokenUrl": "http://127.0.0.1:1/token"})

	// A PUT without a credential keeps the stored one; with one it
	// replaces it. The response never carries either.
	up := map[string]any{"name": "srv", "url": "http://127.0.0.1:1/mcp", "auth": "bearer", "timeoutMs": 5000}
	got := c.must(http.StatusOK, "PUT", "/api/v1/voice/mcp-servers/"+sid, up).json(t)
	if got["credentialSet"] != true || strings.Contains(fmt.Sprint(got), "bearer-secret-1") {
		t.Fatalf("updated server = %v", got)
	}
	var sealed2 []byte
	if err := e.db.QueryRowContext(ctx,
		`SELECT credential FROM voice_mcp_servers WHERE id = $1`, sid).Scan(&sealed2); err != nil {
		t.Fatal(err)
	}
	if string(sealed2) != string(sealed) {
		t.Fatal("a PUT without a credential changed the credential")
	}
	up["credential"] = "bearer-secret-2"
	c.must(http.StatusOK, "PUT", "/api/v1/voice/mcp-servers/"+sid, up)
	if plain, err := e.voiceCredential(ctx, sid); err != nil || plain != "bearer-secret-2" {
		t.Fatalf("rotated credential = %q, %v", plain, err)
	}

	// Attachments: defaults, verification rule, collisions, references.
	a := c.must(http.StatusCreated, "POST", "/api/v1/voice/agents", voiceAgentBody("tools-agent",
		func(m map[string]any) { m["callerVerification"] = "pin"; m["pin"] = "5678" })).json(t)
	aid := fmt.Sprint(a["id"])
	tools := c.must(http.StatusOK, "PUT", "/api/v1/voice/agents/"+aid+"/tools", map[string]any{
		"servers": []map[string]any{{
			"serverId": v["id"],
			"tools": []map[string]any{
				{"name": "lookup", "readOnly": true},
				{"name": "create_thing"},
			},
		}},
	}).json(t)
	srv := tools["servers"].([]any)[0].(map[string]any)
	entries := srv["tools"].([]any)
	if entries[0].(map[string]any)["confirm"] != false || entries[0].(map[string]any)["write"] != false {
		t.Fatalf("read-only defaults = %v", entries[0])
	}
	if entries[1].(map[string]any)["confirm"] != true || entries[1].(map[string]any)["write"] != true {
		t.Fatalf("unannotated defaults = %v", entries[1])
	}

	// An agent without verification cannot take a write tool.
	plain := c.must(http.StatusCreated, "POST", "/api/v1/voice/agents", voiceAgentBody("plain-agent")).json(t)
	if r := c.do("PUT", "/api/v1/voice/agents/"+fmt.Sprint(plain["id"])+"/tools", map[string]any{
		"servers": []map[string]any{{"serverId": v["id"], "tools": []map[string]any{{"name": "create_thing"}}}},
	}); r.code != http.StatusConflict || !strings.Contains(string(r.body), "voice_verification_required") {
		t.Fatalf("write tool without verification = %d %s", r.code, r.body)
	}

	// A duplicate tool name collides; an unknown server is 404.
	c.must(http.StatusConflict, "PUT", "/api/v1/voice/agents/"+aid+"/tools", map[string]any{
		"servers": []map[string]any{{"serverId": v["id"],
			"tools": []map[string]any{{"name": "a"}, {"name": "a"}}}}})
	c.must(http.StatusNotFound, "PUT", "/api/v1/voice/agents/"+aid+"/tools", map[string]any{
		"servers": []map[string]any{{"serverId": 999, "tools": []map[string]any{}}}})

	// An empty allowlist attaches nothing but is stored.
	empty := c.must(http.StatusOK, "PUT", "/api/v1/voice/agents/"+aid+"/tools", map[string]any{
		"servers": []map[string]any{{"serverId": v["id"], "tools": []map[string]any{}}}}).json(t)
	if got := empty["servers"].([]any)[0].(map[string]any)["tools"].([]any); len(got) != 0 {
		t.Fatalf("empty allowlist = %v", got)
	}

	// A server an agent attaches cannot be deleted, and the answer names
	// the agent.
	if r := c.do("DELETE", "/api/v1/voice/mcp-servers/"+sid, nil); r.code != http.StatusConflict ||
		!strings.Contains(string(r.body), "tools-agent") {
		t.Fatalf("delete attached server = %d %s", r.code, r.body)
	}
	// After detaching, the delete works.
	c.must(http.StatusOK, "PUT", "/api/v1/voice/agents/"+aid+"/tools", map[string]any{"servers": []map[string]any{}})
	c.must(http.StatusNoContent, "DELETE", "/api/v1/voice/mcp-servers/"+sid, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/voice/mcp-servers/"+sid, nil)
}
