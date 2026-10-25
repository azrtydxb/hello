// Package phonebook renders a phone's address books in each vendor's remote
// phonebook XML (spec phonebooks-ptt, P-4 and P-6). The formats are
// vendor-fixed and the data set large, so each vendor has an encoder here
// and not an administrator template. Output is deterministic (sorted by
// last name, first name and number) so a directory's ETag is stable.
//
// Vendor names are the strings of internal/prov's Vendor type; this package
// does not import internal/prov, which imports it.
package phonebook

import (
	"bytes"
	"encoding/xml"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/phonebook"
)

// Book is one book of a phone's delivered set, with its entries.
type Book struct {
	Name    string
	Entries []phonebook.EntryInput
}

// MaxSources is how many separate sources the vendors that take several
// (Yealink) are given; further books are merged into the last one.
const MaxSources = 5

// MaxGrandstreamEntries is the Grandstream WP8x6's phonebook capacity.
const MaxGrandstreamEntries = 1000

// Format is how a vendor takes a directory.
type Format struct {
	// ContentType of the XML.
	ContentType string
	// MaxNameRunes is the vendor's display name limit (spec vendor
	// reference); longer names are cut by rune count.
	MaxNameRunes int
	// MaxEntries is the most entries (contacts) the vendor takes; zero is
	// no limit.
	MaxEntries int
	// Sources is the number of separate sources the vendor can point at;
	// 1 means one merged file.
	Sources int

	encode func(b *bytes.Buffer, recs []record, maxName int)
}

const ctXML = "application/xml; charset=utf-8"

var formats = map[string]Format{
	"yealink":     {ContentType: ctXML, MaxNameRunes: 99, Sources: MaxSources, encode: encodeYealink},
	"poly":        {ContentType: ctXML, MaxNameRunes: 40, Sources: 1, encode: encodePoly},
	"grandstream": {ContentType: ctXML, MaxNameRunes: 64, MaxEntries: MaxGrandstreamEntries, Sources: 1, encode: encodeGrandstream},
	"snom":        {ContentType: ctXML, MaxNameRunes: 64, Sources: 1, encode: encodeSnom},
	"fanvil":      {ContentType: ctXML, MaxNameRunes: 64, Sources: 1, encode: encodeFanvil},
}

// FormatFor returns the vendor's directory format; false for a vendor with
// none (generic phones).
func FormatFor(vendor string) (Format, bool) {
	f, ok := formats[vendor]
	return f, ok
}

// Merge flattens books in order into one entry list, dropping every number
// whose lookup key an earlier book (or an earlier entry) already has, and
// every entry left with no number: the earlier book wins (spec P-2). A
// number with no key cannot be compared and is always kept.
func Merge(books []Book) []phonebook.EntryInput {
	seen := map[string]bool{}
	var out []phonebook.EntryInput
	for _, b := range books {
		for _, e := range b.Entries {
			var nums []phonebook.Number
			for _, n := range e.Numbers {
				if n.Key == "" || !seen[n.Key] {
					nums = append(nums, n)
				}
			}
			if len(nums) == 0 {
				continue
			}
			for _, n := range nums {
				if n.Key != "" {
					seen[n.Key] = true
				}
			}
			e.Numbers = nums
			out = append(out, e)
		}
	}
	return out
}

// Group reduces books to at most max sources: the first max-1 stay as they
// are and the rest are concatenated into the last, named after its first
// book. Each source is a separate directory on the phone, so a number can
// appear in more than one.
func Group(books []Book, max int) []Book {
	if max < 1 || len(books) <= max {
		return books
	}
	out := append([]Book(nil), books[:max-1]...)
	rest := books[max-1:]
	last := Book{Name: rest[0].Name + " and more"}
	for _, b := range rest {
		last.Entries = append(last.Entries, b.Entries...)
	}
	return append(out, last)
}

// record is one directory record.
type record struct {
	first, last, company string
	numbers              []phonebook.Number
}

// Render renders entries (already merged in priority order) as the vendor's
// directory. Entries past the vendor's capacity are dropped from the end of
// the priority order, so the earlier books win; truncated reports that.
func Render(vendor string, entries []phonebook.EntryInput) (body []byte, truncated bool, err error) {
	f, ok := formats[vendor]
	if !ok {
		return nil, false, errNoFormat(vendor)
	}
	if f.MaxEntries > 0 && len(entries) > f.MaxEntries {
		entries, truncated = entries[:f.MaxEntries], true
	}
	recs := make([]record, 0, len(entries))
	for _, e := range entries {
		r := record{first: clean(e.FirstName), last: clean(e.LastName), company: clean(e.Company), numbers: cleanNumbers(e.Numbers)}
		if r.first == "" && r.last == "" {
			r.last, r.company = r.company, ""
		}
		recs = append(recs, r)
	}
	sort.SliceStable(recs, func(i, j int) bool { return less(recs[i], recs[j]) })
	var b bytes.Buffer
	f.encode(&b, recs, f.MaxNameRunes)
	return b.Bytes(), truncated, nil
}

type errNoFormat string

func (e errNoFormat) Error() string { return "phonebook: no directory format for vendor " + string(e) }

