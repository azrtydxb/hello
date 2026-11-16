package store

// Address books and phone groups (spec phonebooks-ptt, phase A). Book and
// group changes write their audit row in the same transaction; none bumps
// the configuration revision (hello-sip reads no book until the caller-name
// phase). Personal books are never reachable here: every query names a kind
// other than personal, so an administrator cannot read or change one.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/azrtydxb/momo"

	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// bookCols selects a book with its entry count; the extensions book counts
// the extensions it would list.
const bookCols = `b.id, b.kind, b.name, b.version,
	CASE WHEN b.kind = 'extensions' THEN (SELECT count(*) FROM extensions WHERE NOT directory_hidden)
	     ELSE (SELECT count(*) FROM phonebook_entries e WHERE e.book_id = b.id) END,
	b.created_at, b.updated_at`

func scanBook(r interface{ Scan(...any) error }) (phonebook.Book, error) {
	var b phonebook.Book
	err := r.Scan(&b.ID, &b.Kind, &b.Name, &b.Version, &b.EntryCount, &b.CreatedAt, &b.UpdatedAt)
	return b, err
}

// ListPhonebooks returns up to limit books with an id above after, by id
// (the company and extensions books first), and the id to continue after,
// 0 on the last page. Personal books are not listed.
func (s *Store) ListPhonebooks(ctx context.Context, after int64, limit int) ([]phonebook.Book, int64, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+bookCols+` FROM phonebooks b
		WHERE b.kind <> 'personal' AND b.id > @after ORDER BY b.id LIMIT @limit`, momo.Named("after", after), momo.Named("limit", limit+1))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []phonebook.Book{}
	for rows.Next() {
		b, err := scanBook(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	out, next := page(out, limit, func(b phonebook.Book) int64 { return b.ID })
	return out, next, nil
}

// page cuts the limit+1 rows a list query read to limit and names the
// cursor of the next page.
func page[T any](rows []T, limit int, id func(T) int64) ([]T, int64) {
	if len(rows) <= limit {
		return rows, 0
	}
	return rows[:limit], id(rows[limit-1])
}

// GetPhonebook returns one book; a personal book is ErrNotFound.
func (s *Store) GetPhonebook(ctx context.Context, id int64) (phonebook.Book, error) {
	b, err := scanBook(s.q(ctx).QueryRowContext(ctx, `SELECT `+bookCols+` FROM phonebooks b WHERE b.id = @id AND b.kind <> 'personal'`, momo.Named("id", id)))
	return b, mapErr(err)
}

// CreatePhonebook creates a named book. Past phonebook.MaxNamedBooks, or
// with a name another named book has, it is a conflict.
func (s *Store) CreatePhonebook(ctx context.Context, actor, name string) (phonebook.Book, error) {
	var out phonebook.Book
	err := s.tx(ctx, func(tx *dbTx) error {
		// Serialise creations, so two cannot both pass the limit.
		if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(@key)`, momo.Named("key", int64(bookLockKey))); err != nil {
			return err
		}
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM phonebooks WHERE kind = 'named'`).Scan(&n); err != nil {
			return err
		}
		if n >= phonebook.MaxNamedBooks {
			return conflict("at most %d named books", phonebook.MaxNamedBooks)
		}
		var id int64
		if err := tx.QueryRowContext(ctx, `INSERT INTO phonebooks (kind, name) VALUES ('named', @name) RETURNING id`, momo.Named("name", name)).Scan(&id); err != nil {
			return namedBookError(err, name)
		}
		var err error
		if out, err = scanBook(tx.QueryRowContext(ctx, `SELECT `+bookCols+` FROM phonebooks b WHERE b.id = @id`, momo.Named("id", id))); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "phonebook", strconv.FormatInt(id, 10))
	})
	return out, err
}

// bookLockKey serialises changes of the set of named books.
const bookLockKey = 0x68656c6c6f626b73 // "hellobks"

// namedBookError names the clash behind a unique violation of a book name.
func namedBookError(err error, name string) error {
	if errors.Is(mapErr(err), ErrConflict) {
		return conflict("a book named %q already exists", name)
	}
	return err
}

// lockBook locks a non-personal book and returns its kind.
func lockBook(ctx context.Context, tx *dbTx, id int64) (phonebook.Kind, error) {
	var k phonebook.Kind
	err := tx.QueryRowContext(ctx, `SELECT kind FROM phonebooks WHERE id = @id AND kind <> 'personal' FOR UPDATE`, momo.Named("id", id)).Scan(&k)
	return k, mapErr(err)
}

// errExtensionsBook rejects a change to the computed extensions book.
func errExtensionsBook() error {
	return conflict("the extensions book is computed from the extensions and cannot be changed; hide an extension from it on the extension")
}

