// Package replay sends one request to Hello's API handler in-process, as
// the MCP server, the AI assistant and proposal apply do (spec ai-access
// S-15, ai-agent S-13). Only the headers the caller passes reach the
// handler, so every replay is authorised, validated and audited exactly as
// a direct call with the same credentials.
package replay

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/azrtydxb/hello/internal/auth"
)

const (
	// Timeout bounds one replayed request.
	Timeout = 30 * time.Second
	// BodyCap bounds what a replayed response may buffer.
	BodyCap = 8 << 20
)

// ErrNested refuses a replay started from inside a replay.
var ErrNested = errors.New("a replayed request cannot start another replay")

// Request is one replayed API call. Header is copied as given: nothing else
// of the caller's request reaches the handler.
type Request struct {
	Method, Path string
	Query        url.Values
	Body         []byte
	Header       http.Header
	// ClientID is the OAuth client the replay is made for, carried in the
	// replay marker (auth.Replay); empty for the assistant and apply.
	ClientID string
	// RemoteAddr is the original caller's address, for the handler's
	// rate limits and logs.
	RemoteAddr string
}

// Result is a replayed response, or the failure that replaced it.
type Result struct {
	Status int
	Header http.Header
	Body   []byte
	// Fail is "timeout", "internal" or "too_large" when the handler did not
	// answer normally.
	Fail string
}

// Do sends r to h in-process. The replay marker rides in the context, where
// no external request can set it; ctx may carry an agent identity
// (auth.WithAgent), which the API middleware honours.
func Do(ctx context.Context, h http.Handler, r Request) (Result, error) {
	if _, nested := auth.ReplayFrom(ctx); nested {
		return Result{}, ErrNested
	}
	ctx, cancel := context.WithTimeout(auth.WithReplay(ctx, auth.Replay{ClientID: r.ClientID}), Timeout)
	defer cancel()
	target := "http://hello-control" + r.Path
	if len(r.Query) > 0 {
		target += "?" + r.Query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(r.Body))
	if err != nil {
		return Result{}, err
	}
	req.Header = r.Header.Clone()
	if req.Header == nil {
		req.Header = http.Header{}
	}
	if r.Body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.RemoteAddr = r.RemoteAddr
	rec := &recorder{header: http.Header{}}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if p := recover(); p != nil {
				slog.Error("replay panicked", "method", r.Method, "path", r.Path, "panic", fmt.Sprint(p))
				rec.fail("internal")
			}
		}()
		h.ServeHTTP(rec, req)
	}()
	select {
	case <-done:
	case <-ctx.Done():
		rec.fail("timeout")
	}
	return rec.result(), nil
}

// recorder buffers a replayed response; it is safe for a handler that keeps
// writing after a timeout has been answered.
type recorder struct {
	mu      sync.Mutex
	header  http.Header
	status  int
	buf     bytes.Buffer
	failure string
	closed  bool
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(code int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = code
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.closed {
		return 0, http.ErrHandlerTimeout
	}
	if r.buf.Len()+len(p) > BodyCap {
		r.failure = "too_large"
		r.closed = true
		return 0, errors.New("replay: response too large")
	}
	return r.buf.Write(p)
}

func (r *recorder) fail(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failure == "" {
		r.failure = reason
	}
	r.closed = true
}

func (r *recorder) result() Result {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return Result{Status: status, Header: r.header.Clone(), Body: bytes.Clone(r.buf.Bytes()), Fail: r.failure}
}
