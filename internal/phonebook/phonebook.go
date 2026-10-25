// Package phonebook is the address-book data model of spec phonebooks-ptt:
// books and their entries, the number lookup key and the CSV form entries
// are imported and exported in. Persistence is internal/store.
package phonebook

import (
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Kind is what a book is for.
type Kind string

// The four kinds of book (spec P-1). Company and Extensions exist exactly
// once; the extensions book has no stored entries.
const (
	KindCompany    Kind = "company"
	KindNamed      Kind = "named"
	KindExtensions Kind = "extensions"
	KindPersonal   Kind = "personal"
)

// Limits (spec P-17).
const (
	MaxEntriesPerBook  = 5000
	MaxPersonalEntries = 1000
	MaxNamedBooks      = 100
	MaxBooksPerGroup   = 5
	MaxIndexEntries    = 200000
	MaxNumbersPerEntry = 3
	MaxNameLen         = 60 // first name, last name and company, in characters
	MaxNumberLen       = 32
	MaxNoteLen         = 255
	MaxBookNameLen     = 100
	MaxDescriptionLen  = 255
	MaxCSVBytes        = 2 << 20
	MaxCSVRows         = 5000
)

// Labels a number can carry.
const (
	LabelWork   = "work"
	LabelMobile = "mobile"
	LabelHome   = "home"
	LabelOther  = "other"
)

// Labels lists the number labels in the order CSV columns use.
var Labels = []string{LabelWork, LabelMobile, LabelHome, LabelOther}

// Book is an address book.
type Book struct {
	ID         int64     `json:"id"`
	Kind       Kind      `json:"kind"`
	Name       string    `json:"name"`
	Version    int64     `json:"version"`
	EntryCount int       `json:"entryCount"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// Number is one number of an entry: a label and the number as typed. Key is
// its lookup key, empty when the number cannot be resolved; it is stored
// but not part of the API.
type Number struct {
	Label  string `json:"label"`
	Number string `json:"number"`
	Key    string `json:"-"`
}

// EntryInput is what an administrator or a user writes for an entry.
type EntryInput struct {
	FirstName string   `json:"firstName"`
	LastName  string   `json:"lastName"`
	Company   string   `json:"company"`
	Numbers   []Number `json:"numbers"`
	Note      string   `json:"note"`
}

// Entry is a stored entry.
type Entry struct {
	ID     int64 `json:"id"`
	BookID int64 `json:"bookId"`
	EntryInput
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// FieldError is one failing field of an entry.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// Normalize trims the entry's text, fills each number's lookup key and
// returns what is wrong with it: no name, no number, an unknown label, or a
// field past its limit (characters are counted, not bytes).
func (in *EntryInput) Normalize(d Dialing) []FieldError {
	var out []FieldError
	in.FirstName, in.LastName = strings.TrimSpace(in.FirstName), strings.TrimSpace(in.LastName)
	in.Company, in.Note = strings.TrimSpace(in.Company), strings.TrimSpace(in.Note)
	for _, f := range []struct {
		path string
		v    string
		max  int
	}{
		{"firstName", in.FirstName, MaxNameLen}, {"lastName", in.LastName, MaxNameLen},
		{"company", in.Company, MaxNameLen}, {"note", in.Note, MaxNoteLen},
	} {
		if !text(f.v, f.max, f.path == "note") {
			out = append(out, FieldError{f.path, fmt.Sprintf("must be at most %d printable characters", f.max)})
		}
	}
	if in.FirstName == "" && in.LastName == "" && in.Company == "" {
		out = append(out, FieldError{"lastName", "a first name, last name or company is required"})
	}
	if len(in.Numbers) == 0 {
		out = append(out, FieldError{"numbers", "at least one number is required"})
	}
	if len(in.Numbers) > MaxNumbersPerEntry {
		out = append(out, FieldError{"numbers", fmt.Sprintf("at most %d numbers", MaxNumbersPerEntry)})
		return out
	}
	for i := range in.Numbers {
		n := &in.Numbers[i]
		n.Number = strings.TrimSpace(n.Number)
		path := fmt.Sprintf("numbers[%d]", i)
		switch {
		case n.Label != LabelWork && n.Label != LabelMobile && n.Label != LabelHome && n.Label != LabelOther:
			out = append(out, FieldError{path + ".label", "must be work, mobile, home or other"})
		case n.Number == "" || utf8.RuneCountInString(n.Number) > MaxNumberLen || !text(n.Number, MaxNumberLen, false):
			out = append(out, FieldError{path + ".number", fmt.Sprintf("must be 1-%d printable characters", MaxNumberLen)})
		}
		for _, p := range in.Numbers[:i] {
			if p.Label == n.Label {
				out = append(out, FieldError{path + ".label", "each label can be used once per entry"})
				break
			}
		}
		n.Key, _ = d.Key(n.Number, nil)
	}
	return out
}

// text reports whether s is within max characters and has no control
// character (a note may also hold line breaks and tabs).
func text(s string, max int, multiline bool) bool {
	if utf8.RuneCountInString(s) > max || !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if unicode.IsControl(r) && (!multiline || (r != '\n' && r != '\t')) {
			return false
		}
	}
	return true
}

// Group is a phone group: what it delivers to its phones (spec P-2).
type Group struct {
	ID                int64     `json:"id"`
	Name              string    `json:"name"`
	Description       string    `json:"description"`
	IncludeExtensions bool      `json:"includeExtensions"`
	IncludeCompany    bool      `json:"includeCompany"`
	BookIDs           []int64   `json:"bookIds"`
	PhoneCount        int       `json:"phoneCount"`
	CreatedAt         time.Time `json:"createdAt"`
	UpdatedAt         time.Time `json:"updatedAt"`
}

// ValidName reports whether s is a usable book or group name: 1 to max
// printable characters, no surrounding space.
func ValidName(s string, max int) bool {
	return s != "" && s == strings.TrimSpace(s) && text(s, max, false)
}
