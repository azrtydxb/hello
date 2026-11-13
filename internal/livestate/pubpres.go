package livestate

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/valkey-io/valkey-go"
)

// Published presence (phone-feature-parity S-35). A phone PUBLISHes its
// presence document (RFC 3903); the event state is kept in Valkey, one record
// per publication under hello:pubpres:<extension>:<etag>, with the
// publication's own expiry as its TTL. Both hello-sip nodes read the same
// records, so a PUBLISH that lands on one node is served to a subscriber on
// the other, and a refresh or removal on either node finds the publication.

const pubPresPrefix = "hello:pubpres:"

// PresenceDoc is one publication: the basic status and note of a PIDF
// document (RFC 3863), published by a device of Extension.
type PresenceDoc struct {
	Extension string `json:"extension"`
	// ETag is the publication's entity-tag (the SIP-ETag the publisher
	// quotes in SIP-If-Match).
	ETag   string `json:"etag"`
	Device string `json:"device"`
	// Basic is "open" or "closed".
	Basic     string    `json:"basic"`
	Note      string    `json:"note,omitempty"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// presenceDocPattern is the SCAN pattern for one extension's publications.
func presenceDocPattern(ext string) string { return pubPresPrefix + ext + ":*" }

func presenceDocKey(ext, etag string) string { return pubPresPrefix + ext + ":" + etag }

// presenceReplaceScript stores the new publication in place of the one the
// publisher named (KEYS[1]: old key, KEYS[2]: new key, ARGV[1]: record,
// ARGV[2]: ms). It returns 0, storing nothing, when the old one is gone.
var presenceReplaceScript = valkey.NewLuaScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return 0 end
redis.call('DEL', KEYS[1])
redis.call('SET', KEYS[2], ARGV[1], 'PX', ARGV[2])
return 1`)

// PresencePut stores a publication with the given lifetime. A non-empty
// replaces names the entity-tag this publication refreshes or modifies; the
// call stores nothing and reports false when that publication is gone
// (expired or removed), which the publisher is told with 412.
func (s *Store) PresencePut(ctx context.Context, doc PresenceDoc, replaces string, ttl time.Duration) (bool, error) {
	b, err := json.Marshal(doc)
	if err != nil {
		return false, err
	}
	if replaces == "" {
		if err := s.c.Do(ctx, s.c.B().Set().Key(presenceDocKey(doc.Extension, doc.ETag)).Value(string(b)).Px(ttl).Build()).Error(); err != nil {
			return false, fmt.Errorf("livestate: publish presence: %w", err)
		}
		return true, nil
	}
	n, err := presenceReplaceScript.Exec(ctx, s.c,
		[]string{presenceDocKey(doc.Extension, replaces), presenceDocKey(doc.Extension, doc.ETag)},
		[]string{string(b), fmt.Sprint(ttl.Milliseconds())}).AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: refresh presence: %w", err)
	}
	return n == 1, nil
}

// PresenceDelete removes a publication; false means there was none.
func (s *Store) PresenceDelete(ctx context.Context, ext, etag string) (bool, error) {
	n, err := s.c.Do(ctx, s.c.B().Del().Key(presenceDocKey(ext, etag)).Build()).AsInt64()
	if err != nil {
		return false, fmt.Errorf("livestate: remove presence: %w", err)
	}
	return n == 1, nil
}

// PresenceDocs lists the live publications of one extension, newest first.
func (s *Store) PresenceDocs(ctx context.Context, ext string) ([]PresenceDoc, error) {
	var (
		out    []PresenceDoc
		cursor uint64
	)
	for {
		e, err := s.c.Do(ctx, s.c.B().Scan().Cursor(cursor).Match(presenceDocPattern(ext)).Count(200).Build()).AsScanEntry()
		if err != nil {
			return nil, fmt.Errorf("livestate: presence scan: %w", err)
		}
		for _, k := range e.Elements {
			v, err := s.c.Do(ctx, s.c.B().Get().Key(k).Build()).ToString()
			if valkey.IsValkeyNil(err) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("livestate: presence get: %w", err)
			}
			var d PresenceDoc
			if err := json.Unmarshal([]byte(v), &d); err != nil || !strings.EqualFold(d.Extension, ext) {
				continue
			}
			out = append(out, d)
		}
		if e.Cursor == 0 {
			break
		}
		cursor = e.Cursor
	}
	SortPresenceDocs(out)
	return out, nil
}

// SortPresenceDocs orders publications newest first.
func SortPresenceDocs(d []PresenceDoc) {
	sort.SliceStable(d, func(i, j int) bool { return d[i].UpdatedAt.After(d[j].UpdatedAt) })
}
