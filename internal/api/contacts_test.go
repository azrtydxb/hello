package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/oauth"
)

// meRoutes is every personal-contact route with a body that would succeed
// for a linked user.
func meRoutes(contact string) []struct{ method, path string } {
	return []struct{ method, path string }{
		{"GET", "/api/v1/me/contacts"}, {"POST", "/api/v1/me/contacts"},
		{"PUT", "/api/v1/me/contacts/" + contact}, {"DELETE", "/api/v1/me/contacts/" + contact},
		{"POST", "/api/v1/me/contacts/import"}, {"GET", "/api/v1/me/contacts/export"},
	}
}

// TestMeContactsScope (spec P-9, P-19) fails if a user can read, change,
// import or export another user's contacts through any route, if an
// administrator's session can read a personal book, if a service account
// reaches /me/contacts, if a user without a linked extension can add
// contacts, if a bad request is accepted, or if an audit row names a
// contact.
func TestMeContactsScope(t *testing.T) {
	e := newEnv(t, noLive{})
	alice := e.login()
	bob, bobID := e.asRole("bob", auth.RoleViewer)
	carol, carolID := e.asRole("carol", auth.RoleOperator)
	dave, _ := e.asRole("dave", auth.RoleViewer)

	e101 := alice.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	e102 := alice.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"}).json(t)
	for id, ext := range map[int64]any{bobID: e101["id"], carolID: e102["id"]} {
		alice.must(http.StatusOK, "PATCH", "/api/v1/users/"+strconv.FormatInt(id, 10), map[string]any{"extensionId": ext})
	}

	t.Run("no linked extension, no contacts", func(t *testing.T) {
		for _, c := range []*client{dave, alice} { // dave is a viewer, alice an admin: the role does not matter
			for _, rt := range meRoutes("1") {
				var body any
				if rt.method == "POST" || rt.method == "PUT" {
					body = entryBody("Nope", "5551234")
				}
				var r response
				if strings.HasSuffix(rt.path, "/import") {
					r = c.upload(rt.path, "last_name,work\nNope,5551234\n")
				} else {
					r = c.do(rt.method, rt.path, body)
				}
				if r.code != http.StatusForbidden || r.json(t)["error"].(map[string]any)["code"] != "no_extension" {
					t.Errorf("%s %s: %d %s, want 403 no_extension", rt.method, rt.path, r.code, r.body)
				}
			}
		}
	})

	t.Run("a user's own contacts", func(t *testing.T) {
		if items := listItems(t, bob.must(http.StatusOK, "GET", "/api/v1/me/contacts", nil)); len(items) != 0 {
			t.Fatalf("a new book is not empty: %v", items)
		}
		mine := bob.must(http.StatusCreated, "POST", "/api/v1/me/contacts", entryBody("Bobs Secret", "+3225550101")).json(t)
		id := rowID(mine)
		theirs := carol.must(http.StatusCreated, "POST", "/api/v1/me/contacts", entryBody("Carols Secret", "+3225550202")).json(t)

		if items := listItems(t, bob.must(http.StatusOK, "GET", "/api/v1/me/contacts", nil)); len(items) != 1 || items[0].(map[string]any)["lastName"] != "Bobs Secret" {
			t.Fatalf("bob's list: %v", items)
		}
		if got := string(carol.must(http.StatusOK, "GET", "/api/v1/me/contacts/export", nil).body); strings.Contains(got, "Bobs Secret") || !strings.Contains(got, "Carols Secret") {
			t.Errorf("carol's export: %s", got)
		}
		// Carol cannot change or delete bob's contact by id: it is not in her book.
		carol.must(http.StatusNotFound, "PUT", "/api/v1/me/contacts/"+id, entryBody("Taken", "+3225550999"))
		carol.must(http.StatusNotFound, "DELETE", "/api/v1/me/contacts/"+id, nil)
		bob.must(http.StatusNotFound, "DELETE", "/api/v1/me/contacts/"+rowID(theirs), nil)
		if got := string(bob.must(http.StatusOK, "GET", "/api/v1/me/contacts/export", nil).body); !strings.Contains(got, "Bobs Secret") || strings.Contains(got, "Taken") {
			t.Errorf("bob's export after carol's attempts: %s", got)
		}
		// Replacing one's own contact works and keeps it in the book.
		if got := bob.must(http.StatusOK, "PUT", "/api/v1/me/contacts/"+id, entryBody("Bobs Renamed", "+3225550101")).json(t); got["lastName"] != "Bobs Renamed" {
			t.Errorf("update: %v", got)
		}

		// No route reaches it for an administrator or through the book routes.
		var bookIDs []string
		for _, b := range listItems(t, alice.must(http.StatusOK, "GET", "/api/v1/phonebooks?limit=200", nil)) {
			bookIDs = append(bookIDs, rowID(b.(map[string]any)))
		}
		var personal int64
		if err := e.db.QueryRowContext(context.Background(), `SELECT id FROM phonebooks WHERE kind = 'personal' AND owner_user_id = $1`, bobID).Scan(&personal); err != nil {
			t.Fatal(err)
		}
		pid := strconv.FormatInt(personal, 10)
		for _, listed := range bookIDs {
			if listed == pid {
				t.Error("the personal book is listed")
			}
		}
		for _, p := range []string{"", "/entries", "/export"} {
			if r := alice.do("GET", "/api/v1/phonebooks/"+pid+p, nil); r.code != http.StatusNotFound {
				t.Errorf("admin GET book %s: %d", p, r.code)
			}
		}
		// The lookup diagnostic does not name a caller from a personal book.
		r := alice.must(http.StatusOK, "GET", "/api/v1/phonebooks/lookup?number=%2B3225550101&callee=101", nil).json(t)
		if r["result"] != "none" || strings.Contains(fmt.Sprint(r), "Bobs") {
			t.Errorf("lookup names a personal contact: %v", r)
		}

		bob.must(http.StatusNoContent, "DELETE", "/api/v1/me/contacts/"+id, nil)
		bob.must(http.StatusNotFound, "DELETE", "/api/v1/me/contacts/"+id, nil)
	})

	t.Run("bad requests", func(t *testing.T) {
		bob.must(http.StatusBadRequest, "GET", "/api/v1/me/contacts?limit=0", nil)
		bob.must(http.StatusBadRequest, "POST", "/api/v1/me/contacts", map[string]any{"lastName": "No number", "numbers": []any{}})
		bob.must(http.StatusBadRequest, "POST", "/api/v1/me/contacts", entryBody(strings.Repeat("x", 61), "5551234"))
		bob.must(http.StatusBadRequest, "PUT", "/api/v1/me/contacts/1", map[string]any{"numbers": []any{}})
		bob.must(http.StatusBadRequest, "POST", "/api/v1/me/contacts/import?mode=sideways", nil)
		bob.must(http.StatusBadRequest, "POST", "/api/v1/me/contacts/import?dry_run=maybe", nil)
		if r := bob.upload("/api/v1/me/contacts/import", "last_name,work\n,5551234\n").json(t); r["ok"] != false {
			t.Errorf("an import with a failing row: %v", r)
		}
	})

	t.Run("csv and the limit", func(t *testing.T) {
		// A dry run writes nothing.
		if r := bob.upload("/api/v1/me/contacts/import?dry_run=true", "last_name,work\nDry,5551234\n").json(t); r["ok"] != true || r["imported"].(float64) != 0 {
			t.Fatalf("dry run: %v", r)
		}
		if items := listItems(t, bob.must(http.StatusOK, "GET", "/api/v1/me/contacts", nil)); len(items) != 0 {
			t.Fatalf("a dry run wrote %d contacts", len(items))
		}
		var csv strings.Builder
		csv.WriteString("last_name,work\n")
		for i := range 1000 {
			fmt.Fprintf(&csv, "N%04d,+3225%06d\n", i, i)
		}
		if r := bob.upload("/api/v1/me/contacts/import", csv.String()).json(t); r["ok"] != true || r["imported"].(float64) != 1000 {
			t.Fatalf("import of 1000: %v", r)
		}
		bob.must(http.StatusConflict, "POST", "/api/v1/me/contacts", entryBody("One Too Many", "+3225999999"))
		if r := bob.upload("/api/v1/me/contacts/import", "last_name,work\nMore,5551234\n"); r.code != http.StatusConflict {
			t.Errorf("append past the limit: %d %s", r.code, r.body)
		}
		if r := bob.upload("/api/v1/me/contacts/import?dry_run=true", "last_name,work\nMore,5551234\n"); r.code != http.StatusConflict {
			t.Errorf("dry run past the limit: %d %s", r.code, r.body)
		}
		if r := bob.upload("/api/v1/me/contacts/import?mode=replace", "last_name,work\nOnly,5551234\n").json(t); r["ok"] != true {
			t.Errorf("replace: %v", r)
		}
		if items := listItems(t, bob.must(http.StatusOK, "GET", "/api/v1/me/contacts", nil)); len(items) != 1 {
			t.Errorf("after replace: %d contacts", len(items))
		}
	})

	t.Run("audit rows carry the user, not the contacts", func(t *testing.T) {
		rows, err := e.db.QueryContext(context.Background(), `SELECT actor || ' ' || action || ' ' || resource || ' ' || resource_id FROM audit_events WHERE resource = 'personal_contact'`)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = rows.Close() }()
		n := 0
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				t.Fatal(err)
			}
			n++
			if strings.Contains(line, "Secret") || strings.Contains(line, "5550") {
				t.Errorf("audit row names a contact: %q", line)
			}
		}
		if n == 0 {
			t.Error("personal contact changes are not audited")
		}
	})
}

