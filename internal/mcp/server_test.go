package mcp

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// TestMCPTransport fails if initialize with 2026-07-28 or 2025-11-25 does
// not negotiate that version, a session cookie alone is admitted, a foreign
// Origin is not 403, an unauthenticated request lacks the challenge, or a
// body over 1 MiB is read (spec S-13).
func TestMCPTransport(t *testing.T) {
	ts, _ := newTestServer(t, echoAPI(), fixtureOps(), nil)

	for _, v := range []string{"2026-07-28", "2025-11-25"} {
		cs := connect(t, ts.URL, tokRead, v)
		if got := cs.InitializeResult().ProtocolVersion; got != v {
			t.Errorf("asked %s, negotiated %s", v, got)
		}
		if _, err := cs.ListTools(context.Background(), nil); err != nil {
			t.Errorf("%s: tools/list: %v", v, err)
		}
	}

	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	t.Run("session cookie alone", func(t *testing.T) {
		resp, _ := post(t, ts.URL, list, map[string]string{"Cookie": "hello_session=valid"})
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("cookie-only request = %d, want 401", resp.StatusCode)
		}
		ch := resp.Header.Get("WWW-Authenticate")
		if !strings.HasPrefix(ch, "Bearer ") || !strings.Contains(ch, `resource_metadata="`+testMetadata+`"`) || !strings.Contains(ch, `scope="read"`) {
			t.Fatalf("401 challenge = %q", ch)
		}
	})
	t.Run("bad token", func(t *testing.T) {
		resp, _ := post(t, ts.URL, list, map[string]string{"Authorization": "Bearer nope"})
		if resp.StatusCode != http.StatusUnauthorized || !strings.Contains(resp.Header.Get("WWW-Authenticate"), "resource_metadata=") {
			t.Fatalf("bad token = %d %q", resp.StatusCode, resp.Header.Get("WWW-Authenticate"))
		}
	})
	t.Run("oauth audience", func(t *testing.T) {
		if resp, _ := post(t, ts.URL, list, map[string]string{"Authorization": "Bearer " + tokOAuth}); resp.StatusCode != http.StatusOK {
			t.Fatalf("MCP-bound token = %d, want 200", resp.StatusCode)
		}
		if resp, _ := post(t, ts.URL, list, map[string]string{"Authorization": "Bearer " + tokOAuthAPI}); resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("API-bound token on /mcp = %d, want 401", resp.StatusCode)
		}
	})
	t.Run("store down", func(t *testing.T) {
		if resp, _ := post(t, ts.URL, list, map[string]string{"Authorization": "Bearer " + tokBroken}); resp.StatusCode != http.StatusServiceUnavailable {
			t.Fatalf("lookup failure = %d, want 503", resp.StatusCode)
		}
	})
	t.Run("origin", func(t *testing.T) {
		for origin, want := range map[string]int{
			"https://evil.test":                     http.StatusForbidden,
			"null":                                  http.StatusForbidden,
			"chrome-extension://abcdefghijklmnopqr": http.StatusForbidden,
			testPublic:                              http.StatusOK,
			"https://console.test":                  http.StatusOK,
		} {
			resp, _ := post(t, ts.URL, list, map[string]string{"Authorization": "Bearer " + tokRead, "Origin": origin})
			if resp.StatusCode != want {
				t.Errorf("Origin %s = %d, want %d", origin, resp.StatusCode, want)
			}
		}
	})
	t.Run("body cap", func(t *testing.T) {
		var n atomic.Int64
		body := &countingReader{n: &n, left: 4 << 20}
		req, _ := http.NewRequest(http.MethodPost, ts.URL, body)
		req.Header.Set("Authorization", "Bearer "+tokRead)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusRequestEntityTooLarge {
				t.Fatalf("4 MiB body = %d, want 413", resp.StatusCode)
			}
		}
		if got := n.Load(); got >= 4<<20 {
			t.Fatalf("the server read the whole %d-byte body", got)
		}
	})
	t.Run("GET and DELETE", func(t *testing.T) {
		for _, m := range []string{http.MethodGet, http.MethodDelete} {
			req, _ := http.NewRequest(m, ts.URL, nil)
			req.Header.Set("Authorization", "Bearer "+tokRead)
			req.Header.Set("Accept", "application/json, text/event-stream")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_ = resp.Body.Close()
			if resp.StatusCode != http.StatusMethodNotAllowed {
				t.Errorf("%s = %d, want 405", m, resp.StatusCode)
			}
		}
	})
}

// countingReader yields left bytes of a JSON string, counting what is read.
type countingReader struct {
	n    *atomic.Int64
	left int
	open bool
}

func (c *countingReader) Read(p []byte) (int, error) {
	if c.left <= 0 {
		return 0, io.EOF
	}
	k := min(len(p), c.left)
	for i := range k {
		p[i] = 'a'
	}
	if !c.open {
		copy(p, `{"x":"`)
		c.open = true
	}
	c.left -= k
	c.n.Add(int64(k))
	return k, nil
}
