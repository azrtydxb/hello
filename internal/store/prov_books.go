package store

// The address books a phone is delivered (spec phonebooks-ptt, P-2): what
// the provisioning handler renders as the phone's directory and what the
// rendered configuration points the phone at.

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
	provbook "github.com/azrtydxb/hello/internal/prov/phonebook"
)

type bookRef struct {
	id      int64
	name    string
	version int64
	kind    phonebook.Kind
}

// phoneBookRefs lists the books of a phone's delivered set in order: the
// extensions book and the company book when its group (or no group) turns
// them on, then the group's named books by position, then the personal
// book of the user linked to the phone's bound extension (spec P-2, P-5).
// A phone whose extension has no linked user, or whose user has added no
// contact yet, has no personal book.
func phoneBookRefs(ctx context.Context, q querier, phoneID int64) ([]bookRef, error) {
	var (
		group             sql.NullInt64
		inclExt, inclComp bool
	)
	err := q.QueryRowContext(ctx, `
		SELECT p.group_id, COALESCE(g.include_extensions, TRUE), COALESCE(g.include_company, TRUE)
		FROM phones p LEFT JOIN phone_groups g ON g.id = p.group_id WHERE p.id = $1`, phoneID).Scan(&group, &inclExt, &inclComp)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, prov.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `
		SELECT b.id, b.name, b.version, b.kind FROM phonebooks b
		LEFT JOIN phone_group_books gb ON gb.book_id = b.id AND gb.group_id = $3
		WHERE (b.kind = 'extensions' AND $1) OR (b.kind = 'company' AND $2) OR (b.kind = 'named' AND gb.group_id IS NOT NULL)
		ORDER BY CASE b.kind WHEN 'extensions' THEN 0 WHEN 'company' THEN 1 ELSE 2 END, gb.position, b.id`,
		inclExt, inclComp, group)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []bookRef
	for rows.Next() {
		var r bookRef
		if err := rows.Scan(&r.id, &r.name, &r.version, &r.kind); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// The personal book is reached through the phone's binding and the
	// user linked to its extension, never by an id, so unlinking takes
	// effect at the next fetch.
	var p bookRef
	err = q.QueryRowContext(ctx, `
		SELECT b.id, b.name, b.version, b.kind
		FROM phones ph
		JOIN devices d ON d.id = ph.device_id
		JOIN users u ON u.extension_id = d.extension_id
		JOIN phonebooks b ON b.owner_user_id = u.id AND b.kind = 'personal'
		WHERE ph.id = $1`, phoneID).Scan(&p.id, &p.name, &p.version, &p.kind)
	switch {
	case err == nil:
		out = append(out, p)
	case !errors.Is(err, sql.ErrNoRows):
		return nil, err
	}
	return out, nil
}

// DeliveredBooks implements prov.Store.
func (s *Store) DeliveredBooks(ctx context.Context, phoneID int64) ([]provbook.Book, error) {
	refs, err := phoneBookRefs(ctx, s.db, phoneID)
	if err != nil {
		return nil, err
	}
	out := make([]provbook.Book, 0, len(refs))
	for _, r := range refs {
		entries, err := s.bookEntries(ctx, r.id, r.kind)
		if errors.Is(err, ErrNotFound) {
			continue // deleted since the listing
		}
		if err != nil {
			return nil, err
		}
		out = append(out, provbook.Book{Name: r.name, Entries: entries})
	}
	return out, nil
}

// DeliveredVersion implements prov.Store: a string that changes whenever
// the phone's delivered set or anything in it does (spec P-11). It names
// each book of the set with its version, which every entry and book change
// bumps in its own transaction, and the extensions book by a fingerprint
// (how many extensions, how many hidden, the latest change), which changes
// when an extension is added, removed, renamed or hidden. It is read before
// the books, so a render cached under it is never older than its key.
func (s *Store) DeliveredVersion(ctx context.Context, phoneID int64) (prov.BooksVersion, error) {
	refs, err := phoneBookRefs(ctx, s.db, phoneID)
	if err != nil {
		return prov.BooksVersion{}, err
	}
	var (
		b   strings.Builder
		out prov.BooksVersion
	)
	for _, r := range refs {
		b.WriteString(strconv.FormatInt(r.id, 10) + "." + strconv.FormatInt(r.version, 10))
		switch r.kind {
		case phonebook.KindExtensions:
			var count, hidden int
			var latest time.Time
			if err := s.db.QueryRowContext(ctx, `SELECT count(*), count(*) FILTER (WHERE directory_hidden),
				COALESCE(max(updated_at), 'epoch'::timestamptz) FROM extensions`).Scan(&count, &hidden, &latest); err != nil {
				return prov.BooksVersion{}, err
			}
			b.WriteString("x" + strconv.Itoa(count) + "." + strconv.Itoa(hidden) + "." + strconv.FormatInt(latest.UnixNano(), 10))
		case phonebook.KindPersonal:
			out.Personal = true
		}
		b.WriteByte(';')
	}
	out.Key = b.String()
	return out, nil
}

// bookInfo is RenderData.Book for a phone: nil when it has no books or
// provisioning has no public URL.
func (s *Store) bookInfo(ctx context.Context, phoneID int64, token string) (*prov.BookInfo, error) {
	refs, err := phoneBookRefs(ctx, s.db, phoneID)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(refs))
	for i, r := range refs {
		names[i] = r.name
	}
	return prov.NewBookInfo(s.prov.phoneBase(token), names, s.prov.BookRefresh), nil
}
