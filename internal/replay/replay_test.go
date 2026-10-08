package replay

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
)

// TestDo fails if a replay passes any header but the caller's, drops the
// method, path, query or body, loses the context's agent identity, can start
// another replay, survives a panic without answering "internal", or buffers a
// response over the cap.
func TestDo(t *testing.T) {
	ctx := context.Background()
	t.Run("only the passed headers, request and context reach the handler", func(t *testing.T) {
		var got *http.Request
		var body strings.Builder
		h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got = r
			b := make([]byte, 16)
			n, _ := r.Body.Read(b)
			body.Write(b[:n])
			w.Header().Set("X-Out", "1")
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"ok":true}`))
		})
		res, err := Do(auth.WithAgent(ctx, auth.Agent{UserID: 3, TaskID: "t"}), h, Request{
			Method: http.MethodPost, Path: "/api/v1/x", Query: url.Values{"a": {"b c"}}, Body: []byte(`{"n":1}`),
			Header:   http.Header{"Authorization": {"Bearer t"}, "Cookie": {"c=1"}},
			ClientID: "client", RemoteAddr: "192.0.2.1:1",
		})
		if err != nil || res.Status != 202 || string(res.Body) != `{"ok":true}` || res.Header.Get("X-Out") != "1" || res.Fail != "" {
			t.Fatalf("Do = %+v, %v", res, err)
		}
		if got.Method != "POST" || got.URL.RequestURI() != "/api/v1/x?a=b+c" || body.String() != `{"n":1}` || got.RemoteAddr != "192.0.2.1:1" {
			t.Errorf("request %s %s body %q from %s", got.Method, got.URL.RequestURI(), body.String(), got.RemoteAddr)
		}
		if len(got.Header) != 3 || got.Header.Get("Authorization") != "Bearer t" || got.Header.Get("Cookie") != "c=1" || got.Header.Get("Content-Type") != "application/json" {
			t.Errorf("headers %v, want Authorization, Cookie and Content-Type only", got.Header)
		}
		if rp, ok := auth.ReplayFrom(got.Context()); !ok || rp.ClientID != "client" {
			t.Errorf("replay marker = %+v, %v", rp, ok)
		}
		if a, ok := auth.AgentFrom(got.Context()); !ok || a.UserID != 3 {
			t.Errorf("agent in the handler's context = %+v, %v", a, ok)
		}
	})
	t.Run("nested", func(t *testing.T) {
		_, err := Do(auth.WithReplay(ctx, auth.Replay{}), http.NotFoundHandler(), Request{Method: "GET", Path: "/"})
		if !errors.Is(err, ErrNested) {
			t.Fatalf("nested Do = %v", err)
		}
	})
	t.Run("panic", func(t *testing.T) {
		res, err := Do(ctx, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }), Request{Method: "GET", Path: "/"})
		if err != nil || res.Fail != "internal" {
			t.Fatalf("panicking handler = %+v, %v", res, err)
		}
	})
	t.Run("too large", func(t *testing.T) {
		h := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			chunk := make([]byte, 1<<20)
			for range 9 {
				if _, err := w.Write(chunk); err != nil {
					return
				}
			}
		})
		res, err := Do(ctx, h, Request{Method: "GET", Path: "/"})
		if err != nil || res.Fail != "too_large" {
			t.Fatalf("large response = fail %q, %v", res.Fail, err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		c, cancel := context.WithCancel(ctx)
		release := make(chan struct{})
		defer close(release)
		go cancel()
		res, err := Do(c, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }), Request{Method: "GET", Path: "/"})
		if err != nil || res.Fail != "timeout" {
			t.Fatalf("cancelled replay = fail %q, %v", res.Fail, err)
		}
	})
}
