package voiceprompt

import (
	"strings"
	"time"
	"unicode"
)

// SpellDigits writes a phone number digit by digit, with a comma between the
// digits so the voice pauses on each ("+3249" -> "plus, 3, 2, 4, 9"). Only
// digits, '+', '*' and '#' are kept.
func SpellDigits(l Lang, s string) string {
	var parts []string
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r == '*', r == '#':
			parts = append(parts, string(r))
		case r == '+':
			parts = append(parts, Text(l, WordPlus, nil))
		}
	}
	return strings.Join(parts, ", ")
}

// When words the time t of a message relative to now, in zone loc: "today
// at 14:32", "yesterday at 14:32", "on Monday at 14:32" within the last six
// days, "on 3 March at 14:32" beyond, with the year when it is not the
// current one. A time in the future counts as today.
func When(l Lang, loc *time.Location, now, t time.Time) string {
	if loc == nil {
		loc = time.UTC
	}
	n, m := now.In(loc), t.In(loc)
	v := Vars{"time": m.Format("15:04")}
	switch days := dayDiff(n, m); {
	case days <= 0:
		return Text(l, WhenToday, v)
	case days == 1:
		return Text(l, WhenYday, v)
	case days < 7:
		v["weekday"] = Text(l, Key(weekdayPfx+itoa(int(m.Weekday()))), nil)
		return Text(l, WhenWeekday, v)
	}
	v["day"] = itoa(m.Day())
	v["month"] = Text(l, Key(monthPfx+itoa(int(m.Month()))), nil)
	if m.Year() != n.Year() {
		v["year"] = itoa(m.Year())
		return Text(l, WhenDateYear, v)
	}
	return Text(l, WhenDate, v)
}

// dayDiff is the number of calendar days from b to a (a later than b is
// positive), counted in their shared zone so a daylight-saving change does
// not shift it.
func dayDiff(a, b time.Time) int {
	da := time.Date(a.Year(), a.Month(), a.Day(), 12, 0, 0, 0, time.UTC)
	db := time.Date(b.Year(), b.Month(), b.Day(), 12, 0, 0, 0, time.UTC)
	return int(da.Sub(db).Hours() / 24)
}

// Caller is everything known about the sender of a message.
type Caller struct {
	// Number is the caller's number or extension as the call carried it.
	Number string
	// Extension is the display name of the extension the number belongs to,
	// "" when it is not one of ours.
	Extension string
	// Contact is the phonebook name of the number, "".
	Contact string
	// CallerID is the display name the call carried, "".
	CallerID string
}

// CallerSource says which source named a caller.
type CallerSource int

// The sources, in the order they are tried.
const (
	FromExtension CallerSource = iota
	FromContact
	FromCallerID
	FromNumber
	FromUnknown
)

// Who returns how the caller is read out, and which source gave it: the
// extension's display name, else the phonebook contact, else the caller-ID
// name, else the number digit by digit, else "an unknown caller". A
// caller-ID name that is only the number again, or a placeholder such as
// "Anonymous", is not a name.
func (c Caller) Who(l Lang) (string, CallerSource) {
	if n := strings.TrimSpace(c.Extension); n != "" {
		return n, FromExtension
	}
	if n := strings.TrimSpace(c.Contact); n != "" {
		return n, FromContact
	}
	if n := strings.TrimSpace(c.CallerID); usableName(n, c.Number) {
		return n, FromCallerID
	}
	if d := SpellDigits(l, c.Number); d != "" {
		return d, FromNumber
	}
	return Text(l, WhoUnknown, nil), FromUnknown
}

func usableName(name, number string) bool {
	if name == "" || name == number {
		return false
	}
	switch strings.ToLower(name) {
	case "anonymous", "unknown", "unavailable", "restricted", "private", "withheld", "wireless caller":
		return false
	}
	for _, r := range name {
		if unicode.IsLetter(r) {
			return true
		}
	}
	return false // only digits and punctuation: a number, not a name
}

// Header is the spoken header of message n (1-based): "Message 1, from Jan
// Peeters, today at 14:32."
func Header(l Lang, n int, c Caller, loc *time.Location, now, at time.Time) string {
	who, _ := c.Who(l)
	return Text(l, MsgHeader, Vars{"n": itoa(n), "who": who, "when": When(l, loc, now, at)})
}

// Variant is the situation a greeting answers.
type Variant int

// The greeting variants.
const (
	Unavailable Variant = iota // no answer, do not disturb, and the default
	Busy
	Unreachable
)

// GreetingText is the default spoken greeting for variant v. who is the name
// or extension of the box owner (see Owner); "" names "this voicemail box".
func GreetingText(l Lang, v Variant, who string) string {
	if who == "" {
		who = Text(l, WhoMailbox, nil)
	}
	return Text(l, greetKey(v), Vars{"who": who})
}

// GreetingTail is the greeting without the name, for playing after a
// recorded name.
func GreetingTail(l Lang, v Variant) string {
	switch v {
	case Busy:
		return Text(l, GreetBusyTail, nil)
	case Unreachable:
		return Text(l, GreetUnreachableTail, nil)
	}
	return Text(l, GreetUnavailableTail, nil)
}

func greetKey(v Variant) Key {
	switch v {
	case Busy:
		return GreetBusy
	case Unreachable:
		return GreetUnreachable
	}
	return GreetUnavailable
}

// Owner is how a greeting names the box owner: the display name, else
// "extension 1 0 0".
func Owner(l Lang, name, extension string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	if d := SpellDigits(l, extension); d != "" {
		return Text(l, WhoExtension, Vars{"digits": d})
	}
	return Text(l, WhoMailbox, nil)
}
