package mcp

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/azrtydxb/hello/internal/api"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// syncBuffer is a log sink safe for the server's goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// TestOAuthMCPMetrics (MCP half) fails if an MCP request or a tool call
// does not move its metric, a tool call is not logged with tool, actor,
// client, status and duration, or a credential, an argument or a withheld
// value reaches the log (spec S-20).
func TestOAuthMCPMetrics(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := NewMetrics(reg)
	logs := &syncBuffer{}
	st := &apiStore{}
	ts, _ := newTestServer(t, api.Handler(api.Config{Store: st}), fixtureOps(), func(o *Options) {
		o.Metrics = m
		o.Log = slog.New(slog.NewJSONHandler(logs, nil))
	})
	ctx := context.Background()
	cs := connect(t, ts.URL, tokOAuth, "2025-11-25")
	if _, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "listExtensions", Arguments: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := cs.CallTool(ctx, &sdk.CallToolParams{Name: "getExtension", Arguments: map[string]any{"id": 99}}); err != nil {
		t.Fatal(err)
	}
	ws := connect(t, ts.URL, tokWrite, "2025-11-25")
	if _, err := ws.CallTool(ctx, &sdk.CallToolParams{Name: "createDevice", Arguments: map[string]any{
		"body": map[string]any{"extensionId": 7, "sipUsername": "argument-marker"}}}); err != nil {
		t.Fatal(err)
	}
	post(t, ts.URL, callBody("listExtensions", nil), nil)

	for _, c := range []struct {
		c    prometheus.Collector
		want float64
		name string
	}{
		{m.ToolCalls.WithLabelValues("listExtensions", "ok"), 1, "tool ok"},
		{m.ToolCalls.WithLabelValues("getExtension", "error"), 1, "tool error"},
		{m.ToolCalls.WithLabelValues("createDevice", "ok"), 1, "createDevice ok"},
		{m.Requests.WithLabelValues("tools/call", "ok"), 3, "requests ok"},
		{m.Requests.WithLabelValues("tools/call", "unauthorized"), 0, "unauthenticated tools/call before parsing"},
		{m.Requests.WithLabelValues("other", "unauthorized"), 1, "unauthorized"},
	} {
		if got := testutil.ToFloat64(c.c); got != c.want {
			t.Errorf("%s = %v, want %v", c.name, got, c.want)
		}
	}
	if got := testutil.ToFloat64(m.Requests.WithLabelValues("initialize", "ok")); got < 2 {
		t.Errorf("initialize requests = %v", got)
	}
	if n := testutil.CollectAndCount(m.ToolSeconds); n != 1 {
		t.Errorf("tool_call_seconds series = %d", n)
	}
	if n, _ := testutil.GatherAndCount(reg, "hello_mcp_requests_total", "hello_mcp_tool_calls_total", "hello_mcp_tool_call_seconds"); n == 0 {
		t.Error("metrics not registered")
	}

	out := logs.String()
	for _, want := range []string{`"tool":"listExtensions"`, `"actor":"user:ann"`, `"client":"https://client.test/cimd"`, `"status":200`, `"status":404`, `"duration"`, `"actor":"token:12"`} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %s:\n%s", want, out)
		}
	}
	st.mu.Lock()
	secret := st.secrets[0]
	st.mu.Unlock()
	for _, leak := range []string{tokOAuth, tokWrite, "argument-marker", secret, "Ann"} {
		if strings.Contains(out, leak) {
			t.Errorf("log contains %q", leak)
		}
	}
}
