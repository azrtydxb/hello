package crm

import (
	"strings"
	"sync"
	"time"
	"unicode"
)

// ContactIndex finds a contact by phone number in a user's address book that
// a connector read as a whole (Outlook and Google have no reliable search on
// a phone number). Numbers are compared on their digits: on the last
// eight digits. A number two different contacts share is
// ambiguous and names nobody, so a caller is never given a wrong name.
type ContactIndex struct {
	by     map[string]*Contact
	ambig  map[string]bool
	Loaded time.Time
	N      int
}

// MaxIndexed bounds the contacts one index keeps.
const MaxIndexed = 20000

// NewContactIndex returns an empty index.
func NewContactIndex() *ContactIndex {
	return &ContactIndex{by: map[string]*Contact{}, ambig: map[string]bool{}}
}

// keyDigits is how many trailing digits identify a number. A national number
// and its international form share their last eight digits in every numbering
// plan Hello meets (+32 50 12 34 56 and 050 12 34 56), where nine would miss
// the trunk prefix; contacts that collide on them are ambiguous, not wrong.
const keyDigits = 8

// digitsKey reduces a phone string to the digits compared: the digits, with
// a leading 00 dropped, cut to the last eight; "" for a string too short.
func digitsKey(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	d := strings.TrimPrefix(b.String(), "00")
	if len(d) < MinLookupDigits {
		return ""
	}
	if len(d) > keyDigits {
		d = d[len(d)-keyDigits:]
	}
	return d
}

// Add indexes a contact under each of its phone numbers.
func (x *ContactIndex) Add(c Contact, phones ...string) {
	if x.N >= MaxIndexed {
		return
	}
	x.N++
	for _, p := range phones {
		k := digitsKey(p)
		if k == "" {
			continue
		}
		if cur, ok := x.by[k]; ok && cur.ID != c.ID {
			x.ambig[k] = true
			continue
		}
		cc := c
		x.by[k] = &cc
	}
}

// Find returns the contact whose number ends like number; nil for none or an
// ambiguous number.
func (x *ContactIndex) Find(number string) *Contact {
	k := digitsKey(number)
	if k == "" || x.ambig[k] {
		return nil
	}
	return x.by[k]
}

// IndexCache keeps one ContactIndex per account for a while, so the many
// calls a day from a user's contacts cost one read of the address book per
// TTL, not one per call.
type IndexCache struct {
	TTL time.Duration
	Now func() time.Time

	mu sync.Mutex
	m  map[string]*indexEntry
}

type indexEntry struct {
	mu  sync.Mutex // held while loading
	idx *ContactIndex
	at  time.Time
}

func (c *IndexCache) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// Get returns the index for key, calling load when it is missing or older
// than the TTL (10 minutes by default). Concurrent callers for one key wait
// for one load. A failed load is returned and nothing is cached.
func (c *IndexCache) Get(key string, load func() (*ContactIndex, error)) (*ContactIndex, error) {
	ttl := c.TTL
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	c.mu.Lock()
	if c.m == nil {
		c.m = map[string]*indexEntry{}
	}
	e, ok := c.m[key]
	if !ok {
		e = &indexEntry{}
		c.m[key] = e
		// Bound the cache: drop expired entries when it grows.
		if len(c.m) > 512 {
			for k, v := range c.m {
				if k != key && c.now().Sub(v.at) > ttl {
					delete(c.m, k)
				}
			}
		}
	}
	c.mu.Unlock()
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.idx != nil && c.now().Sub(e.at) < ttl {
		return e.idx, nil
	}
	idx, err := load()
	if err != nil {
		return nil, err
	}
	e.idx, e.at = idx, c.now()
	return idx, nil
}

// Forget drops an account's index (after a disconnect).
func (c *IndexCache) Forget(key string) {
	c.mu.Lock()
	delete(c.m, key)
	c.mu.Unlock()
}
