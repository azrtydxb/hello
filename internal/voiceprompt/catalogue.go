// Package voiceprompt is the catalogue of every sentence Hello speaks to a
// caller in the voicemail application, the wording of times and callers, and
// nothing else: no audio, no network. The call flow refers to prompts by
// Key and never holds a string of its own, so a language is added by
// writing one more table (see dutch.go) and listing it in tables.
//
// A template holds {name} placeholders that Vars fill in. A count has up to
// three templates, Key+".zero", Key+".one" and Key itself (the plural), which
// Count picks between. A language that lacks a key falls back to English,
// and TestCatalogueComplete makes sure no shipped language does.
package voiceprompt

import (
	"sort"
	"strings"
)

// Lang is a prompt language: an ISO 639-1 code.
type Lang string

// Languages with a complete table.
const (
	English Lang = "en"
	Dutch   Lang = "nl"
)

// Default is the language used when none is configured or the configured one
// is unknown.
const Default = English

// Key names one prompt.
type Key string

// Vars fills the {name} placeholders of a template.
type Vars map[string]string

// The prompts. Keys are stable identifiers: they are part of no stored data,
// but they name the templates in every language table.
const (
	// Retrieval session.
	PINEnter     Key = "pin.enter"
	PINInvalid   Key = "pin.invalid"
	PINLocked    Key = "pin.locked"
	NoMailbox    Key = "mailbox.none"
	Goodbye      Key = "goodbye"
	InvalidKey   Key = "key.invalid"
	Counts       Key = "counts"
	CountNew     Key = "count.new"   // .zero .one and plural
	CountSaved   Key = "count.saved" // .zero .one and plural
	MainMenu     Key = "menu.main"
	NoNew        Key = "folder.new.empty"
	NoSaved      Key = "folder.saved.empty"
	NewFolder    Key = "folder.new"
	SavedFolder  Key = "folder.saved"
	MsgHeader    Key = "msg.header"
	MsgMenu      Key = "msg.menu"
	MsgMenuShort Key = "msg.menu.short"
	MsgDeleted   Key = "msg.deleted"
	MsgSaved     Key = "msg.saved"
	MsgNoPrev    Key = "msg.noprev"
	MsgEnd       Key = "msg.end"
	MsgUnavail   Key = "msg.unavailable"

	// Who a message is from, and when.
	WhoUnknown   Key = "who.unknown"
	WhoExtension Key = "who.extension"
	WhoMailbox   Key = "who.mailbox"
	WordPlus     Key = "word.plus"
	WhenToday    Key = "when.today"
	WhenYday     Key = "when.yesterday"
	WhenWeekday  Key = "when.weekday"
	WhenDate     Key = "when.date"
	WhenDateYear Key = "when.dateyear"
	weekdayPfx       = "weekday."
	monthPfx         = "month."

	// Mailbox options and greeting recording.
	OptMenu       Key = "opt.menu"
	RecUnavail    Key = "rec.unavailable"
	RecBusy       Key = "rec.busy"
	RecName       Key = "rec.name"
	RecReview     Key = "rec.review"
	RecConfirm    Key = "rec.confirm"
	RecSaved      Key = "rec.saved"
	RecCancelled  Key = "rec.cancelled"
	RecTooShort   Key = "rec.tooshort"
	RecFailed     Key = "rec.failed"
	RevertDone    Key = "revert.done"
	PINNew        Key = "pin.new"
	PINConfirm    Key = "pin.confirm"
	PINMismatch   Key = "pin.mismatch"
	PINTooShort   Key = "pin.tooshort"
	PINChanged    Key = "pin.changed"
	PINChangeFail Key = "pin.changefail"

	// Default greetings, whole and as the tail that follows a recorded name.
	GreetUnavailable     Key = "greet.unavailable"
	GreetBusy            Key = "greet.busy"
	GreetUnreachable     Key = "greet.unreachable"
	GreetUnavailableTail Key = "greet.unavailable.tail"
	GreetBusyTail        Key = "greet.busy.tail"
	GreetUnreachableTail Key = "greet.unreachable.tail"

	// Dial-by-name directory (pbx-gaps S-13). DirectoryOne speaks one choice
	// ("For {name}, press {digit}"), DirectoryConnecting the hand-over.
	DirectoryEnter      Key = "directory.enter"
	DirectoryConnecting Key = "directory.connecting"
	DirectoryOne        Key = "directory.one"
	DirectoryChoose     Key = "directory.choose"
	DirectoryMore       Key = "directory.more"
	DirectoryNone       Key = "directory.none"
	DirectoryTooLong    Key = "directory.toolong"
)

