package voiceprompt

import (
	"strings"
	"testing"
	"time"
)

// TestCatalogueComplete fails if a shipped language lacks a key English has,
// or has one English does not (a typo that would never be spoken).
func TestCatalogueComplete(t *testing.T) {
	for _, l := range Languages() {
		for k := range english {
			if _, ok := tables[l][k]; !ok {
				t.Errorf("%s lacks %q", l, k)
			}
		}
		for k, v := range tables[l] {
			if _, ok := english[k]; !ok {
				t.Errorf("%s has %q, which English lacks", l, k)
			}
			if strings.TrimSpace(v) == "" {
				t.Errorf("%s: %q is empty", l, k)
			}
			if strings.Count(v, "{") != strings.Count(v, "}") {
				t.Errorf("%s: %q has unbalanced braces", l, k)
			}
		}
	}
}

// TestPlaceholdersMatch fails if a translation uses placeholders English does
// not, or omits one, which would speak a hole.
func TestPlaceholdersMatch(t *testing.T) {
	names := func(s string) map[string]bool {
		out := map[string]bool{}
		for {
			i := strings.IndexByte(s, '{')
			if i < 0 {
				return out
			}
			j := strings.IndexByte(s[i:], '}')
			out[s[i+1:i+j]] = true
			s = s[i+j+1:]
		}
	}
	for _, l := range Languages() {
		for k, en := range english {
			a, b := names(en), names(tables[l][k])
			for n := range a {
				if !b[n] {
					t.Errorf("%s %q lacks {%s}", l, k, n)
				}
			}
			for n := range b {
				if !a[n] {
					t.Errorf("%s %q has {%s}, which English lacks", l, k, n)
				}
			}
		}
	}
}

