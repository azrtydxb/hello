package prov

import (
	"net/http"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
	pb "github.com/azrtydxb/hello/internal/prov/phonebook"
)

func entry(last, n string) phonebook.EntryInput {
	k, _ := phonebook.Key(n)
	return phonebook.EntryInput{LastName: last, Numbers: []phonebook.Number{{Label: "work", Number: n, Key: k}}}
}

func twoBooks() []pb.Book {
	return []pb.Book{
		{Name: "Extensions", Entries: []phonebook.EntryInput{entry("Alice", "1001"), entry("Bob", "1002")}},
		{Name: "Company", Entries: []phonebook.EntryInput{entry("Carol", "+32470123456"), entry("Bob Again", "1002")}},
	}
}

// vendorCase is a phone of each vendor with the file its configuration is
// fetched as.
type vendorCase struct {
	vendor Vendor
	model  string
	mac    string
	config func(mac string) string
}

var vendorCases = []vendorCase{
	{Yealink, "T54W", macA, func(m string) string { return m + ".cfg" }},
	{Poly, "VVX 450", macA, func(m string) string { return m + "-hello.cfg" }},
	{Grandstream, "GRP2614", macA, func(m string) string { return "cfg" + m + ".xml" }},
	{Snom, "D785", macA, strings.ToUpper},
	{Fanvil, "X6U", macA, func(m string) string { return m + ".cfg" }},
}

// TestPhonebookEndpointAuth fails if an unknown token, disabled phone,
// plain-HTTP request or MAC mismatch is served a directory; if a denial
// answers other than an empty 404; if If-None-Match with the current ETag
// does not give 304; if the fetch is not audited with kind phonebook; or
// if two renders of the same data differ by a byte (spec P-4).
func TestPhonebookEndpointAuth(t *testing.T) {
	e := newEnv(t)
	p, tok := e.s.addPhone(Yealink, "T54W", macA)
	p.books = twoBooks()

	w := e.do(req{path: devPath(tok, "pb/0.xml")})
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/xml") {
		t.Fatalf("served: %d %q", w.Code, w.Header().Get("Content-Type"))
	}
	row := e.last()
	if row.Kind != KindPhonebook || row.Result != ResultServed || row.PhoneID != p.id {
		t.Fatalf("audit row %+v", row)
	}
	body := w.Body.String()
	if !strings.Contains(body, "<YealinkIPPhoneDirectory>") || strings.Count(body, "<Telephone>1002</Telephone>") != 1 ||
		!strings.Contains(body, "Carol") {
		t.Fatalf("merged directory wrong: %s", body)
	}
	if got := e.s.phone(p.id); got.lastFile != "pb/0.xml" || got.fetched == 0 {
		t.Fatalf("fetch not recorded: %+v", got)
	}

	etag := w.Header().Get("ETag")
	if w2 := e.do(req{path: devPath(tok, "pb/0.xml")}); w2.Body.String() != body || w2.Header().Get("ETag") != etag {
		t.Error("two renders differ")
	}
	if w = e.do(req{path: devPath(tok, "pb/0.xml"), inm: etag}); w.Code != http.StatusNotModified || w.Body.Len() != 0 || e.last().Result != ResultNotModified {
		t.Fatalf("If-None-Match: %d", w.Code)
	}

	// Denials are empty 404s.
	w = e.do(req{path: devPath(tok, "pb/0.xml"), proto: "http"})
	expectEmpty(t, e, w, http.StatusNotFound, ResultPlainHTTP)
	if !e.s.phone(p.id).exposed {
		t.Error("plain HTTP did not flag the token")
	}
	w = e.do(req{path: devPath("a2345678a2345678a2345678a2345678a2345678a2345678abcd", "pb/0.xml")})
	expectEmpty(t, e, w, http.StatusNotFound, ResultUnknownToken)
	for _, name := range []string{"pb/6.xml", "pb/x.xml", "pb/0.cfg", "pb/", "pb/../0.xml"} {
		w = e.do(req{path: devPath(tok, name)})
		if w.Code != http.StatusNotFound || w.Body.Len() != 0 {
			t.Errorf("%s: %d with %d bytes", name, w.Code, w.Body.Len())
		}
	}
	// Yealink's slots: 1 and 2 are the two books, 3 does not exist.
	for slot, want := range map[string]string{"1": "Alice", "2": "Carol"} {
		w = e.do(req{path: devPath(tok, "pb/"+slot+".xml")})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("slot %s: %d %s", slot, w.Code, w.Body.String())
		}
	}
	w = e.do(req{path: devPath(tok, "pb/3.xml")})
	expectEmpty(t, e, w, http.StatusNotFound, ResultNotFound)

	// A disabled phone is denied.
	p.enabled = false
	w = e.do(req{path: devPath(tok, "pb/0.xml")})
	expectEmpty(t, e, w, http.StatusNotFound, ResultNotAllowlisted)
}

