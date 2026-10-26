package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// slowDownServer answers the first n object PUTs with 503 SlowDown without
// reading the body (as an overloaded MinIO does), then accepts.
func slowDownServer(t *testing.T, n int32) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var puts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && q.Has("location"):
			_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPut:
			if puts.Add(1) <= n {
				w.Header().Set("Content-Type", "application/xml")
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, `<Error><Code>SlowDown</Code><Message>Resource requested is unreadable, please reduce your request rate</Message></Error>`)
				return
			}
			_, _ = io.Copy(io.Discard, r.Body)
			w.Header().Set("ETag", `"e"`)
		default:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &puts
}

// A SlowDown from the store is retried by minio-go and must end in success;
// under -race this is the path that used to report a data race.
func TestPutRetriesAfterSlowDown(t *testing.T) {
	srv, puts := slowDownServer(t, 2)
	m, err := NewMinioObjects(strings.TrimPrefix(srv.URL, "http://"), "k", "s", false)
	if err != nil {
		t.Fatal(err)
	}
	body := bytes.Repeat([]byte("x"), 4<<20)
	if err := m.PutRecording(context.Background(), "rec/a.wav", bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("PutRecording after SlowDown: %v", err)
	}
	if got := puts.Load(); got != 3 {
		t.Fatalf("PUT attempts = %d, want 3", got)
	}
}
