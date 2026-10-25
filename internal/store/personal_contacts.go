package store

// Personal contacts (spec phonebooks-ptt, P-1, P-5, P-9, P-19): one book
// per user, edited only by that user and delivered only to the phones of
// the extension the user is linked to. Every method here takes the owner's
// user id from the caller's own session and nothing here takes a book id, so
// no route can name another user's book. Audit rows carry the user and a
// count, never a contact.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"

	"github.com/azrtydxb/hello/internal/phonebook"
)

// ErrNoExtensionLink is returned for a user an administrator has not linked
// to an extension: such a user has no personal book (spec P-3).
var ErrNoExtensionLink = errors.New("store: user is not linked to an extension")

// personalBookName is what a user's book is called in the vendors' source
// lists; it is the same for every user.
const personalBookName = "Personal"

// requireLink checks, inside tx and holding a share lock on the user so a
// concurrent unlink waits, that userID is linked to an extension.
func requireLink(ctx context.Context, tx *sql.Tx, userID int64) error {
	var ext sql.NullInt64
	err := tx.QueryRowContext(ctx, `SELECT extension_id FROM users WHERE id = $1 FOR SHARE`, userID).Scan(&ext)
	if err != nil {
		return mapErr(err)
	}
	if !ext.Valid {
		return ErrNoExtensionLink
	}
	return nil
}

// personalBook returns the id of the user's book, locked, creating it when
// create is set; 0 when there is none and create is not set.
func personalBook(ctx context.Context, tx *sql.Tx, userID int64, create bool) (int64, error) {
	if create {
		if _, err := tx.ExecContext(ctx, `INSERT INTO phonebooks (kind, name, owner_user_id) VALUES ('personal', $2, $1)
			ON CONFLICT (owner_user_id) WHERE kind = 'personal' DO NOTHING`, userID, personalBookName); err != nil {
			return 0, err
		}
	}
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM phonebooks WHERE kind = 'personal' AND owner_user_id = $1 FOR UPDATE`, userID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// personalRoom refuses a write that would leave the book with more than
// phonebook.MaxPersonalEntries entries or the lookup index with more than
// phonebook.MaxIndexEntries (spec P-17).
func personalRoom(ctx context.Context, tx *sql.Tx, bookID int64, add int) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM phonebook_entries WHERE book_id = $1`, bookID).Scan(&n); err != nil {
		return err
	}
	if n+add > phonebook.MaxPersonalEntries {
		return conflict("a personal book holds at most %d contacts", phonebook.MaxPersonalEntries)
	}
	return indexRoom(ctx, tx, add)
}

// ListContacts returns up to limit of the user's contacts with an id above
// after, by id, and the id to continue after (0 on the last page).
func (s *Store) ListContacts(ctx context.Context, userID, after int64, limit int) ([]phonebook.Entry, int64, error) {
	out := []phonebook.Entry{}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := requireLink(ctx, tx, userID); err != nil {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+entryCols+` FROM phonebook_entries
			WHERE book_id = (SELECT id FROM phonebooks WHERE kind = 'personal' AND owner_user_id = $1) AND id > $2
			ORDER BY id LIMIT $3`, userID, after, limit+1)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				return err
			}
			out = append(out, e)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, 0, err
	}
	out, next := page(out, limit, func(e phonebook.Entry) int64 { return e.ID })
	return out, next, nil
}

// ExportContacts returns every contact of the user in delivery order.
func (s *Store) ExportContacts(ctx context.Context, userID int64) ([]phonebook.EntryInput, error) {
	out := []phonebook.EntryInput{}
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := requireLink(ctx, tx, userID); err != nil {
			return err
		}
		id, err := personalBook(ctx, tx, userID, false)
		if err != nil || id == 0 {
			return err
		}
		rows, err := tx.QueryContext(ctx, `SELECT `+entryCols+` FROM phonebook_entries
			WHERE book_id = $1 ORDER BY lower(last_name), lower(first_name), id`, id)
		if err != nil {
			return err
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			e, err := scanEntry(rows)
			if err != nil {
				return err
			}
			out = append(out, e.EntryInput)
		}
		return rows.Err()
	})
	return out, err
}

