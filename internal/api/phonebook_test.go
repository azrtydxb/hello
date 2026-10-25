package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/auth"
	"github.com/azrtydxb/hello/internal/phonebook"
)

// upload sends content as the multipart part "file" of a POST.
func (c *client) upload(path, content string) response {
	c.e.t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	h := textproto.MIMEHeader{}
	h.Set("Content-Disposition", `form-data; name="file"; filename="book.csv"`)
	h.Set("Content-Type", "text/csv")
	part, err := mw.CreatePart(h)
	if err != nil {
		c.e.t.Fatal(err)
	}
	_, _ = part.Write([]byte(content))
	_ = mw.Close()
	c.header.Set("Content-Type", mw.FormDataContentType())
	defer c.header.Del("Content-Type")
	return c.do("POST", path, body.String())
}

func rowID(m map[string]any) string { return strconv.FormatInt(int64(m["id"].(float64)), 10) }

func listItems(t *testing.T, r response) []any {
	t.Helper()
	return r.json(t)["items"].([]any)
}

// asRole logs in a new user with role and returns its client.
func (e *env) asRole(name string, role auth.Role) (*client, int64) {
	e.t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		e.t.Fatal(err)
	}
	uid, err := e.st.CreateUser(context.Background(), "test", name, hash, role)
	if err != nil {
		e.t.Fatal(err)
	}
	c := e.client()
	c.must(http.StatusNoContent, "POST", "/api/v1/auth/login", map[string]string{"username": name, "password": testPassword})
	return c, uid
}

func entryBody(last string, numbers ...string) map[string]any {
	ns := []map[string]string{}
	for i, n := range numbers {
		ns = append(ns, map[string]string{"label": phonebook.Labels[i], "number": n})
	}
	return map[string]any{"firstName": "T", "lastName": last, "numbers": ns}
}

func bookID(t *testing.T, c *client, kind string) string {
	t.Helper()
	for _, b := range listItems(t, c.must(http.StatusOK, "GET", "/api/v1/phonebooks", nil)) {
		if b.(map[string]any)["kind"] == kind {
			return rowID(b.(map[string]any))
		}
	}
	t.Fatalf("no %s book", kind)
	return ""
}

func entryCount(t *testing.T, c *client, book string) int {
	t.Helper()
	return int(c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+book, nil).json(t)["entryCount"].(float64))
}

