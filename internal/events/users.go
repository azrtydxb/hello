package events

import (
	"context"
	"database/sql"
	"slices"
	"sync"
	"time"
)

// userQuerier is the part of *sql.DB and *sql.Tx the user lookup needs.
type userQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// usersOf returns the user each extension number is linked to; an extension
// no user owns is absent from the result.
func usersOf(ctx context.Context, q userQuerier, exts []string) (map[string]int64, error) {
	out := map[string]int64{}
	if len(exts) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `
		SELECT e.number, ue.user_id
		FROM user_extensions ue JOIN extensions e ON e.id = ue.extension_id
		WHERE e.number = ANY($1::text[])`, pgArray(exts))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			n  string
			id int64
		)
		if err := rows.Scan(&n, &id); err != nil {
			return nil, err
		}
		out[n] = id
	}
	return out, rows.Err()
}

// userTTL is how long the live path remembers which user owns an extension:
// a link change shows in live events within this time, and always in stored
// ones, which resolve it again when they are ingested.
const userTTL = 30 * time.Second

type userEntry struct {
	id      int64
	expires time.Time
}

// userCache remembers extension-to-user lookups for the live stream.
type userCache struct {
	mu  sync.Mutex
	m   map[string]userEntry
	now func() time.Time
}

// fill sets UserID on each event from the cache, looking up the extensions
// it does not know in one query. A failed lookup leaves the events without a
// user rather than holding the stream up.
func (c *userCache) fill(ctx context.Context, q userQuerier, es []*Event) {
	now := time.Now
	if c.now != nil {
		now = c.now
	}
	t := now()
	var miss []string
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]userEntry{}
	}
	for _, e := range es {
		ext := e.Data.Extension
		if ext == "" {
			continue
		}
		if u, ok := c.m[ext]; ok && t.Before(u.expires) {
			e.Data.UserID = u.id
		} else if !slices.Contains(miss, ext) {
			miss = append(miss, ext)
		}
	}
	c.mu.Unlock()
	if len(miss) == 0 {
		return
	}
	lctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	found, err := usersOf(lctx, q, miss)
	if err != nil {
		return
	}
	c.mu.Lock()
	for _, ext := range miss {
		c.m[ext] = userEntry{id: found[ext], expires: t.Add(userTTL)}
	}
	if len(c.m) > 4096 {
		clear(c.m) // a bound on a long-running pod; the next events refill it
	}
	c.mu.Unlock()
	for _, e := range es {
		if id, ok := found[e.Data.Extension]; ok {
			e.Data.UserID = id
		}
	}
}
