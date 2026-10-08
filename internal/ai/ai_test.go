package ai

import (
	"errors"
	"fmt"
	"testing"
)

// TestError fails if an AI error stops matching by code through wrapping,
// loses its cause, or prints more than its code and message.
func TestError(t *testing.T) {
	cause := errors.New("dial tcp: refused")
	err := fmt.Errorf("generate: %w", &Error{Code: CodeProviderError, Message: "the endpoint refused", Err: cause})
	if CodeOf(err) != CodeProviderError || !errors.Is(err, &Error{Code: CodeProviderError}) || errors.Is(err, &Error{Code: CodeBusy}) {
		t.Fatalf("code of %v = %q", err, CodeOf(err))
	}
	if !errors.Is(err, cause) {
		t.Error("the cause is lost")
	}
	if got := (&Error{Code: CodeBusy}).Error(); got != "ai_busy" {
		t.Errorf("Error() = %q", got)
	}
	if CodeOf(cause) != "" {
		t.Error("a foreign error has a code")
	}
	if (Usage{InputTokens: 1, OutputTokens: 2, ReasoningTokens: 4}).Total() != 7 {
		t.Error("Total drifted: reasoning tokens count against the budget")
	}
}