// TestPhonebookAPI fails if the seeded books are missing, a personal book
// can be seen or touched through any route, the company or extensions book
// can be deleted, the extensions book can be edited, a name or entry is
// accepted that breaks its limits, an entry of another book is reachable
// through this one, a hidden extension is listed in the extensions book, or
// paging repeats or loses a row.
func TestPhonebookAPI(t *testing.T) {
	e := newEnv(t, noLive{})
	c := e.login()
	ctx := context.Background()

	company, ext := bookID(t, c, "company"), bookID(t, c, "extensions")
	if bookID(t, c, "company") == ext {
		t.Fatal("company and extensions are one book")
	}

	t.Run("personal books are invisible", func(t *testing.T) {
		var uid, pid int64
		if err := e.db.QueryRowContext(ctx, `SELECT id FROM users WHERE username = $1`, testUser).Scan(&uid); err != nil {
			t.Fatal(err)
		}
		if err := e.db.QueryRowContext(ctx, `INSERT INTO phonebooks (kind, name, owner_user_id) VALUES ('personal', 'Mine', $1) RETURNING id`, uid).Scan(&pid); err != nil {
			t.Fatal(err)
		}
		if _, err := e.db.ExecContext(ctx, `INSERT INTO phonebook_entries (book_id, last_name, numbers) VALUES ($1, 'Secret', '[{"label":"work","number":"1234","key":"1234"}]')`, pid); err != nil {
			t.Fatal(err)
		}
		for _, b := range listItems(t, c.must(http.StatusOK, "GET", "/api/v1/phonebooks?limit=200", nil)) {
			if b.(map[string]any)["kind"] == "personal" {
				t.Errorf("a personal book is listed: %v", b)
			}
		}
		base := "/api/v1/phonebooks/" + strconv.FormatInt(pid, 10)
		for _, rq := range []struct{ method, path string }{
			{"GET", base}, {"PUT", base}, {"DELETE", base}, {"GET", base + "/entries"}, {"POST", base + "/entries"},
			{"GET", base + "/export"}, {"POST", base + "/import"},
		} {
			var body any
			switch rq.method {
			case "PUT":
				body = map[string]any{"name": "X"}
			case "POST":
				body = entryBody("A", "1234")
			}
			if rq.path == base+"/import" {
				if r := c.upload(rq.path, "last_name,work\nA,1234\n"); r.code != http.StatusNotFound {
					t.Errorf("%s %s = %d %s, want 404", rq.method, rq.path, r.code, r.body)
				}
				continue
			}
			if r := c.do(rq.method, rq.path, body); r.code != http.StatusNotFound {
				t.Errorf("%s %s = %d %s, want 404", rq.method, rq.path, r.code, r.body)
			}
		}
		var n int
		if err := e.db.QueryRowContext(ctx, `SELECT count(*) FROM phonebook_entries WHERE book_id = $1`, pid).Scan(&n); err != nil || n != 1 {
			t.Errorf("the personal book's entries changed: %d (%v)", n, err)
		}
	})

	t.Run("books", func(t *testing.T) {
		b := c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "Suppliers"}).json(t)
		if b["kind"] != "named" || b["name"] != "Suppliers" || b["entryCount"] != float64(0) || b["version"] != float64(1) {
			t.Fatalf("created book %v", b)
		}
		c.must(http.StatusConflict, "POST", "/api/v1/phonebooks", map[string]string{"name": "suppliers"})
		for name, body := range map[string]any{
			"empty": map[string]string{"name": ""}, "spaces": map[string]string{"name": " x "},
			"long": map[string]string{"name": strings.Repeat("n", 101)}, "control": map[string]string{"name": "a\nb"},
			"unknown field": map[string]string{"nom": "x"}, "not json": "nope",
		} {
			if r := c.do("POST", "/api/v1/phonebooks", body); r.code != http.StatusBadRequest {
				t.Errorf("create with %s = %d %s, want 400", name, r.code, r.body)
			}
		}
		if got := c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+rowID(b), nil).json(t); got["name"] != "Suppliers" {
			t.Errorf("get = %v", got)
		}
		for _, p := range []string{"999999", "abc", "0"} {
			c.must(http.StatusNotFound, "GET", "/api/v1/phonebooks/"+p, nil)
		}
		other := c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "Partners"}).json(t)
		if r := c.must(http.StatusOK, "PUT", "/api/v1/phonebooks/"+rowID(b), map[string]string{"name": "Vendors"}).json(t); r["name"] != "Vendors" {
			t.Errorf("rename = %v", r)
		}
		c.must(http.StatusConflict, "PUT", "/api/v1/phonebooks/"+rowID(b), map[string]string{"name": "PARTNERS"})
		c.must(http.StatusBadRequest, "PUT", "/api/v1/phonebooks/"+rowID(b), map[string]string{"name": ""})
		c.must(http.StatusNotFound, "PUT", "/api/v1/phonebooks/999999", map[string]string{"name": "x"})
		c.must(http.StatusConflict, "PUT", "/api/v1/phonebooks/"+ext, map[string]string{"name": "x"})
		c.must(http.StatusOK, "PUT", "/api/v1/phonebooks/"+company, map[string]string{"name": "Corporate"})

		c.must(http.StatusConflict, "DELETE", "/api/v1/phonebooks/"+company, nil)
		c.must(http.StatusConflict, "DELETE", "/api/v1/phonebooks/"+ext, nil)
		c.must(http.StatusNoContent, "DELETE", "/api/v1/phonebooks/"+rowID(other), nil)
		c.must(http.StatusNotFound, "DELETE", "/api/v1/phonebooks/"+rowID(other), nil)
		c.must(http.StatusNotFound, "GET", "/api/v1/phonebooks/"+rowID(other), nil)
		if got := bookID(t, c, "company"); got != company {
			t.Errorf("company book changed id to %s", got)
		}
	})

	t.Run("paging", func(t *testing.T) {
		for i := 0; i < 4; i++ {
			c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": fmt.Sprintf("Page %d", i)})
		}
		var seen []string
		path := "/api/v1/phonebooks?limit=2"
		for pages := 0; ; pages++ {
			if pages > 10 {
				t.Fatal("paging does not end")
			}
			r := c.must(http.StatusOK, "GET", path, nil).json(t)
			for _, b := range r["items"].([]any) {
				seen = append(seen, rowID(b.(map[string]any)))
			}
			if r["next"] == "" {
				break
			}
			if len(r["items"].([]any)) != 2 {
				t.Fatalf("a short page with a cursor: %v", r)
			}
			path = "/api/v1/phonebooks?limit=2&cursor=" + r["next"].(string)
		}
		all := listItems(t, c.must(http.StatusOK, "GET", "/api/v1/phonebooks?limit=200", nil))
		if len(seen) != len(all) || len(all) < 6 {
			t.Fatalf("paged %v, listed %d", seen, len(all))
		}
		for i, b := range all {
			if rowID(b.(map[string]any)) != seen[i] {
				t.Fatalf("page order %v differs from the list at %d", seen, i)
			}
		}
		for _, q := range []string{"limit=0", "limit=201", "limit=x", "cursor=0", "cursor=x", "cursor=-3"} {
			c.must(http.StatusBadRequest, "GET", "/api/v1/phonebooks?"+q, nil)
			c.must(http.StatusBadRequest, "GET", "/api/v1/phonebooks/"+company+"/entries?"+q, nil)
			c.must(http.StatusBadRequest, "GET", "/api/v1/phone-groups?"+q, nil)
		}
	})

	t.Run("entries", func(t *testing.T) {
		base := "/api/v1/phonebooks/" + company + "/entries"
		r := c.must(http.StatusCreated, "POST", base, map[string]any{
			"firstName": " Ada ", "lastName": "Lovelace", "company": "AE", "note": "n",
			"numbers": []map[string]string{{"label": "work", "number": " +41 44 111 22 33 "}, {"label": "mobile", "number": "079 123 45 67"}}}).json(t)
		if r["firstName"] != "Ada" || r["bookId"].(float64) <= 0 || len(r["numbers"].([]any)) != 2 {
			t.Fatalf("created entry %v", r)
		}
		n0 := r["numbers"].([]any)[0].(map[string]any)
		if n0["number"] != "+41 44 111 22 33" || n0["label"] != "work" || len(n0) != 2 {
			t.Errorf("the number is not returned as typed, without its key: %v", n0)
		}
		for name, body := range map[string]any{
			"no name":       map[string]any{"numbers": []map[string]string{{"label": "work", "number": "1234"}}},
			"no numbers":    map[string]any{"lastName": "A"},
			"empty numbers": map[string]any{"lastName": "A", "numbers": []string{}},
			"bad label":     map[string]any{"lastName": "A", "numbers": []map[string]string{{"label": "fax", "number": "1234"}}},
			"four numbers": map[string]any{"lastName": "A", "numbers": []map[string]string{
				{"label": "work", "number": "1"}, {"label": "mobile", "number": "2"}, {"label": "home", "number": "3"}, {"label": "other", "number": "4"}}},
			"long name":   map[string]any{"lastName": strings.Repeat("x", 61), "numbers": []map[string]string{{"label": "work", "number": "1234"}}},
			"long number": map[string]any{"lastName": "A", "numbers": []map[string]string{{"label": "work", "number": strings.Repeat("1", 33)}}},
			"long note":   map[string]any{"lastName": "A", "note": strings.Repeat("n", 256), "numbers": []map[string]string{{"label": "work", "number": "1234"}}},
			"key given":   map[string]any{"lastName": "A", "numbers": []map[string]string{{"label": "work", "number": "1234", "key": "x"}}},
			"unknown":     map[string]any{"lastName": "A", "nickname": "x", "numbers": []map[string]string{{"label": "work", "number": "1234"}}},
		} {
			rr := c.do("POST", base, body)
			if rr.code != http.StatusBadRequest {
				t.Errorf("%s = %d %s, want 400", name, rr.code, rr.body)
			}
		}
		if rr := c.do("POST", base, map[string]any{"numbers": []map[string]string{{"label": "work", "number": "1234"}}}); !strings.Contains(string(rr.body), "lastName") {
			t.Errorf("a missing name names no field: %s", rr.body)
		}
		if n := entryCount(t, c, company); n != 1 {
			t.Fatalf("refused entries were stored: %d", n)
		}

		eid := rowID(r)
		up := c.must(http.StatusOK, "PUT", base+"/"+eid, entryBody("Byron", "2345")).json(t)
		if up["lastName"] != "Byron" || up["company"] != "" || len(up["numbers"].([]any)) != 1 {
			t.Errorf("update replaces every field: %v", up)
		}
		c.must(http.StatusBadRequest, "PUT", base+"/"+eid, map[string]any{"lastName": ""})
		c.must(http.StatusNotFound, "PUT", base+"/999999", entryBody("X", "1234"))
		c.must(http.StatusNotFound, "PUT", base+"/abc", entryBody("X", "1234"))
		c.must(http.StatusNotFound, "PUT", "/api/v1/phonebooks/999999/entries/"+eid, entryBody("X", "1234"))

		// An entry is reachable only through its own book.
		named := c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "Other book"}).json(t)
		c.must(http.StatusNotFound, "PUT", "/api/v1/phonebooks/"+rowID(named)+"/entries/"+eid, entryBody("X", "1234"))
		c.must(http.StatusNotFound, "DELETE", "/api/v1/phonebooks/"+rowID(named)+"/entries/"+eid, nil)
		c.must(http.StatusNotFound, "POST", "/api/v1/phonebooks/999999/entries", entryBody("X", "1234"))
		c.must(http.StatusNotFound, "GET", "/api/v1/phonebooks/999999/entries", nil)
		if n := entryCount(t, c, company); n != 1 {
			t.Errorf("a refused change touched the company book: %d entries", n)
		}

		list := listItems(t, c.must(http.StatusOK, "GET", base, nil))
		if len(list) != 1 || list[0].(map[string]any)["lastName"] != "Byron" {
			t.Errorf("list = %v", list)
		}
		c.must(http.StatusNoContent, "DELETE", base+"/"+eid, nil)
		c.must(http.StatusNotFound, "DELETE", base+"/"+eid, nil)
		if n := entryCount(t, c, company); n != 0 {
			t.Errorf("entries after delete = %d", n)
		}
	})

	t.Run("extensions book", func(t *testing.T) {
		base := "/api/v1/phonebooks/" + ext + "/entries"
		e101 := c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
		c.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"})
		if e101["directoryHidden"] != false {
			t.Fatalf("a new extension is hidden: %v", e101)
		}
		list := listItems(t, c.must(http.StatusOK, "GET", base, nil))
		if len(list) != 2 || list[0].(map[string]any)["lastName"] != "Sales" || list[0].(map[string]any)["numbers"].([]any)[0].(map[string]any)["number"] != "101" {
			t.Fatalf("extensions book = %v", list)
		}
		if n := entryCount(t, c, ext); n != 2 {
			t.Errorf("entryCount = %d", n)
		}

		// It cannot be edited, imported into or its entries changed.
		c.must(http.StatusConflict, "POST", base, entryBody("X", "1234"))
		c.must(http.StatusConflict, "PUT", base+"/"+rowID(e101), entryBody("X", "1234"))
		c.must(http.StatusConflict, "DELETE", base+"/"+rowID(e101), nil)
		if r := c.upload("/api/v1/phonebooks/"+ext+"/import", "last_name,work\nA,1234\n"); r.code != http.StatusConflict {
			t.Errorf("import into the extensions book = %d %s", r.code, r.body)
		}
		if n := entryCount(t, c, ext); n != 2 {
			t.Errorf("a refused change altered the extensions book: %d", n)
		}

		// A hidden extension leaves the book, in the list and the export.
		hidden := c.must(http.StatusOK, "PATCH", "/api/v1/extensions/"+rowID(e101), map[string]any{"directoryHidden": true}).json(t)
		if hidden["directoryHidden"] != true {
			t.Fatalf("hide: %v", hidden)
		}
		list = listItems(t, c.must(http.StatusOK, "GET", base, nil))
		csv := string(c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+ext+"/export", nil).body)
		if len(list) != 1 || strings.Contains(csv, "Sales") || !strings.Contains(csv, "Support") {
			t.Errorf("after hiding 101: list %v, export %q", list, csv)
		}
		c.must(http.StatusOK, "PATCH", "/api/v1/extensions/"+rowID(e101), map[string]any{"directoryHidden": false})
		if n := entryCount(t, c, ext); n != 2 {
			t.Errorf("after unhiding: %d", n)
		}
	})
}

