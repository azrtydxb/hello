package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/migrations"
	"github.com/pressly/goose/v3"
)

func entry(last string, numbers ...string) phonebook.EntryInput {
	e := phonebook.EntryInput{FirstName: "T", LastName: last}
	labels := phonebook.Labels
	for i, n := range numbers {
		k, _ := phonebook.Key(n)
		e.Numbers = append(e.Numbers, phonebook.Number{Label: labels[i], Number: n, Key: k})
	}
	return e
}

func bookByKind(t *testing.T, s *Store, kind phonebook.Kind) phonebook.Book {
	t.Helper()
	bs, _, err := s.ListPhonebooks(context.Background(), 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bs {
		if b.Kind == kind {
			return b
		}
	}
	t.Fatalf("no %s book in %+v", kind, bs)
	return phonebook.Book{}
}

func objects(t *testing.T, s *Store) map[string]bool {
	t.Helper()
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT table_name FROM information_schema.tables WHERE table_schema = current_schema()
		UNION ALL
		SELECT table_name || '.' || column_name FROM information_schema.columns WHERE table_schema = current_schema()`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	out := map[string]bool{}
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out[n] = true
	}
	return out
}

// TestPhonebookMigration (spec P-1, P-21) fails if the company and
// extensions books are missing after migration, a second company or
// extensions book can be created, a personal book has no owner, deleting a
// book leaves its entries, the extensions book is stored, or Down leaves any
// new object (or Up does not run again after it).
func TestPhonebookMigration(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	company, ext := bookByKind(t, s, phonebook.KindCompany), bookByKind(t, s, phonebook.KindExtensions)
	if company.Name != "Company" || ext.Name != "Extensions" {
		t.Fatalf("seeded books %+v %+v", company, ext)
	}

	t.Run("books exist once", func(t *testing.T) {
		for _, kind := range []string{"company", "extensions"} {
			if _, err := s.db.ExecContext(ctx, `INSERT INTO phonebooks (kind, name) VALUES ($1, 'Again')`, kind); err == nil {
				t.Errorf("a second %s book was created", kind)
			}
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO phonebooks (kind, name) VALUES ('personal', 'Mine')`); err == nil {
			t.Error("a personal book without an owner was created")
		}
		uid, err := s.CreateUser(ctx, "test", "owner", "x", auth.RoleViewer)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.db.ExecContext(ctx, `INSERT INTO phonebooks (kind, name, owner_user_id) VALUES ('named', 'N', $1)`, uid); err == nil {
			t.Error("a named book with an owner was created")
		}
		for i := 0; i < 2; i++ {
			_, err := s.db.ExecContext(ctx, `INSERT INTO phonebooks (kind, name, owner_user_id) VALUES ('personal', 'Mine', $1)`, uid)
			if (err == nil) != (i == 0) {
				t.Errorf("personal book %d for one owner: %v", i+1, err)
			}
		}
		if _, err := s.db.ExecContext(ctx, `DELETE FROM users WHERE id = $1`, uid); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM phonebooks WHERE kind = 'personal'`).Scan(&n); err != nil || n != 0 {
			t.Errorf("deleting the owner left %d personal books (%v)", n, err)
		}
	})

	t.Run("deleting a book removes its entries", func(t *testing.T) {
		b, err := s.CreatePhonebook(ctx, "test", "Temp")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.CreatePhonebookEntry(ctx, "test", b.ID, entry("A", "1234")); err != nil {
			t.Fatal(err)
		}
		if err := s.DeletePhonebook(ctx, "test", b.ID); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM phonebook_entries WHERE book_id = $1`, b.ID).Scan(&n); err != nil || n != 0 {
			t.Fatalf("%d entries left (%v)", n, err)
		}
	})

	t.Run("the extensions book is not stored", func(t *testing.T) {
		if _, err := s.CreatePhonebookEntry(ctx, "test", ext.ID, entry("A", "1234")); !errors.Is(err, ErrInUse) {
			t.Errorf("create entry: %v, want a conflict", err)
		}
		if _, err := s.ImportPhonebook(ctx, "test", ext.ID, []phonebook.EntryInput{entry("A", "1234")}, false); !errors.Is(err, ErrInUse) {
			t.Errorf("import: %v, want a conflict", err)
		}
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM phonebook_entries WHERE book_id = $1`, ext.ID).Scan(&n); err != nil || n != 0 {
			t.Errorf("%d stored extension entries (%v)", n, err)
		}
	})

	t.Run("down removes everything and up runs again", func(t *testing.T) {
		db := scratchDB(t)
		p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.UpTo(ctx, 10); err != nil {
			t.Fatal(err)
		}
		before := objects(t, New(db))
		if _, err := p.UpTo(ctx, 11); err != nil {
			t.Fatal(err)
		}
		after := objects(t, New(db))
		for _, want := range []string{"phonebooks", "phonebook_entries", "phone_groups", "phone_group_books",
			"phones.group_id", "users.extension_id", "extensions.directory_hidden"} {
			if !after[want] || before[want] {
				t.Errorf("%s: before %v after %v", want, before[want], after[want])
			}
		}
		if _, err := p.DownTo(ctx, 10); err != nil {
			t.Fatal(err)
		}
		for n := range objects(t, New(db)) {
			if !before[n] {
				t.Errorf("down left %s", n)
			}
		}
		for n := range before {
			if !objects(t, New(db))[n] {
				t.Errorf("down dropped %s", n)
			}
		}
		if _, err := p.Up(ctx); err != nil {
			t.Fatalf("up after down: %v", err)
		}
	})
}

// TestPhonebookEntries fails if an entry change does not bump the book's
// version or write its audit row, an import is not all-or-nothing or does
// not honour append and replace, a limit is exceeded, an entry of another
// book can be changed through this one, or a list page repeats or loses an
// entry.
func TestPhonebookEntries(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	company := bookByKind(t, s, phonebook.KindCompany)
	named, err := s.CreatePhonebook(ctx, "test", "Suppliers")
	if err != nil {
		t.Fatal(err)
	}
	version := func(id int64) int64 {
		t.Helper()
		b, err := s.GetPhonebook(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return b.Version
	}
	count := func(id int64) int {
		t.Helper()
		b, err := s.GetPhonebook(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return b.EntryCount
	}

	v := version(company.ID)
	e1, err := s.CreatePhonebookEntry(ctx, "user:a", company.ID, entry("Zed", "+41 79 123 45 67", "044 111 22 33"))
	if err != nil {
		t.Fatal(err)
	}
	if got := version(company.ID); got != v+1 {
		t.Errorf("version after create = %d, want %d", got, v+1)
	}
	if e1.Numbers[0].Key != "+41791234567" || e1.Numbers[1].Label != "mobile" || e1.BookID != company.ID {
		t.Errorf("stored entry %+v", e1)
	}
	e2, err := s.CreatePhonebookEntry(ctx, "user:a", company.ID, entry("Alpha", "1234"))
	if err != nil {
		t.Fatal(err)
	}
	upd, err := s.UpdatePhonebookEntry(ctx, "user:a", company.ID, e2.ID, entry("Beta", "2345"))
	if err != nil || upd.LastName != "Beta" || upd.Numbers[0].Number != "2345" || !upd.UpdatedAt.After(e2.CreatedAt) {
		t.Fatalf("update: %+v, %v", upd, err)
	}
	if got := version(company.ID); got != v+3 {
		t.Errorf("version after create, create, update = %d, want %d", got, v+3)
	}

	// An entry is reachable only through its own book.
	if _, err := s.UpdatePhonebookEntry(ctx, "user:a", named.ID, e1.ID, entry("X", "1234")); !errors.Is(err, ErrNotFound) {
		t.Errorf("update through another book: %v, want ErrNotFound", err)
	}
	if err := s.DeletePhonebookEntry(ctx, "user:a", named.ID, e1.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete through another book: %v, want ErrNotFound", err)
	}
	if got := version(named.ID); got != 1 {
		t.Errorf("a failed change bumped the other book to version %d", got)
	}
	if _, err := s.UpdatePhonebookEntry(ctx, "user:a", company.ID, 1<<40, entry("X", "1234")); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown entry: %v, want ErrNotFound", err)
	}

	// Listing pages by id and loses nothing.
	for i := 0; i < 4; i++ {
		if _, err := s.CreatePhonebookEntry(ctx, "user:a", company.ID, entry(fmt.Sprintf("P%d", i), fmt.Sprintf("10%02d", i))); err != nil {
			t.Fatal(err)
		}
	}
	var got []int64
	for after, pages := int64(0), 0; ; pages++ {
		es, next, err := s.ListPhonebookEntries(ctx, company.ID, after, 4)
		if err != nil {
			t.Fatal(err)
		}
		if pages > 5 {
			t.Fatal("paging does not end")
		}
		for _, e := range es {
			got = append(got, e.ID)
		}
		if next == 0 {
			break
		}
		if len(es) != 4 {
			t.Fatalf("a page of %d with a next cursor", len(es))
		}
		after = next
	}
	if len(got) != 6 {
		t.Fatalf("paged entries = %v, want 6 distinct", got)
	}
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Fatalf("pages out of order: %v", got)
		}
	}

	// Export order is by last name, then first name.
	_, exported, err := s.ExportPhonebook(ctx, company.ID)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, e := range exported {
		names = append(names, e.LastName)
	}
	if want := "Beta P0 P1 P2 P3 Zed"; strings.Join(names, " ") != want {
		t.Errorf("export order %v, want %s", names, want)
	}

	if err := s.DeletePhonebookEntry(ctx, "user:a", company.ID, e1.ID); err != nil {
		t.Fatal(err)
	}
	if count(company.ID) != 5 {
		t.Errorf("entries after delete = %d", count(company.ID))
	}

	t.Run("import", func(t *testing.T) {
		// Append adds to what is there.
		before, err := s.ImportPhonebook(ctx, "user:a", named.ID, []phonebook.EntryInput{entry("I1", "1111"), entry("I2", "2222")}, false)
		if err != nil || before != 0 || count(named.ID) != 2 {
			t.Fatalf("append: before %d, count %d, %v", before, count(named.ID), err)
		}
		before, err = s.ImportPhonebook(ctx, "user:a", named.ID, []phonebook.EntryInput{entry("I3", "3333")}, false)
		if err != nil || before != 2 || count(named.ID) != 3 {
			t.Fatalf("second append: before %d, count %d, %v", before, count(named.ID), err)
		}
		// A failing row (past a database CHECK Normalize would have caught)
		// leaves the book exactly as it was, replace or not.
		bad := entry("Bad", "4444")
		bad.LastName = strings.Repeat("x", 61)
		v := version(named.ID)
		for _, replace := range []bool{false, true} {
			if _, err := s.ImportPhonebook(ctx, "user:a", named.ID, []phonebook.EntryInput{entry("Good", "5555"), bad}, replace); err == nil {
				t.Fatalf("import with a bad row (replace=%v) succeeded", replace)
			}
			if count(named.ID) != 3 || version(named.ID) != v {
				t.Fatalf("a failed import (replace=%v) changed the book: %d entries, version %d", replace, count(named.ID), version(named.ID))
			}
		}
		// Replace swaps the content.
		before, err = s.ImportPhonebook(ctx, "user:a", named.ID, []phonebook.EntryInput{entry("R1", "6666")}, true)
		if err != nil || before != 3 || count(named.ID) != 1 {
			t.Fatalf("replace: before %d, count %d, %v", before, count(named.ID), err)
		}
		es, _, _ := s.ListPhonebookEntries(ctx, named.ID, 0, 10)
		if len(es) != 1 || es[0].LastName != "R1" {
			t.Fatalf("after replace: %+v", es)
		}
	})

	t.Run("limits", func(t *testing.T) {
		full := make([]phonebook.EntryInput, phonebook.MaxEntriesPerBook)
		for i := range full {
			full[i] = entry(fmt.Sprintf("L%d", i), fmt.Sprintf("%d", 10000+i))
		}
		if _, err := s.ImportPhonebook(ctx, "user:a", named.ID, full, true); err != nil {
			t.Fatalf("replace with exactly the limit: %v", err)
		}
		if _, err := s.CreatePhonebookEntry(ctx, "user:a", named.ID, entry("One more", "1234")); !errors.Is(err, ErrInUse) {
			t.Errorf("entry %d: %v, want a conflict", phonebook.MaxEntriesPerBook+1, err)
		}
		if _, err := s.ImportPhonebook(ctx, "user:a", named.ID, full[:1], false); !errors.Is(err, ErrInUse) {
			t.Errorf("append past the limit: %v, want a conflict", err)
		}
		if count(named.ID) != phonebook.MaxEntriesPerBook {
			t.Errorf("a refused write changed the book to %d entries", count(named.ID))
		}
		// Replacing a full book with a full file passes: the limit is on
		// what is left.
		if _, err := s.ImportPhonebook(ctx, "user:a", named.ID, full, true); err != nil {
			t.Errorf("replace a full book with a full file: %v", err)
		}
	})

	t.Run("named book limit and names", func(t *testing.T) {
		if _, err := s.CreatePhonebook(ctx, "test", "suppliers"); !errors.Is(err, ErrInUse) {
			t.Errorf("a name differing in case: %v, want a conflict", err)
		}
		for i := 0; i < phonebook.MaxNamedBooks-1; i++ {
			if _, err := s.CreatePhonebook(ctx, "test", fmt.Sprintf("Book %d", i)); err != nil {
				t.Fatalf("book %d: %v", i, err)
			}
		}
		if _, err := s.CreatePhonebook(ctx, "test", "One too many"); !errors.Is(err, ErrInUse) {
			t.Errorf("book %d: %v, want a conflict", phonebook.MaxNamedBooks+1, err)
		}
	})

	t.Run("audit", func(t *testing.T) {
		var n int
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE actor = 'user:a' AND resource = 'phonebook_entry'`).Scan(&n); err != nil || n != 8 {
			t.Errorf("%d entry audit rows (%v), want 8", n, err)
		}
		if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE resource = 'phonebook' AND action LIKE 'import:%'`).Scan(&n); err != nil || n < 4 {
			t.Errorf("%d import audit rows (%v)", n, err)
		}
	})
}

// TestPhonebookBooks fails if the company or extensions book can be
// deleted, the extensions book renamed, a personal book is visible through
// any admin read or write, or a hidden extension is listed in the
// extensions book.
func TestPhonebookBooks(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	company, ext := bookByKind(t, s, phonebook.KindCompany), bookByKind(t, s, phonebook.KindExtensions)
	for _, b := range []phonebook.Book{company, ext} {
		if err := s.DeletePhonebook(ctx, "test", b.ID); !errors.Is(err, ErrInUse) {
			t.Errorf("delete the %s book: %v, want a conflict", b.Kind, err)
		}
	}
	if _, err := s.RenamePhonebook(ctx, "test", ext.ID, "X"); !errors.Is(err, ErrInUse) {
		t.Errorf("rename the extensions book: %v, want a conflict", err)
	}
	if b, err := s.RenamePhonebook(ctx, "test", company.ID, "Corporate"); err != nil || b.Name != "Corporate" {
		t.Errorf("rename the company book: %+v, %v", b, err)
	}
	if err := s.DeletePhonebook(ctx, "test", 1<<40); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete an unknown book: %v", err)
	}

	// The extensions book lists extensions and follows their visibility.
	e1, err := s.CreateExtension(ctx, "test", "101", "Sales", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateExtension(ctx, "test", "102", "Support", "", nil); err != nil {
		t.Fatal(err)
	}
	es, _, err := s.ListPhonebookEntries(ctx, ext.ID, 0, 10)
	if err != nil || len(es) != 2 || es[0].LastName != "Sales" || es[0].Numbers[0].Number != "101" || es[0].ID != e1.ID {
		t.Fatalf("extensions book: %+v, %v", es, err)
	}
	hide := true
	if _, err := s.UpdateExtension(ctx, "test", e1.ID, ExtensionChange{DirectoryHidden: &hide}, nil); err != nil {
		t.Fatal(err)
	}
	es, _, _ = s.ListPhonebookEntries(ctx, ext.ID, 0, 10)
	_, exported, _ := s.ExportPhonebook(ctx, ext.ID)
	b, _ := s.GetPhonebook(ctx, ext.ID)
	if len(es) != 1 || es[0].LastName != "Support" || len(exported) != 1 || b.EntryCount != 1 {
		t.Fatalf("after hiding 101: entries %+v, export %+v, count %d", es, exported, b.EntryCount)
	}

	// Personal books are invisible and untouchable here.
	uid, err := s.CreateUser(ctx, "test", "owner", "x", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	var pid int64
	if err := s.db.QueryRowContext(ctx, `INSERT INTO phonebooks (kind, name, owner_user_id) VALUES ('personal', 'Mine', $1) RETURNING id`, uid).Scan(&pid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `INSERT INTO phonebook_entries (book_id, last_name, numbers) VALUES ($1, 'Secret', '[{"label":"work","number":"1234","key":"1234"}]')`, pid); err != nil {
		t.Fatal(err)
	}
	bs, _, _ := s.ListPhonebooks(ctx, 0, 100)
	for _, b := range bs {
		if b.Kind == phonebook.KindPersonal || b.ID == pid {
			t.Errorf("a personal book is listed: %+v", b)
		}
	}
	if _, err := s.GetPhonebook(ctx, pid); !errors.Is(err, ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, _, err := s.ListPhonebookEntries(ctx, pid, 0, 10); !errors.Is(err, ErrNotFound) {
		t.Errorf("list entries: %v", err)
	}
	if _, _, err := s.ExportPhonebook(ctx, pid); !errors.Is(err, ErrNotFound) {
		t.Errorf("export: %v", err)
	}
	if _, err := s.CreatePhonebookEntry(ctx, "test", pid, entry("A", "1234")); !errors.Is(err, ErrNotFound) {
		t.Errorf("create entry: %v", err)
	}
	if _, err := s.ImportPhonebook(ctx, "test", pid, []phonebook.EntryInput{entry("A", "1234")}, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("import: %v", err)
	}
	if _, err := s.RenamePhonebook(ctx, "test", pid, "X"); !errors.Is(err, ErrNotFound) {
		t.Errorf("rename: %v", err)
	}
	if err := s.DeletePhonebook(ctx, "test", pid); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete: %v", err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM phonebook_entries WHERE book_id = $1`, pid).Scan(&n); err != nil || n != 1 {
		t.Errorf("the personal book's entries changed: %d (%v)", n, err)
	}
}

