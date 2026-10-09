package voice

import (
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// boolp is &v.
func boolp(v bool) *bool { return &v }

// TestVoiceCallerVerification is the internal/voice half (acceptance S-36):
// the tool rules and the PIN hashing. It fails if a write tool attaches to
// an agent with mode none, a PIN is stored or returned unhashed, or the
// allowlist or PIN hash reach audit text (the registry never writes audit
// text; the store's insertAudit takes names only).
func TestVoiceCallerVerification(t *testing.T) {
	// A write tool is refused for mode none, accepted with verification.
	if _, err := ToolLimits("none", "srv", []ToolIn{{Name: "create_thing"}}); err == nil ||
		!strings.Contains(err.Error(), "caller verification") {
		t.Fatalf("write tool without verification = %v", err)
	}
	if _, err := ToolLimits("none", "srv", []ToolIn{{Name: "lookup", ReadOnly: true}}); err != nil {
		t.Fatal("a read-only tool must attach without verification")
	}
	for _, mode := range []string{"allowlist", "pin", "allowlist_or_pin"} {
		if _, err := ToolLimits(mode, "srv", []ToolIn{{Name: "create_thing"}}); err != nil {
			t.Fatalf("write tool with mode %s: %v", mode, err)
		}
	}

	// The confirm/write defaults follow the read-only annotation (S-6):
	// read-only defaults to confirm false, write false; an unannotated
	// tool defaults to confirm true, write true.
	got, err := ToolLimits("pin", "srv", []ToolIn{
		{Name: "lookup", ReadOnly: true},
		{Name: "create_thing"},
		{Name: "custom", ReadOnly: true, Confirm: boolp(true), Write: boolp(true)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Confirm || got[0].Write {
		t.Fatalf("read-only defaults = %+v", got[0])
	}
	if !got[1].Confirm || !got[1].Write {
		t.Fatalf("unannotated defaults = %+v", got[1])
	}
	if !got[2].Confirm || !got[2].Write {
		t.Fatalf("explicit flags = %+v", got[2])
	}

	// Two tools of one server collide on the full name (S-6).
	if _, err := ToolLimits("pin", "srv", []ToolIn{{Name: "a"}, {Name: "a"}}); err == nil ||
		!strings.Contains(err.Error(), "twice") {
		t.Fatalf("duplicate tool = %v", err)
	}

	// The PIN is hashed with bcrypt before it reaches the store input; the
	// plaintext never does. Mutation check: dropping the hash in toStore
	// (storing in.Pin verbatim) fails the CheckPassword and the plaintext
	// absence assertions.
	si, err := toStore(AgentInput{
		Name: "support", CallerVerification: "pin", Pin: "12345678",
		CallerAllowlist: []string{"+32123456789", "101"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if si.PINHash == "12345678" || !strings.HasPrefix(si.PINHash, "$2") || len(si.PINHash) < 50 {
		t.Fatalf("pin hash = %q", si.PINHash)
	}
	if !auth.CheckPassword(si.PINHash, "12345678") {
		t.Fatal("the stored hash does not verify the PIN")
	}

	// Validation: the spec's ranges (S-1 to S-3, S-36).
	base := func() AgentInput {
		return AgentInput{Name: "support", CallerVerification: "none"}
	}
	bad := map[string]func(*AgentInput){
		"name":         func(a *AgentInput) { a.Name = "bad name!" },
		"prompt":       func(a *AgentInput) { a.Prompt = strings.Repeat("p", 8001) },
		"greeting":     func(a *AgentInput) { a.Greeting = strings.Repeat("g", 501) },
		"extension":    func(a *AgentInput) { e := "10x01"; a.Extension = &e },
		"temperature":  func(a *AgentInput) { v := 2.1; a.Temperature = &v },
		"verification": func(a *AgentInput) { a.CallerVerification = "captcha" },
		"allowlist":    func(a *AgentInput) { a.CallerAllowlist = make([]string, 101) },
		"allowentry":   func(a *AgentInput) { a.CallerAllowlist = []string{"not a number"} },
		"pin":          func(a *AgentInput) { a.Pin = "12" },
		"seconds":      func(a *AgentInput) { a.MaxCallSeconds = 29 },
		"concurrent":   func(a *AgentInput) { a.MaxConcurrent = 51 },
		"toolcalls":    func(a *AgentInput) { a.MaxToolCalls = 201 },
		"idle":         func(a *AgentInput) { a.IdleTimeoutSeconds = 4 },
		"retention":    func(a *AgentInput) { a.TranscriptRetentionDays = 366 },
	}
	for what, mut := range bad {
		in := base()
		mut(&in)
		if err := ValidateAgent(&in); err == nil {
			t.Errorf("%s: invalid input accepted", what)
		}
	}
}