// TestPhonebookCSV (spec P-7) fails if a dry run writes anything, an error
// row lacks its row and column, one failing row lets others be stored, a
// file above 2 MiB or 5000 rows is accepted, an export cell starting with =
// is not neutralised, import then export does not round-trip, or append
// and replace do not do what they say.
func TestPhonebookCSV(t *testing.T) {
	e := newEnv(t, noLive{})
	c := e.login()
	book := rowID(c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "CSV"}).json(t))
	imp := "/api/v1/phonebooks/" + book + "/import"
	const header = "first_name,last_name,company,work,mobile,home,other,note\n"

	good := header + "Ada,Lovelace,AE,+41 44 111 22 33,079 123 45 67,,,first\n" + "Charles,Babbage,AE,,,044 222 33 44,,\n"
	dry := c.upload(imp+"?dry_run=true", good)
	if r := dry.json(t); dry.code != 200 || r["ok"] != true || r["dryRun"] != true || r["rows"] != float64(2) || r["imported"] != float64(0) {
		t.Fatalf("dry run = %d %s", dry.code, dry.body)
	}
	if n := entryCount(t, c, book); n != 0 {
		t.Fatalf("a dry run wrote %d entries", n)
	}

	t.Run("errors name row and column", func(t *testing.T) {
		bad := header +
			"A,One,,1234,,,,\n" + // row 2 fine
			",,,5678,,,,\n" + // row 3: no name
			"B,Two,,,,,,\n" + // row 4: no number
			"C,Three,," + strings.Repeat("9", 40) + ",,,,\n" + // row 5: number too long, in work
			"D,Four,,4321,,,,\n" // row 6 fine
		for _, mode := range []string{"dry_run=true", "dry_run=false"} {
			r := c.upload(imp+"?"+mode, bad)
			want := http.StatusOK
			if mode == "dry_run=false" {
				want = http.StatusBadRequest
			}
			if r.code != want {
				t.Fatalf("%s = %d %s, want %d", mode, r.code, r.body, want)
			}
			res := r.json(t)
			errs := res["errors"].([]any)
			if res["ok"] != false || len(errs) != 3 || res["imported"] != float64(0) {
				t.Fatalf("%s: result %s", mode, r.body)
			}
			for i, w := range []struct {
				row    float64
				column string
			}{{3, "last_name"}, {4, "work"}, {5, "work"}} {
				got := errs[i].(map[string]any)
				if got["row"] != w.row || got["column"] != w.column || got["reason"] == "" {
					t.Errorf("%s: error %d = %v, want row %v column %s", mode, i, got, w.row, w.column)
				}
			}
		}
		if n := entryCount(t, c, book); n != 0 {
			t.Fatalf("a failing row let %d others be stored", n)
		}
	})

	t.Run("malformed requests", func(t *testing.T) {
		for _, q := range []string{"dry_run=maybe", "mode=merge"} {
			if r := c.upload(imp+"?"+q, good); r.code != http.StatusBadRequest {
				t.Errorf("%s = %d", q, r.code)
			}
		}
		if r := c.do("POST", imp, good); r.code != http.StatusBadRequest {
			t.Errorf("a plain body = %d %s", r.code, r.body)
		}
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		_ = mw.WriteField("other", good)
		_ = mw.Close()
		c.header.Set("Content-Type", mw.FormDataContentType())
		r := c.do("POST", imp, body.String())
		c.header.Del("Content-Type")
		if r.code != http.StatusBadRequest {
			t.Errorf("no file part = %d %s", r.code, r.body)
		}
		for name, content := range map[string]string{
			"no header": "", "unknown column": "last_name,work,fax\nA,1234,5\n", "duplicate column": "last_name,last_name,work\nA,B,1234\n",
		} {
			r := c.upload(imp+"?dry_run=true", content)
			if res := r.json(t); r.code != 200 || res["ok"] != false || len(res["errors"].([]any)) == 0 {
				t.Errorf("%s: %d %s", name, r.code, r.body)
			}
		}
		c.must(http.StatusNotFound, "POST", "/api/v1/phonebooks/999999/import", "")
	})

	t.Run("apply, export, round trip", func(t *testing.T) {
		r := c.upload(imp, "\xef\xbb\xbf"+strings.ReplaceAll(good, ",", ";")).json(t)
		if r["ok"] != true || r["imported"] != float64(2) || r["mode"] != "append" {
			t.Fatalf("apply (BOM, semicolons) = %v", r)
		}
		exp := c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+book+"/export", nil)
		if ct := exp.header.Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") || !strings.Contains(exp.header.Get("Content-Disposition"), "attachment") {
			t.Errorf("export headers %v", exp.header)
		}
		lines := strings.Split(strings.TrimSpace(string(exp.body)), "\n")
		if len(lines) != 3 || lines[0] != strings.TrimSpace(header) || !strings.HasPrefix(lines[1], "Charles,Babbage") {
			t.Fatalf("export =\n%s", exp.body)
		}
		// A number starting with + is neutralised for a spreadsheet and
		// still imports back as the number.
		if !strings.Contains(lines[2], "'+41 44 111 22 33") {
			t.Errorf("the + cell is not neutralised: %s", lines[2])
		}
		copyBook := rowID(c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "Copy"}).json(t))
		if r := c.upload("/api/v1/phonebooks/"+copyBook+"/import", string(exp.body)).json(t); r["ok"] != true {
			t.Fatalf("re-import = %v", r)
		}
		again := c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+copyBook+"/export", nil)
		if string(again.body) != string(exp.body) {
			t.Errorf("import then export does not round-trip:\n%s\n---\n%s", exp.body, again.body)
		}
		for _, en := range listItems(t, c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+copyBook+"/entries", nil)) {
			for _, n := range en.(map[string]any)["numbers"].([]any) {
				if strings.HasPrefix(n.(map[string]any)["number"].(string), "'") {
					t.Errorf("the apostrophe was stored: %v", n)
				}
			}
		}
	})

	t.Run("formulas are stored as text and neutralised on export", func(t *testing.T) {
		fb := rowID(c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "Formulas"}).json(t))
		c.upload("/api/v1/phonebooks/"+fb+"/import", header+"=SUM(1),Smith,,1234,,,,\n")
		exp := string(c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+fb+"/export", nil).body)
		if !strings.Contains(exp, "'=SUM(1)") {
			t.Errorf("the = cell is not neutralised:\n%s", exp)
		}
		if strings.Contains(exp, "\n=") || strings.Contains(exp, ",=") {
			t.Errorf("a formula cell is exported bare:\n%s", exp)
		}
		es := listItems(t, c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+fb+"/entries", nil))
		if len(es) != 1 || es[0].(map[string]any)["firstName"] != "=SUM(1)" {
			t.Errorf("the formula is not stored as the text it was: %v", es)
		}
	})

	t.Run("append and replace", func(t *testing.T) {
		ab := rowID(c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "Modes"}).json(t))
		path := "/api/v1/phonebooks/" + ab + "/import"
		c.upload(path, header+"A,One,,1111,,,,\nB,Two,,2222,,,,\n")
		c.upload(path+"?mode=append", header+"C,Three,,3333,,,,\n")
		if n := entryCount(t, c, ab); n != 3 {
			t.Fatalf("after append: %d", n)
		}
		if r := c.upload(path+"?mode=replace&dry_run=true", header+"D,Four,,4444,,,,\n").json(t); r["mode"] != "replace" || r["ok"] != true {
			t.Fatalf("replace dry run = %v", r)
		}
		if n := entryCount(t, c, ab); n != 3 {
			t.Fatalf("a replace dry run changed the book: %d", n)
		}
		c.upload(path+"?mode=replace", header+"D,Four,,4444,,,,\n")
		es := listItems(t, c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+ab+"/entries", nil))
		if len(es) != 1 || es[0].(map[string]any)["lastName"] != "Four" {
			t.Fatalf("after replace: %v", es)
		}
	})

	t.Run("duplicates warn", func(t *testing.T) {
		r := c.upload(imp+"?dry_run=true", header+"A,One,,+41 79 123 45 67,,,,\nB,Two,,,0041 79 123 45 67,,,\n").json(t)
		w := r["warnings"].([]any)
		if r["ok"] != true || len(w) != 1 || w[0].(map[string]any)["row"] != float64(3) {
			t.Fatalf("warnings = %v", r)
		}
	})

	t.Run("limits", func(t *testing.T) {
		lb := rowID(c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "Limits"}).json(t))
		path := "/api/v1/phonebooks/" + lb + "/import"
		rows := func(n int) string {
			var b strings.Builder
			b.WriteString("last_name,work\n")
			for i := range n {
				fmt.Fprintf(&b, "n%d,%d\n", i, 10000+i)
			}
			return b.String()
		}
		over := rows(phonebook.MaxCSVRows + 1)
		for _, q := range []string{"?dry_run=true", ""} {
			if r := c.upload(path+q, over); r.code != http.StatusBadRequest {
				t.Errorf("5001 rows%s = %d %s", q, r.code, r.body)
			}
		}
		big := "last_name,work,note\nx,1234," + strings.Repeat("a", phonebook.MaxCSVBytes) + "\n"
		if r := c.upload(path, big); r.code != http.StatusBadRequest {
			t.Errorf("a file over 2 MiB = %d %s", r.code, r.body)
		}
		if n := entryCount(t, c, lb); n != 0 {
			t.Fatalf("refused files stored %d entries", n)
		}
		if r := c.upload(path, rows(phonebook.MaxCSVRows)); r.code != http.StatusOK {
			t.Fatalf("5000 rows = %d %s", r.code, r.body)
		}
		if n := entryCount(t, c, lb); n != phonebook.MaxEntriesPerBook {
			t.Fatalf("entries = %d", n)
		}
		c.must(http.StatusConflict, "POST", "/api/v1/phonebooks/"+lb+"/entries", entryBody("One more", "1234"))
		for _, q := range []string{"?dry_run=true", ""} {
			if r := c.upload(path+q, "last_name,work\nX,1234\n"); r.code != http.StatusConflict {
				t.Errorf("append past the limit%s = %d %s", q, r.code, r.body)
			}
		}
		if r := c.upload(path+"?mode=replace", rows(phonebook.MaxCSVRows)); r.code != http.StatusOK {
			t.Errorf("replace a full book with a full file = %d %s", r.code, r.body)
		}
		if n := entryCount(t, c, lb); n != phonebook.MaxEntriesPerBook {
			t.Errorf("entries after refusals = %d", n)
		}
	})
}

