package phonebook

import (
	"bytes"
	"sort"
	"strings"
	"unicode/utf8"
)

// ContentTypeVCard is the type of the vCard slot.
const ContentTypeVCard = "text/vcard; charset=utf-8"

// VCardModel reports whether Hello offers the vCard slot for a phone: the
// Grandstream WP816, WP826 and WP836 (firmware 1.0.3.24 and later;
// phone-feature-parity S-31). The WP8x6 Administration Guide (Phonebook:
// "Download vCard Phonebook", "Upload vCard Phonebook") says these phones
// import a vCard phonebook file, but names no provisioning key that fetches
// one, so the slot is not referenced from the template: an administrator
// downloads it and uploads it on the phone's web page.
func VCardModel(vendor, model string) bool {
	if vendor != "grandstream" {
		return false
	}
	m := strings.ToUpper(strings.NewReplacer(" ", "", "-", "", "_", "").Replace(model))
	return len(m) >= 5 && strings.HasPrefix(m, "WP8") && m[4] == '6' && (m[3] == '1' || m[3] == '2' || m[3] == '3')
}

// Capacity is how many contacts the books deliver to the phone, how many
// its model takes (0 for no limit), and how many are dropped from the end of
// the delivery order. Vendors without a directory format report zeros.
func Capacity(vendor, model string, books []Book) (delivered, capacity, dropped int) {
	f, ok := FormatForModel(vendor, model)
	if !ok {
		return 0, 0, 0
	}
	delivered = len(Merge(books))
	capacity = f.MaxEntries
	if capacity > 0 && delivered > capacity {
		dropped = delivered - capacity
	}
	return delivered, capacity, dropped
}

// RenderVCard renders the books the way RenderBooks does (same merge, same
// model capacity, same sort), as one vCard 3.0 file (RFC 2426), one card per
// contact with all of its numbers. truncated reports entries dropped at the
// capacity.
func RenderVCard(vendor, model string, books []Book) (body []byte, truncated bool, err error) {
	f, ok := FormatForModel(vendor, model)
	if !ok {
		return nil, false, errNoFormat(vendor)
	}
	entries := Merge(books)
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
	for _, r := range recs {
		first, last := cut(r.first, f.MaxNameRunes), cut(r.last, f.MaxNameRunes)
		fn := strings.TrimSpace(first + " " + last)
		vcLine(&b, "BEGIN:VCARD")
		vcLine(&b, "VERSION:3.0")
		vcLine(&b, "N:"+vcEsc(last)+";"+vcEsc(first)+";;;")
		vcLine(&b, "FN:"+vcEsc(fn))
		if r.company != "" {
			vcLine(&b, "ORG:"+vcEsc(cut(r.company, f.MaxNameRunes)))
		}
		for _, n := range r.numbers {
			vcLine(&b, "TEL;TYPE="+vcType(n.Label)+":"+vcEsc(n.Number))
		}
		vcLine(&b, "END:VCARD")
	}
	return b.Bytes(), truncated, nil
}

// vcType maps a number label to a vCard TEL type.
func vcType(label string) string {
	switch label {
	case "mobile":
		return "CELL"
	case "home":
		return "HOME"
	}
	return "WORK"
}

// vcEsc escapes a text value (RFC 2426 section 4): backslash, comma and
// semicolon. Newlines cannot occur (clean strips control characters).
func vcEsc(s string) string {
	return strings.NewReplacer(`\`, `\\`, `,`, `\,`, `;`, `\;`).Replace(s)
}

// vcLine writes a content line folded at 75 octets with CRLF and a leading
// space on continuations, never splitting a UTF-8 sequence.
func vcLine(b *bytes.Buffer, line string) {
	const limit = 75
	first := true
	for len(line) > 0 {
		max := limit
		if !first {
			max--
			b.WriteByte(' ')
		}
		if len(line) <= max {
			b.WriteString(line)
			line = ""
		} else {
			cutAt := max
			for cutAt > 0 && !utf8.RuneStart(line[cutAt]) {
				cutAt--
			}
			b.WriteString(line[:cutAt])
			line = line[cutAt:]
		}
		b.WriteString("\r\n")
		first = false
	}
}
