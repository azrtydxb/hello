package s3retry

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rtFunc func(*http.Request) (*http.Response, error)

func (f rtFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// TestBodyAbandonedAfterAnyResponse fails if a response that minio-go retries
// on its S3 error code but whose status is below 500 (a 400 RequestTimeout)
// leaves the attempt's body readable: the write loop could then read it
// while the next attempt rewinds it, and the retry sends a short body
// (MinIO: "did not provide the number of bytes specified by Content-Length").
func TestBodyAbandonedAfterAnyResponse(t *testing.T) {
	for _, code := range []int{200, 400, 503} {
		var sent io.ReadCloser
		rt := Transport(rtFunc(func(r *http.Request) (*http.Response, error) {
			sent = r.Body
			return &http.Response{StatusCode: code, Body: http.NoBody, Request: r}, nil
		}))
		req, _ := http.NewRequest("PUT", "http://x/", io.NopCloser(strings.NewReader("payload")))
		if _, err := rt.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
		if _, err := sent.Read(make([]byte, 4)); !errors.Is(err, errAbandoned) {
			t.Errorf("status %d: read after the response = %v, want errAbandoned", code, err)
		}
	}
}
