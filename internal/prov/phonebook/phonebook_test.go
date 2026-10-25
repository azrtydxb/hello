package phonebook

import (
	"bytes"
	"encoding/xml"
	"errors"
	"flag"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/phonebook"
)

var update = flag.Bool("update", false, "rewrite the golden files")

func num(label, n string) phonebook.Number {
	k, _ := phonebook.Key(n)
	return phonebook.Number{Label: label, Number: n, Key: k}
}

// fixed is the data set every vendor renders: accents, an ampersand, a
// control character, a name over every vendor limit, an entry with three
// numbers and a company-only entry, given out of order.
func fixed() []phonebook.EntryInput {
	return []phonebook.EntryInput{
		{FirstName: "Zoë", LastName: "Müller", Numbers: []phonebook.Number{num("work", "+32 2 555 01 02")}},
		{FirstName: "Tom", LastName: "Smith & Sons\x07", Company: "R&D", Numbers: []phonebook.Number{num("work", "1001"), num("mobile", "+32470123456"), num("home", "0471 22 33 44")}},
		{Company: "Acme <Corp>", Numbers: []phonebook.Number{num("other", "02 555 00 00")}},
		{FirstName: "Ann", LastName: strings.Repeat("Ü", 120), Numbers: []phonebook.Number{num("work", "1002")}},
		{FirstName: "Bob", LastName: "Adams", Numbers: []phonebook.Number{num("work", "1003")}},
	}
}

func TestPhonebookVendorRender(t *testing.T) {
	for _, vendor := range []string{"yealink", "poly", "grandstream", "snom", "fanvil"} {
		t.Run(vendor, func(t *testing.T) {
			body, truncated, err := Render(vendor, fixed())
			if err != nil || truncated {
				t.Fatalf("Render: truncated=%v err=%v", truncated, err)
			}
			golden := filepath.Join("testdata", vendor+".xml")
			if *update {
				if err := os.WriteFile(golden, body, 0o644); err != nil { //nolint:gosec // test data
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden) //nolint:gosec // test data
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(body, want) {
				t.Errorf("%s differs from %s:\n%s", vendor, golden, body)
			}
			wellFormed(t, body)
			if bytes.ContainsRune(body, 0x07) {
				t.Error("a control character reached the XML")
			}
			f, _ := FormatFor(vendor)
			for _, name := range textsNamed(t, body, nameTags[vendor]) {
				if n := utf8.RuneCountInString(name); n > f.MaxNameRunes {
					t.Errorf("name of %d runes exceeds the limit %d", n, f.MaxNameRunes)
				}
			}
			// No in-limit entry is dropped: every short name is present.
			for _, name := range []string{"Adams", "Zoë"} {
				if !strings.Contains(string(body), name) {
					t.Errorf("entry %q missing", name)
				}
			}
			again, _, _ := Render(vendor, fixed())
			if !bytes.Equal(body, again) {
				t.Error("two renders of the same data differ")
			}
		})
	}
}

// nameTags are the elements that hold a display name per vendor.
var nameTags = map[string]string{
	"yealink": "Name", "snom": "Name", "fanvil": "Name", "poly": "ln", "grandstream": "LastName",
}

func wellFormed(t *testing.T, body []byte) {
	t.Helper()
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		if _, err := d.Token(); errors.Is(err, io.EOF) {
			return
		} else if err != nil {
			t.Fatalf("not well-formed: %v", err)
		}
	}
}

func textsNamed(t *testing.T, body []byte, tag string) []string {
	t.Helper()
	var out []string
	d := xml.NewDecoder(bytes.NewReader(body))
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == tag {
			var s string
			if err := d.DecodeElement(&s, &se); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
	}
}

func TestSortedByLastFirstNumber(t *testing.T) {
	body, _, _ := Render("yealink", fixed())
	names := textsNamed(t, body, "Name")
	if len(names) != 5 || !strings.HasPrefix(names[0], "Acme") || !strings.HasPrefix(names[1], "Bob Adams") {
		t.Fatalf("not sorted by last name: %q", names)
	}
}

func TestMultipleNumbersKeepLabels(t *testing.T) {
	body, _, _ := Render("snom", fixed())
	for _, want := range []string{"(work)", "(mobile)", "(home)"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("snom output lacks a %s entry", want)
		}
	}
	y, _, _ := Render("yealink", fixed())
	if n := strings.Count(string(y), "<Telephone>"); n != 7 {
		t.Errorf("yealink has %d numbers, want 7 in repeated Telephone elements", n)
	}
}

// TestGrandstreamCapacity fails if the Grandstream directory exceeds the
// WP8x6's 1000 entries, or the cut is not taken from the end of the
// priority order.
func TestGrandstreamCapacity(t *testing.T) {
	var in []phonebook.EntryInput
	for i := range 1500 {
		in = append(in, phonebook.EntryInput{LastName: "L" + leftPad(i), Numbers: []phonebook.Number{num("work", "5"+leftPad(i))}})
	}
	body, truncated, err := Render("grandstream", in)
	if err != nil || !truncated {
		t.Fatalf("truncated=%v err=%v", truncated, err)
	}
	if n := bytes.Count(body, []byte("<Contact>")); n != MaxGrandstreamEntries {
		t.Fatalf("%d contacts, want %d", n, MaxGrandstreamEntries)
	}
	if !bytes.Contains(body, []byte("L0999<")) || bytes.Contains(body, []byte("L1000<")) {
		t.Error("the cut did not keep the first 1000 of the priority order")
	}
	if _, truncated, _ := Render("yealink", in); truncated {
		t.Error("yealink has no capacity limit here")
	}
}

func leftPad(i int) string {
	s := strconv.Itoa(i)
	return strings.Repeat("0", 4-len(s)) + s
}

func TestMergeEarlierBookWins(t *testing.T) {
	ext := Book{Name: "Extensions", Entries: []phonebook.EntryInput{{LastName: "Alice", Numbers: []phonebook.Number{num("work", "1001")}}}}
	co := Book{Name: "Company", Entries: []phonebook.EntryInput{
		{LastName: "Alice Again", Numbers: []phonebook.Number{num("work", "1001")}},
		{LastName: "Carol", Numbers: []phonebook.Number{num("work", "1001"), num("mobile", "+32470123456")}},
	}}
	got := Merge([]Book{ext, co})
	if len(got) != 2 || got[0].LastName != "Alice" || got[1].LastName != "Carol" {
		t.Fatalf("merged %+v", got)
	}
	if len(got[1].Numbers) != 1 || got[1].Numbers[0].Number != "+32470123456" {
		t.Errorf("the duplicate number was kept: %+v", got[1].Numbers)
	}
}

func TestGroup(t *testing.T) {
	var books []Book
	for i := range 7 {
		books = append(books, Book{Name: "B" + strconv.Itoa(i), Entries: []phonebook.EntryInput{{LastName: strconv.Itoa(i)}}})
	}
	g := Group(books, MaxSources)
	if len(g) != MaxSources || g[4].Name != "B4 and more" || len(g[4].Entries) != 3 {
		t.Fatalf("grouped %+v", g)
	}
	if len(Group(books[:3], MaxSources)) != 3 {
		t.Error("a short set must stay as it is")
	}
}

func TestUnknownVendor(t *testing.T) {
	if _, _, err := Render("generic", fixed()); err == nil {
		t.Error("generic has no directory format")
	}
}
