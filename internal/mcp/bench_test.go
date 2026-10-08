package mcp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/api"
)

// TestToolCallOverhead (HELLO_BENCH=1) fails if a tool call's p99 latency
// exceeds the same direct API call's p99 by 2 ms or more (spec S-15).
func TestToolCallOverhead(t *testing.T) {
	if os.Getenv("HELLO_BENCH") != "1" {
		t.Skip("HELLO_BENCH=1 not set")
	}
	apiH := api.Handler(api.Config{AI: testAI(t), Store: &apiStore{}})
	ts, _ := newTestServer(t, apiH, fixtureOps(), nil)
	direct := httpServe(t, apiH)
	client := &http.Client{Transport: &http.Transport{MaxIdleConnsPerHost: 4}}
	do := func(req *http.Request) {
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s = %d", req.URL, resp.StatusCode)
		}
	}
	body := callBody("listExtensions", map[string]any{})
	viaMCP := func() {
		req, _ := http.NewRequest(http.MethodPost, ts.URL, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tokRead)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		do(req)
	}
	viaAPI := func() {
		req, _ := http.NewRequest(http.MethodGet, direct+"/api/v1/extensions", nil)
		req.Header.Set("Authorization", "Bearer "+tokRead)
		do(req)
	}
	p99 := func(f func()) time.Duration {
		for range 200 {
			f()
		}
		d := make([]time.Duration, 2000)
		for i := range d {
			start := time.Now()
			f()
			d[i] = time.Since(start)
		}
		slices.Sort(d)
		return d[len(d)*99/100]
	}
	a, m := p99(viaAPI), p99(viaMCP)
	t.Logf("p99 direct %v, via MCP %v", a, m)
	if m-a >= 2*time.Millisecond {
		t.Fatalf("tool call p99 %v exceeds the direct call's %v by 2 ms or more", m, a)
	}
}

func httpServe(t *testing.T, h http.Handler) string {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL
}