// CreateContact adds a validated contact to the user's book, creating the
// book with the first one.
func (s *Store) CreateContact(ctx context.Context, actor string, userID int64, in phonebook.EntryInput) (phonebook.Entry, error) {
	var out phonebook.Entry
	err := s.tx(ctx, func(tx *sql.Tx) error {
		if err := requireLink(ctx, tx, userID); err != nil {
			return err
		}
		book, err := personalBook(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		if err := personalRoom(ctx, tx, book, 1); err != nil {
			return err
		}
		if out, err = insertEntry(ctx, tx, book, in); err != nil {
			return err
		}
		if err := touchBook(ctx, tx, book); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "personal_contact", strconv.FormatInt(userID, 10))
	})
	return out, err
}

// UpdateContact replaces a contact of the user's book. A contact of
// another book, or one that does not exist, is ErrNotFound.
func (s *Store) UpdateContact(ctx context.Context, actor string, userID, id int64, in phonebook.EntryInput) (phonebook.Entry, error) {
	var out phonebook.Entry
	numbers, err := marshalNumbers(in.Numbers)
	if err != nil {
		return out, err
	}
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := requireLink(ctx, tx, userID); err != nil {
			return err
		}
		book, err := personalBook(ctx, tx, userID, false)
		if err != nil {
			return err
		}
		if book == 0 {
			return ErrNotFound
		}
		out, err = scanEntry(tx.QueryRowContext(ctx, `
			UPDATE phonebook_entries SET first_name = $3, last_name = $4, company = $5, numbers = $6, note = $7, updated_at = now()
			WHERE id = $1 AND book_id = $2 RETURNING `+entryCols,
			id, book, in.FirstName, in.LastName, in.Company, string(numbers), in.Note))
		if err != nil {
			return mapErr(err)
		}
		if err := touchBook(ctx, tx, book); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "personal_contact", strconv.FormatInt(userID, 10))
	})
	return out, err
}

// DeleteContact removes a contact of the user's book.
func (s *Store) DeleteContact(ctx context.Context, actor string, userID, id int64) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		if err := requireLink(ctx, tx, userID); err != nil {
			return err
		}
		book, err := personalBook(ctx, tx, userID, false)
		if err != nil {
			return err
		}
		if book == 0 {
			return ErrNotFound
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM phonebook_entries WHERE id = $1 AND book_id = $2`, id, book)
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if err := touchBook(ctx, tx, book); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "personal_contact", strconv.FormatInt(userID, 10))
	})
}

// ImportContacts adds validated contacts to the user's book in one
// transaction, after deleting its contacts first when replace is set. The
// result must fit the personal book's and the index's limits. It returns
// how many contacts the book held before.
func (s *Store) ImportContacts(ctx context.Context, actor string, userID int64, in []phonebook.EntryInput, replace bool) (before int, err error) {
	err = s.tx(ctx, func(tx *sql.Tx) error {
		if err := requireLink(ctx, tx, userID); err != nil {
			return err
		}
		book, err := personalBook(ctx, tx, userID, true)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM phonebook_entries WHERE book_id = $1`, book).Scan(&before); err != nil {
			return err
		}
		if replace {
			if _, err := tx.ExecContext(ctx, `DELETE FROM phonebook_entries WHERE book_id = $1`, book); err != nil {
				return err
			}
		}
		// Counted after the delete, so a replacement is measured by what it
		// leaves, not by what it removes.
		if err := personalRoom(ctx, tx, book, len(in)); err != nil {
			return err
		}
		for _, e := range in {
			if _, err := insertEntry(ctx, tx, book, e); err != nil {
				return err
			}
		}
		if err := touchBook(ctx, tx, book); err != nil {
			return err
		}
		mode := "append"
		if replace {
			mode = "replace"
		}
		return insertAudit(ctx, tx, actor, fmt.Sprintf("import:%s:%d", mode, len(in)), "personal_contact", strconv.FormatInt(userID, 10))
	})
	return before, err
}
