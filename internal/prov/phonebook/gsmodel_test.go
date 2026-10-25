package phonebook

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
)

func TestFormatForModelCapacity(t *testing.T) {
	for model, want := range map[string]int{
		"WP810": 500, "WP820": 500, "WP822": 500, "WP 825": 500,
		"WP816": 1000, "WP836": 1000, "WP856": 1000, "GRP2614": 1000, "": 1000,
	} {
		if f, _ := FormatForModel("grandstream", model); f.MaxEntries != want {
			t.Errorf("%q: MaxEntries %d, want %d", model, f.MaxEntries, want)
		}
	}
	if f, _ := FormatForModel("yealink", "WP822"); f.MaxEntries != 0 {
		t.Error("another vendor must keep its own capacity")
	}
}

func TestRenderBooksWP8xxCapacity(t *testing.T) {
	var in []phonebook.EntryInput
	for i := range 700 {
		in = append(in, phonebook.EntryInput{LastName: "L" + leftPad(i), Numbers: []phonebook.Number{num("work", "5"+leftPad(i))}})
	}
	books := []Book{{Name: "All", Entries: in}}
	for model, want := range map[string]int{"WP822": 500, "WP836": 700} {
		body, truncated, err := RenderBooks("grandstream", model, books)
		if err != nil {
			t.Fatal(err)
		}
		if n := bytes.Count(body, []byte("<Contact>")); n != want || truncated != (want < 700) {
			t.Errorf("%s: %d contacts, truncated %v; want %d", model, n, truncated, want)
		}
	}
}

// TestGrandstreamGroupsAndCompany pins pbgroup, Group and Company output.
func TestGrandstreamGroupsAndCompany(t *testing.T) {
	books := []Book{
		{Name: "Extensions", Entries: []phonebook.EntryInput{{FirstName: "Ann", LastName: "Lee", Numbers: []phonebook.Number{num("work", "1002")}}}},
		{Name: "Company", Entries: []phonebook.EntryInput{{FirstName: "Tom", LastName: "Smith", Company: "R&D", Numbers: []phonebook.Number{num("work", "0471 22 33 44")}}}},
	}
	body, _, err := RenderBooks("grandstream", "WP836", books)
	if err != nil {
		t.Fatal(err)
	}
	golden := filepath.Join("testdata", "grandstream_groups.xml")
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
		t.Errorf("differs from %s:\n%s", golden, body)
	}
	for _, w := range []string{"<pbgroup>", "<name>Company</name>", "<Group>2</Group>", "<Company>R&amp;D</Company>"} {
		if !strings.Contains(string(body), w) {
			t.Errorf("missing %s", w)
		}
	}
	wellFormed(t, body)
}
