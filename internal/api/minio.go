package api

// The MinIO object store behind voicemail audio (spec contract 6): bucket
// hello-voicemail, presigned GET URLs for playback and uploads for greetings.

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// VoicemailBucket is the one bucket voicemail audio lives in.
const VoicemailBucket = "hello-voicemail"

// presignTTL is how long a playback URL stays valid (spec contract 6).
const presignTTL = 15 * time.Minute

// Objects is the voicemail audio store the API and mailer need. It is the
// seam that keeps MinIO out of internal/store and the mailer: a fake in
// tests, MinIO in main.
type Objects interface {
	// Presign returns a GET URL for object, valid 15 minutes.
	Presign(ctx context.Context, object string) (string, error)
	// Put uploads r (size known) as object.
	Put(ctx context.Context, object string, r io.Reader, size int64) error
	// Get returns the object's bytes (mailer attachments).
	Get(ctx context.Context, object string) ([]byte, error)
	// Remove deletes the object; an absent object is not an error.
	Remove(ctx context.Context, object string) error
	// EnsureBucket creates the bucket when it is missing.
	EnsureBucket(ctx context.Context) error
}

// MinioObjects is the MinIO-backed Objects.
type MinioObjects struct {
	cli *minio.Client
}

// NewMinioObjects builds the MinIO client once, at startup. minio.New does
// not dial, so an unreachable endpoint surfaces on first use, not here.
func NewMinioObjects(endpoint, accessKey, secretKey string, secure bool) (*MinioObjects, error) {
	cli, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	return &MinioObjects{cli: cli}, err
}

func (m *MinioObjects) Presign(ctx context.Context, object string) (string, error) {
	u, err := m.cli.PresignedGetObject(ctx, VoicemailBucket, object, presignTTL, nil)
	if err != nil {
		return "", fmt.Errorf("api: presign %s: %w", object, err)
	}
	return u.String(), nil
}

func (m *MinioObjects) Put(ctx context.Context, object string, r io.Reader, size int64) error {
	_, err := m.cli.PutObject(ctx, VoicemailBucket, object, r, size, minio.PutObjectOptions{ContentType: "audio/wav"})
	if err != nil {
		return fmt.Errorf("api: put %s: %w", object, err)
	}
	return nil
}

func (m *MinioObjects) Get(ctx context.Context, object string) ([]byte, error) {
	obj, err := m.cli.GetObject(ctx, VoicemailBucket, object, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("api: get %s: %w", object, err)
	}
	defer func() { _ = obj.Close() }()
	b, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("api: read %s: %w", object, err)
	}
	// minio-go's GetObject is lazy: the error surfaces on read, so an absent
	// greeting or message audio fails here.
	return b, nil
}

func (m *MinioObjects) Remove(ctx context.Context, object string) error {
	if err := m.cli.RemoveObject(ctx, VoicemailBucket, object, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("api: remove %s: %w", object, err)
	}
	return nil
}

func (m *MinioObjects) EnsureBucket(ctx context.Context) error {
	exists, err := m.cli.BucketExists(ctx, VoicemailBucket)
	if err != nil {
		return fmt.Errorf("api: bucket check %s: %w", VoicemailBucket, err)
	}
	if exists {
		return nil
	}
	return m.cli.MakeBucket(ctx, VoicemailBucket, minio.MakeBucketOptions{})
}
