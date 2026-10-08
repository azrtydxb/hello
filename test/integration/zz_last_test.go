package integration

// This file is named to sort last: Go runs a package's tests file by file in
// name order, and this check must see the logs of every lab test before it.

import (
	"strings"
	"testing"
)

// labAIKey is the lab's HELLO_AI_API_KEY (compose.yaml). TestAIAgentEndToEnd
// also remembers a marker it puts in a prompt: neither may reach a log.
const labAIKey = "lab-ai-api-key-0123456789abcdef"

// labVoiceSecret, the lab's HELLO_VOICE_SIP_SECRET (compose.yaml, S-16),
// lives in lab_voice_test.go with the voice tests that use it.

// TestNoSecretsInLogs checks every service's log for any
// secret the lab tests used.
func TestNoSecretsInLogs(t *testing.T) {
	labUp(t)
	out, err := compose("logs", "--no-color").CombinedOutput()
	if err != nil {
		t.Fatalf("compose logs: %v", err)
	}
	logs := string(out)
	secrets.Lock()
	defer secrets.Unlock()
	checks := append([]string{labPassword, "lab-only-nonce-secret-0123456789abcdef", labAIKey, labVoiceSecret}, secrets.values...)
	for _, s := range checks {
		if s != "" && strings.Contains(logs, s) {
			t.Errorf("a secret (%d chars) appears in the lab logs", len(s))
		}
	}
	for _, marker := range []string{"response=\"", "Authorization: Digest"} {
		if strings.Contains(logs, marker) {
			t.Errorf("%q appears in the lab logs", marker)
		}
	}
}
