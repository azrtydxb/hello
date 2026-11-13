package fax

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/azrtydxb/hello/internal/s3retry"
)

// Bucket is the media bucket faxes live in, under the fax/ prefix, next to the
// call recordings (pbx-gaps S-15, "the existing media bucket").
const Bucket = "hello-recordings"

type minioObjects struct {
	c *minio.Client
}

// NewObjects builds the MinIO-backed PDF store. It does not dial: an
// unreachable MinIO shows on first use.
func NewObjects(endpoint, accessKey, secretKey string, secure bool) (Objects, error) {
	if endpoint == "" {
		return nil, errors.New("fax: MinIO endpoint is empty")
	}
	tr, err := s3retry.ClientTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("fax: build MinIO transport: %w", err)
	}
	c, err := minio.New(endpoint, &minio.Options{Creds: credentials.NewStaticV4(accessKey, secretKey, ""), Secure: secure, Transport: tr})
	if err != nil {
		return nil, fmt.Errorf("fax: build MinIO client: %w", err)
	}
	return &minioObjects{c: c}, nil
}

func checkKey(key string) error {
	if !strings.HasPrefix(key, "fax/") || strings.Contains(key, "..") || strings.ContainsAny(key, "\\\x00") {
		return fmt.Errorf("fax: refusing object key %q", key)
	}
	return nil
}

func (m *minioObjects) Put(ctx context.Context, key string, pdf []byte) error {
	if err := checkKey(key); err != nil {
		return err
	}
	_, err := m.c.PutObject(ctx, Bucket, key, bytes.NewReader(pdf), int64(len(pdf)), minio.PutObjectOptions{ContentType: "application/pdf"})
	if err != nil {
		return fmt.Errorf("fax: put %s: %w", key, err)
	}
	return nil
}

func (m *minioObjects) Get(ctx context.Context, key string) ([]byte, error) {
	if err := checkKey(key); err != nil {
		return nil, err
	}
	o, err := m.c.GetObject(ctx, Bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("fax: open %s: %w", key, err)
	}
	defer func() { _ = o.Close() }()
	b, err := io.ReadAll(io.LimitReader(o, MaxReceiveBytes+1))
	if err != nil {
		return nil, fmt.Errorf("fax: read %s: %w", key, err)
	}
	return b, nil
}

func (m *minioObjects) Remove(ctx context.Context, key string) error {
	if err := checkKey(key); err != nil {
		return err
	}
	if err := m.c.RemoveObject(ctx, Bucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("fax: remove %s: %w", key, err)
	}
	return nil
}

// MemObjects is an in-memory Objects for tests.
type MemObjects struct {
	mu sync.Mutex
	M  map[string][]byte
	// FailPut, when set, makes Put fail.
	FailPut error
}

// Put implements Objects.
func (m *MemObjects) Put(_ context.Context, key string, pdf []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.FailPut != nil {
		return m.FailPut
	}
	if m.M == nil {
		m.M = map[string][]byte{}
	}
	m.M[key] = append([]byte(nil), pdf...)
	return nil
}

// Get implements Objects.
func (m *MemObjects) Get(_ context.Context, key string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.M[key]
	if !ok {
		return nil, ErrNoPDF
	}
	return append([]byte(nil), b...), nil
}

// Remove implements Objects.
func (m *MemObjects) Remove(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.M, key)
	return nil
}

// PutRetry stores a PDF with three attempts, a second apart, as recordings
// are stored.
func PutRetry(ctx context.Context, o Objects, key string, pdf []byte) error {
	var err error
	for i := 0; i < 3; i++ {
		if err = o.Put(ctx, key, pdf); err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(time.Duration(i+1) * time.Second):
		}
	}
	return err
}