// TestPhoneGroups fails if a group name can repeat (in any case), books are
// not kept in the order given, a book that is unknown, not named or listed
// twice is accepted, more than five books are accepted, deleting a book
// leaves it in a group, or deleting a group leaves its phones pointing at
// it.
func TestPhoneGroups(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	g, err := s.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Warehouse", IncludeExtensions: true, IncludeCompany: true})
	if err != nil {
		t.Fatal(err)
	}
	if !g.IncludeExtensions || !g.IncludeCompany || len(g.BookIDs) != 0 || g.PhoneCount != 0 {
		t.Fatalf("new group %+v", g)
	}
	if _, err := s.CreatePhoneGroup(ctx, "test", GroupInput{Name: "warehouse"}); !errors.Is(err, ErrInUse) {
		t.Errorf("name differing in case: %v, want a conflict", err)
	}
	other, err := s.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Office", Description: "HQ"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.UpdatePhoneGroup(ctx, "test", other.ID, GroupInput{Name: "WAREHOUSE"}); !errors.Is(err, ErrInUse) {
		t.Errorf("rename onto another group: %v, want a conflict", err)
	}
	u, err := s.UpdatePhoneGroup(ctx, "test", other.ID, GroupInput{Name: "Office 2", Description: "x", IncludeCompany: true})
	if err != nil || u.Name != "Office 2" || u.IncludeExtensions || !u.IncludeCompany {
		t.Fatalf("update: %+v, %v", u, err)
	}
	if _, err := s.UpdatePhoneGroup(ctx, "test", 1<<40, GroupInput{Name: "Nope"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unknown: %v", err)
	}

	var books []phonebook.Book
	for i := 0; i < 6; i++ {
		b, err := s.CreatePhonebook(ctx, "test", fmt.Sprintf("B%d", i))
		if err != nil {
			t.Fatal(err)
		}
		books = append(books, b)
	}
	ids := func(bs ...phonebook.Book) []int64 {
		out := []int64{}
		for _, b := range bs {
			out = append(out, b.ID)
		}
		return out
	}
	got, err := s.SetPhoneGroupBooks(ctx, "test", g.ID, ids(books[2], books[0], books[1]))
	if err != nil || fmt.Sprint(got.BookIDs) != fmt.Sprint(ids(books[2], books[0], books[1])) {
		t.Fatalf("set books: %+v, %v", got, err)
	}
	got, err = s.SetPhoneGroupBooks(ctx, "test", g.ID, ids(books[1], books[2]))
	if err != nil || fmt.Sprint(got.BookIDs) != fmt.Sprint(ids(books[1], books[2])) {
		t.Fatalf("reorder: %+v, %v", got, err)
	}
	company := bookByKind(t, s, phonebook.KindCompany)
	for name, in := range map[string][]int64{
		"unknown":        {1 << 40},
		"company":        {company.ID},
		"twice":          {books[0].ID, books[0].ID},
		"six":            ids(books...),
		"second unknown": {books[0].ID, 1 << 40},
	} {
		_, err := s.SetPhoneGroupBooks(ctx, "test", g.ID, in)
		var ve *ValidationError
		if !errors.As(err, &ve) {
			t.Errorf("%s: %v, want a validation error", name, err)
		}
		if cur, _ := s.GetPhoneGroup(ctx, g.ID); fmt.Sprint(cur.BookIDs) != fmt.Sprint(ids(books[1], books[2])) {
			t.Errorf("%s: a refused set changed the books to %v", name, cur.BookIDs)
		}
	}
	if _, err := s.SetPhoneGroupBooks(ctx, "test", 1<<40, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown group: %v", err)
	}
	if err := s.DeletePhonebook(ctx, "test", books[1].ID); err != nil {
		t.Fatal(err)
	}
	if cur, _ := s.GetPhoneGroup(ctx, g.ID); fmt.Sprint(cur.BookIDs) != fmt.Sprint(ids(books[2])) {
		t.Errorf("after deleting a book the group has %v", cur.BookIDs)
	}
	if got, err := s.SetPhoneGroupBooks(ctx, "test", g.ID, nil); err != nil || len(got.BookIDs) != 0 {
		t.Errorf("clear: %+v, %v", got, err)
	}

	gs, next, err := s.ListPhoneGroups(ctx, 0, 1)
	if err != nil || len(gs) != 1 || next != gs[0].ID {
		t.Fatalf("first page: %+v next %d, %v", gs, next, err)
	}
	gs, next, err = s.ListPhoneGroups(ctx, next, 1)
	if err != nil || len(gs) != 1 || next != 0 || gs[0].ID != other.ID {
		t.Fatalf("second page: %+v next %d, %v", gs, next, err)
	}

	if err := s.DeletePhoneGroup(ctx, "test", g.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetPhoneGroup(ctx, g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("deleted group: %v", err)
	}
	if err := s.DeletePhoneGroup(ctx, "test", g.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("delete again: %v", err)
	}
}

// TestUserExtensionLinkStore (spec P-3) fails if an extension can be linked
// to two users, a link to a missing extension is accepted, unlinking does
// not clear it, the list omits it, or a link is not audited.
func TestUserExtensionLinkStore(t *testing.T) {
	ctx := context.Background()
	s := scratchStore(t)
	admin, err := s.CreateUser(ctx, "test", "admin", "x", auth.RoleAdmin)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := s.CreateUser(ctx, "test", "bob", "x", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	e1, err := s.CreateExtension(ctx, "test", "101", "Sales", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := s.CreateExtension(ctx, "test", "102", "Support", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := s.SetUserExtension(ctx, "user:admin", bob, &e1.ID)
	if err != nil || u.ExtensionID == nil || *u.ExtensionID != e1.ID {
		t.Fatalf("link: %+v, %v", u, err)
	}
	if _, err := s.SetUserExtension(ctx, "user:admin", admin, &e1.ID); !errors.Is(err, ErrInUse) || !strings.Contains(err.Error(), "bob") {
		t.Fatalf("second user on one extension: %v, want a conflict naming bob", err)
	}
	// Moving a user to another extension frees the first.
	if _, err := s.SetUserExtension(ctx, "user:admin", bob, &e2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetUserExtension(ctx, "user:admin", admin, &e1.ID); err != nil {
		t.Fatalf("link the freed extension: %v", err)
	}
	// Linking to the extension a user already has is no conflict.
	if _, err := s.SetUserExtension(ctx, "user:admin", admin, &e1.ID); err != nil {
		t.Fatalf("relink the same extension: %v", err)
	}
	missing := int64(1 << 40)
	if _, err := s.SetUserExtension(ctx, "user:admin", bob, &missing); err == nil {
		t.Fatal("a link to a missing extension was accepted")
	}
	if _, err := s.SetUserExtension(ctx, "user:admin", 1<<40, &e1.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}
	us, err := s.ListUsers(ctx)
	if err != nil {
		t.Fatal(err)
	}
	links := map[string]int64{}
	for _, u := range us {
		if u.ExtensionID != nil {
			links[u.Username] = *u.ExtensionID
		}
	}
	if len(links) != 2 || links["admin"] != e1.ID || links["bob"] != e2.ID {
		t.Fatalf("listed links %v", links)
	}
	if u, err := s.SetUserExtension(ctx, "user:admin", bob, nil); err != nil || u.ExtensionID != nil {
		t.Fatalf("unlink: %+v, %v", u, err)
	}
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE extension_id IS NOT NULL`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d users linked after unlinking bob (%v)", n, err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM audit_events WHERE resource = 'user' AND action IN ('set-extension', 'unset-extension')`).Scan(&n); err != nil || n != 5 {
		t.Fatalf("%d link audit rows (%v), want 5", n, err)
	}
	// Deleting the extension unlinks its user.
	if err := s.DeleteExtension(ctx, "test", e2.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteExtension(ctx, "test", e1.ID, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM users WHERE extension_id IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("%d users still linked to deleted extensions (%v)", n, err)
	}
}