// TestMeContactsServiceAccount (spec P-9) fails if a credential that is not
// a user (a service account) reaches any /me/contacts route, whatever its
// role.
func TestMeContactsServiceAccount(t *testing.T) {
	cred := credential{auth.KindService, auth.PrefixAccess + "svc"}
	l := roleLookup{actors: map[string]*auth.Actor{}}
	l.actors[string(auth.HashToken(cred.secret))] = &auth.Actor{ServiceName: "ci", Role: auth.RoleAdmin, Kind: auth.KindService,
		Scopes: auth.AllScopes, ClientID: "client", Audience: []string{"https://hello.test/api/v1"}}
	as, err := oauth.New(oauth.Options{PublicURL: "https://hello.test"})
	if err != nil {
		t.Fatal(err)
	}
	h := Handler(Config{Store: l, AI: as})
	for _, rt := range meRoutes("1") {
		r := httptest.NewRequest(rt.method, rt.path, nil)
		cred.apply(r)
		rec, ok := serveRecover(h, r)
		if !ok || rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), `"no_user"`) {
			t.Errorf("%s %s as a service account: %d %s", rt.method, rt.path, rec.Code, rec.Body.String())
		}
	}
}

// TestPhonebookLookupAPI (spec P-10, P-19) fails if the diagnostic does not
// name by the order extensions, group books, company; resolves an ambiguous
// suffix; resolves anonymous, a feature code or a short code; accepts an
// unknown callee or a missing number; or is open to a role below admin.
func TestPhonebookLookupAPI(t *testing.T) {
	e := newEnv(t, noLive{})
	alice := e.login()
	viewer, _ := e.asRole("vic", auth.RoleViewer)
	operator, _ := e.asRole("opal", auth.RoleOperator)

	alice.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"})
	company := bookID(t, alice, "company")
	alice.must(http.StatusCreated, "POST", "/api/v1/phonebooks/"+company+"/entries", entryBody("Corp", "+41791234567"))
	alice.must(http.StatusCreated, "POST", "/api/v1/phonebooks/"+company+"/entries", entryBody("Unique", "+41442223344"))
	alice.must(http.StatusCreated, "POST", "/api/v1/phonebooks/"+company+"/entries", entryBody("Twin A", "+33612345678"))
	alice.must(http.StatusCreated, "POST", "/api/v1/phonebooks/"+company+"/entries", entryBody("Twin B", "+49612345678"))

	look := func(number string) map[string]any {
		t.Helper()
		return alice.must(http.StatusOK, "GET", "/api/v1/phonebooks/lookup?number="+url2(number), nil).json(t)
	}
	if r := look("+41 79 123 45 67"); r["result"] != "match" || r["source"] != "company" || r["via"] != "exact" || r["name"] != "T Corp" || r["key"] != "+41791234567" {
		t.Errorf("exact: %v", r)
	}
	if r := look("0041791234567"); r["result"] != "match" || r["name"] != "T Corp" {
		t.Errorf("international prefix: %v", r)
	}
	if r := look("101"); r["result"] != "match" || r["source"] != "extensions" || r["name"] != "Sales" {
		t.Errorf("extension: %v", r)
	}
	if r := look("+39442223344"); r["result"] != "match" || r["via"] != "suffix" || r["name"] != "T Unique" {
		t.Errorf("suffix: %v", r)
	}
	if r := look("+34612345678"); r["result"] != "ambiguous" || r["name"] != nil || r["matches"].(float64) != 2 {
		t.Errorf("ambiguous: %v", r)
	}
	if r := look("+41000000000"); r["result"] != "none" {
		t.Errorf("unknown: %v", r)
	}
	for _, n := range []string{"anonymous", "*72101", "12"} {
		if r := look(n); r["result"] != "unresolvable" || r["key"] != "" {
			t.Errorf("%q: %v", n, r)
		}
	}
	if r := alice.must(http.StatusOK, "GET", "/api/v1/phonebooks/lookup?number=%2B41791234567&callee=101", nil).json(t); r["callee"] != "101" || fmt.Sprint(r["searched"]) != "[extensions company]" {
		t.Errorf("with a callee: %v", r)
	}
	alice.must(http.StatusBadRequest, "GET", "/api/v1/phonebooks/lookup", nil)
	alice.must(http.StatusBadRequest, "GET", "/api/v1/phonebooks/lookup?number=%2B41791234567&callee=999", nil)
	for name, c := range map[string]*client{"viewer": viewer, "operator": operator} {
		if r := c.do("GET", "/api/v1/phonebooks/lookup?number=101", nil); r.code != http.StatusForbidden {
			t.Errorf("%s: %d", name, r.code)
		}
	}
}

// url2 escapes a query value.
func url2(s string) string {
	return strings.NewReplacer("+", "%2B", " ", "%20", "*", "%2A").Replace(s)
}
