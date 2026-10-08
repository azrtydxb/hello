package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestLoadAIAgentDefaults fails if a default differs from the spec's
// Interfaces section, or if the agent turns on without both the base URL
// and the model, or reports another reason than not_configured or
// incomplete_configuration.
func TestLoadAIAgentDefaults(t *testing.T) {
	c, err := LoadControl(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := AIAgent{Provider: "openai", StructuredOutput: "json_schema", ValidationAttempts: 3, MaxSteps: 8,
		MaxConcurrency: 2, RequestsPerMinute: 30, Timeout: 180 * time.Second, DailyTokenBudget: 2_000_000,
		BackgroundBudgetPercent: 80, AgentStartDelay: 2 * time.Minute, AIOpsInterval: time.Minute,
		ExplainMinInterval: 10 * time.Minute}
	if c.AIAgent != want {
		t.Fatalf("defaults = %+v, want %+v", c.AIAgent, want)
	}
	for _, tc := range []struct {
		url, model string
		on         bool
		reason     string
	}{
		{"", "", false, "not_configured"},
		{"http://llm:8000/v1", "", false, "incomplete_configuration"},
		{"", "m", false, "incomplete_configuration"},
		{"http://llm:8000/v1", "m", true, ""},
	} {
		a := AIAgent{BaseURL: tc.url, Model: tc.model}
		if on, reason := a.Enabled(); on != tc.on || reason != tc.reason {
			t.Errorf("Enabled(%q, %q) = %v, %q; want %v, %q", tc.url, tc.model, on, reason, tc.on, tc.reason)
		}
	}
}

// TestLoadAIAgentSet fails if a valid configuration is not read as given or
// the API key reaches a log line.
func TestLoadAIAgentSet(t *testing.T) {
	c, err := LoadControl(env(map[string]string{
		"HELLO_AI_PROVIDER": "anthropic", "HELLO_AI_BASE_URL": "https://api.example", "HELLO_AI_MODEL": "m1",
		"HELLO_AI_API_KEY": "sk-secret-key", "HELLO_AI_ALLOW_PUBLIC_ENDPOINT": "true", "HELLO_AI_STRUCTURED_OUTPUT": "prompt",
		"HELLO_AI_VALIDATION_ATTEMPTS": "5", "HELLO_AI_MAX_STEPS": "4", "HELLO_AI_MAX_CONCURRENCY": "6",
		"HELLO_AI_REQUESTS_PER_MINUTE": "100", "HELLO_AI_TIMEOUT": "30s", "HELLO_AI_DAILY_TOKEN_BUDGET": "5000000000",
		"HELLO_AI_BACKGROUND_BUDGET_PERCENT": "100", "HELLO_AI_AGENT_START_DELAY": "0s", "HELLO_AI_AIOPS_INTERVAL": "0s",
		"HELLO_AI_EXPLAIN_MIN_INTERVAL": "1m",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := AIAgent{Provider: "anthropic", BaseURL: "https://api.example", Model: "m1", APIKey: "sk-secret-key",
		AllowPublic: true, StructuredOutput: "prompt", ValidationAttempts: 5, MaxSteps: 4, MaxConcurrency: 6,
		RequestsPerMinute: 100, Timeout: 30 * time.Second, DailyTokenBudget: 5_000_000_000,
		BackgroundBudgetPercent: 100, ExplainMinInterval: time.Minute}
	if c.AIAgent != want {
		t.Fatalf("AIAgent = %+v, want %+v", c.AIAgent, want)
	}
	if on, _ := c.AIAgent.Enabled(); !on {
		t.Fatal("not enabled with base URL and model")
	}
	var buf strings.Builder
	slog.New(slog.NewTextHandler(&buf, nil)).Info("config", "agent", c.AIAgent, "control", c)
	if strings.Contains(buf.String(), "sk-secret-key") || !strings.Contains(buf.String(), "model=m1") {
		t.Fatalf("log line = %s", buf.String())
	}
}

// TestLoadAIAgentRejects fails if a provider or structured-output mode
// outside the enum, a percent outside 1 to 100, a non-positive bound or a
// base URL that is not http(s) with a host is accepted.
func TestLoadAIAgentRejects(t *testing.T) {
	for key, vals := range map[string][]string{
		"HELLO_AI_PROVIDER":                  {"gemini", "OpenAI"},
		"HELLO_AI_STRUCTURED_OUTPUT":         {"json", "tools"},
		"HELLO_AI_BACKGROUND_BUDGET_PERCENT": {"0", "101", "-5", "x"},
		"HELLO_AI_VALIDATION_ATTEMPTS":       {"0", "-1", "x"},
		"HELLO_AI_MAX_STEPS":                 {"0", "x"},
		"HELLO_AI_MAX_CONCURRENCY":           {"0", "x"},
		"HELLO_AI_REQUESTS_PER_MINUTE":       {"0", "x"},
		"HELLO_AI_DAILY_TOKEN_BUDGET":        {"0", "-1", "x"},
		"HELLO_AI_TIMEOUT":                   {"0s", "-1s", "soon"},
		"HELLO_AI_AGENT_START_DELAY":         {"-1s", "soon"},
		"HELLO_AI_AIOPS_INTERVAL":            {"-1s", "soon"},
		"HELLO_AI_EXPLAIN_MIN_INTERVAL":      {"-1s", "soon"},
		"HELLO_AI_ALLOW_PUBLIC_ENDPOINT":     {"yes", "1"},
		"HELLO_AI_BASE_URL":                  {"llm:8000", "ftp://llm", "http://", "http://u:p@llm/v1"},
	} {
		for _, v := range vals {
			_, err := LoadControl(env(map[string]string{key: v}))
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Errorf("%s=%q accepted: %v", key, v, err)
			}
		}
	}
}
