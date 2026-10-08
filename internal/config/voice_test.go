package config

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

var (
	voiceKey1 = strings.Repeat("a", 32)
	voiceKey2 = strings.Repeat("b", 32)
)

// TestLoadVoiceDefaults fails if an unset voice configuration is not off
// with the spec's defaults, on either service.
func TestLoadVoiceDefaults(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Voice{Tenant: "hello.test", MaxAgents: 50, MaxCalls: 20}
	if c.Voice != want {
		t.Fatalf("control defaults = %+v, want %+v", c.Voice, want)
	}
	s, err := LoadSIP(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if s.Voice != want {
		t.Fatalf("sip defaults = %+v, want %+v", s.Voice, want)
	}
}

// TestLoadVoiceSet fails if a valid configuration is not read as given or a
// secret reaches a log line.
func TestLoadVoiceSet(t *testing.T) {
	m := map[string]string{
		"HELLO_VOICE_SIP_ADDRESS": "192.168.10.142:5060", "HELLO_VOICE_SIP_SECRET": voiceKey1,
		"HELLO_VOICE_SIP_SECRET_NEXT": voiceKey2, "HELLO_VOICE_TENANT": "kw", "HELLO_VOICE_MAX_AGENTS": "7",
		"HELLO_VOICE_MAX_CALLS": "0", "HELLO_VOICE_ALLOW_PUBLIC_MCP": "true", "HELLO_VOICE_ALLOW_LOOPBACK": "true",
	}
	want := Voice{SIPAddress: "192.168.10.142:5060", Secret: voiceKey1, SecretNext: voiceKey2, Tenant: "kw",
		MaxAgents: 7, AllowPublicMCP: true, AllowLoopback: true}
	c, err := LoadControl(env(m))
	if err != nil {
		t.Fatal(err)
	}
	if c.Voice != want || !c.Voice.Enabled() {
		t.Fatalf("voice = %+v, want %+v", c.Voice, want)
	}
	s, err := LoadSIP(env(m))
	if err != nil || s.Voice != want {
		t.Fatalf("sip voice = %+v, %v", s.Voice, err)
	}
	var sb strings.Builder
	slog.New(slog.NewTextHandler(&sb, nil)).Info("cfg", "voice", c.Voice)
	if strings.Contains(sb.String(), voiceKey1) || strings.Contains(sb.String(), voiceKey2) {
		t.Fatalf("a voice secret reached the log: %s", sb.String())
	}
}

// TestLoadVoiceInvalid fails if a bad voice setting loads, or the error
// echoes a secret.
func TestLoadVoiceInvalid(t *testing.T) {
	ok := map[string]string{"HELLO_VOICE_SIP_ADDRESS": "agent:5060", "HELLO_VOICE_SIP_SECRET": voiceKey1}
	with := func(k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range ok {
			m[a] = b
		}
		if v == "-" {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	for _, tc := range []struct{ key, val, bad string }{
		{"HELLO_VOICE_SIP_ADDRESS", "agent", "HELLO_VOICE_SIP_ADDRESS"},
		{"HELLO_VOICE_SIP_ADDRESS", ":5060", "HELLO_VOICE_SIP_ADDRESS"},
		{"HELLO_VOICE_SIP_ADDRESS", "0.0.0.0:5060", "HELLO_VOICE_SIP_ADDRESS"},
		{"HELLO_VOICE_SIP_SECRET", "short-secret", "HELLO_VOICE_SIP_SECRET"},
		{"HELLO_VOICE_SIP_SECRET", "-", "HELLO_VOICE_SIP_SECRET"},
		{"HELLO_VOICE_SIP_SECRET_NEXT", "short-next", "HELLO_VOICE_SIP_SECRET_NEXT"},
		{"HELLO_VOICE_SIP_SECRET_NEXT", voiceKey1, "HELLO_VOICE_SIP_SECRET_NEXT"},
		{"HELLO_VOICE_TENANT", "a|b", "HELLO_VOICE_TENANT"},
		{"HELLO_VOICE_MAX_AGENTS", "0", "HELLO_VOICE_MAX_AGENTS"},
		{"HELLO_VOICE_MAX_CALLS", "-1", "HELLO_VOICE_MAX_CALLS"},
		{"HELLO_VOICE_MAX_CALLS", "many", "HELLO_VOICE_MAX_CALLS"},
		{"HELLO_VOICE_ALLOW_LOOPBACK", "yes", "HELLO_VOICE_ALLOW_LOOPBACK"},
	} {
		t.Run(tc.key+"="+tc.val, func(t *testing.T) {
			for name, load := range map[string]func(func(string) string) error{
				"control": func(g func(string) string) error { _, err := LoadControl(g); return err },
				"sip":     func(g func(string) string) error { _, err := LoadSIP(g); return err },
			} {
				err := load(env(with(tc.key, tc.val)))
				if err == nil || !strings.Contains(err.Error(), tc.bad) {
					t.Errorf("%s: err = %v, want one naming %s", name, err, tc.bad)
				} else if strings.Contains(err.Error(), voiceKey1) || strings.Contains(fmt.Sprint(err), "short-") {
					t.Errorf("%s: error echoes a secret: %v", name, err)
				}
			}
		})
	}
	// A next key alone is refused: nothing to rotate from.
	if _, err := LoadControl(env(map[string]string{"HELLO_VOICE_SIP_SECRET_NEXT": voiceKey2})); err == nil {
		t.Error("next secret without a secret loaded")
	}
}

// TestVoiceDisabledWithoutAddress (config half) fails if voice agents are on
// without an address, or an address without a secret loads.
func TestVoiceDisabledWithoutAddress(t *testing.T) {
	c, err := LoadControl(env(map[string]string{"HELLO_VOICE_SIP_SECRET": voiceKey1}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Voice.Enabled() {
		t.Fatal("voice enabled without HELLO_VOICE_SIP_ADDRESS")
	}
	if _, err := LoadControl(env(map[string]string{"HELLO_VOICE_SIP_ADDRESS": "agent:5060"})); err == nil {
		t.Fatal("address without a secret loaded")
	}
}
