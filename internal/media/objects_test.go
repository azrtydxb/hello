package media

import (
	"context"
	"os"
	"testing"
	"time"
)

func TestObjectKey(t *testing.T) {
	at := time.Unix(1700000000, 0).UTC()
	key, err := ObjectKey(7, "call-id.1@host", at)
	if err != nil {
		t.Fatalf("ObjectKey: %v", err)
	}
	want := "box/7/1700000000-call-id.1@host.wav"
	if key != want {
		t.Errorf("key %q, want %q", key, want)
	}
	again, _ := ObjectKey(7, "call-id.1@host", at)
	if again != key {
		t.Error("ObjectKey must be deterministic")
	}
	// Hostile call IDs must be refused, never sanitised into a different
	// location.
	for _, callID := range []string{"", "a/b", "../box/9/x", "..", "a\\b"} {
		if _, err := ObjectKey(7, callID, at); err == nil {
			t.Errorf("call ID %q accepted, want an error", callID)
		}
	}
}

// TestMinioObjects is the integration test against a real MinIO; it stays
// skipped everywhere the lab is not wired up.
func TestMinioObjects(t *testing.T) {
	endpoint := os.Getenv("MINIO_TEST_ENDPOINT")
	if endpoint == "" {
		t.Skip("MINIO_TEST_ENDPOINT not set; integration only")
	}
	accessKey := os.Getenv("MINIO_TEST_ACCESS_KEY")
	secretKey := os.Getenv("MINIO_TEST_SECRET_KEY")
	if accessKey == "" {
		accessKey = "minioadmin"
	}
	if secretKey == "" {
		secretKey = "minioadmin"
	}
	objects, err := NewMinioObjects(endpoint, accessKey, secretKey, false, testLogger(t))
	if err != nil {
		t.Fatalf("NewMinioObjects: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	key, err := ObjectKey(1, "minio-test-object", time.Now())
	if err != nil {
		t.Fatalf("ObjectKey: %v", err)
	}
	data := EncodeWAV([]byte{1, 2, 3, 4}, sampleRateHz)
	if err := objects.Put(ctx, key, data); err != nil {
		t.Fatalf("Put: %v", err)
	}
	got, err := objects.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("Get returned %d bytes, want %d", len(got), len(data))
	}
}