// TestPhoneGroupsAPI fails if a group name can repeat, the extensions and
// company switches do not default on, books are not kept in the order
// given or a book that is unknown, not named, repeated or a sixth is
// accepted, a phone can be put in a group that does not exist, deleting a
// group leaves its phones pointing at it, or paging repeats a group.
func TestPhoneGroupsAPI(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c

	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Warehouse", "description": "Dock"}).json(t)
	if g["includeExtensions"] != true || g["includeCompany"] != true || len(g["bookIds"].([]any)) != 0 || g["phoneCount"] != float64(0) {
		t.Fatalf("created group %v", g)
	}
	c.must(http.StatusConflict, "POST", "/api/v1/phone-groups", map[string]any{"name": "WAREHOUSE"})
	for name, body := range map[string]any{
		"empty name": map[string]any{"name": ""}, "long description": map[string]any{"name": "x", "description": strings.Repeat("d", 256)},
		"unknown field": map[string]any{"name": "x", "sites": 1}, "no name": map[string]any{},
	} {
		if r := c.do("POST", "/api/v1/phone-groups", body); r.code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", name, r.code, r.body)
		}
	}
	gid := rowID(g)
	if got := c.must(http.StatusOK, "GET", "/api/v1/phone-groups/"+gid, nil).json(t); got["name"] != "Warehouse" {
		t.Errorf("get = %v", got)
	}
	c.must(http.StatusNotFound, "GET", "/api/v1/phone-groups/999999", nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/phone-groups/abc", nil)

	office := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Office"}).json(t)
	up := c.must(http.StatusOK, "PUT", "/api/v1/phone-groups/"+rowID(office), map[string]any{"name": "Head office", "includeCompany": false}).json(t)
	if up["name"] != "Head office" || up["includeCompany"] != false || up["includeExtensions"] != true {
		t.Errorf("replace: %v (omitted switches turn on)", up)
	}
	c.must(http.StatusConflict, "PUT", "/api/v1/phone-groups/"+rowID(office), map[string]any{"name": "warehouse"})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/phone-groups/"+rowID(office), map[string]any{"name": " "})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999", map[string]any{"name": "x"})

	// Books, in order.
	var books []string
	for i := 0; i < 6; i++ {
		books = append(books, rowID(c.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": fmt.Sprintf("B%d", i)}).json(t)))
	}
	nums := func(ss ...string) []int64 {
		out := []int64{}
		for _, s := range ss {
			n, _ := strconv.ParseInt(s, 10, 64)
			out = append(out, n)
		}
		return out
	}
	set := func(want int, group string, ids []int64) response {
		t.Helper()
		return c.must(want, "PUT", "/api/v1/phone-groups/"+group+"/books", map[string]any{"bookIds": ids})
	}
	got := set(http.StatusOK, gid, nums(books[2], books[0], books[1])).json(t)["bookIds"]
	if fmt.Sprint(got) != fmt.Sprint([]any{float64(nums(books[2])[0]), float64(nums(books[0])[0]), float64(nums(books[1])[0])}) {
		t.Errorf("books %v are not in the order given", got)
	}
	company := bookID(t, c, "company")
	for name, ids := range map[string][]int64{
		"unknown": {999999}, "company": nums(company), "twice": nums(books[0], books[0]), "six": nums(books...),
	} {
		set(http.StatusBadRequest, gid, ids)
		if cur := c.must(http.StatusOK, "GET", "/api/v1/phone-groups/"+gid, nil).json(t)["bookIds"].([]any); len(cur) != 3 {
			t.Errorf("%s: a refused set changed the books to %v", name, cur)
		}
	}
	c.must(http.StatusBadRequest, "PUT", "/api/v1/phone-groups/"+gid+"/books", map[string]any{})
	c.must(http.StatusBadRequest, "PUT", "/api/v1/phone-groups/"+gid+"/books", map[string]any{"bookIds": []int64{1}, "extra": 1})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/books", map[string]any{"bookIds": []int64{}})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/abc/books", map[string]any{"bookIds": []int64{}})

	// Phones.
	phone := p.phone("805ec0000010", nil)
	pid := fmt.Sprint(phone["id"])
	if phone["groupId"] != nil {
		t.Fatalf("a new phone has a group: %v", phone["groupId"])
	}
	if r := c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+pid, map[string]any{"groupId": g["id"]}).json(t); r["groupId"] != g["id"] {
		t.Fatalf("assign: %v", r)
	}
	if got := c.must(http.StatusOK, "GET", "/api/v1/phone-groups/"+gid, nil).json(t)["phoneCount"]; got != float64(1) {
		t.Errorf("phoneCount = %v", got)
	}
	if r := c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+pid, map[string]any{"label": "Dock 1"}).json(t); r["groupId"] != g["id"] {
		t.Errorf("an update that omits groupId changed it: %v", r["groupId"])
	}
	for name, v := range map[string]any{"unknown": 999999, "zero": 0, "string": "x"} {
		if r := c.do("PATCH", "/api/v1/phones/"+pid, map[string]any{"groupId": v}); r.code != http.StatusBadRequest {
			t.Errorf("groupId %s = %d %s, want 400", name, r.code, r.body)
		}
	}
	if r := c.must(http.StatusOK, "GET", "/api/v1/phones/"+pid, nil).json(t); r["groupId"] != g["id"] {
		t.Errorf("a refused groupId changed the phone: %v", r["groupId"])
	}
	if r := c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+pid, map[string]any{"groupId": nil}).json(t); r["groupId"] != nil {
		t.Errorf("clear: %v", r["groupId"])
	}
	c.must(http.StatusOK, "PATCH", "/api/v1/phones/"+pid, map[string]any{"groupId": g["id"]})

	// Paging.
	first := c.must(http.StatusOK, "GET", "/api/v1/phone-groups?limit=1", nil).json(t)
	if len(first["items"].([]any)) != 1 || first["next"] == "" {
		t.Fatalf("first page %v", first)
	}
	second := c.must(http.StatusOK, "GET", "/api/v1/phone-groups?limit=1&cursor="+first["next"].(string), nil).json(t)
	if len(second["items"].([]any)) != 1 || second["next"] != "" ||
		rowID(second["items"].([]any)[0].(map[string]any)) == rowID(first["items"].([]any)[0].(map[string]any)) {
		t.Fatalf("second page %v", second)
	}

	// Deleting a group frees its phones and keeps the books.
	c.must(http.StatusNoContent, "DELETE", "/api/v1/phone-groups/"+gid, nil)
	c.must(http.StatusNotFound, "DELETE", "/api/v1/phone-groups/"+gid, nil)
	c.must(http.StatusNotFound, "GET", "/api/v1/phone-groups/"+gid, nil)
	if r := c.must(http.StatusOK, "GET", "/api/v1/phones/"+pid, nil).json(t); r["groupId"] != nil {
		t.Errorf("the phone still names the deleted group: %v", r["groupId"])
	}
	c.must(http.StatusOK, "GET", "/api/v1/phonebooks/"+books[0], nil)
}

