package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// zeroReader yields n zero bytes without holding them.
type zeroReader struct{ n int64 }

func (z *zeroReader) Read(p []byte) (int, error) {
	if z.n <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > z.n {
		p = p[:z.n]
	}
	clear(p)
	z.n -= int64(len(p))
	return len(p), nil
}

// A streamed firmware upload of unknown size must be cut into small parts:
// minio-go's default for size -1 is one ~537 MiB buffer, which OOMKills the
// 256Mi control pod.
func TestPutFirmwareBoundsPartSize(t *testing.T) {
	var (
		mu      sync.Mutex
		maxPart int64
		parts   int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch {
		case r.Method == http.MethodGet && q.Has("location"):
			_, _ = io.WriteString(w, `<LocationConstraint xmlns="http://s3.amazonaws.com/doc/2006-03-01/">us-east-1</LocationConstraint>`)
		case r.Method == http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && q.Has("uploads"):
			_, _ = io.WriteString(w, `<InitiateMultipartUploadResult><UploadId>u1</UploadId></InitiateMultipartUploadResult>`)
		case r.Method == http.MethodPut && q.Has("partNumber"):
			n, _ := io.Copy(io.Discard, r.Body)
			mu.Lock()
			parts++
			maxPart = max(maxPart, n)
			mu.Unlock()
			w.Header().Set("ETag", `"e`+q.Get("partNumber")+`"`)
		case r.Method == http.MethodPost && q.Has("uploadId"):
			_, _ = io.WriteString(w, `<CompleteMultipartUploadResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/"><Bucket>hello-firmware</Bucket><ETag>"done"</ETag></CompleteMultipartUploadResult>`)
		default:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer srv.Close()

	m, err := NewMinioObjects(strings.TrimPrefix(srv.URL, "http://"), "k", "s", false)
	if err != nil {
		t.Fatal(err)
	}
	const size = 40 << 20
	if err := m.PutFirmware(context.Background(), "grandstream/abc/fw.bin", &zeroReader{n: size}); err != nil {
		t.Fatalf("PutFirmware: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	// The body carries a little chunk-signing framing on top of the payload.
	if maxPart > firmwarePartSize+1<<20 {
		t.Fatalf("largest part = %d bytes, want about %d", maxPart, firmwarePartSize)
	}
	if parts < 3 {
		t.Fatalf("parts = %d, want the 40 MiB file split into >= 3", parts)
	}
}

func TestFirmwarePartSizeCoversUploadCap(t *testing.T) {
	if got := firmwarePutOptions().PartSize * 10000; got < maxFirmwareBytes {
		t.Fatalf("10000 parts hold %d bytes, below the %d cap", got, maxFirmwareBytes)
	}
}
