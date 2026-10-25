package provclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
	pb "github.com/azrtydxb/hello/internal/prov/phonebook"
)

// golden reads the vendor's golden directory of internal/prov/phonebook:
// the file Hello's renderer is held to byte for byte.
func golden(t *testing.T, vendor string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "internal", "prov", "phonebook", "testdata", vendor+".xml")) //nolint:gosec // test data
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var directoryVendors = []string{"yealink", "poly", "grandstream", "snom", "fanvil"}

// goldenNumbers are the numbers of the golden data set, sorted: whatever a
// format does with names and labels, every number must survive.
var goldenNumbers = []string{"+32 2 555 01 02", "+32470123456", "02 555 00 00", "0471 22 33 44", "1001", "1002", "1003"}

// TestProvclientDirectoryParsers (spec P-6) fails if any of the five
// parsers rejects its vendor's golden file, accepts another vendor's file,
// loses a contact, number, label or name, or accepts a file with an entry a
// phone could not use.
func TestProvclientDirectoryParsers(t *testing.T) {
	// How many contacts each format makes of the five entries (Poly, Snom
	// and Fanvil have one per number, the others one per entry).
	count := map[string]int{"yealink": 5, "poly": 7, "grandstream": 5, "snom": 7, "fanvil": 7}
	for _, vendor := range directoryVendors {
		t.Run(vendor, func(t *testing.T) {
			cs, err := ParseDirectory(vendor, golden(t, vendor))
			if err != nil {
				t.Fatalf("its own golden file: %v", err)
			}
			if len(cs) != count[vendor] {
				t.Errorf("%d contacts, want %d", len(cs), count[vendor])
			}
			if got := DirectoryNumbers(cs); !slices.Equal(got, goldenNumbers) {
				t.Errorf("numbers = %q, want %q", got, goldenNumbers)
			}
			// No other vendor's parser takes this file.
			for _, other := range directoryVendors {
				if other == vendor {
					continue
				}
				if cs, err := ParseDirectory(other, golden(t, vendor)); err == nil {
					t.Errorf("the %s parser accepted a %s file: %d contacts", other, vendor, len(cs))
				}
			}
			// Nor a document that is not XML, or empty.
			for _, bad := range []string{"", "not xml", string(golden(t, vendor))[:len(golden(t, vendor))/2]} {
				if _, err := ParseDirectory(vendor, []byte(bad)); err == nil {
					t.Errorf("accepted %.20q", bad)
				}
			}
		})
	}

	t.Run("names and labels", func(t *testing.T) {
		find := func(cs []Contact, pred func(Contact) bool) Contact {
			t.Helper()
			for _, c := range cs {
				if pred(c) {
					return c
				}
			}
			t.Fatal("contact not found")
			return Contact{}
		}
		// Yealink: one Name, three numbers, no labels.
		y, _ := ParseYealinkDirectory(golden(t, "yealink"))
		tom := find(y, func(c Contact) bool { return strings.HasPrefix(c.Name, "Tom Smith & Sons") })
		if tom.Name != "Tom Smith & Sons (R&D)" || len(tom.Numbers) != 3 || tom.Numbers[1].Number != "+32470123456" {
			t.Errorf("yealink Tom: %+v", tom)
		}
		if acme := find(y, func(c Contact) bool { return strings.HasPrefix(c.Name, "Acme") }); acme.Name != "Acme <Corp>" {
			t.Errorf("yealink escaping: %+v", acme)
		}
		// Poly: first, last and the label of each number.
		p, _ := ParsePolyDirectory(golden(t, "poly"))
		var labels []string
		for _, c := range p {
			if c.First == "Tom" && c.Last == "Smith & Sons" {
				labels = append(labels, c.Numbers[0].Label+"="+c.Numbers[0].Number)
			}
		}
		if want := []string{"work=1001", "mobile=+32470123456", "home=0471 22 33 44"}; !slices.Equal(labels, want) {
			t.Errorf("poly labels = %q, want %q", labels, want)
		}
		// Grandstream: first, last and typed phones.
		g, _ := ParseGrandstreamDirectory(golden(t, "grandstream"))
		tom = find(g, func(c Contact) bool { return c.First == "Tom" })
		if tom.Last != "Smith & Sons" || len(tom.Numbers) != 3 || tom.Numbers[1] != (ContactNumber{Label: "mobile", Number: "+32470123456"}) {
			t.Errorf("grandstream Tom: %+v", tom)
		}
		if acme := find(g, func(c Contact) bool { return c.Last == "Acme <Corp>" }); acme.Numbers[0].Label != "" {
			t.Errorf("grandstream: an unlabelled number got %q", acme.Numbers[0].Label)
		}
		// Snom and Fanvil: a labelled name per number.
		for _, vendor := range []string{"snom", "fanvil"} {
			cs, _ := ParseDirectory(vendor, golden(t, vendor))
			find(cs, func(c Contact) bool {
				return c.Name == "Tom Smith & Sons (R&D) (mobile)" && c.Numbers[0].Number == "+32470123456"
			})
		}
	})

	t.Run("entries a phone could not use", func(t *testing.T) {
		bad := map[string]string{
			"yealink":     `<YealinkIPPhoneDirectory><DirectoryEntry><Name>A</Name></DirectoryEntry></YealinkIPPhoneDirectory>`,
			"poly":        `<directory><item_list><item><ln>A</ln><fn></fn><ct></ct><lb>work</lb></item></item_list></directory>`,
			"grandstream": `<AddressBook><version>1</version><Contact><LastName>A</LastName><FirstName></FirstName></Contact></AddressBook>`,
			"snom":        `<SnomIPPhoneDirectory><DirectoryEntry><Name></Name><Telephone>1</Telephone></DirectoryEntry></SnomIPPhoneDirectory>`,
			"fanvil":      `<IPPhoneDirectory><DirectoryEntry><Name>A</Name><Telephone>1</Telephone><Telephone>2</Telephone></DirectoryEntry></IPPhoneDirectory>`,
		}
		for vendor, doc := range bad {
			if _, err := ParseDirectory(vendor, []byte(doc)); err == nil {
				t.Errorf("%s accepted %s", vendor, doc)
			}
		}
		if _, err := ParseGrandstreamDirectory([]byte(`<AddressBook><Contact><LastName>A</LastName><Phone><phonenumber>1</phonenumber></Phone></Contact></AddressBook>`)); err == nil {
			t.Error("grandstream accepted a book without a version")
		}
		if _, err := ParseDirectory("generic", []byte("<x/>")); err == nil {
			t.Error("a vendor with no directory format was parsed")
		}
	})
}

