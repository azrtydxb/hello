package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/azrtydxb/hello/internal/s3retry"
)

// Bucket is the existing media bucket (spec M-18): attachments live under
// msg/, next to the call recordings and the faxes.
const Bucket = "hello-recordings"

// ErrNoObject is an object that is not in the store.
var ErrNoObject = errors.New("media: no such object")

// Object is an opened attachment: Body seeks, so a handler can answer Range
// requests for audio and video.
type Object struct {
	Body io.ReadSeekCloser
	Size int64
}

// Objects is the attachment store: MinIO in hello-control, MemObjects in
// tests. Every key is under msg/.
type Objects interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, ctype string) error
	// Open opens an object for reading; an absent one is ErrNoObject. The
	// caller closes Body.
	Open(ctx context.Context, key string) (*Object, error)
	// Remove deletes an object; an absent one is not an error.
	Remove(ctx context.Context, key string) error
	// Exists reports whether an object is still there (an erase checks its
	// deletions with it).
	Exists(ctx context.Context, key string) (bool, error)
}

func checkKey(key string) error {
	if !strings.HasPrefix(key, "msg/") || strings.Contains(key, "..") || strings.ContainsAny(key, "\\\x00") {
		return fmt.Errorf("media: refusing object key %q", key)
	}
	return nil
}

type minioObjects struct {
	c *minio.Client
}

// NewObjects builds the MinIO-backed store. It does not dial: an unreachable
// MinIO shows on first use.
func NewObjects(endpoint, accessKey, secretKey string, secure bool) (Objects, error) {
	if endpoint == "" {
		return nil, errors.New("media: MinIO endpoint is empty")
	}
	tr, err := s3retry.ClientTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("media: build MinIO transport: %w", err)
	}
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: secure, Transport: tr})
	if err != nil {
		return nil, fmt.Errorf("media: build MinIO client: %w", err)
	}
	return &minioObjects{c: c}, nil
}

func (m *minioObjects) Put(ctx context.Context, key string, r io.Reader, size int64, ctype string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if _, err := m.c.PutObject(ctx, Bucket, key, r, size, minio.PutObjectOptions{ContentType: ctype}); err != nil {
		return fmt.Errorf("media: put: %w", err)
	}
	return nil
}

func notFound(err error) bool {
	return minio.ToErrorResponse(err).Code == "NoSuchKey"
}

func (m *minioObjects) Open(ctx context.Context, key string) (*Object, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	o, err := m.c.GetObject(ctx, Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("media: open: %w", err)
	}
	// GetObject is lazy; Stat surfaces an absent object before a byte is
	// written.
	info, err := o.Stat()
	if err != nil {
		_ = o.Close()
		if notFound(err) {
			return nil, ErrNoObject
		}
		return nil, fmt.Errorf("media: stat: %w", err)
	}
	return &Object{Body: o, Size: info.Size}, nil
}

func (m *minioObjects) Remove(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if err := m.c.RemoveObject(ctx, Bucket, key, minio.RemoveObjectOptions{}); err != nil && !notFound(err) {
		return fmt.Errorf("media: remove: %w", err)
	}
	return nil
}

func (m *minioObjects) Exists(ctx context.Context, key string) (bool, error) {
	if err := checkKey(key); err != nil {
		return false, err
	}
	_, err := m.c.StatObject(ctx, Bucket, key, minio.StatObjectOptions{})
	switch {
	case err == nil:
		return true, nil
	case notFound(err):
		return false, nil
	}
	return false, fmt.Errorf("media: stat: %w", err)
}

// MemObjects is an in-memory Objects for tests.
type MemObjects struct {
	mu sync.Mutex
	M  map[string][]byte
	// Types holds each object's stored content type.
	Types map[string]string
	// FailPut and FailRemove, when set, make Put and Remove fail.
	FailPut    error
	FailRemove error
}

// Put implements Objects.
func (m *MemObjects) Put(_ context.Context, key string, r io.Reader, _ int64, ctype string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPut != nil {
		return m.FailPut
	}
	if m.M == nil {
		m.M, m.Types = map[string][]byte{}, map[string]string{}
	}
	m.M[key], m.Types[key] = b, ctype
	return nil
}

type memBody struct{ *bytes.Reader }

func (memBody) Close() error { return nil }

// Open implements Objects.
func (m *MemObjects) Open(_ context.Context, key string) (*Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.M[key]
	if !ok {
		return nil, ErrNoObject
	}
	return &Object{Body: memBody{bytes.NewReader(append([]byte(nil), b...))}, Size: int64(len(b))}, nil
}

// Remove implements Objects.
func (m *MemObjects) Remove(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailRemove != nil {
		return m.FailRemove
	}
	delete(m.M, key)
	return nil
}

// Exists implements Objects.
func (m *MemObjects) Exists(_ context.Context, key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.M[key]
	return ok, nil
}

// Len is how many objects are stored.
func (m *MemObjects) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.M)
}