// RenamePhonebook renames a company or named book; the extensions book
// cannot be changed.
func (s *Store) RenamePhonebook(ctx context.Context, actor string, id int64, name string) (phonebook.Book, error) {
	var out phonebook.Book
	err := s.tx(ctx, func(tx *dbTx) error {
		kind, err := lockBook(ctx, tx, id)
		if err != nil {
			return err
		}
		if kind == phonebook.KindExtensions {
			return errExtensionsBook()
		}
		if _, err := tx.ExecContext(ctx, `UPDATE phonebooks SET name = @name, version = version + 1, updated_at = now() WHERE id = @id`, momo.Named("id", id), momo.Named("name", name)); err != nil {
			return namedBookError(err, name)
		}
		if out, err = scanBook(tx.QueryRowContext(ctx, `SELECT `+bookCols+` FROM phonebooks b WHERE b.id = @id`, momo.Named("id", id))); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "phonebook", strconv.FormatInt(id, 10))
	})
	return out, err
}

// DeletePhonebook deletes a named book with its entries and its place in
// phone groups. The company and extensions books cannot be deleted.
func (s *Store) DeletePhonebook(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *dbTx) error {
		kind, err := lockBook(ctx, tx, id)
		if err != nil {
			return err
		}
		if kind != phonebook.KindNamed {
			return conflict("the %s book exists once and cannot be deleted", kind)
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM phonebooks WHERE id = @id`, momo.Named("id", id)); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "phonebook", strconv.FormatInt(id, 10))
	})
}

// storedNumber is a number as the numbers column holds it.
type storedNumber struct {
	Label  string `json:"label"`
	Number string `json:"number"`
	Key    string `json:"key"`
}

func marshalNumbers(ns []phonebook.Number) ([]byte, error) {
	out := make([]storedNumber, len(ns))
	for i, n := range ns {
		out[i] = storedNumber{n.Label, n.Number, n.Key}
	}
	return json.Marshal(out)
}

const entryCols = `id, book_id, first_name, last_name, company, numbers, note, created_at, updated_at`

func scanEntry(r interface{ Scan(...any) error }) (phonebook.Entry, error) {
	var (
		e       phonebook.Entry
		numbers []byte
	)
	if err := r.Scan(&e.ID, &e.BookID, &e.FirstName, &e.LastName, &e.Company, &numbers, &e.Note, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return e, err
	}
	var ns []storedNumber
	if err := json.Unmarshal(numbers, &ns); err != nil {
		return e, fmt.Errorf("phonebook entry %d numbers: %w", e.ID, err)
	}
	e.Numbers = make([]phonebook.Number, len(ns))
	for i, n := range ns {
		e.Numbers[i] = phonebook.Number{Label: n.Label, Number: n.Number, Key: n.Key}
	}
	return e, nil
}

// extensionEntry is how the extensions book lists an extension: its name
// and number. The id is the extension's.
func extensionEntry(bookID, id int64, number, name string, created, updated time.Time) phonebook.Entry {
	return phonebook.Entry{ID: id, BookID: bookID, EntryInput: phonebook.EntryInput{
		LastName: name, Numbers: []phonebook.Number{{Label: phonebook.LabelWork, Number: number, Key: number}}},
		CreatedAt: created, UpdatedAt: updated}
}

// ListPhonebookEntries returns up to limit entries of a book with an id
// above after, by id, and the id to continue after (0 on the last page).
// The extensions book lists the extensions not hidden from it.
func (s *Store) ListPhonebookEntries(ctx context.Context, bookID, after int64, limit int) ([]phonebook.Entry, int64, error) {
	b, err := s.GetPhonebook(ctx, bookID)
	if err != nil {
		return nil, 0, err
	}
	var rows *dbRows
	if b.Kind == phonebook.KindExtensions {
		rows, err = s.q(ctx).QueryContext(ctx, `SELECT id, number, name, created_at, updated_at FROM extensions
			WHERE NOT directory_hidden AND id > @after ORDER BY id LIMIT @limit`, momo.Named("after", after), momo.Named("limit", limit+1))
	} else {
		rows, err = s.q(ctx).QueryContext(ctx, `SELECT `+entryCols+` FROM phonebook_entries
			WHERE book_id = @book_id AND id > @after ORDER BY id LIMIT @limit`, momo.Named("book_id", bookID), momo.Named("after", after), momo.Named("limit", limit+1))
	}
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []phonebook.Entry{}
	for rows.Next() {
		var e phonebook.Entry
		if b.Kind == phonebook.KindExtensions {
			var id int64
			var number, name string
			var created, updated time.Time
			if err := rows.Scan(&id, &number, &name, &created, &updated); err != nil {
				return nil, 0, err
			}
			e = extensionEntry(bookID, id, number, name, created, updated)
		} else if e, err = scanEntry(rows); err != nil {
			return nil, 0, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	out, next := page(out, limit, func(e phonebook.Entry) int64 { return e.ID })
	return out, next, nil
}

// ExportPhonebook returns every entry of a book in the order a directory is
// delivered: by last name, first name and id.
func (s *Store) ExportPhonebook(ctx context.Context, bookID int64) (phonebook.Book, []phonebook.EntryInput, error) {
	b, err := s.GetPhonebook(ctx, bookID)
	if err != nil {
		return b, nil, err
	}
	out, err := s.bookEntries(ctx, bookID, b.Kind)
	return b, out, err
}

// bookEntries reads every entry of a book of any kind, in delivery order.
// It is the only reader a personal book has besides its owner's: callers
// are the provisioning handler, through a phone's delivered set, and the
// owner's own routes.
func (s *Store) bookEntries(ctx context.Context, bookID int64, kind phonebook.Kind) ([]phonebook.EntryInput, error) {
	var (
		rows *dbRows
		err  error
	)
	if kind == phonebook.KindExtensions {
		rows, err = s.q(ctx).QueryContext(ctx, `SELECT id, number, name, created_at, updated_at FROM extensions
			WHERE NOT directory_hidden ORDER BY lower(name), number`)
	} else {
		rows, err = s.q(ctx).QueryContext(ctx, `SELECT `+entryCols+` FROM phonebook_entries
			WHERE book_id = @book_id ORDER BY lower(last_name), lower(first_name), id`, momo.Named("book_id", bookID))
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := []phonebook.EntryInput{}
	for rows.Next() {
		var e phonebook.Entry
		if kind == phonebook.KindExtensions {
			var id int64
			var number, name string
			var created, updated time.Time
			if err := rows.Scan(&id, &number, &name, &created, &updated); err != nil {
				return nil, err
			}
			e = extensionEntry(bookID, id, number, name, created, updated)
		} else if e, err = scanEntry(rows); err != nil {
			return nil, err
		}
		out = append(out, e.EntryInput)
	}
	return out, rows.Err()
}

// indexRoom refuses a write that would take the lookup index past
// phonebook.MaxIndexEntries entries in total (spec P-17).
func indexRoom(ctx context.Context, tx *dbTx, add int) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM phonebook_entries) + (SELECT count(*) FROM extensions)`).Scan(&n); err != nil {
		return err
	}
	if n+add > phonebook.MaxIndexEntries {
		return conflict("the lookup index would hold more than %d entries", phonebook.MaxIndexEntries)
	}
	return nil
}

