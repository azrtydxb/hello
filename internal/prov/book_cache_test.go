package prov

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/azrtydxb/hello/internal/phonebook"
	pb "github.com/azrtydxb/hello/internal/prov/phonebook"
)

// memCache is an in-memory BookCache that records each Set's ttl.
type memCache struct {
	mu   sync.Mutex
	m    map[string][]byte
	ttls map[string]time.Duration
}

func newMemCache() *memCache {
	return &memCache{m: map[string][]byte{}, ttls: map[string]time.Duration{}}
}

func (c *memCache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.m[key]
	return b, ok
}

func (c *memCache) Set(_ context.Context, key string, body []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.m[key], c.ttls[key] = body, ttl
}

// TestBookRenderCache (spec P-11) fails if a second fetch of an unchanged
// set reads the books again, if a book change (a new set version) is served
// from the cache, if two vendors or slots share a key, if a render is
// cached with the wrong lifetime, if a denied request or an outage is
// cached or answered from the cache, or if a personal book's render is
// cached under a key another phone could reach.
func TestBookRenderCache(t *testing.T) {
	cache := newMemCache()
	e := newEnv(t, func(o *Options) { o.BookCache = cache })
	p, tok := e.s.addPhone(Yealink, "T54W", macA)
	p.books = twoBooks()

	first := e.do(req{path: devPath(tok, "pb/0.xml")})
	if first.Code != http.StatusOK || e.s.bookReads != 1 {
		t.Fatalf("first fetch: %d after %d book reads", first.Code, e.s.bookReads)
	}
	second := e.do(req{path: devPath(tok, "pb/0.xml")})
	if second.Code != http.StatusOK || second.Body.String() != first.Body.String() || e.s.bookReads != 1 {
		t.Fatalf("second fetch: %d after %d book reads (want the cache to answer)", second.Code, e.s.bookReads)
	}
	if etag := first.Header().Get("ETag"); e.do(req{path: devPath(tok, "pb/0.xml"), inm: etag}).Code != http.StatusNotModified {
		t.Error("a cached render does not answer If-None-Match with 304")
	}
	if row := e.last(); row.Result != ResultNotModified {
		t.Errorf("audit of a cached 304: %+v", row)
	}

	// Another slot is another render; each is cached on its own.
	if w := e.do(req{path: devPath(tok, "pb/1.xml")}); !strings.Contains(w.Body.String(), "Alice") || strings.Contains(w.Body.String(), "Carol") {
		t.Fatalf("slot 1 served the merged render: %s", w.Body.String())
	}
	if len(cache.m) != 2 {
		t.Errorf("%d cached renders, want one per slot (2)", len(cache.m))
	}

	// A change to a book bumps the set's version: the old render is not
	// served and the new one is.
	p.books = append(twoBooks(), pb.Book{Name: "Warehouse", Entries: []phonebook.EntryInput{entry("Zed", "+3225550199")}})
	p.bookVersion++
	w := e.do(req{path: devPath(tok, "pb/0.xml")})
	if !strings.Contains(w.Body.String(), "Zed") {
		t.Fatalf("a changed set was served from the cache: %s", w.Body.String())
	}
	if w.Header().Get("ETag") == first.Header().Get("ETag") {
		t.Error("the ETag did not change with the content")
	}

	for key, ttl := range cache.ttls {
		if ttl != bookCacheTTL {
			t.Errorf("%s cached for %s, want %s", key, ttl, bookCacheTTL)
		}
		if !strings.HasPrefix(key, "hello:pb:render:yealink:") {
			t.Errorf("key %q is not hello:pb:render:<vendor>:<hash>", key)
		}
	}

	// A denial reads nothing and caches nothing.
	n, reads := len(cache.m), e.s.bookReads
	w = e.do(req{path: devPath(tok, "pb/0.xml"), proto: "http"})
	expectEmpty(t, e, w, http.StatusNotFound, ResultPlainHTTP)
	if len(cache.m) != n || e.s.bookReads != reads {
		t.Error("a denied request touched the cache or the books")
	}

	// A store outage while reading the version is a 503, not a stale hit.
	e.s.outage = http.ErrHandlerTimeout
	if w = e.do(req{path: devPath(tok, "pb/0.xml")}); w.Code == http.StatusOK {
		t.Errorf("an outage was answered from the cache: %d", w.Code)
	}
}

// TestBookRenderCachePersonal fails if a set with a personal book is cached
// for longer than five minutes, or under a key two phones share (spec P-11).
func TestBookRenderCachePersonal(t *testing.T) {
	cache := newMemCache()
	e := newEnv(t, func(o *Options) { o.BookCache = cache })
	a, ta := e.s.addPhone(Yealink, "T54W", macA)
	b, tb := e.s.addPhone(Yealink, "T54W", macB)
	for _, p := range []*fakePhone{a, b} {
		p.books, p.personalBook = twoBooks(), true
	}
	if w := e.do(req{path: devPath(ta, "pb/0.xml")}); w.Code != http.StatusOK {
		t.Fatalf("phone a: %d", w.Code)
	}
	if w := e.do(req{path: devPath(tb, "pb/0.xml")}); w.Code != http.StatusOK {
		t.Fatalf("phone b: %d", w.Code)
	}
	if len(cache.m) != 2 {
		t.Fatalf("%d cache keys for two phones with a personal book, want 2", len(cache.m))
	}
	for key, ttl := range cache.ttls {
		if ttl != personalBookCacheTTL {
			t.Errorf("%s cached for %s, want %s", key, ttl, personalBookCacheTTL)
		}
	}
	// Without a personal book two phones of one set share the render.
	if k1, k2 := bookCacheKey("yealink", 0, BooksVersion{Key: "x"}, 1), bookCacheKey("yealink", 0, BooksVersion{Key: "x"}, 2); k1 != k2 {
		t.Error("a shared set has a key per phone")
	}
	if bookCacheKey("yealink", 0, BooksVersion{Key: "x"}, 1) == bookCacheKey("poly", 0, BooksVersion{Key: "x"}, 1) ||
		bookCacheKey("yealink", 0, BooksVersion{Key: "x"}, 1) == bookCacheKey("yealink", 1, BooksVersion{Key: "x"}, 1) {
		t.Error("vendors or slots share a cache key")
	}
}
