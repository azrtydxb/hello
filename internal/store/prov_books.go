package store

// The address books a phone is delivered (spec phonebooks-ptt, P-2): what
// the provisioning handler renders as the phone's directory and what the
// rendered configuration points the phone at.

import (
	"context"
	"database/sql"
	"errors"

	"github.com/azrtydxb/hello/internal/prov"
	"github.com/azrtydxb/hello/internal/prov/phonebook"
)

type bookRef struct {
	id   int64
	name string
}

// phoneBookRefs lists the books of a phone's delivered set in order: the
// extensions book and the company book when its group (or no group) turns
// them on, then the group's named books by position. The personal book of
// the phone's linked user arrives with the personal contacts phase.
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
		SELECT b.id, b.name FROM phonebooks b
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
		if err := rows.Scan(&r.id, &r.name); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// DeliveredBooks implements prov.Store.
func (s *Store) DeliveredBooks(ctx context.Context, phoneID int64) ([]phonebook.Book, error) {
	refs, err := phoneBookRefs(ctx, s.db, phoneID)
	if err != nil {
		return nil, err
	}
	out := make([]phonebook.Book, 0, len(refs))
	for _, r := range refs {
		_, entries, err := s.ExportPhonebook(ctx, r.id)
		if errors.Is(err, ErrNotFound) {
			continue // deleted since the listing
		}
		if err != nil {
			return nil, err
		}
		out = append(out, phonebook.Book{Name: r.name, Entries: entries})
	}
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