// writableBook locks a book for an entry change and refuses the computed
// extensions book.
func writableBook(ctx context.Context, tx *dbTx, id int64) error {
	kind, err := lockBook(ctx, tx, id)
	if err != nil {
		return err
	}
	if kind == phonebook.KindExtensions {
		return errExtensionsBook()
	}
	return nil
}

// touchBook bumps the book's version in the transaction that changed it.
func touchBook(ctx context.Context, tx *dbTx, id int64) error {
	_, err := tx.ExecContext(ctx, `UPDATE phonebooks SET version = version + 1, updated_at = now() WHERE id = @id`, momo.Named("id", id))
	return err
}

func insertEntry(ctx context.Context, tx *dbTx, bookID int64, in phonebook.EntryInput) (phonebook.Entry, error) {
	numbers, err := marshalNumbers(in.Numbers)
	if err != nil {
		return phonebook.Entry{}, err
	}
	return scanEntry(tx.QueryRowContext(ctx, `
		INSERT INTO phonebook_entries (book_id, first_name, last_name, company, numbers, note)
		VALUES (@book_id, @first_name, @last_name, @company, @numbers, @note) RETURNING `+entryCols,
		momo.Named("book_id", bookID), momo.Named("first_name", in.FirstName), momo.Named("last_name", in.LastName), momo.Named("company", in.Company), momo.Named("numbers", string(numbers)), momo.Named("note", in.Note)))
}

