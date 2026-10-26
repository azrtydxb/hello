package backup

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"

	"github.com/azrtydxb/hello/internal/s3retry"
)

// Object is an entry of an object store.
type Object struct {
	Key  string
	Size int64
	ETag string
}

// Store is one S3-compatible server, as backups use it. The live MinIO and
// the destination are both Stores; when they are the same server, objects
// are copied by the server and never pass through the pod.
type Store interface {
	// ID names the server; two Stores with the same ID share a server.
	ID() string
	List(ctx context.Context, bucket, prefix string) ([]Object, error)
	// Stat reports whether the object exists.
	Stat(ctx context.Context, bucket, key string) (Object, bool, error)
	Get(ctx context.Context, bucket, key string) (io.ReadCloser, error)
	Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error
	// Copy copies within the server.
	Copy(ctx context.Context, dstBucket, dstKey, srcBucket, srcKey string) error
	Remove(ctx context.Context, bucket, key string) error
	EnsureBucket(ctx context.Context, bucket string) error
}

// MinioStore is the minio-go implementation of Store.
type MinioStore struct {
	cli *minio.Client
	id  string
	// Region is used when creating buckets.
	Region string
}

// NewMinio builds a client for endpoint (host[:port]); it does not dial.
func NewMinio(endpoint, access, secret string, secure bool, region string, pathStyle bool) (*MinioStore, error) {
	tr, err := s3retry.ClientTransport(secure)
	if err != nil {
		return nil, fmt.Errorf("backup: transport: %w", err)
	}
	lookup := minio.BucketLookupAuto
	if pathStyle {
		lookup = minio.BucketLookupPath
	}
	cli, err := minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(access, secret, ""), Secure: secure, Transport: tr,
		Region: region, BucketLookup: lookup,
	})
	if err != nil {
		return nil, fmt.Errorf("backup: object store: %w", err)
	}
	return &MinioStore{cli: cli, id: endpoint, Region: region}, nil
}

// ParseEndpoint splits "https://host:port" or "host:port" into the host and
// whether TLS is used; a bare host is TLS.
func ParseEndpoint(raw string) (host string, secure bool, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", false, errors.New("endpoint is required")
	}
	if !strings.Contains(raw, "://") {
		return raw, true, nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") || (u.Path != "" && u.Path != "/") {
		return "", false, errors.New("must be https://host[:port]")
	}
	return u.Host, u.Scheme == "https", nil
}

func (m *MinioStore) ID() string { return m.id }

func (m *MinioStore) List(ctx context.Context, bucket, prefix string) ([]Object, error) {
	var out []Object
	for o := range m.cli.ListObjects(ctx, bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if o.Err != nil {
			if minio.ToErrorResponse(o.Err).Code == "NoSuchBucket" {
				return nil, nil // a bucket never created holds nothing
			}
			return nil, o.Err
		}
		out = append(out, Object{Key: o.Key, Size: o.Size, ETag: strings.Trim(o.ETag, `"`)})
	}
	return out, nil
}

func (m *MinioStore) Stat(ctx context.Context, bucket, key string) (Object, bool, error) {
	i, err := m.cli.StatObject(ctx, bucket, key, minio.StatObjectOptions{})
	if err != nil {
		if minio.ToErrorResponse(err).Code == "NoSuchKey" || minio.ToErrorResponse(err).StatusCode == 404 {
			return Object{}, false, nil
		}
		return Object{}, false, err
	}
	return Object{Key: key, Size: i.Size, ETag: strings.Trim(i.ETag, `"`)}, true, nil
}

func (m *MinioStore) Get(ctx context.Context, bucket, key string) (io.ReadCloser, error) {
	o, err := m.cli.GetObject(ctx, bucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, err
	}
	// GetObject is lazy: surface an absent object now.
	if _, err := o.Stat(); err != nil {
		_ = o.Close()
		return nil, err
	}
	return o, nil
}

func (m *MinioStore) Put(ctx context.Context, bucket, key string, r io.Reader, size int64) error {
	_, err := m.cli.PutObject(ctx, bucket, key, r, size, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	return err
}

func (m *MinioStore) Copy(ctx context.Context, dstBucket, dstKey, srcBucket, srcKey string) error {
	_, err := m.cli.CopyObject(ctx, minio.CopyDestOptions{Bucket: dstBucket, Object: dstKey},
		minio.CopySrcOptions{Bucket: srcBucket, Object: srcKey})
	return err
}

func (m *MinioStore) Remove(ctx context.Context, bucket, key string) error {
	return m.cli.RemoveObject(ctx, bucket, key, minio.RemoveObjectOptions{})
}

func (m *MinioStore) EnsureBucket(ctx context.Context, bucket string) error {
	ok, err := m.cli.BucketExists(ctx, bucket)
	if err != nil || ok {
		return err
	}
	err = m.cli.MakeBucket(ctx, bucket, minio.MakeBucketOptions{Region: m.Region})
	if code := minio.ToErrorResponse(err).Code; code == "BucketAlreadyOwnedByYou" || code == "BucketAlreadyExists" {
		return nil
	}
	return err
}

// CopyObject copies src/key to dst/key: by the server when both buckets live
// on the same server, else through the caller (an external S3 destination
// cannot read the live MinIO).
func CopyObject(ctx context.Context, from Store, fromBucket, fromKey string, to Store, toBucket, toKey string, size int64) error {
	if from.ID() == to.ID() {
		return to.Copy(ctx, toBucket, toKey, fromBucket, fromKey)
	}
	r, err := from.Get(ctx, fromBucket, fromKey)
	if err != nil {
		return err
	}
	defer func() { _ = r.Close() }()
	return to.Put(ctx, toBucket, toKey, r, size)
}
