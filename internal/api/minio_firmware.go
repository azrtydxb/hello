package api

// Firmware files in MinIO (spec S-13): bucket hello-firmware, key
// <vendor>/<sha256>/<filename>, created on first use.

import (
	"context"
	"fmt"
	"io"

	"github.com/minio/minio-go/v7"
)

// FirmwareBucket holds uploaded phone firmware.
const FirmwareBucket = "hello-firmware"

// EnsureFirmwareBucket creates hello-firmware when it is missing.
func (m *MinioObjects) EnsureFirmwareBucket(ctx context.Context) error {
	return m.ensure(ctx, FirmwareBucket)
}

// PutFirmware implements FirmwareObjects; the size is unknown, so the
// client uploads in parts.
func (m *MinioObjects) PutFirmware(ctx context.Context, key string, r io.Reader) error {
	if err := m.ensure(ctx, FirmwareBucket); err != nil {
		return err
	}
	_, err := m.cli.PutObject(ctx, FirmwareBucket, key, r, -1, minio.PutObjectOptions{ContentType: "application/octet-stream"})
	if err != nil {
		return fmt.Errorf("api: put firmware %s: %w", key, err)
	}
	return nil
}

// MoveFirmware implements FirmwareObjects.
func (m *MinioObjects) MoveFirmware(ctx context.Context, from, to string) error {
	_, err := m.cli.CopyObject(ctx, minio.CopyDestOptions{Bucket: FirmwareBucket, Object: to},
		minio.CopySrcOptions{Bucket: FirmwareBucket, Object: from})
	if err != nil {
		return fmt.Errorf("api: copy firmware %s: %w", from, err)
	}
	return m.RemoveFirmware(ctx, from)
}

// RemoveFirmware implements FirmwareObjects.
func (m *MinioObjects) RemoveFirmware(ctx context.Context, key string) error {
	if err := m.cli.RemoveObject(ctx, FirmwareBucket, key, minio.RemoveObjectOptions{}); err != nil {
		return fmt.Errorf("api: remove firmware %s: %w", key, err)
	}
	return nil
}

// OpenFirmware implements prov.Opener: the object, seekable for Range.
// The Stat surfaces a missing object or an outage here instead of on the
// first read.
func (m *MinioObjects) OpenFirmware(ctx context.Context, key string) (io.ReadSeekCloser, error) {
	obj, err := m.cli.GetObject(ctx, FirmwareBucket, key, minio.GetObjectOptions{})
	if err != nil {
		return nil, fmt.Errorf("api: open firmware %s: %w", key, err)
	}
	if _, err := obj.Stat(); err != nil {
		_ = obj.Close()
		return nil, fmt.Errorf("api: open firmware %s: %w", key, err)
	}
	return obj, nil
}