func bookRoom(ctx context.Context, tx *dbTx, bookID int64, add int) error {
	var n int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM phonebook_entries WHERE book_id = @book_id`, momo.Named("book_id", bookID)).Scan(&n); err != nil {
		return err
	}
	if n+add > phonebook.MaxEntriesPerBook {
		return conflict("a book holds at most %d entries", phonebook.MaxEntriesPerBook)
	}
	return indexRoom(ctx, tx, add)
}

// CreatePhonebookEntry adds a validated entry to a company or named book.
func (s *Store) CreatePhonebookEntry(ctx context.Context, actor string, bookID int64, in phonebook.EntryInput) (phonebook.Entry, error) {
	var out phonebook.Entry
	err := s.tx(ctx, func(tx *dbTx) error {
		if err := writableBook(ctx, tx, bookID); err != nil {
			return err
		}
		if err := bookRoom(ctx, tx, bookID, 1); err != nil {
			return err
		}
		var err error
		if out, err = insertEntry(ctx, tx, bookID, in); err != nil {
			return err
		}
		if err := touchBook(ctx, tx, bookID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "phonebook_entry", strconv.FormatInt(out.ID, 10))
	})
	return out, err
}

// UpdatePhonebookEntry replaces an entry of a book. An entry another book
// has is ErrNotFound.
func (s *Store) UpdatePhonebookEntry(ctx context.Context, actor string, bookID, id int64, in phonebook.EntryInput) (phonebook.Entry, error) {
	var out phonebook.Entry
	numbers, err := marshalNumbers(in.Numbers)
	if err != nil {
		return out, err
	}
	err = s.tx(ctx, func(tx *dbTx) error {
		if err := writableBook(ctx, tx, bookID); err != nil {
			return err
		}
		var err error
		out, err = scanEntry(tx.QueryRowContext(ctx, `
			UPDATE phonebook_entries SET first_name = @first_name, last_name = @last_name, company = @company, numbers = @numbers, note = @note, updated_at = now()
			WHERE id = @id AND book_id = @book_id RETURNING `+entryCols,
			momo.Named("id", id), momo.Named("book_id", bookID), momo.Named("first_name", in.FirstName), momo.Named("last_name", in.LastName), momo.Named("company", in.Company), momo.Named("numbers", string(numbers)), momo.Named("note", in.Note)))
		if err != nil {
			return err
		}
		if err := touchBook(ctx, tx, bookID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "phonebook_entry", strconv.FormatInt(id, 10))
	})
	return out, err
}

// DeletePhonebookEntry removes an entry of a book.
func (s *Store) DeletePhonebookEntry(ctx context.Context, actor string, bookID, id int64) error {
	return s.tx(ctx, func(tx *dbTx) error {
		if err := writableBook(ctx, tx, bookID); err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM phonebook_entries WHERE id = @id AND book_id = @book_id`, momo.Named("id", id), momo.Named("book_id", bookID))
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if err := touchBook(ctx, tx, bookID); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "phonebook_entry", strconv.FormatInt(id, 10))
	})
}