// TestUserExtensionLink (spec P-3) fails if an extension can be linked to
// two users, a link to a missing extension is accepted, a non-admin sets a
// link, unlinking does not clear it, or a link is not audited.
func TestUserExtensionLink(t *testing.T) {
	e := newEnv(t, noLive{})
	alice := e.login()
	bob, bobID := e.asRole("bob", auth.RoleViewer)
	op, _ := e.asRole("opal", auth.RoleOperator)
	var aliceID int64
	if err := e.db.QueryRowContext(context.Background(), `SELECT id FROM users WHERE username = $1`, testUser).Scan(&aliceID); err != nil {
		t.Fatal(err)
	}
	e101 := alice.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "101", "name": "Sales"}).json(t)
	e102 := alice.must(http.StatusCreated, "POST", "/api/v1/extensions", map[string]string{"number": "102", "name": "Support"}).json(t)
	bobURL, aliceURL := "/api/v1/users/"+strconv.FormatInt(bobID, 10), "/api/v1/users/"+strconv.FormatInt(aliceID, 10)

	for name, c := range map[string]*client{"viewer": bob, "operator": op} {
		if r := c.do("PATCH", bobURL, map[string]any{"extensionId": e101["id"]}); r.code != http.StatusForbidden {
			t.Errorf("%s sets a link: %d %s", name, r.code, r.body)
		}
	}
	if got := alice.must(http.StatusOK, "PATCH", bobURL, map[string]any{"extensionId": e101["id"]}).json(t); got["extensionId"] != e101["id"] || got["role"] != "viewer" {
		t.Fatalf("link = %v", got)
	}
	if r := alice.must(http.StatusConflict, "PATCH", aliceURL, map[string]any{"extensionId": e101["id"]}).json(t); !strings.Contains(fmt.Sprint(r), "bob") {
		t.Errorf("the conflict does not name the user: %v", r)
	}
	alice.must(http.StatusBadRequest, "PATCH", aliceURL, map[string]any{"extensionId": 999999})
	alice.must(http.StatusBadRequest, "PATCH", aliceURL, map[string]any{"extensionId": 0})
	alice.must(http.StatusBadRequest, "PATCH", aliceURL, map[string]any{"extensionId": "x"})
	alice.must(http.StatusBadRequest, "PATCH", aliceURL, map[string]any{})
	alice.must(http.StatusNotFound, "PATCH", "/api/v1/users/999999", map[string]any{"extensionId": e102["id"]})

	// A role change alone leaves the link; both at once change both.
	if got := alice.must(http.StatusOK, "PATCH", bobURL, map[string]any{"role": "operator"}).json(t); got["extensionId"] != e101["id"] || got["role"] != "operator" {
		t.Errorf("a role change touched the link: %v", got)
	}
	if got := alice.must(http.StatusOK, "PATCH", bobURL, map[string]any{"role": "viewer", "extensionId": e102["id"]}).json(t); got["extensionId"] != e102["id"] || got["role"] != "viewer" {
		t.Errorf("role and link together: %v", got)
	}
	// Nothing was applied when the role is invalid.
	alice.must(http.StatusBadRequest, "PATCH", bobURL, map[string]any{"role": "root", "extensionId": e101["id"]})
	// 101 is free again, so another user can take it.
	alice.must(http.StatusOK, "PATCH", aliceURL, map[string]any{"extensionId": e101["id"]})

	listed := map[string]any{}
	for _, u := range listItems(t, alice.must(http.StatusOK, "GET", "/api/v1/users", nil)) {
		listed[u.(map[string]any)["username"].(string)] = u.(map[string]any)["extensionId"]
	}
	if listed["alice"] != e101["id"] || listed["bob"] != e102["id"] || listed["opal"] != nil {
		t.Errorf("listed links %v", listed)
	}

	if got := alice.must(http.StatusOK, "PATCH", bobURL, map[string]any{"extensionId": nil}).json(t); got["extensionId"] != nil {
		t.Errorf("unlink = %v", got)
	}
	var n int
	if err := e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM users WHERE username = 'bob' AND extension_id IS NOT NULL`).Scan(&n); err != nil || n != 0 {
		t.Errorf("unlinking left the link (%d, %v)", n, err)
	}
	if err := e.db.QueryRowContext(context.Background(), `SELECT count(*) FROM audit_events WHERE resource = 'user' AND resource_id = $1 AND action IN ('set-extension', 'unset-extension')`,
		strconv.FormatInt(bobID, 10)).Scan(&n); err != nil || n != 3 {
		t.Errorf("%d audit rows for bob's link changes (%v), want 3", n, err)
	}
	// 102 is free for another extension holder again.
	alice.must(http.StatusOK, "PATCH", aliceURL, map[string]any{"extensionId": e102["id"]})
}

// TestPhonebookRBAC (spec P-19) fails if a viewer can write anything, an
// operator can create, rename or delete a book, assign books to a group or
// touch a group, an admin-only route accepts an operator, a viewer can
// export, or a refused request changes anything.
func TestPhonebookRBAC(t *testing.T) {
	e := newEnv(t, noLive{})
	alice := e.login()
	viewer, _ := e.asRole("vic", auth.RoleViewer)
	operator, _ := e.asRole("opal", auth.RoleOperator)

	book := rowID(alice.must(http.StatusCreated, "POST", "/api/v1/phonebooks", map[string]string{"name": "Shared"}).json(t))
	entry := rowID(alice.must(http.StatusCreated, "POST", "/api/v1/phonebooks/"+book+"/entries", entryBody("Seed", "1234")).json(t))
	group := rowID(alice.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "G"}).json(t))
	books, groups := "/api/v1/phonebooks", "/api/v1/phone-groups"
	csv := "last_name,work\nNew,5555\n"

	type call struct {
		method, path string
		body         any
		upload       bool
		role         auth.Role // the least role the route needs
	}
	calls := []call{
		{"GET", books, nil, false, auth.RoleViewer},
		{"POST", books, map[string]string{"name": "New"}, false, auth.RoleAdmin},
		{"GET", books + "/" + book, nil, false, auth.RoleViewer},
		{"PUT", books + "/" + book, map[string]string{"name": "Renamed"}, false, auth.RoleAdmin},
		{"DELETE", books + "/" + book, nil, false, auth.RoleAdmin},
		{"GET", books + "/" + book + "/entries", nil, false, auth.RoleViewer},
		{"POST", books + "/" + book + "/entries", entryBody("New", "5555"), false, auth.RoleOperator},
		{"PUT", books + "/" + book + "/entries/" + entry, entryBody("Changed", "5555"), false, auth.RoleOperator},
		{"DELETE", books + "/" + book + "/entries/" + entry, nil, false, auth.RoleOperator},
		{"POST", books + "/" + book + "/import", csv, true, auth.RoleOperator},
		{"GET", books + "/" + book + "/export", nil, false, auth.RoleOperator},
		{"GET", groups, nil, false, auth.RoleViewer},
		{"POST", groups, map[string]any{"name": "New"}, false, auth.RoleAdmin},
		{"GET", groups + "/" + group, nil, false, auth.RoleViewer},
		{"PUT", groups + "/" + group, map[string]any{"name": "Renamed"}, false, auth.RoleAdmin},
		{"DELETE", groups + "/" + group, nil, false, auth.RoleAdmin},
		{"PUT", groups + "/" + group + "/books", map[string]any{"bookIds": []int64{}}, false, auth.RoleAdmin},
	}
	send := func(c *client, k call) response {
		if k.upload {
			return c.upload(k.path, k.body.(string))
		}
		return c.do(k.method, k.path, k.body)
	}
	snapshot := func() string {
		var s string
		if err := e.db.QueryRowContext(context.Background(), `
			SELECT (SELECT count(*) FROM phonebooks) || '/' || (SELECT count(*) FROM phonebook_entries) || '/' ||
			       (SELECT count(*) FROM phone_groups) || '/' || (SELECT string_agg(name, ',' ORDER BY id) FROM phonebooks) || '/' ||
			       (SELECT string_agg(last_name, ',' ORDER BY id) FROM phonebook_entries) || '/' ||
			       (SELECT string_agg(name, ',' ORDER BY id) FROM phone_groups)`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	rank := map[auth.Role]int{auth.RoleViewer: 0, auth.RoleOperator: 1, auth.RoleAdmin: 2}
	before := snapshot()
	for _, k := range calls {
		for name, c := range map[string]*client{"viewer": viewer, "operator": operator} {
			role := auth.Role(name)
			if rank[role] >= rank[k.role] {
				continue
			}
			if r := send(c, k); r.code != http.StatusForbidden {
				t.Errorf("%s %s %s = %d %s, want 403", name, k.method, k.path, r.code, r.body)
			}
		}
	}
	if after := snapshot(); after != before {
		t.Fatalf("a refused request changed data:\n%s\n%s", before, after)
	}

	// What each role may do.
	viewer200 := []string{books, books + "/" + book, books + "/" + book + "/entries", groups, groups + "/" + group}
	for _, p := range viewer200 {
		for name, c := range map[string]*client{"viewer": viewer, "operator": operator, "admin": alice} {
			if r := c.do("GET", p, nil); r.code != http.StatusOK {
				t.Errorf("%s GET %s = %d %s", name, p, r.code, r.body)
			}
		}
	}
	eb := books + "/" + book + "/entries"
	created := rowID(operator.must(http.StatusCreated, "POST", eb, entryBody("Op", "6666")).json(t))
	operator.must(http.StatusOK, "PUT", eb+"/"+created, entryBody("Op2", "6667"))
	if r := operator.upload(books+"/"+book+"/import", csv); r.code != http.StatusOK {
		t.Errorf("operator import = %d %s", r.code, r.body)
	}
	operator.must(http.StatusOK, "GET", books+"/"+book+"/export", nil)
	operator.must(http.StatusNoContent, "DELETE", eb+"/"+created, nil)
	// The company book's entries are the operator's too.
	company := bookID(t, operator, "company")
	operator.must(http.StatusCreated, "POST", books+"/"+company+"/entries", entryBody("Co", "7777"))
	alice.must(http.StatusOK, "GET", books+"/"+book+"/export", nil)
}

// TestPhoneGroupMulticastAndPTT fails if a group's multicast channels or PTT
// settings are not stored and returned in order, an address outside the
// multicast range or inside 224.0.0.0/24, a port below 1024, a duplicate
// listen address, an eleventh channel, a PTT default channel the phone does
// not take or a PTT channel twice is accepted, if a paging group can name a
// channel that sends only, uses G.722 or does not exist, or if a paging
// group with a channel needs members; or if removing a channel leaves a
// paging group pointing at it.
func TestPhoneGroupMulticastAndPTT(t *testing.T) {
	p := newProvEnv(t, ProvConfig{})
	c := p.c
	g := c.must(http.StatusCreated, "POST", "/api/v1/phone-groups", map[string]any{"name": "Warehouse"}).json(t)
	path := "/api/v1/phone-groups/" + rowID(g)
	if mc := g["multicast"].([]any); len(mc) != 0 {
		t.Fatalf("new group multicast = %v", mc)
	}
	ch := func(label, addr string, port int, codec, role string, prio int) map[string]any {
		return map[string]any{"label": label, "address": addr, "port": port, "codec": codec, "role": role, "priority": prio}
	}
	put := func(want int, channels ...map[string]any) response {
		return c.must(want, "PUT", path+"/multicast", map[string]any{"channels": channels})
	}

	got := put(http.StatusOK, ch("Dock", "239.255.0.1", 5004, "PCMU", "both", 1), ch("Out", "239.255.0.2", 5006, "G722", "listen", 2)).json(t)
	mc := got["multicast"].([]any)
	if len(mc) != 2 || mc[0].(map[string]any)["label"] != "Dock" || mc[1].(map[string]any)["codec"] != "G722" {
		t.Fatalf("multicast = %v", mc)
	}
	dock := rowID(mc[0].(map[string]any))
	for name, bad := range map[string]map[string]any{
		"unicast":  ch("a", "10.0.0.1", 5004, "PCMU", "both", 1),
		"reserved": ch("a", "224.0.0.251", 5004, "PCMU", "both", 1),
		"ipv6":     ch("a", "ff02::1", 5004, "PCMU", "both", 1),
		"low port": ch("a", "239.1.1.1", 80, "PCMU", "both", 1),
		"codec":    ch("a", "239.1.1.1", 5004, "OPUS", "both", 1),
		"role":     ch("a", "239.1.1.1", 5004, "PCMU", "talk", 1),
		"priority": ch("a", "239.1.1.1", 5004, "PCMU", "both", 26),
		"no label": ch("", "239.1.1.1", 5004, "PCMU", "both", 1),
	} {
		if r := put(http.StatusBadRequest, bad); len(r.body) == 0 {
			t.Errorf("%s: empty error body", name)
		}
	}
	put(http.StatusBadRequest, ch("a", "239.1.1.1", 5004, "PCMU", "both", 1), ch("b", "239.1.1.1", 5004, "PCMA", "listen", 2))
	var many []map[string]any
	for i := range 11 {
		many = append(many, ch(fmt.Sprintf("c%d", i), fmt.Sprintf("239.1.1.%d", i+1), 5004, "PCMU", "both", 1))
	}
	put(http.StatusBadRequest, many...)
	c.must(http.StatusBadRequest, "PUT", path+"/multicast", map[string]any{})
	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/multicast", map[string]any{"channels": []any{}})

	// PTT.
	pttPath := path + "/ptt"
	good := map[string]any{"enabled": true, "address": "224.0.1.116:5001", "codec": "PCMU", "defaultChannel": 1,
		"channels": []map[string]any{{"channel": 1, "label": "All", "transmit": true, "subscribe": true}, {"channel": 24, "label": "Priority", "transmit": true, "subscribe": false}}}
	pg := c.must(http.StatusOK, "PUT", pttPath, good).json(t)
	ptt := pg["ptt"].(map[string]any)
	if ptt["enabled"] != true || len(ptt["channels"].([]any)) != 2 || ptt["defaultChannel"] != float64(1) {
		t.Fatalf("ptt = %v", ptt)
	}
	if len(pg["multicast"].([]any)) != 2 {
		t.Fatalf("setting PTT dropped the multicast channels: %v", pg["multicast"])
	}
	for name, mutate := range map[string]func(m map[string]any){
		"address": func(m map[string]any) { m["address"] = "10.0.0.1:5001" },
		"default": func(m map[string]any) { m["defaultChannel"] = 5 },
		"codec":   func(m map[string]any) { m["codec"] = "PCMA" },
		"twice": func(m map[string]any) {
			m["channels"] = []map[string]any{{"channel": 1, "label": "a", "transmit": true}, {"channel": 1, "label": "b", "transmit": true}}
		},
		"no transmit": func(m map[string]any) {
			m["channels"] = []map[string]any{{"channel": 1, "label": "a", "transmit": false}}
		},
	} {
		body := map[string]any{}
		for k, v := range good {
			body[k] = v
		}
		mutate(body)
		if r := c.do("PUT", pttPath, body); r.code != http.StatusBadRequest {
			t.Errorf("ptt %s: status %d, want 400", name, r.code)
		}
	}

	c.must(http.StatusNotFound, "PUT", "/api/v1/phone-groups/999999/ptt", good)

	// A paging group may page a listening G.711 channel, with no members.
	grp := func(channel any, want int) response {
		body := map[string]any{"name": "dock", "code": "*61", "members": []int{}, "multicastChannelId": channel}
		return c.must(want, "POST", "/api/v1/paging-groups", body)
	}
	c.must(http.StatusBadRequest, "POST", "/api/v1/paging-groups", map[string]any{"name": "none", "code": "*62", "members": []int{}})
	grp(999999, http.StatusBadRequest)
	chans := mc
	grp(json.Number(rowID(chans[1].(map[string]any))), http.StatusBadRequest) // G.722
	made := grp(json.Number(dock), http.StatusCreated).json(t)
	if made["multicastChannelId"] == nil || made["multicastChannel"].(map[string]any)["address"] != "239.255.0.1" {
		t.Fatalf("paging group = %v", made)
	}
	// Dropping the channel unlinks the paging group (its channel set to null).
	put(http.StatusOK, ch("Out", "239.255.0.2", 5006, "G722", "listen", 2))
	after := c.must(http.StatusOK, "GET", "/api/v1/paging-groups/"+rowID(made), nil).json(t)
	if after["multicastChannelId"] != nil {
		t.Fatalf("paging group still points at the removed channel: %v", after)
	}
}