// TestProvclientDirectoryRoundTrip fails if what Hello's renderer writes
// for a book is not what each parser reads back: every number of every
// entry, from the same data as the entries were rendered from.
func TestProvclientDirectoryRoundTrip(t *testing.T) {
	entries := []phonebook.EntryInput{
		{FirstName: "Ada", LastName: "Lovelace", Numbers: []phonebook.Number{{Label: "work", Number: "1001"}, {Label: "mobile", Number: "+441234567890"}}},
		{Company: "Acme & Sons", Numbers: []phonebook.Number{{Label: "work", Number: "+3225550100"}}},
		{FirstName: "Zoë", LastName: "Müller", Numbers: []phonebook.Number{{Label: "home", Number: "0471 22 33 44"}}},
	}
	var want []string
	for _, e := range entries {
		for _, n := range e.Numbers {
			want = append(want, n.Number)
		}
	}
	slices.Sort(want)
	for _, vendor := range directoryVendors {
		body, _, err := pb.Render(vendor, entries)
		if err != nil {
			t.Fatal(err)
		}
		cs, err := ParseDirectory(vendor, body)
		if err != nil {
			t.Fatalf("%s: Hello's own render does not parse: %v\n%s", vendor, err, body)
		}
		if got := DirectoryNumbers(cs); !slices.Equal(got, want) {
			t.Errorf("%s: numbers = %q, want %q", vendor, got, want)
		}
	}
}

// TestClientDirectory fails if the fetch does not send the phone's
// User-Agent, parses another format than the vendor's, or takes an error
// status or an empty body for a directory.
func TestClientDirectory(t *testing.T) {
	var ua string
	status := http.StatusOK
	body := golden(t, "yealink")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer srv.Close()
	c := Client{HTTP: srv.Client()}
	m := model(t, "yealink")
	cs, err := c.Directory(context.Background(), m, mac, srv.URL+"/p/x/pb/0.xml")
	if err != nil || len(cs) != 5 {
		t.Fatalf("directory: %d contacts, %v", len(cs), err)
	}
	if ua != m.UserAgent(mac) {
		t.Errorf("User-Agent = %q, want %q", ua, m.UserAgent(mac))
	}
	if _, err := c.Directory(context.Background(), model(t, "poly"), mac, srv.URL); err == nil {
		t.Error("a Yealink directory parsed as Poly's")
	}
	status = http.StatusNotFound
	if _, err := c.Directory(context.Background(), m, mac, srv.URL); err == nil {
		t.Error("a 404 was taken for a directory")
	}
	status, body = http.StatusOK, nil
	if _, err := c.Directory(context.Background(), m, mac, srv.URL); err == nil {
		t.Error("an empty body was taken for a directory")
	}
}