// ImportPhonebook adds validated entries to a company or named book in one
// transaction, after deleting its entries first when replace is set. The
// result must fit the book's and the index's limits. It returns how many
// entries the book held before.
func (s *Store) ImportPhonebook(ctx context.Context, actor string, bookID int64, in []phonebook.EntryInput, replace bool) (before int, err error) {
	err = s.tx(ctx, func(tx *dbTx) error {
		if err := writableBook(ctx, tx, bookID); err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM phonebook_entries WHERE book_id = @book_id`, momo.Named("book_id", bookID)).Scan(&before); err != nil {
			return err
		}
		if replace {
			if _, err := tx.ExecContext(ctx, `DELETE FROM phonebook_entries WHERE book_id = @book_id`, momo.Named("book_id", bookID)); err != nil {
				return err
			}
		}
		// Counted after the delete, so a replacement is measured by what it
		// leaves, not by what it removes.
		if err := bookRoom(ctx, tx, bookID, len(in)); err != nil {
			return err
		}
		for _, e := range in {
			if _, err := insertEntry(ctx, tx, bookID, e); err != nil {
				return err
			}
		}
		if err := touchBook(ctx, tx, bookID); err != nil {
			return err
		}
		mode := "append"
		if replace {
			mode = "replace"
		}
		return insertAudit(ctx, tx, actor, fmt.Sprintf("import:%s:%d", mode, len(in)), "phonebook", strconv.FormatInt(bookID, 10))
	})
	return before, err
}

// GroupInput is what a phone group is created or replaced with.
type GroupInput struct {
	Name              string
	Description       string
	IncludeExtensions bool
	IncludeCompany    bool
}

const phoneGroupCols = `g.id, g.name, g.description, g.include_extensions, g.include_company,
	COALESCE((SELECT jsonb_agg(gb.book_id ORDER BY gb.position) FROM phone_group_books gb WHERE gb.group_id = g.id), '[]'::jsonb),
	(SELECT count(*) FROM phones p WHERE p.group_id = g.id), g.created_at, g.updated_at,
	g.ptt_enabled, g.ptt_address, g.ptt_codec, g.ptt_default_channel, g.ptt_accept_busy, g.ptt_channel_id,
	g.ptt_priority_channel, g.ptt_emergency_channel, g.group_paging_enabled, g.group_paging_default,
	g.group_paging_priority, g.group_paging_emergency, g.paging_allow_in_dnd, g.paging_barge,
	g.paging_priority_active, g.paging_timeout, g.intercom_barge, g.account_sharing, g.syslog_enabled, g.syslog_level, g.ldap_enabled, g.side_key`

func scanPhoneGroup(r interface{ Scan(...any) error }) (phonebook.Group, error) {
	var (
		g   phonebook.Group
		ids []byte
		ptt sql.NullInt64
	)
	if err := r.Scan(&g.ID, &g.Name, &g.Description, &g.IncludeExtensions, &g.IncludeCompany, &ids, &g.PhoneCount, &g.CreatedAt, &g.UpdatedAt,
		&g.PTT.Enabled, &g.PTT.Address, &g.PTT.Codec, &g.PTT.DefaultChannel, &g.PTT.AcceptBusy, &ptt,
		&g.PTT.PriorityChannel, &g.PTT.EmergencyChannel, &g.PTT.GroupPaging, &g.PTT.DefaultGroup,
		&g.PTT.PriorityGroup, &g.PTT.EmergencyGroup, &g.PagingOptions.AllowInDND, &g.PagingOptions.Barge,
		&g.PagingOptions.PriorityActive, &g.PagingOptions.Timeout, &g.PagingOptions.IntercomBarge, &g.AccountSharing,
		&g.Syslog.Enabled, &g.Syslog.Level, &g.LDAPEnabled, &g.SideKey); err != nil {
		return g, err
	}
	g.PTT.ChannelID = nullInt(ptt)
	g.Multicast, g.PTT.Channels = []phonebook.MulticastChannel{}, []phonebook.PTTChannel{}
	if err := json.Unmarshal(ids, &g.BookIDs); err != nil {
		return g, fmt.Errorf("phone group %d books: %w", g.ID, err)
	}
	return g, nil
}

// ListPhoneGroups returns up to limit groups with an id above after, by id,
// and the id to continue after (0 on the last page).
func (s *Store) ListPhoneGroups(ctx context.Context, after int64, limit int) ([]phonebook.Group, int64, error) {
	rows, err := s.q(ctx).QueryContext(ctx, `SELECT `+phoneGroupCols+` FROM phone_groups g WHERE g.id > @after ORDER BY g.id LIMIT @limit`, momo.Named("after", after), momo.Named("limit", limit+1))
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = rows.Close() }()
	out := []phonebook.Group{}
	for rows.Next() {
		g, err := scanPhoneGroup(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, g)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	out, next := page(out, limit, func(g phonebook.Group) int64 { return g.ID })
	if err := loadGroupChannels(ctx, s.q(ctx), out); err != nil {
		return nil, 0, err
	}
	return out, next, nil
}

// GetPhoneGroup returns one group.
func (s *Store) GetPhoneGroup(ctx context.Context, id int64) (phonebook.Group, error) {
	return getPhoneGroup(ctx, s.q(ctx), id)
}

func getPhoneGroup(ctx context.Context, q querier, id int64) (phonebook.Group, error) {
	g, err := scanPhoneGroup(q.QueryRowContext(ctx, `SELECT `+phoneGroupCols+` FROM phone_groups g WHERE g.id = @id`, momo.Named("id", id)))
	if err != nil {
		return g, mapErr(err)
	}
	gs := []phonebook.Group{g}
	if err := loadGroupChannels(ctx, q, gs); err != nil {
		return g, err
	}
	return gs[0], nil
}

// loadGroupChannels fills the multicast channels and PTT channels of gs,
// and their effective PTT address and codec: the group's catalog PTT
// channel, else the catalog's first enabled one, else the group's own
// (pre-catalog) address.
func loadGroupChannels(ctx context.Context, q querier, gs []phonebook.Group) error {
	if len(gs) == 0 {
		return nil
	}
	ids := make([]int64, len(gs))
	byID := map[int64]*phonebook.Group{}
	for i := range gs {
		ids[i] = gs[i].ID
		byID[gs[i].ID] = &gs[i]
	}
	ptts, err := catalogChannels(ctx, q, phonebook.KindPTT, true)
	if err != nil {
		return err
	}
	for i := range gs {
		if c, ok := resolvePTT(ptts, gs[i].PTT.ChannelID); ok {
			gs[i].PTT.Address, gs[i].PTT.Codec = c.HostPort(), c.Codec
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT l.group_id, c.id, c.label, c.address, c.port, c.codec, l.role, c.priority, c.enabled
		FROM phone_group_multicast l JOIN multicast_channels c ON c.id = l.channel_id
		WHERE l.group_id = ANY(@ids) ORDER BY l.group_id, l.position`, momo.Named("ids", ids))
	if err != nil {
		return err
	}
	for rows.Next() {
		var gid int64
		var c phonebook.MulticastChannel
		if err := rows.Scan(&gid, &c.ID, &c.Label, &c.Address, &c.Port, &c.Codec, &c.Role, &c.Priority, &c.Enabled); err != nil {
			_ = rows.Close()
			return err
		}
		byID[gid].Multicast = append(byID[gid].Multicast, c)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	prows, err := q.QueryContext(ctx, `SELECT group_id, channel, label, transmit, subscribe
		FROM ptt_channels WHERE group_id = ANY(@ids) ORDER BY group_id, channel`, momo.Named("ids", ids))
	if err != nil {
		return err
	}
	defer func() { _ = prows.Close() }()
	for prows.Next() {
		var gid int64
		var c phonebook.PTTChannel
		if err := prows.Scan(&gid, &c.Channel, &c.Label, &c.Transmit, &c.Subscribe); err != nil {
			return err
		}
		byID[gid].PTT.Channels = append(byID[gid].PTT.Channels, c)
	}
	return prows.Err()
}

// SetPhoneGroupAccountSharing switches WP8x6 account sharing on or off for a
// group (phone-feature-parity S-11). The group's WP8x6 phones are given
// P22521 = 1 (Enable Account Sharing) and P22522 by their role (0 host, 1
// guest), at their next fetch or the automatic resync; switched off, P22521 =
// 0.
func (s *Store) SetPhoneGroupAccountSharing(ctx context.Context, actor string, id int64, enabled bool) (phonebook.Group, error) {
	var out phonebook.Group
	err := s.tx(ctx, func(tx *dbTx) error {
		res, err := tx.ExecContext(ctx, `UPDATE phone_groups SET account_sharing = @account_sharing, updated_at = now() WHERE id = @id`, momo.Named("id", id), momo.Named("account_sharing", enabled))
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if enabled {
			if err := s.ensureSharingSecret(ctx, tx, id); err != nil {
				return err
			}
		}
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "set-account-sharing", "phone_group", strconv.FormatInt(id, 10))
	})
	return out, err
}

// RegeneratePhoneGroupSharingSecret replaces the group's account sharing
// password with a new generated one. The group's WP8x6 phones take it at
// their next fetch or resync; until then they no longer find the shared line
// of phones that already have it. The secret is never returned.
func (s *Store) RegeneratePhoneGroupSharingSecret(ctx context.Context, actor string, id int64) (phonebook.Group, error) {
	var out phonebook.Group
	if s.box == nil {
		return out, prov.ErrSealed
	}
	err := s.tx(ctx, func(tx *dbTx) error {
		sealed, err := s.box.Seal(newAdminPassword(), prov.SharingSecretAAD(id))
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `UPDATE phone_groups SET account_sharing_secret_enc = @secret, updated_at = now() WHERE id = @id`, momo.Named("id", id), momo.Named("secret", sealed))
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "regenerate-account-sharing-secret", "phone_group", strconv.FormatInt(id, 10))
	})
	return out, err
}