func TestParse(t *testing.T) {
	for in, want := range map[string]Lang{"": English, "en": English, "NL": Dutch, "nl-BE": Dutch, "nl_NL": Dutch, "xx": English, " nl ": Dutch} {
		if got := Parse(in); got != want {
			t.Errorf("Parse(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTextAndCount(t *testing.T) {
	if got := Text(English, MsgHeader, Vars{"n": "1", "who": "Jan Peeters", "when": "today at 14:32"}); got != "Message 1, from Jan Peeters, today at 14:32." {
		t.Errorf("header = %q", got)
	}
	// An unknown language falls back to English; an unknown key is empty.
	if got := Text("xx", Goodbye, nil); got != "Goodbye." {
		t.Errorf("fallback = %q", got)
	}
	if got := Text(English, "nope", nil); got != "" {
		t.Errorf("unknown key = %q", got)
	}
	for _, tc := range []struct {
		n    int
		want string
	}{{0, "no new messages"}, {1, "1 new message"}, {2, "2 new messages"}, {12, "12 new messages"}} {
		if got := Count(English, CountNew, tc.n, nil); got != tc.want {
			t.Errorf("Count(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
	got := Text(English, Counts, Vars{"new": Count(English, CountNew, 2, nil), "saved": Count(English, CountSaved, 1, nil)})
	if got != "You have 2 new messages and 1 saved message." {
		t.Errorf("counts = %q", got)
	}
}

func TestStatic(t *testing.T) {
	st := Static(English)
	if len(st) < 20 {
		t.Fatalf("only %d static prompts", len(st))
	}
	has := map[Key]bool{}
	for _, e := range st {
		has[e.Key] = true
		if strings.Contains(e.Text, "{") {
			t.Errorf("static %q has a placeholder", e.Key)
		}
	}
	for _, k := range []Key{PINEnter, MsgMenu, OptMenu, RecUnavail, RecConfirm, MainMenu} {
		if !has[k] {
			t.Errorf("%q is not static", k)
		}
	}
	for _, k := range []Key{MsgHeader, Counts, GreetUnavailable, "weekday.1", WordPlus} {
		if has[k] {
			t.Errorf("%q must not be static", k)
		}
	}
}

func TestSpellDigits(t *testing.T) {
	for in, want := range map[string]string{
		"0471234": "0, 4, 7, 1, 2, 3, 4",
		"+3249":   "plus, 3, 2, 4, 9",
		"":        "",
		"ab-1 2":  "1, 2",
	} {
		if got := SpellDigits(English, in); got != want {
			t.Errorf("SpellDigits(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestWhen covers today, yesterday, a weekday, a date this year and a date in
// another year, in the system zone rather than UTC.
func TestWhen(t *testing.T) {
	brussels, err := time.LoadLocation("Europe/Brussels")
	if err != nil {
		t.Skip("no tzdata")
	}
	now := time.Date(2026, 10, 25, 20, 0, 0, 0, brussels) // a Sunday
	at := func(y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, brussels)
	}
	for _, tc := range []struct {
		name string
		t    time.Time
		want string
	}{
		{"today", at(2026, 10, 25, 14, 32), "today at 14:32"},
		{"today early", at(2026, 10, 25, 0, 5), "today at 00:05"},
		{"yesterday", at(2026, 10, 24, 23, 59), "yesterday at 23:59"},
		{"weekday", at(2026, 10, 22, 9, 0), "on Thursday at 09:00"},
		{"six days", at(2026, 10, 19, 9, 0), "on Monday at 09:00"},
		{"seven days is a date", at(2026, 10, 18, 9, 0), "on 18 October at 09:00"},
		{"this year", at(2026, 3, 3, 8, 15), "on 3 March at 08:15"},
		{"other year", at(2025, 12, 31, 8, 15), "on 31 December 2025 at 08:15"},
		{"future is today", at(2026, 10, 26, 8, 15), "today at 08:15"},
		// 23:30 UTC on the 24th is 01:30 on the 25th in Brussels: today.
		{"utc input in local zone", time.Date(2026, 10, 24, 23, 30, 0, 0, time.UTC), "today at 01:30"},
	} {
		if got := When(English, brussels, now, tc.t); got != tc.want {
			t.Errorf("%s: When = %q, want %q", tc.name, got, tc.want)
		}
	}
	// Across the autumn daylight-saving change (25 Oct 2026, 03:00 -> 02:00)
	// yesterday stays yesterday.
	if got := When(English, brussels, at(2026, 10, 25, 1, 0), at(2026, 10, 24, 1, 0)); got != "yesterday at 01:00" {
		t.Errorf("across DST: %q", got)
	}
	if got := When(Dutch, brussels, now, at(2026, 10, 24, 10, 0)); got != "gisteren om 10:00" {
		t.Errorf("dutch: %q", got)
	}
	if got := When(English, nil, now, now); got != "today at 19:00" {
		t.Errorf("nil zone is UTC: %q", got)
	}
}

// TestCallerOrder fails if the name sources are not tried as: extension
// display name, phonebook contact, caller-ID name, number digit by digit.
func TestCallerOrder(t *testing.T) {
	full := Caller{Number: "0471234567", Extension: "Front desk", Contact: "Jan Peeters", CallerID: "J Peeters"}
	for _, tc := range []struct {
		name string
		c    Caller
		want string
		src  CallerSource
	}{
		{"extension first", full, "Front desk", FromExtension},
		{"contact second", Caller{Number: "0471234567", Contact: "Jan Peeters", CallerID: "J Peeters"}, "Jan Peeters", FromContact},
		{"caller id third", Caller{Number: "0471234567", CallerID: "J Peeters"}, "J Peeters", FromCallerID},
		{"number fourth", Caller{Number: "0471234567"}, "0, 4, 7, 1, 2, 3, 4, 5, 6, 7", FromNumber},
		{"caller id that is the number", Caller{Number: "0471234567", CallerID: "0471234567"}, "0, 4, 7, 1, 2, 3, 4, 5, 6, 7", FromNumber},
		{"caller id of digits", Caller{Number: "100", CallerID: "+32 471"}, "1, 0, 0", FromNumber},
		{"anonymous placeholder", Caller{Number: "100", CallerID: "Anonymous"}, "1, 0, 0", FromNumber},
		{"blank names", Caller{Number: "100", Extension: "  ", Contact: " "}, "1, 0, 0", FromNumber},
		{"nothing", Caller{}, "an unknown caller", FromUnknown},
		{"international", Caller{Number: "+32471"}, "plus, 3, 2, 4, 7, 1", FromNumber},
	} {
		got, src := tc.c.Who(English)
		if got != tc.want || src != tc.src {
			t.Errorf("%s: Who = %q (%d), want %q (%d)", tc.name, got, src, tc.want, tc.src)
		}
	}
}

func TestHeaderMatchesTheRequirement(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 10, 25, 20, 0, 0, 0, loc)
	got := Header(English, 1, Caller{Number: "201", Extension: "Jan Peeters"}, loc, now, time.Date(2026, 10, 25, 14, 32, 0, 0, loc))
	if got != "Message 1, from Jan Peeters, today at 14:32." {
		t.Errorf("header = %q", got)
	}
}

func TestGreetings(t *testing.T) {
	if got := GreetingText(English, Unavailable, Owner(English, "Jan Peeters", "201")); got != "Jan Peeters is not available. Please leave a message after the tone." {
		t.Errorf("unavailable = %q", got)
	}
	if got := GreetingText(English, Busy, Owner(English, "", "201")); got != "extension 2, 0, 1 is busy at the moment. Please leave a message after the tone." {
		t.Errorf("busy = %q", got)
	}
	if got := GreetingText(English, Unreachable, ""); !strings.HasPrefix(got, "this voicemail box cannot be reached") {
		t.Errorf("unreachable = %q", got)
	}
	if got := GreetingTail(English, Busy); got != "is busy at the moment. Please leave a message after the tone." {
		t.Errorf("tail = %q", got)
	}
}
