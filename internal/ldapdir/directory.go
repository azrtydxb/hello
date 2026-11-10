package ldapdir

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"time"

	"github.com/azrtydxb/hello/internal/prov"
	provbook "github.com/azrtydxb/hello/internal/prov/phonebook"
)

// ErrNotFound is returned by a Backend for a phone that is gone.
var ErrNotFound = prov.ErrNotFound

// ErrUnavailable is a directory that cannot be read: PostgreSQL is down and
// no earlier render of the set is cached.
var ErrUnavailable = errors.New("ldap: directory unavailable")

// Backend is what the LDAP server needs from hello-control's database. A
// phone id of 0 stands for an anonymous session (HELLO_LDAP_ANON), which
// sees the extensions and company books only.
type Backend interface {
	// Authenticate reports whether password is the phone's current LDAP
	// password (or its previous one, until the phone has fetched the new
	// one) and its group has LDAP on. An unknown, disabled or non-LDAP
	// phone is false, not an error.
	Authenticate(ctx context.Context, phoneID int64, password string) (bool, error)
	// LDAPVersion and LDAPBooks are the phone's delivered set and its
	// version, the same ones the XML directory renders from (spec P-2,
	// P-11).
	LDAPVersion(ctx context.Context, phoneID int64) (prov.BooksVersion, error)
	LDAPBooks(ctx context.Context, phoneID int64) ([]provbook.Book, error)
}

const (
	setKeyPrefix  = "hello:ldap:set:"
	lastKeyPrefix = "hello:ldap:last:"
	setTTL        = time.Hour
	personalTTL   = 5 * time.Minute
	lastTTL       = 24 * time.Hour
)

// directory reads a phone's set through the cache. It is keyed by the set's
// version, like the XML directory's, so a change to any book of the set
// names a new key (spec S-25).
type directory struct {
	b     Backend
	cache prov.BookCache
	base  string
	log   *slog.Logger
}

func setKey(v prov.BooksVersion, phoneID int64) string {
	h := sha256.New()
	_, _ = io.WriteString(h, v.Key)
	if v.Personal {
		_, _ = io.WriteString(h, "\x00phone:"+strconv.FormatInt(phoneID, 10))
	}
	return setKeyPrefix + hex.EncodeToString(h.Sum(nil))
}

// entries returns the phone's entries in directory order.
func (d *directory) entries(ctx context.Context, phoneID int64) ([]*entry, error) {
	contacts, err := d.contacts(ctx, phoneID)
	if err != nil {
		return nil, err
	}
	return buildEntries(d.base, contacts), nil
}

func (d *directory) contacts(ctx context.Context, phoneID int64) ([]provbook.Contact, error) {
	lastKey := lastKeyPrefix + strconv.FormatInt(phoneID, 10)
	v, err := d.b.LDAPVersion(ctx, phoneID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		// PostgreSQL is down: the last rendered set, if there is one.
		if d.cache != nil {
			if body, ok := d.cache.Get(ctx, lastKey); ok {
				if cs, ok := decodeContacts(body); ok {
					d.log.Warn("ldap: serving the last rendered directory", "error", err)
					return cs, nil
				}
			}
		}
		return nil, ErrUnavailable
	}
	key := setKey(v, phoneID)
	if d.cache != nil {
		if body, ok := d.cache.Get(ctx, key); ok {
			if cs, ok := decodeContacts(body); ok {
				return cs, nil
			}
		}
	}
	books, err := d.b.LDAPBooks(ctx, phoneID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, err
		}
		return nil, ErrUnavailable
	}
	cs := provbook.Contacts(books)
	if d.cache != nil {
		if body, err := json.Marshal(cs); err == nil {
			ttl := setTTL
			if v.Personal {
				ttl = personalTTL
			}
			d.cache.Set(ctx, key, body, ttl)
			d.cache.Set(ctx, lastKey, body, lastTTL)
		}
	}
	return cs, nil
}

func decodeContacts(b []byte) ([]provbook.Contact, bool) {
	var cs []provbook.Contact
	if err := json.Unmarshal(b, &cs); err != nil {
		return nil, false
	}
	return cs, true
}