// SetPhoneGroupMulticast replaces the catalog paging channels a group
// uses, each with its role; none means every enabled paging channel. opts,
// when not nil, replaces how the group's phones take a page. It is a
// configuration change like the catalog's.
func (s *Store) SetPhoneGroupMulticast(ctx context.Context, actor string, id int64, in []phonebook.MulticastInput, opts *phonebook.PagingOptions) (phonebook.Group, error) {
	var out phonebook.Group
	err := s.configChange(ctx, actor, "set-multicast", "phone_group", nil, func(tx *dbTx) (int64, error) {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phone_groups WHERE id = @id FOR UPDATE`, momo.Named("id", id)).Scan(&one); err != nil {
			return id, err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM phone_group_multicast WHERE group_id = @id`, momo.Named("id", id)); err != nil {
			return id, err
		}
		for i, c := range in {
			if err := checkCatalogChannel(ctx, tx, "channels["+strconv.Itoa(i)+"].channelId", c.ChannelID, phonebook.KindPaging); err != nil {
				return id, err
			}
			if _, err := tx.ExecContext(ctx, `INSERT INTO phone_group_multicast (group_id, channel_id, position, role)
				VALUES (@group_id, @channel_id, @position, @role)`, momo.Named("group_id", id), momo.Named("channel_id", c.ChannelID), momo.Named("position", i), momo.Named("role", c.Role)); err != nil {
				return id, err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE phone_groups SET updated_at = now() WHERE id = @id`, momo.Named("id", id)); err != nil {
			return id, err
		}
		if opts != nil {
			if _, err := tx.ExecContext(ctx, `UPDATE phone_groups SET paging_allow_in_dnd = @allow_in_dnd, paging_barge = @barge,
				paging_priority_active = @priority_active, paging_timeout = @timeout, intercom_barge = @intercom_barge WHERE id = @id`,
				momo.Named("id", id), momo.Named("allow_in_dnd", opts.AllowInDND), momo.Named("barge", opts.Barge), momo.Named("priority_active", opts.PriorityActive), momo.Named("timeout", opts.Timeout), momo.Named("intercom_barge", opts.IntercomBarge)); err != nil {
				return id, err
			}
		}
		var err error
		out, err = getPhoneGroup(ctx, tx, id)
		return id, err
	})
	return out, err
}

// SetPhoneGroupSyslog replaces whether a group's phones send their log to
// the syslog sink, and at what level (phone-feature-parity S-27).
func (s *Store) SetPhoneGroupSyslog(ctx context.Context, actor string, id int64, in phonebook.Syslog) (phonebook.Group, error) {
	var out phonebook.Group
	err := s.tx(ctx, func(tx *dbTx) error {
		res, err := tx.ExecContext(ctx, `UPDATE phone_groups SET syslog_enabled = @enabled, syslog_level = @level, updated_at = now() WHERE id = @id`,
			momo.Named("id", id), momo.Named("enabled", in.Enabled), momo.Named("level", in.Level))
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "set-syslog", "phone_group", strconv.FormatInt(id, 10))
	})
	return out, mapErr(err)
}

// SetPhoneGroupPTT replaces a group's push-to-talk and group paging
// settings and channels. PTT on with no PTT channels defines the default
// channel, so the phones' PTT button always has a channel to talk on, and
// group paging on with no group channels defines the default group.
func (s *Store) SetPhoneGroupPTT(ctx context.Context, actor string, id int64, in phonebook.PTT) (phonebook.Group, error) {
	var out phonebook.Group
	// What is off is not checked: keep it in range for the table.
	def := phonebook.DefaultPTT()
	if in.DefaultChannel < 1 || in.DefaultChannel > phonebook.MaxPTTChannels {
		in.DefaultChannel = def.DefaultChannel
	}
	if in.PriorityChannel < 1 || in.PriorityChannel > phonebook.MaxPTTChannels {
		in.PriorityChannel = def.PriorityChannel
	}
	if in.EmergencyChannel < 1 || in.EmergencyChannel > phonebook.MaxPTTChannels {
		in.EmergencyChannel = def.EmergencyChannel
	}
	switch in.DefaultGroup {
	case 26, 27, 49, 50:
	default:
		in.DefaultGroup = def.DefaultGroup
	}
	if in.PriorityGroup < phonebook.MinGroupChannel || in.PriorityGroup > phonebook.MaxGroupChannel {
		in.PriorityGroup = def.PriorityGroup
	}
	if in.EmergencyGroup < phonebook.MinGroupChannel || in.EmergencyGroup > phonebook.MaxGroupChannel {
		in.EmergencyGroup = def.EmergencyGroup
	}
	err := s.tx(ctx, func(tx *dbTx) error {
		if in.ChannelID != nil {
			if err := checkPTTChannel(ctx, tx, "channelId", *in.ChannelID); err != nil {
				return err
			}
		}
		res, err := tx.ExecContext(ctx, `UPDATE phone_groups SET ptt_enabled = @enabled, ptt_channel_id = @channel_id,
			ptt_default_channel = @default_channel, ptt_accept_busy = @accept_busy, ptt_priority_channel = @priority_channel, ptt_emergency_channel = @emergency_channel,
			group_paging_enabled = @group_paging, group_paging_default = @default_group, group_paging_priority = @priority_group,
			group_paging_emergency = @emergency_group, updated_at = now() WHERE id = @id`,
			momo.Named("id", id), momo.Named("enabled", in.Enabled), momo.Named("channel_id", in.ChannelID), momo.Named("default_channel", in.DefaultChannel), momo.Named("accept_busy", in.AcceptBusy), momo.Named("priority_channel", in.PriorityChannel), momo.Named("emergency_channel", in.EmergencyChannel),
			momo.Named("group_paging", in.GroupPaging), momo.Named("default_group", in.DefaultGroup), momo.Named("priority_group", in.PriorityGroup), momo.Named("emergency_group", in.EmergencyGroup))
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM ptt_channels WHERE group_id = @id`, momo.Named("id", id)); err != nil {
			return err
		}
		chans := in.Channels
		hasPTT, hasGroup := false, false
		for _, c := range chans {
			if c.Channel <= phonebook.MaxPTTChannels {
				hasPTT = true
			} else {
				hasGroup = true
			}
		}
		if in.Enabled && !hasPTT {
			chans = append(chans, phonebook.PTTChannel{Channel: in.DefaultChannel, Label: "Channel " + strconv.Itoa(in.DefaultChannel), Transmit: true, Subscribe: true})
		}
		if in.GroupPaging && !hasGroup {
			chans = append(chans, phonebook.PTTChannel{Channel: in.DefaultGroup, Label: "Group " + strconv.Itoa(in.DefaultGroup), Transmit: true, Subscribe: true})
		}
		for _, c := range chans {
			if _, err := tx.ExecContext(ctx, `INSERT INTO ptt_channels (group_id, channel, label, transmit, subscribe)
				VALUES (@group_id, @channel, @label, @transmit, @subscribe)`, momo.Named("group_id", id), momo.Named("channel", c.Channel), momo.Named("label", c.Label), momo.Named("transmit", c.Transmit), momo.Named("subscribe", c.Subscribe)); err != nil {
				return err
			}
		}
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "set-ptt", "phone_group", strconv.FormatInt(id, 10))
	})
	return out, err
}

