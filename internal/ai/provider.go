package ai

import (
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/azrtydxb/go-ai-sdk/provider"
	"github.com/azrtydxb/go-ai-sdk/providers/anthropic"
	"github.com/azrtydxb/go-ai-sdk/providers/openai"
	"github.com/azrtydxb/hello/internal/config"
)

// newModel builds the language model (spec S-2). It is the only place in
// Hello that builds one: every connection goes through go-ai-sdk with an
// HTTP client whose dialer is dial (the privacy check of S-3).
func newModel(cfg config.AIAgent, dial dialFunc) (provider.LanguageModel, error) {
	client := &http.Client{Transport: &http.Transport{
		// No proxy: a proxy would dial the endpoint for us and bypass the
		// privacy check.
		Proxy:       nil,
		DialContext: dial,
		// A fresh connection per call, so the privacy check runs before
		// every call and a rebinding fails the next one (spec S-3).
		DisableKeepAlives:     true,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: time.Second,
	}}
	switch cfg.Provider {
	case "", "openai":
		return openai.New(openai.WithBaseURL(cfg.BaseURL), openai.WithAPIKey(cfg.APIKey), openai.WithHTTPClient(client)).Model(cfg.Model), nil
	case "anthropic":
		return anthropic.New(anthropic.WithBaseURL(cfg.BaseURL), anthropic.WithAPIKey(cfg.APIKey), anthropic.WithHTTPClient(client)).Model(cfg.Model), nil
	}
	return nil, fmt.Errorf("HELLO_AI_PROVIDER %q is not openai or anthropic", cfg.Provider)
}

// netDial is the real dialer under the privacy check.
var netDial dialFunc = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
