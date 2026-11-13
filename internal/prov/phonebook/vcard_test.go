package phonebook

import (
	"bytes"
	"strconv"
	"strings"
	"testing"

	"github.com/azrtydxb/hello/internal/phonebook"
)

func bigBook(n int) []Book {
	var in []phonebook.EntryInput
	for i := range n {
		in = append(in, phonebook.EntryInput{LastName: "L" + leftPad(i), Numbers: []phonebook.Number{num("work", "5"+leftPad(i))}})
	}
	return []Book{{Name: "All", Entries: in}}
}

// TestGrandstreamPhonebookCap fails if a WP822 directory exceeds 500 entries
// or a WP8x6 directory exceeds 1000, if truncation drops the earlier books
// instead of the tail of the delivery order, if the vCard slot exceeds the
// same caps or is malformed, or if Capacity misreports what is dropped.
func TestGrandstreamPhonebookCap(t *testing.T) {
	for _, c := range []struct {
		model            string
		delivered, limit int
	}{{"WP822", 1200, 500}, {"WP836", 1200, 1000}, {"WP836", 800, 1000}} {
		books := bigBook(c.delivered)
		body, truncated, err := RenderBooks("grandstream", c.model, books)
		if err != nil {
			t.Fatal(err)
		}
		want := min(c.delivered, c.limit)
		if n := bytes.Count(body, []byte("<Contact>")); n != want || truncated != (c.delivered > c.limit) {
			t.Errorf("%s: %d contacts, truncated %v; want %d", c.model, n, truncated, want)
		}
		d, limit, dropped := Capacity("grandstream", c.model, books)
		if d != c.delivered || limit != c.limit || dropped != c.delivered-want {
			t.Errorf("%s: Capacity = %d, %d, %d", c.model, d, limit, dropped)
		}
		if c.model == "WP836" {
			vc, vtrunc, err := RenderVCard("grandstream", c.model, books)
			if err != nil {
				t.Fatal(err)
			}
			if n := bytes.Count(vc, []byte("BEGIN:VCARD\r\n")); n != want || vtrunc != (c.delivered > c.limit) {
				t.Errorf("vCard: %d cards, truncated %v; want %d", n, vtrunc, want)
			}
		}
	}
	// The earlier book wins: with 3 slots left, the extensions book keeps its
	// entries and the company book loses its tail.
	ext := Book{Name: "Extensions"}
	for i := range 600 {
		ext.Entries = append(ext.Entries, phonebook.EntryInput{LastName: "E" + leftPad(i), Numbers: []phonebook.Number{num("work", "1"+leftPad(i))}})
	}
	co := Book{Name: "Company"}
	for i := range 600 {
		co.Entries = append(co.Entries, phonebook.EntryInput{LastName: "C" + leftPad(i), Numbers: []phonebook.Number{num("work", "2"+leftPad(i))}})
	}
	body, _, _ := RenderBooks("grandstream", "WP822", []Book{ext, co})
	if !bytes.Contains(body, []byte("<LastName>E0599</LastName>")) && !bytes.Contains(body, []byte("<LastName>E"+leftPad(499)+"</LastName>")) {
		t.Error("the first book lost entries to the cap")
	}
	if bytes.Contains(body, []byte("<LastName>C")) {
		t.Error("a later book was kept over an earlier one")
	}
}

func TestVCardModel(t *testing.T) {
	for model, want := range map[string]bool{"WP816": true, "WP826": true, "WP836": true, "wp 836": true,
		"WP856": false, "WP822": false, "WP820": false, "GRP2614": false} {
		if got := VCardModel("grandstream", model); got != want {
			t.Errorf("%s: %v", model, got)
		}
	}
	if VCardModel("yealink", "WP836") {
		t.Error("another vendor was offered the slot")
	}
}

func TestRenderVCard(t *testing.T) {
	books := []Book{{Name: "Extensions", Entries: []phonebook.EntryInput{
		{FirstName: "Ann", LastName: "Lee, Jr;", Company: "R&D", Numbers: []phonebook.Number{num("work", "1002"), num("mobile", "+32470123456")}},
		{LastName: "Zed\r\nEND:VCARD", Numbers: []phonebook.Number{num("home", "1003")}},
	}}}
	body, _, err := RenderVCard("grandstream", "WP836", books)
	if err != nil {
		t.Fatal(err)
	}
	got := string(body)
	for _, want := range []string{
		"BEGIN:VCARD\r\nVERSION:3.0\r\nN:Lee\\, Jr\\;;Ann;;;\r\nFN:Ann Lee\\, Jr\\;\r\nORG:R&D\r\nTEL;TYPE=WORK:1002\r\nTEL;TYPE=CELL:+32470123456\r\nEND:VCARD\r\n",
		"TEL;TYPE=HOME:1003\r\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	// A control character in a name cannot end the card early.
	var begins, ends int
	for _, l := range strings.Split(got, "\r\n") {
		switch l {
		case "BEGIN:VCARD":
			begins++
		case "END:VCARD":
			ends++
		}
	}
	if begins != 2 || ends != 2 {
		t.Errorf("cards injected (%d begin, %d end):\n%s", begins, ends, got)
	}
	for _, l := range strings.Split(strings.TrimSuffix(got, "\r\n"), "\r\n") {
		if len(l) > 75 {
			t.Errorf("line over 75 octets: %q", l)
		}
	}
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Error("a bare newline in the file")
	}
	if _, _, err := RenderVCard("generic", "x", books); err == nil {
		t.Error("a vendor without a directory format rendered")
	}
}

func TestVCardFolding(t *testing.T) {
	long := strings.Repeat("é", 80) // 160 octets, two-byte runes
	var b bytes.Buffer
	vcLine(&b, "FN:"+long)
	lines := strings.Split(strings.TrimSuffix(b.String(), "\r\n"), "\r\n")
	if len(lines) < 3 {
		t.Fatalf("not folded: %q", lines)
	}
	var joined string
	for i, l := range lines {
		if len(l) > 75 {
			t.Errorf("line %d has %d octets", i, len(l))
		}
		if i > 0 {
			if l[0] != ' ' {
				t.Errorf("continuation %d lacks the space", i)
			}
			l = l[1:]
		}
		joined += l
	}
	if joined != "FN:"+long {
		t.Error("unfolding does not restore the line (a rune was split)")
	}
	_ = strconv.Itoa
}
