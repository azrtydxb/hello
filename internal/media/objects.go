package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// voicemailBucket is contract 6's fixed bucket for voicemail audio.
const voicemailBucket = "hello-voicemail"

// Objects is the voicemail audio store (contract 6: bucket hello-voicemail,
// key box/<box-id>/<unix>-<callid>.wav). The client is built once at
// startup and shared; Put and Get are safe for concurrent use.
type Objects interface {
	// Put stores data under key, replacing any earlier object with the
	// same key.
	Put(ctx context.Context, key string, data []byte) error
	// Get returns the bytes stored under key. A missing object is an
	// error, not an empty slice with a nil error.
	Get(ctx context.Context, key string) ([]byte, error)
}

// ObjectKey builds the canonical storage key for one voicemail message:
// box/<box-id>/<unix>-<callid>.wav. The call ID arrives from the SIP
// Call-ID header and is attacker-influenced, so it is validated as the
// plain token it claims to be: no path separators, no dot-dot, never
// empty. ObjectKey refuses rather than sanitises, so a hostile Call-ID
// fails loudly instead of storing audio somewhere else in the bucket.
func ObjectKey(boxID int64, callID string, at time.Time) (string, error) {
	if callID == "" {
		return "", errors.New("media: call ID is empty")
	}
	if strings.Contains(callID, "/") || strings.Contains(callID, "\\") {
		return "", errors.New("media: call ID contains a path separator")
	}
	if strings.Contains(callID, "..") {
		return "", errors.New("media: call ID contains dot-dot")
	}
	return fmt.Sprintf("box/%d/%d-%s.wav", boxID, at.Unix(), callID), nil
}

// minioObjects is the Objects implementation over MinIO (or any S3
// compatible server). It holds a client built once and nothing else
// mutable, so concurrent Put and Get never race.
type minioObjects struct {
	client *minio.Client
	log    *slog.Logger
}

// NewMinioObjects builds the MinIO client once at startup and makes sure
// the voicemail bucket exists. BucketExists runs first so the common path
// stays read-only; if MakeBucket then fails because another hello-sip node
// won the race to create the bucket, the re-check treats that as success
// rather than taking the whole node down. The secret key is never logged:
// errors name the bucket or endpoint, never a credential value.
func NewMinioObjects(endpoint, accessKey, secretKey string, secure bool, log *slog.Logger) (Objects, error) {
	if endpoint == "" {
		return nil, errors.New("media: MinIO endpoint is empty")
	}
	if log == nil {
		log = slog.Default()
	}
	client, err := minio.New(endpoint, &minio.Options{
		Creds:  credentials.NewStaticV4(accessKey, secretKey, ""),
		Secure: secure,
	})
	if err != nil {
		return nil, fmt.Errorf("media: build MinIO client for %s: %w", endpoint, err)
	}
	o := &minioObjects{client: client, log: log}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	exists, err := client.BucketExists(ctx, voicemailBucket)
	if err != nil {
		return nil, fmt.Errorf("media: check bucket %s: %w", voicemailBucket, err)
	}
	if exists {
		return o, nil
	}
	if err := client.MakeBucket(ctx, voicemailBucket, minio.MakeBucketOptions{}); err != nil {
		existsNow, recheckErr := client.BucketExists(ctx, voicemailBucket)
		if recheckErr != nil || !existsNow {
			return nil, fmt.Errorf("media: create bucket %s: %w", voicemailBucket, err)
		}
		o.log.Info("media: voicemail bucket created by another node", "bucket", voicemailBucket)
	}
	return o, nil
}

// Put stores the WAV audio for one message. The voicemail flow retries
// around this (plan task 2, S-8); Objects itself stays a single honest
// attempt so a failure is visible to whoever can act on it.
func (o *minioObjects) Put(ctx context.Context, key string, data []byte) error {
	_, err := o.client.PutObject(ctx, voicemailBucket, key,
		bytes.NewReader(data), int64(len(data)),
		minio.PutObjectOptions{ContentType: "audio/wav"})
	if err != nil {
		return fmt.Errorf("media: put voicemail object %s: %w", key, err)
	}
	return nil
}

// Get returns the bytes of one stored message. A missing object surfaces
// as the MinIO error, not as empty bytes and success.
func (o *minioObjects) Get(ctx context.Context, key string) ([]byte, error) {
	obj, err := o.client.GetObject(ctx, voicemailBucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("media: open voicemail object %s: %w", key, err)
	}
	defer func() {
		// The bytes are already in memory; a close error after a complete
		// read adds nothing the caller could act on.
		_ = obj.Close()
	}()
	data, err := io.ReadAll(obj)
	if err != nil {
		return nil, fmt.Errorf("media: read voicemail object %s: %w", key, err)
	}
	return data, nil
}