// TestPhonebookPolyDirectoryName fails if Poly's <mac>-directory.xml is
// not served the merged set on GET, a PUT is not accepted and discarded, or
// another MAC's name is served (spec P-4).
func TestPhonebookPolyDirectoryName(t *testing.T) {
	e := newEnv(t)
	p, tok := e.s.addPhone(Poly, "VVX 450", macA)
	p.books = twoBooks()

	w := e.do(req{path: devPath(tok, macA+"-directory.xml"), ua: "FileTransport PolycomVVX-VVX_450-UA/6.4.0.1500"})
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<directory>") || !strings.Contains(w.Body.String(), "<ln>Carol</ln>") {
		t.Fatalf("GET: %d %s", w.Code, w.Body.String())
	}
	if row := e.last(); row.Kind != KindPhonebook || row.Result != ResultServed {
		t.Fatalf("audit row %+v", row)
	}
	w = e.do(req{method: http.MethodPut, path: devPath(tok, macA+"-directory.xml"), body: "<directory/>"})
	if w.Code != http.StatusNoContent || e.last().Result != ResultUploadDiscarded {
		t.Fatalf("PUT: %d", w.Code)
	}
	w = e.do(req{path: devPath(tok, macB+"-directory.xml")})
	expectEmpty(t, e, w, http.StatusNotFound, ResultMACMismatch)
	// Poly has one merged file: no separate sources.
	w = e.do(req{path: devPath(tok, "pb/1.xml")})
	expectEmpty(t, e, w, http.StatusNotFound, ResultNotFound)
}

// TestPhonebookNone fails if a phone with no books, or a vendor with no
// directory format, is served a directory, and pins the Yealink
// -contact.xml request to an empty 404 (spec P-4: served only if the lab
// shows a phone asks for it).
func TestPhonebookNone(t *testing.T) {
	e := newEnv(t)
	_, tok := e.s.addPhone(Snom, "D785", macA)
	expectEmpty(t, e, e.do(req{path: devPath(tok, "pb/0.xml")}), http.StatusNotFound, ResultNotFound)

	g, gtok := e.s.addPhone(Generic, "X", macB)
	g.books = twoBooks()
	expectEmpty(t, e, e.do(req{path: devPath(gtok, "pb/0.xml")}), http.StatusNotFound, ResultNotFound)

	y, ytok := e.s.addPhone(Yealink, "T54W", "805ec0000001")
	y.books = twoBooks()
	expectEmpty(t, e, e.do(req{path: devPath(ytok, "805ec0000001-contact.xml")}), http.StatusNotFound, ResultNotFound)
}

// TestPhonebookOutage fails if a store outage while reading the books
// is not a 503 with Retry-After.
func TestPhonebookOutage(t *testing.T) {
	e := newEnv(t)
	p, tok := e.s.addPhone(Fanvil, "X6U", macA)
	p.books = twoBooks()
	if w := e.do(req{path: devPath(tok, "pb/0.xml")}); w.Code != http.StatusOK {
		t.Fatalf("served %d", w.Code)
	}
	e.s.mu.Lock()
	e.s.outage = http.ErrHandlerTimeout
	e.s.mu.Unlock()
	if w := e.do(req{path: devPath(tok, "pb/0.xml")}); w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") == "" {
		t.Fatalf("outage: %d", w.Code)
	}
}

