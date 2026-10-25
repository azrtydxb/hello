package store

// The caller-name lookup diagnostic (spec phonebooks-ptt, P-8, P-10): which
// book and entry would name a number. It reads the same data, in the same
// order, as the lookup index of the caller-name phase, minus the personal
// book, which no administrator route reads.

import (
	"context"
	"database/sql"
	"strings"

	"github.com/azrtydxb/hello/internal/phonebook"
)

// lookupSource is one book the diagnostic searches.
type lookupSource struct {
	id   int64
	kind phonebook.Kind
	name string
}

// LookupPhonebook says which entry of the extensions, named and company
// books would name number as a caller (spec P-10). callee is the extension
// called, whose phones' groups add their named books; empty searches the
// extensions and company books only. An unknown callee is a field error.
func (s *Store) LookupPhonebook(ctx context.Context, d phonebook.Dialing, number, callee string) (phonebook.LookupResult, error) {
	res := phonebook.LookupResult{Number: number, Callee: callee, Result: phonebook.LookupUnresolvable, Searched: []string{}}
	isExt := func(digits string) bool {
		var ok bool
		return s.db.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM extensions WHERE number = $1)`, digits).Scan(&ok) == nil && ok
	}
	if callee != "" && !isExt(callee) {
		return res, fieldError("callee", "no such extension")
	}
	key, ok := d.Key(number, isExt)
	if !ok {
		return res, nil
	}
	res.Key, res.Result = key, phonebook.LookupNone
	sources, err := s.lookupSources(ctx, callee)
	if err != nil {
		return res, err
	}
	for _, src := range sources {
		res.Searched = append(res.Searched, searchedName(src))
	}

	// An exact match on the key, in book order, wins.
	for _, src := range sources {
		in, id, found, err := s.exactMatch(ctx, src, key)
		if err != nil {
			return res, err
		}
		if found {
			return matched(res, src, id, in, "exact"), nil
		}
	}

	// Otherwise the last 9 digits, when exactly one entry has them.
	suffix, ok := phonebook.Suffix(key)
	if !ok {
		return res, nil
	}
	type hit struct {
		src lookupSource
		id  int64
		in  phonebook.EntryInput
	}
	var hits []hit
	for _, src := range sources {
		if src.kind == phonebook.KindExtensions {
			continue // extension numbers are short; they match exactly or not at all
		}
		rows, err := s.db.QueryContext(ctx, `
			SELECT DISTINCT e.id, e.first_name, e.last_name, e.company
			FROM phonebook_entries e, jsonb_array_elements(e.numbers) n
			WHERE e.book_id = $1 AND length(ltrim(n->>'key', '+')) >= 9 AND right(ltrim(n->>'key', '+'), 9) = $2
			ORDER BY e.id LIMIT 2`, src.id, suffix)
		if err != nil {
			return res, err
		}
		for rows.Next() {
			var h hit
			h.src = src
			if err := rows.Scan(&h.id, &h.in.FirstName, &h.in.LastName, &h.in.Company); err != nil {
				_ = rows.Close()
				return res, err
			}
			hits = append(hits, h)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return res, err
		}
		_ = rows.Close()
	}
	switch len(hits) {
	case 0:
		return res, nil
	case 1:
		return matched(res, hits[0].src, hits[0].id, hits[0].in, "suffix"), nil
	}
	res.Result, res.Matches = phonebook.LookupAmbiguous, len(hits)
	return res, nil
}

func searchedName(src lookupSource) string {
	if src.kind == phonebook.KindNamed {
		return src.name
	}
	return string(src.kind)
}

func matched(res phonebook.LookupResult, src lookupSource, id int64, in phonebook.EntryInput, via string) phonebook.LookupResult {
	res.Result, res.Via = phonebook.LookupMatch, via
	res.Source, res.Book, res.BookID = string(src.kind), src.name, src.id
	res.EntryID, res.Name = id, in.DisplayName()
	return res
}

// lookupSources lists the books in lookup order: extensions, the named
// books of the groups of the callee's phones (each group's books in its own
// order, a book once), then company. The callee's personal book is not
// searched.
func (s *Store) lookupSources(ctx context.Context, callee string) ([]lookupSource, error) {
	var out []lookupSource
	add := func(kind phonebook.Kind) error {
		var src lookupSource
		err := s.db.QueryRowContext(ctx, `SELECT id, kind, name FROM phonebooks WHERE kind = $1`, string(kind)).Scan(&src.id, &src.kind, &src.name)
		if err != nil {
			return mapErr(err)
		}
		out = append(out, src)
		return nil
	}
	if err := add(phonebook.KindExtensions); err != nil {
		return nil, err
	}
	if callee != "" {
		rows, err := s.db.QueryContext(ctx, `
			SELECT DISTINCT p.group_id, gb.position, b.id, b.name
			FROM extensions e
			JOIN devices d ON d.extension_id = e.id
			JOIN phones p ON p.device_id = d.id
			JOIN phone_group_books gb ON gb.group_id = p.group_id
			JOIN phonebooks b ON b.id = gb.book_id AND b.kind = 'named'
			WHERE e.number = $1
			ORDER BY p.group_id, gb.position`, callee)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		seen := map[int64]bool{}
		for rows.Next() {
			var group int64
			var pos int
			src := lookupSource{kind: phonebook.KindNamed}
			if err := rows.Scan(&group, &pos, &src.id, &src.name); err != nil {
				return nil, err
			}
			if !seen[src.id] {
				seen[src.id] = true
				out = append(out, src)
			}
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	if err := add(phonebook.KindCompany); err != nil {
		return nil, err
	}
	return out, nil
}

// exactMatch finds the entry of a book with a number whose key is key; of
// several, the lowest id.
func (s *Store) exactMatch(ctx context.Context, src lookupSource, key string) (in phonebook.EntryInput, id int64, found bool, err error) {
	if src.kind == phonebook.KindExtensions {
		err = s.db.QueryRowContext(ctx, `SELECT id, name FROM extensions WHERE number = $1 AND NOT directory_hidden`, key).Scan(&id, &in.LastName)
	} else {
		err = s.db.QueryRowContext(ctx, `
			SELECT e.id, e.first_name, e.last_name, e.company
			FROM phonebook_entries e, jsonb_array_elements(e.numbers) n
			WHERE e.book_id = $1 AND n->>'key' = $2
			ORDER BY e.id LIMIT 1`, src.id, key).Scan(&id, &in.FirstName, &in.LastName, &in.Company)
	}
	switch {
	case err == sql.ErrNoRows:
		return in, 0, false, nil
	case err != nil:
		return in, 0, false, err
	}
	in.LastName = strings.TrimSpace(in.LastName)
	return in, id, true, nil
}