func phoneGroupNameError(err error, name string) error {
	if errors.Is(mapErr(err), ErrConflict) {
		return conflict("a phone group named %q already exists", name)
	}
	return err
}

// CreatePhoneGroup creates a group with no named books, with PTT on and
// channel 1 defined (the Grandstream default channel).
func (s *Store) CreatePhoneGroup(ctx context.Context, actor string, in GroupInput) (phonebook.Group, error) {
	var out phonebook.Group
	err := s.tx(ctx, func(tx *dbTx) error {
		var id int64
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO phone_groups (name, description, include_extensions, include_company)
			VALUES (@name, @description, @include_extensions, @include_company) RETURNING id`,
			momo.Named("name", in.Name), momo.Named("description", in.Description), momo.Named("include_extensions", in.IncludeExtensions), momo.Named("include_company", in.IncludeCompany)).Scan(&id); err != nil {
			return phoneGroupNameError(err, in.Name)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE phone_groups SET ptt_enabled = TRUE, ptt_default_channel = 1 WHERE id = @id`, momo.Named("id", id)); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO ptt_channels (group_id, channel, label, transmit, subscribe)
			VALUES (@id, 1, 'Channel 1', TRUE, TRUE)`, momo.Named("id", id)); err != nil {
			return err
		}
		if err := s.ensureSharingSecret(ctx, tx, id); err != nil {
			return err
		}
		var err error
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "create", "phone_group", strconv.FormatInt(id, 10))
	})
	return out, err
}

// UpdatePhoneGroup replaces a group's name, description and switches; its
// named books stay.
func (s *Store) UpdatePhoneGroup(ctx context.Context, actor string, id int64, in GroupInput) (phonebook.Group, error) {
	var out phonebook.Group
	err := s.tx(ctx, func(tx *dbTx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE phone_groups SET name = @name, description = @description, include_extensions = @include_extensions, include_company = @include_company, updated_at = now()
			WHERE id = @id`, momo.Named("id", id), momo.Named("name", in.Name), momo.Named("description", in.Description), momo.Named("include_extensions", in.IncludeExtensions), momo.Named("include_company", in.IncludeCompany))
		if err != nil {
			return phoneGroupNameError(err, in.Name)
		}
		if err := requireRow(res); err != nil {
			return err
		}
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "update", "phone_group", strconv.FormatInt(id, 10))
	})
	return out, err
}