// TestProvisionedPhonebookKeys fails if any vendor's built-in template
// omits the key that points at the book URL (with the current token) or
// the refresh setting where one exists, leaves unused Yealink sources set,
// or writes the keys for a phone with no books (spec P-6).
func TestProvisionedPhonebookKeys(t *testing.T) {
	for _, vc := range vendorCases {
		t.Run(string(vc.vendor), func(t *testing.T) {
			e := newEnv(t)
			p, tok := e.s.addPhone(vc.vendor, vc.model, vc.mac)
			p.books = twoBooks()
			w := e.do(req{path: devPath(tok, vc.config(vc.mac))})
			if w.Code != http.StatusOK {
				t.Fatalf("config: %d", w.Code)
			}
			cfg := w.Body.String()
			base := "https://prov.hello.test/p/" + tok + "/pb/"
			for _, want := range wantKeys[vc.vendor] {
				want = strings.NewReplacer("{base}", base, "{host}", strings.TrimPrefix(base, "https://")).Replace(want)
				if !strings.Contains(cfg, want) {
					t.Errorf("config lacks %q:\n%s", want, cfg)
				}
			}

			// A phone with no books gets none of the keys.
			p.books = nil
			w = e.do(req{path: devPath(tok, vc.config(vc.mac))})
			if strings.Contains(w.Body.String(), "/pb/") {
				t.Errorf("a phone with no books is pointed at a directory:\n%s", w.Body.String())
			}
			for _, key := range noKeys[vc.vendor] {
				if strings.Contains(w.Body.String(), key) {
					t.Errorf("a phone with no books has %q", key)
				}
			}
		})
	}
}

var wantKeys = map[Vendor][]string{
	Yealink: {
		"features.remote_phonebook.enable = 1", "features.remote_phonebook.flash_time = 900",
		"remote_phonebook.data.1.url = {base}1.xml", "remote_phonebook.data.1.name = Extensions",
		"remote_phonebook.data.2.url = {base}2.xml", "remote_phonebook.data.2.name = Company",
		// The three unused sources are cleared.
		"remote_phonebook.data.3.url = \n", "remote_phonebook.data.4.url = \n", "remote_phonebook.data.5.name = \n",
	},
	Poly:        {`<dir dir.local.readonly="1"/>`},
	Grandstream: {"<P330>3</P330>", "<P331>{host}0.xml</P331>", "<P332>15</P332>"},
	Snom:        {"<dkey_directory perm=\"R\">url {base}0.xml</dkey_directory>"},
	Fanvil:      {"Remote Phonebook1 URL    :{base}0.xml", "Remote Phonebook Update  :15"},
}

var noKeys = map[Vendor][]string{
	Yealink:     {"remote_phonebook", "features.remote_phonebook"},
	Poly:        {"dir.local.readonly"},
	Grandstream: {"<P330>", "<P331>", "<P332>"},
	Snom:        {"dkey_directory"},
	Fanvil:      {"PHONEBOOK CONFIG MODULE", "Remote Phonebook"},
}

// TestPhonebookEveryVendorServed fails if any first-class vendor's slot 0
// does not answer with that vendor's directory root.
func TestPhonebookEveryVendorServed(t *testing.T) {
	roots := map[Vendor]string{Yealink: "<YealinkIPPhoneDirectory>", Poly: "<directory>", Grandstream: "<AddressBook>",
		Snom: "<SnomIPPhoneDirectory>", Fanvil: "<IPPhoneDirectory>"}
	for _, vc := range vendorCases {
		e := newEnv(t)
		p, tok := e.s.addPhone(vc.vendor, vc.model, vc.mac)
		p.books = twoBooks()
		w := e.do(req{path: devPath(tok, "pb/0.xml")})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), roots[vc.vendor]) {
			t.Errorf("%s: %d %s", vc.vendor, w.Code, w.Body.String())
		}
	}
}
