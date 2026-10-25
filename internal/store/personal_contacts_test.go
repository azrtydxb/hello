package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/phonebook"
	"github.com/azrtydxb/hello/internal/prov"
)

// TestPersonalContactsStore (spec P-1, P-5, P-9, P-17) fails if a user
// without a linked extension can read or write contacts, if one user can
// list, change or delete another's contact, if a personal book is reachable
// through the administrator's book routes, if it is not delivered last to the
// phones of the linked extension (and only to them), if unlinking does not
// stop its delivery, if a change does not change the delivered version, if
// the book can exceed 1,000 contacts, or if an audit row names a contact.
func TestPersonalContactsStore(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	bob, err := st.CreateUser(ctx, "test", "bob", "x", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	amy, err := st.CreateUser(ctx, "test", "amy", "x", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.CreateExtension(ctx, "test", "102", "Support", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	// No link, no personal book.
	if _, _, err := st.ListContacts(ctx, bob, 0, 10); !errors.Is(err, ErrNoExtensionLink) {
		t.Fatalf("list without a link: %v", err)
	}
	if _, err := st.CreateContact(ctx, "user:bob", bob, entry("Secret", "+3225550001")); !errors.Is(err, ErrNoExtensionLink) {
		t.Fatalf("create without a link: %v", err)
	}
	if _, err := st.ExportContacts(ctx, bob); !errors.Is(err, ErrNoExtensionLink) {
		t.Fatalf("export without a link: %v", err)
	}
	if _, err := st.ImportContacts(ctx, "user:bob", bob, []phonebook.EntryInput{entry("Secret", "+3225550001")}, false); !errors.Is(err, ErrNoExtensionLink) {
		t.Fatalf("import without a link: %v", err)
	}

	if _, err := st.SetUserExtension(ctx, "test", bob, &ext); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserExtension(ctx, "test", amy, &other.ID); err != nil {
		t.Fatal(err)
	}
	// Before the first contact the book does not exist; listing is empty.
	if es, next, err := st.ListContacts(ctx, bob, 0, 10); err != nil || len(es) != 0 || next != 0 {
		t.Fatalf("empty list: %v, %d, %v", es, next, err)
	}

	phone, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000019", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	amyPhone, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000020", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: other.ID, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	delivered := func(id int64) []string {
		bs, err := st.DeliveredBooks(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, b := range bs {
			out = append(out, b.Name)
		}
		return out
	}
	if got := fmt.Sprint(delivered(phone.Phone.ID)); got != "[Extensions Company]" {
		t.Fatalf("before any contact: %s", got)
	}
	v0, err := st.DeliveredVersion(ctx, phone.Phone.ID)
	if err != nil || v0.Personal {
		t.Fatalf("version before: %+v, %v", v0, err)
	}

	c, err := st.CreateContact(ctx, "user:bob", bob, entry("Hidden Friend", "+32 2 555 0001"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(delivered(phone.Phone.ID)); got != "[Extensions Company Personal]" {
		t.Fatalf("with a contact: %s (the personal book comes last)", got)
	}
	if got := fmt.Sprint(delivered(amyPhone.Phone.ID)); got != "[Extensions Company]" {
		t.Fatalf("another extension's phone got a personal book: %s", got)
	}
	bs, _ := st.DeliveredBooks(ctx, phone.Phone.ID)
	if n := len(bs[2].Entries); n != 1 || bs[2].Entries[0].LastName != "Hidden Friend" {
		t.Fatalf("personal entries: %+v", bs[2].Entries)
	}
	v1, err := st.DeliveredVersion(ctx, phone.Phone.ID)
	if err != nil || !v1.Personal || v1.Key == v0.Key {
		t.Fatalf("version after a create: %+v, %v (was %+v)", v1, err, v0)
	}
	if va, err := st.DeliveredVersion(ctx, amyPhone.Phone.ID); err != nil || va.Personal || va.Key == v1.Key {
		t.Fatalf("another phone's version: %+v, %v", va, err)
	}

	// A change bumps the version; the key never repeats.
	c2, err := st.UpdateContact(ctx, "user:bob", bob, c.ID, entry("Hidden Friend", "+3225550002"))
	if err != nil {
		t.Fatal(err)
	}
	v2, _ := st.DeliveredVersion(ctx, phone.Phone.ID)
	if v2.Key == v1.Key {
		t.Fatal("an update did not change the delivered version")
	}
	if c2.BookID != c.BookID {
		t.Fatal("update moved the contact to another book")
	}

	// Amy cannot see, change or delete bob's contact; her list is her own.
	if _, err := st.UpdateContact(ctx, "user:amy", amy, c.ID, entry("Mine Now", "+3225550003")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("amy updates bob's contact: %v", err)
	}
	if err := st.DeleteContact(ctx, "user:amy", amy, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("amy deletes bob's contact: %v", err)
	}
	if es, _, err := st.ListContacts(ctx, amy, 0, 10); err != nil || len(es) != 0 {
		t.Fatalf("amy's list: %v, %v", es, err)
	}
	if exp, err := st.ExportContacts(ctx, amy); err != nil || len(exp) != 0 {
		t.Fatalf("amy's export: %v, %v", exp, err)
	}

	// No administrator route reaches a personal book.
	if _, err := st.GetPhonebook(ctx, c.BookID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetPhonebook of a personal book: %v", err)
	}
	if _, _, err := st.ExportPhonebook(ctx, c.BookID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ExportPhonebook of a personal book: %v", err)
	}
	if _, _, err := st.ListPhonebookEntries(ctx, c.BookID, 0, 10); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ListPhonebookEntries of a personal book: %v", err)
	}
	if _, err := st.CreatePhonebookEntry(ctx, "user:admin", c.BookID, entry("Planted", "+3225550004")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an administrator wrote into a personal book: %v", err)
	}
	if err := st.DeletePhonebook(ctx, "user:admin", c.BookID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("an administrator deleted a personal book: %v", err)
	}
	books, _, err := st.ListPhonebooks(ctx, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range books {
		if b.Kind == phonebook.KindPersonal {
			t.Fatalf("a personal book is listed: %+v", b)
		}
	}
	// The diagnostic never names a number from a personal book.
	res, err := st.LookupPhonebook(ctx, phonebook.Dialing{}, "+3225550002", "101")
	if err != nil || res.Result != phonebook.LookupNone {
		t.Fatalf("lookup of a personal number: %+v, %v", res, err)
	}

	// Audit rows carry the user, never a contact.
	rows, err := db.QueryContext(ctx, `SELECT actor, action, resource, resource_id FROM audit_events WHERE resource = 'personal_contact'`)
	if err != nil {
		t.Fatal(err)
	}
	var audits int
	for rows.Next() {
		var actor, action, resource, id string
		if err := rows.Scan(&actor, &action, &resource, &id); err != nil {
			t.Fatal(err)
		}
		audits++
		if strings.Contains(actor+action+resource+id, "Hidden") || strings.Contains(actor+action+resource+id, "5550") {
			t.Errorf("audit row names a contact: %s %s %s %s", actor, action, resource, id)
		}
	}
	_ = rows.Close()
	if audits != 2 {
		t.Errorf("%d personal audit rows, want 2 (create, update)", audits)
	}

	// Unlinking stops delivery at the next fetch; relinking restores it.
	if _, err := st.SetUserExtension(ctx, "test", bob, nil); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(delivered(phone.Phone.ID)); got != "[Extensions Company]" {
		t.Fatalf("after unlinking: %s", got)
	}
	if v, _ := st.DeliveredVersion(ctx, phone.Phone.ID); v.Personal {
		t.Fatal("the version still names the personal book after unlinking")
	}
	if _, _, err := st.ListContacts(ctx, bob, 0, 10); !errors.Is(err, ErrNoExtensionLink) {
		t.Fatalf("list after unlinking: %v", err)
	}
	if _, err := st.SetUserExtension(ctx, "test", bob, &ext); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(delivered(phone.Phone.ID)); got != "[Extensions Company Personal]" {
		t.Fatalf("after relinking: %s", got)
	}

	// Delete.
	if err := st.DeleteContact(ctx, "user:bob", bob, c.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteContact(ctx, "user:bob", bob, c.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

// TestPersonalContactsLimit (spec P-17) fails if a personal book can hold
// more than 1,000 contacts through create or import, if a failed import
// leaves a partial book, or if replace is not measured by what it leaves.
func TestPersonalContactsLimit(t *testing.T) {
	st, _, ext := provStore(t)
	ctx := context.Background()
	bob, err := st.CreateUser(ctx, "test", "bob", "x", auth.RoleViewer)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserExtension(ctx, "test", bob, &ext); err != nil {
		t.Fatal(err)
	}
	gen := func(n int) []phonebook.EntryInput {
		out := make([]phonebook.EntryInput, n)
		for i := range out {
			out[i] = entry(fmt.Sprintf("C%04d", i), fmt.Sprintf("+3225%06d", i))
		}
		return out
	}
	if before, err := st.ImportContacts(ctx, "user:bob", bob, gen(phonebook.MaxPersonalEntries), false); err != nil || before != 0 {
		t.Fatalf("import to the limit: %d, %v", before, err)
	}
	if _, err := st.CreateContact(ctx, "user:bob", bob, entry("One Too Many", "+3225999999")); err == nil || !errors.Is(err, ErrInUse) {
		t.Fatalf("create past the limit: %v, want a conflict", err)
	}
	if _, err := st.ImportContacts(ctx, "user:bob", bob, gen(1), false); !errors.Is(err, ErrInUse) {
		t.Fatalf("append past the limit: %v, want a conflict", err)
	}
	exp, err := st.ExportContacts(ctx, bob)
	if err != nil || len(exp) != phonebook.MaxPersonalEntries {
		t.Fatalf("a refused write changed the book: %d, %v", len(exp), err)
	}
	// Replace is measured by what it leaves.
	if before, err := st.ImportContacts(ctx, "user:bob", bob, gen(3), true); err != nil || before != phonebook.MaxPersonalEntries {
		t.Fatalf("replace: %d, %v", before, err)
	}
	if exp, _ = st.ExportContacts(ctx, bob); len(exp) != 3 {
		t.Fatalf("after replace: %d contacts", len(exp))
	}
	if _, err := st.ImportContacts(ctx, "user:bob", bob, gen(phonebook.MaxPersonalEntries+1), true); !errors.Is(err, ErrInUse) {
		t.Fatalf("replace past the limit: %v", err)
	}
	if exp, _ = st.ExportContacts(ctx, bob); len(exp) != 3 {
		t.Fatalf("a refused replace changed the book: %d contacts", len(exp))
	}
}

// TestLookupPhonebookStore (spec P-8, P-10) fails if the diagnostic does not
// name by the order extensions, the callee's group books, company; if an
// exact key does not beat a suffix; if an ambiguous suffix resolves; if a
// short code, feature code or anonymous resolves; if a hidden extension names
// a caller; or if an unknown callee is accepted.
func TestLookupPhonebookStore(t *testing.T) {
	st, db, ext := provStore(t)
	ctx := context.Background()
	d := phonebook.Dialing{CountryCode: "41"}
	phone, err := st.CreatePhone(ctx, "test", PhoneInput{MAC: "805ec0000029", Vendor: prov.Yealink, Model: "T54W",
		ExtensionID: ext, Enabled: true, Realm: "hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	company := bookByKind(t, st, phonebook.KindCompany)
	wh, err := st.CreatePhonebook(ctx, "test", "Warehouse")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePhonebookEntry(ctx, "test", company.ID, entry("Corp", "+41791234567")); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreatePhonebookEntry(ctx, "test", wh.ID, entry("Dock", "+41791234567", "+41442223344")); err != nil {
		t.Fatal(err)
	}
	g, err := st.CreatePhoneGroup(ctx, "test", GroupInput{Name: "Site", IncludeExtensions: true, IncludeCompany: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetPhoneGroupBooks(ctx, "test", g.ID, []int64{wh.ID}); err != nil {
		t.Fatal(err)
	}
	grp := &g.ID
	if _, _, err := st.UpdatePhone(ctx, "test", phone.Phone.ID, PhoneChange{GroupID: &grp}); err != nil {
		t.Fatal(err)
	}

	lookup := func(number, callee string) phonebook.LookupResult {
		t.Helper()
		r, err := st.LookupPhonebook(ctx, d, number, callee)
		if err != nil {
			t.Fatalf("%q: %v", number, err)
		}
		return r
	}
	// The callee's group book beats company for the same number; without a
	// callee the group's book is not searched.
	if r := lookup("079 123 45 67", "101"); r.Result != phonebook.LookupMatch || r.Source != "named" || r.Book != "Warehouse" ||
		r.Name != "T Dock" || r.Via != "exact" || r.Key != "+41791234567" {
		t.Fatalf("callee 101: %+v", r)
	}
	if r := lookup("0041791234567", ""); r.Result != phonebook.LookupMatch || r.Source != "company" || r.Name != "T Corp" {
		t.Fatalf("no callee: %+v", r)
	}
	if r := lookup("sip:+41791234567@host;user=phone", "101"); r.Source != "named" {
		t.Fatalf("sip uri: %+v", r)
	}
	if r := lookup("0041791234567", "101"); fmt.Sprint(r.Searched) != "[extensions Warehouse company]" {
		t.Fatalf("searched: %v", r.Searched)
	}
	// An extension is named by the extensions book first.
	if r := lookup("101", ""); r.Result != phonebook.LookupMatch || r.Source != "extensions" || r.Name != "Sales" {
		t.Fatalf("extension: %+v", r)
	}
	if _, err := db.ExecContext(ctx, `UPDATE extensions SET directory_hidden = TRUE WHERE number = '101'`); err != nil {
		t.Fatal(err)
	}
	if r := lookup("101", ""); r.Result != phonebook.LookupNone {
		t.Fatalf("hidden extension: %+v", r)
	}
	// Last-9 fallback: one entry resolves, two do not.
	if r := lookup("+49791234567", ""); r.Result != phonebook.LookupMatch || r.Via != "suffix" {
		t.Fatalf("suffix with one entry: %+v", r)
	}
	if _, err := st.CreatePhonebookEntry(ctx, "test", company.ID, entry("Other", "+33791234567")); err != nil {
		t.Fatal(err)
	}
	if r := lookup("+49791234567", ""); r.Result != phonebook.LookupAmbiguous || r.Name != "" || r.Matches != 2 {
		t.Fatalf("ambiguous suffix: %+v", r)
	}
	// Never resolvable.
	for _, n := range []string{"anonymous", "*72599", "12", "not a number", ""} {
		if r := lookup(n, ""); r.Result != phonebook.LookupUnresolvable || r.Key != "" {
			t.Errorf("%q: %+v", n, r)
		}
	}
	if r := lookup("+41445556677", ""); r.Result != phonebook.LookupNone {
		t.Fatalf("unknown number: %+v", r)
	}
	if _, err := st.LookupPhonebook(ctx, d, "+41791234567", "999"); err == nil {
		t.Fatal("an unknown callee was accepted")
	}
}