// tables holds the language tables. Add a language here and in its own file.
var tables = map[Lang]map[Key]string{
	English: english,
	Dutch:   dutch,
}

// Languages lists the languages with a table, sorted.
func Languages() []Lang {
	out := make([]Lang, 0, len(tables))
	for l := range tables {
		out = append(out, l)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Parse reads a configured language: "nl", "nl-BE" and "NL" all give Dutch;
// an empty or unknown value gives Default.
func Parse(s string) Lang {
	s = strings.ToLower(strings.TrimSpace(s))
	if i := strings.IndexAny(s, "-_"); i > 0 {
		s = s[:i]
	}
	if _, ok := tables[Lang(s)]; ok {
		return Lang(s)
	}
	return Default
}

// template returns the raw template, falling back to English; ok is false
// when no table has the key.
func template(l Lang, k Key) (string, bool) {
	if t, ok := tables[l][k]; ok {
		return t, true
	}
	t, ok := tables[Default][k]
	return t, ok
}

// Text renders the prompt k in language l. Placeholders without a value are
// left empty; a key no table knows renders as "".
func Text(l Lang, k Key, v Vars) string {
	t, _ := template(l, k)
	return fill(t, v)
}

// Count renders the count form of k for n: k.zero for 0, k.one for 1 and k
// itself otherwise, with {n} bound to the number. A language without the
// .zero or .one form uses the plural.
func Count(l Lang, k Key, n int, v Vars) string {
	key := k
	switch n {
	case 0:
		if _, ok := template(l, k+".zero"); ok {
			key = k + ".zero"
		}
	case 1:
		if _, ok := template(l, k+".one"); ok {
			key = k + ".one"
		}
	}
	w := Vars{"n": itoa(n)}
	for a, b := range v {
		w[a] = b
	}
	return Text(l, key, w)
}

func fill(t string, v Vars) string {
	if !strings.Contains(t, "{") {
		return t
	}
	var b strings.Builder
	for {
		i := strings.IndexByte(t, '{')
		if i < 0 {
			b.WriteString(t)
			break
		}
		j := strings.IndexByte(t[i:], '}')
		if j < 0 {
			b.WriteString(t)
			break
		}
		b.WriteString(t[:i])
		b.WriteString(v[t[i+1:i+j]])
		t = t[i+j+1:]
	}
	return b.String()
}

// Entry is one prompt of a language: its key and rendered text.
type Entry struct {
	Key  Key
	Text string
}

// Static lists the prompts of l that have no placeholder, in key order.
// These are spoken the same on every call, so they can be synthesized ahead
// of time; the others (counts, message headers, greetings with a name) are
// synthesized on first use and cached by their text.
func Static(l Lang) []Entry {
	var out []Entry
	for k := range tables[Default] {
		t, _ := template(l, k)
		if t == "" || strings.Contains(t, "{") || isFragment(k) {
			continue
		}
		out = append(out, Entry{Key: k, Text: t})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// isFragment is true for the keys that are only ever part of a longer
// sentence (a weekday, a month, "plus"): they are never spoken alone.
func isFragment(k Key) bool {
	s := string(k)
	return strings.HasPrefix(s, "weekday.") || strings.HasPrefix(s, "month.") ||
		strings.HasPrefix(s, "word.") || strings.HasPrefix(s, "who.") || strings.HasPrefix(s, "when.") ||
		strings.HasSuffix(s, ".tail") || strings.HasPrefix(s, "count.")
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
