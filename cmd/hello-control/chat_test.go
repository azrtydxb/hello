package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/config"
)

// TestLANChatListenerIsolation (the cmd half) fails if the chat listener
// starts, or touches its dependencies, when HELLO_MSG_CHAT_LAN_ADDR is unset.
func TestLANChatListenerStartsOnlyWhenAsked(t *testing.T) {
	reg := prometheus.NewRegistry()
	started, err := startChat(context.Background(), config.Control{}, nil, nil, nil, nil, reg, slog.New(slog.DiscardHandler))
	if err != nil || started {
		t.Fatalf("started = %v, err = %v with no HELLO_MSG_CHAT_LAN_ADDR", started, err)
	}
	if n, _ := reg.Gather(); len(n) != 0 {
		t.Fatalf("an unset listener registered %d metrics", len(n))
	}
}