func less(a, b record) bool {
	if x, y := strings.ToLower(a.last), strings.ToLower(b.last); x != y {
		return x < y
	}
	if x, y := strings.ToLower(a.first), strings.ToLower(b.first); x != y {
		return x < y
	}
	return firstNumber(a) < firstNumber(b)
}

func firstNumber(r record) string {
	if len(r.numbers) == 0 {
		return ""
	}
	return r.numbers[0].Number
}

func cleanNumbers(ns []phonebook.Number) []phonebook.Number {
	out := make([]phonebook.Number, 0, len(ns))
	for _, n := range ns {
		if num := clean(n.Number); num != "" {
			out = append(out, phonebook.Number{Label: n.Label, Number: num, Key: n.Key})
		}
	}
	return out
}

// clean drops control characters and surrounding space.
func clean(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == utf8.RuneError {
			return -1
		}
		return r
	}, s))
}

// cut shortens s to at most max runes.
func cut(s string, max int) string {
	if max <= 0 || utf8.RuneCountInString(s) <= max {
		return s
	}
	return strings.TrimSpace(string([]rune(s)[:max]))
}

// esc is s as XML text.
func esc(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// displayName is "First Last" with the company in brackets; a non-empty
// label is appended in brackets as well, so several numbers of one entry
// stay distinguishable in a format that has one number per entry. The
// result is at most max runes, the label kept.
func displayName(r record, label string, max int) string {
	name := strings.TrimSpace(r.first + " " + r.last)
	if r.company != "" {
		name += " (" + r.company + ")"
	}
	if label == "" {
		return cut(name, max)
	}
	suffix := " (" + label + ")"
	return cut(name, max-utf8.RuneCountInString(suffix)) + suffix
}

const header = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"

func encodeYealink(b *bytes.Buffer, recs []record, maxName int) {
	b.WriteString(header + "<YealinkIPPhoneDirectory>\n")
	for _, r := range recs {
		b.WriteString("  <DirectoryEntry>\n    <Name>" + esc(displayName(r, "", maxName)) + "</Name>\n")
		for _, n := range r.numbers {
			b.WriteString("    <Telephone>" + esc(n.Number) + "</Telephone>\n")
		}
		b.WriteString("  </DirectoryEntry>\n")
	}
	b.WriteString("</YealinkIPPhoneDirectory>\n")
}

// encodePoly writes the phone's local directory format, one item per
// number with the number's label in lb.
func encodePoly(b *bytes.Buffer, recs []record, maxName int) {
	b.WriteString(`<?xml version="1.0" standalone="yes"?>` + "\n<directory>\n  <item_list>\n")
	for _, r := range recs {
		for _, n := range r.numbers {
			b.WriteString("    <item>\n      <ln>" + esc(cut(r.last, maxName)) + "</ln>\n      <fn>" + esc(cut(r.first, maxName)) +
				"</fn>\n      <ct>" + esc(n.Number) + "</ct>\n      <lb>" + esc(n.Label) + "</lb>\n    </item>\n")
		}
	}
	b.WriteString("  </item_list>\n</directory>\n")
}

// grandstreamTypes maps a number label to the Phone type attribute the
// Grandstream phonebook format knows; "other" has none.
var grandstreamTypes = map[string]string{"work": "Work", "mobile": "Mobile", "home": "Home"}

func encodeGrandstream(b *bytes.Buffer, recs []record, maxName int) {
	b.WriteString(header + "<AddressBook>\n  <version>1</version>\n")
	for _, r := range recs {
		b.WriteString("  <Contact>\n    <LastName>" + esc(cut(r.last, maxName)) + "</LastName>\n    <FirstName>" + esc(cut(r.first, maxName)) + "</FirstName>\n")
		for _, n := range r.numbers {
			attr := ""
			if t := grandstreamTypes[n.Label]; t != "" {
				attr = ` type="` + t + `"`
			}
			b.WriteString("    <Phone" + attr + ">\n      <phonenumber>" + esc(n.Number) + "</phonenumber>\n      <accountindex>1</accountindex>\n    </Phone>\n")
		}
		b.WriteString("  </Contact>\n")
	}
	b.WriteString("</AddressBook>\n")
}

// encodeFlat writes the one-number-per-entry format Snom and Fanvil share.
func encodeFlat(b *bytes.Buffer, root string, recs []record, maxName int) {
	b.WriteString(header + "<" + root + ">\n")
	for _, r := range recs {
		for _, n := range r.numbers {
			label := ""
			if len(r.numbers) > 1 {
				label = n.Label
			}
			b.WriteString("  <DirectoryEntry>\n    <Name>" + esc(displayName(r, label, maxName)) + "</Name>\n    <Telephone>" + esc(n.Number) + "</Telephone>\n  </DirectoryEntry>\n")
		}
	}
	b.WriteString("</" + root + ">\n")
}

func encodeSnom(b *bytes.Buffer, recs []record, maxName int) {
	encodeFlat(b, "SnomIPPhoneDirectory", recs, maxName)
}

func encodeFanvil(b *bytes.Buffer, recs []record, maxName int) {
	encodeFlat(b, "IPPhoneDirectory", recs, maxName)
}
