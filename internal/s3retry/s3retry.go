// Package s3retry makes minio-go's request retries race free.
//
// minio-go retries a throttled request (503 SlowDown, "reduce your request
// rate") by seeking the same seekable body back to the start and sending it
// again. An overloaded server answers before it has read the whole body, so
// net/http's write loop can still be reading the previous attempt's body
// when the next attempt seeks it: a data race (and, without -race, a
// corrupted upload). Transport closes that window: when a response that
// minio-go will retry comes back, it stops the previous attempt's body reads
// and waits for any in flight read to finish before returning.
package s3retry

import (
	"errors"
	"io"
	"net/http"
	"sync"

	"github.com/minio/minio-go/v7"
)

// errAbandoned fails the write loop's next read of an attempt whose server
// already answered with a retryable status.
var errAbandoned = errors.New("s3retry: request body abandoned after a retryable response")

// ClientTransport is minio-go's default transport for secure, wrapped by
// Transport. Pass it as minio.Options.Transport.
func ClientTransport(secure bool) (http.RoundTripper, error) {
	base, err := minio.DefaultTransport(secure)
	if err != nil {
		return nil, err
	}
	return Transport(base), nil
}

// Transport wraps base (http.DefaultTransport when nil).
func Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &transport{base: base}
}

type transport struct{ base http.RoundTripper }

func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Body == nil || req.Body == http.NoBody {
		return t.base.RoundTrip(req)
	}
	gb := &gatedBody{rc: req.Body}
	// RoundTrip must not modify the caller's request.
	r2 := req.Clone(req.Context())
	r2.Body = gb
	resp, err := t.base.RoundTrip(r2)
	// minio-go retries on S3 error codes as well as on statuses (a 400
	// RequestTimeout from a server too slow to read the body is retried), so
	// the status alone cannot say whether a rewind follows. Once a response
	// is back this attempt's body is finished with either way: a server that
	// answered 2xx has read all of it. Nothing may read it after this point.
	gb.abandon()
	return resp, err
}

// gatedBody serialises reads with abandon.
type gatedBody struct {
	mu        sync.Mutex
	rc        io.ReadCloser
	abandoned bool
}

func (g *gatedBody) Read(p []byte) (int, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.abandoned {
		return 0, errAbandoned
	}
	return g.rc.Read(p)
}

// Close closes the wrapped body. minio-go's bodies are no-op closers, and a
// real closer is safe to close while a read is queued behind the gate.
func (g *gatedBody) Close() error { return g.rc.Close() }

// abandon returns once no read is running and none will start.
func (g *gatedBody) abandon() {
	g.mu.Lock()
	g.abandoned = true
	g.mu.Unlock()
}
