package livestate

import (
	"context"
	"testing"
	"time"
)

// TestPublishedPresence: a publication is listed under its extension only,
// a refresh replaces it by its entity-tag, a refresh of a gone publication
// stores nothing, and a removal deletes it. It fails if another extension
// sees the record, a stale entity-tag still refreshes, or both the old and
// the new record stay after a modify.
func TestPublishedPresence(t *testing.T) {
	s, ctx := store(t), context.Background()
	doc := func(ext, etag, basic string) PresenceDoc {
		return PresenceDoc{Extension: ext, ETag: etag, Device: "d" + ext, Basic: basic, UpdatedAt: time.Now()}
	}
	if ok, err := s.PresencePut(ctx, doc("101", "e1", "open"), "", time.Minute); err != nil || !ok {
		t.Fatalf("put = %v, %v", ok, err)
	}
	if ok, _ := s.PresencePut(ctx, doc("1010", "x1", "closed"), "", time.Minute); !ok {
		t.Fatal("put 1010")
	}
	got, err := s.PresenceDocs(ctx, "101")
	if err != nil || len(got) != 1 || got[0].ETag != "e1" {
		t.Fatalf("101 docs = %+v, %v", got, err)
	}
	if ok, _ := s.PresencePut(ctx, doc("101", "e2", "closed"), "stale", time.Minute); ok {
		t.Fatal("a stale entity-tag refreshed the publication")
	}
	if ok, err := s.PresencePut(ctx, doc("101", "e2", "closed"), "e1", time.Minute); err != nil || !ok {
		t.Fatalf("modify = %v, %v", ok, err)
	}
	got, _ = s.PresenceDocs(ctx, "101")
	if len(got) != 1 || got[0].ETag != "e2" || got[0].Basic != "closed" {
		t.Fatalf("after modify = %+v", got)
	}
	if ok, _ := s.PresenceDelete(ctx, "101", "e2"); !ok {
		t.Fatal("delete")
	}
	if ok, _ := s.PresenceDelete(ctx, "101", "e2"); ok {
		t.Fatal("second delete reported a record")
	}
	if got, _ := s.PresenceDocs(ctx, "101"); len(got) != 0 {
		t.Fatalf("after delete = %+v", got)
	}
}
