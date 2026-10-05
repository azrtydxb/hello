package api

// The MinIO object store behind audio (spec contracts 4 and 6): bucket
// hello-voicemail for voicemail audio, hello-recordings and
// hello-announcements for the Phase 5 media, presigned GET URLs for playback
// and uploads from hello-control.

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// The buckets, one per media kind (spec contract 4).
const (
	VoicemailBucket     = "hello-voicemail"
	RecordingsBucket    = "hello-recordings"
	AnnouncementsBucket = "hello-announcements"
)

// presignTTL is how long a playback URL stays valid (spec contract 6).
const presignTTL = 15 * time.Minute

// Objects is the audio store the API and mailer need. It is the seam that
// keeps MinIO out of internal/store and the mailer: a fake in tests, MinIO in
// main.
type Objects interface {
	// Presign returns a GET URL for a voicemail object, valid 15 minutes.
	Presign(ctx context.Context, object string) (string, error)
	// Put uploads r (size known) as a voicemail object.
	Put(ctx context.Context, object string, r io.Reader, size int64) error
	// Get returns a voicemail object's bytes (mailer attachments).
	Get(ctx context.Context, object string) ([]byte, error)
	// Remove deletes a voicemail object; an absent object is not an error.
	Remove(ctx context.Context, object string) error
	// EnsureBucket creates hello-voicemail when it is missing.
	EnsureBucket(ctx context.Context) error

	// PresignRecording returns a GET URL for a recording, valid 15 minutes.
	PresignRecording(ctx context.Context, object string) (string, error)
	// PutRecording uploads r (size known) as recording audio.
	PutRecording(ctx context.Context, object string, r io.Reader, size int64) error
	// RemoveRecording deletes a recording object.
	RemoveRecording(ctx context.Context, object string) error
	// PutAnnouncement uploads r (size known) as announcement audio.
	PutAnnouncement(ctx context.Context, object string, r io.Reader, size int64) error
	// RemoveAnnouncement deletes announcement audio.
	RemoveAnnouncement(ctx context.Context, object string) error
	// EnsureMediaBuckets creates hello-recordings and hello-announcements when
	// they are missing.
	EnsureMediaBuckets(ctx context.Context) error
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
	return m.ensure(ctx, VoicemailBucket)
}

func (m *MinioObjects) PresignRecording(ctx context.Context, object string) (string, error) {
	u, err := m.cli.PresignedGetObject(ctx, RecordingsBucket, object, presignTTL, nil)
	if err != nil {
		return "", fmt.Errorf("api: presign recording %s: %w", object, err)
	}
	return u.String(), nil
}

func (m *MinioObjects) PutRecording(ctx context.Context, object string, r io.Reader, size int64) error {
	_, err := m.cli.PutObject(ctx, RecordingsBucket, object, r, size, minio.PutObjectOptions{ContentType: "audio/wav"})
	if err != nil {
		return fmt.Errorf("api: put recording %s: %w", object, err)
	}
	return nil
}

func (m *MinioObjects) RemoveRecording(ctx context.Context, object string) error {
	if err := m.cli.RemoveObject(ctx, RecordingsBucket, object, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("api: remove recording %s: %w", object, err)
	}
	return nil
}

func (m *MinioObjects) PutAnnouncement(ctx context.Context, object string, r io.Reader, size int64) error {
	_, err := m.cli.PutObject(ctx, AnnouncementsBucket, object, r, size, minio.PutObjectOptions{ContentType: "audio/wav"})
	if err != nil {
		return fmt.Errorf("api: put announcement %s: %w", object, err)
	}
	return nil
}

func (m *MinioObjects) RemoveAnnouncement(ctx context.Context, object string) error {
	if err := m.cli.RemoveObject(ctx, AnnouncementsBucket, object, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("api: remove announcement %s: %w", object, err)
	}
	return nil
}

func (m *MinioObjects) EnsureMediaBuckets(ctx context.Context) error {
	for _, bucket := range []string{RecordingsBucket, AnnouncementsBucket} {
		if err := m.ensure(ctx, bucket); err != nil {
			return err
		}
	}
	return nil
}

// ensure creates bucket when it is missing.
func (m *MinioObjects) ensure(ctx context.Context, bucket string) error {
	exists, err := m.cli.BucketExists(ctx, bucket)
	if err != nil {
		return fmt.Errorf("api: bucket check %s: %w", bucket, err)
	}
	if exists {
		return nil
	}
	return m.cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{})
}
