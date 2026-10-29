package snapshot

import (
	"sort"
	"strings"
	"unicode"
)

// The dial-by-name directory (pbx-gaps S-13). The index is built in memory
// with the snapshot from the extensions book, so a caller's search never
// touches PostgreSQL. A name matches when the keypad spelling of its first
// or last word begins with the keys the caller pressed. Extensions marked
// directory_hidden are not in the index at all, so a search cannot reveal
// that they exist.

// DirectoryMaxKeys is the longest entry the directory takes.
const DirectoryMaxKeys = 7

// DirectoryEntry is one extension in the directory, with the name split
// into the words callers spell.
type DirectoryEntry struct {
	Number string
	Name   string
	First  string // first word of the name
	Last   string // last word of the name; the same as First for one word
}

type dirItem struct {
	DirectoryEntry
	firstKeys, lastKeys string
}

// foldLetter maps a letter to the unaccented ASCII letter a keypad carries;
// false means the rune is not a letter a keypad can spell.
func foldLetter(r rune) (rune, bool) {
	r = unicode.ToLower(r)
	switch {
	case r >= 'a' && r <= 'z':
		return r, true
	case strings.ContainsRune("àáâãäåāăą", r):
		return 'a', true
	case strings.ContainsRune("çćčĉċ", r):
		return 'c', true
	case strings.ContainsRune("ďđ", r):
		return 'd', true
	case strings.ContainsRune("èéêëēĕėęě", r):
		return 'e', true
	case strings.ContainsRune("ğĝġ", r):
		return 'g', true
	case strings.ContainsRune("ìíîïĩīĭįı", r):
		return 'i', true
	case r == 'ł':
		return 'l', true
	case strings.ContainsRune("ñńňņ", r):
		return 'n', true
	case strings.ContainsRune("òóôõöøōŏő", r):
		return 'o', true
	case strings.ContainsRune("ŕřŗ", r):
		return 'r', true
	case strings.ContainsRune("śšşŝß", r):
		return 's', true
	case strings.ContainsRune("ťţ", r):
		return 't', true
	case strings.ContainsRune("ùúûüũūŭůűų", r):
		return 'u', true
	case strings.ContainsRune("ýÿ", r):
		return 'y', true
	case strings.ContainsRune("źżž", r):
		return 'z', true
	}
	return 0, false
}

// keypadDigit is the key that carries a lower-case ASCII letter.
func keypadDigit(r rune) byte {
	switch {
	case r <= 'c':
		return '2'
	case r <= 'f':
		return '3'
	case r <= 'i':
		return '4'
	case r <= 'l':
		return '5'
	case r <= 'o':
		return '6'
	case r <= 's':
		return '7'
	case r <= 'v':
		return '8'
	}
	return '9'
}

// KeypadSpelling is the digits that spell s on a telephone keypad (2 ABC,
// 3 DEF, 4 GHI, 5 JKL, 6 MNO, 7 PQRS, 8 TUV, 9 WXYZ). Accents fold to their
// letter; anything that is not a letter is skipped.
func KeypadSpelling(s string) string {
	var b strings.Builder
	for _, r := range s {
		if l, ok := foldLetter(r); ok {
			b.WriteByte(keypadDigit(l))
		}
	}
	return b.String()
}

// ValidDirectoryKeys reports whether keys is a usable search: 1 to
// DirectoryMaxKeys digits, each a key that carries letters (2 to 9).
func ValidDirectoryKeys(keys string) bool {
	if keys == "" || len(keys) > DirectoryMaxKeys {
		return false
	}
	for i := 0; i < len(keys); i++ {
		if keys[i] < '2' || keys[i] > '9' {
			return false
		}
	}
	return true
}

// buildDirectory indexes the extensions that may be found by name.
func buildDirectory(exts map[string]Extension) []dirItem {
	out := make([]dirItem, 0, len(exts))
	for _, e := range exts {
		if e.DirectoryHidden {
			continue
		}
		words := strings.Fields(e.Name)
		if len(words) == 0 {
			continue
		}
		it := dirItem{DirectoryEntry: DirectoryEntry{Number: e.Number, Name: e.Name, First: words[0], Last: words[len(words)-1]}}
		it.firstKeys, it.lastKeys = KeypadSpelling(it.First), KeypadSpelling(it.Last)
		if it.firstKeys == "" && it.lastKeys == "" {
			continue
		}
		out = append(out, it)
	}
	return out
}

// DirectorySearch lists the extensions whose first or last name begins with
// the keypad spelling keys, in last-name order (then first name, then
// number). Invalid keys match nothing.
func (s *Snapshot) DirectorySearch(keys string) []DirectoryEntry {
	if !ValidDirectoryKeys(keys) {
		return nil
	}
	var out []DirectoryEntry
	for _, it := range s.dir {
		if strings.HasPrefix(it.firstKeys, keys) || strings.HasPrefix(it.lastKeys, keys) {
			out = append(out, it.DirectoryEntry)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if la, lb := strings.ToLower(a.Last), strings.ToLower(b.Last); la != lb {
			return la < lb
		}
		if fa, fb := strings.ToLower(a.First), strings.ToLower(b.First); fa != fb {
			return fa < fb
		}
		return a.Number < b.Number
	})
	return out
}