// DeletePhoneGroup deletes a group; its phones fall back to no group.
func (s *Store) DeletePhoneGroup(ctx context.Context, actor string, id int64) error {
	return s.tx(ctx, func(tx *dbTx) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM phone_groups WHERE id = @id`, momo.Named("id", id))
		if err != nil {
			return err
		}
		if err := requireRow(res); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "delete", "phone_group", strconv.FormatInt(id, 10))
	})
}

// SetPhoneGroupBooks sets the ordered named books a group delivers. Each
// must be an existing named book, at most phonebook.MaxBooksPerGroup, none
// twice; a failure names the position.
func (s *Store) SetPhoneGroupBooks(ctx context.Context, actor string, id int64, bookIDs []int64) (phonebook.Group, error) {
	var out phonebook.Group
	err := s.tx(ctx, func(tx *dbTx) error {
		var one int
		if err := tx.QueryRowContext(ctx, `SELECT 1 FROM phone_groups WHERE id = @id FOR UPDATE`, momo.Named("id", id)).Scan(&one); err != nil {
			return err
		}
		if len(bookIDs) > phonebook.MaxBooksPerGroup {
			return fieldError("bookIds", fmt.Sprintf("at most %d books", phonebook.MaxBooksPerGroup))
		}
		for i, b := range bookIDs {
			if slices.Index(bookIDs, b) != i {
				return fieldError(fmt.Sprintf("bookIds[%d]", i), "a book can be assigned once")
			}
			var kind phonebook.Kind
			switch err := tx.QueryRowContext(ctx, `SELECT kind FROM phonebooks WHERE id = @id`, momo.Named("id", b)).Scan(&kind); {
			case errors.Is(err, sql.ErrNoRows):
				return fieldError(fmt.Sprintf("bookIds[%d]", i), "no such book")
			case err != nil:
				return err
			case kind != phonebook.KindNamed:
				return fieldError(fmt.Sprintf("bookIds[%d]", i), "only named books are assigned; the extensions and company books have switches on the group")
			}
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM phone_group_books WHERE group_id = @id`, momo.Named("id", id)); err != nil {
			return err
		}
		for i, b := range bookIDs {
			if _, err := tx.ExecContext(ctx, `INSERT INTO phone_group_books (group_id, book_id, position) VALUES (@group_id, @book_id, @position)`, momo.Named("group_id", id), momo.Named("book_id", b), momo.Named("position", i)); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `UPDATE phone_groups SET updated_at = now() WHERE id = @id`, momo.Named("id", id)); err != nil {
			return err
		}
		var err error
		if out, err = getPhoneGroup(ctx, tx, id); err != nil {
			return err
		}
		return insertAudit(ctx, tx, actor, "set-books", "phone_group", strconv.FormatInt(id, 10))
	})
	return out, err
}
