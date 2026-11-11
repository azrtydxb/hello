package messaging

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestOptOutKeywords fails if a keyword inside a longer sentence opts out,
// if STOP, stop and "Stop." do not, if START does not read as an opt-in, if
// an address's own keyword list is ignored or if HELP is not recognised.
func TestOptOutKeywords(t *testing.T) {
	for _, body := range []string{"STOP", "stop", "Stop.", "  stop!! ", "StopAll", "UNSUBSCRIBE", "cancel", "End", "quit", "\tstop\n"} {
		if got := ClassifyKeyword(body, nil); got != KeywordOptOut {
			t.Errorf("%q = %v, want an opt-out", body, got)
		}
	}
	for _, body := range []string{
		"please stop calling me at 5", "stop it", "I will not stop", "don't stop", "stopping", "", "   ", "stop stop",
		"cancel my order", "end of month", "stop.please", "1", "stöp",
	} {
		if got := ClassifyKeyword(body, nil); got != KeywordNone {
			t.Errorf("%q = %v, want no keyword", body, got)
		}
	}
	for _, body := range []string{"START", "start.", "Unstop"} {
		if got := ClassifyKeyword(body, nil); got != KeywordOptIn {
			t.Errorf("%q = %v, want an opt-in", body, got)
		}
	}
	for _, body := range []string{"HELP", "info", "Help?"} {
		if got := ClassifyKeyword(body, nil); got != KeywordHelp {
			t.Errorf("%q = %v, want help", body, got)
		}
	}
	// An address's own list replaces the default set.
	own := []string{"arret", "NON"}
	if got := ClassifyKeyword("Arret", own); got != KeywordOptOut {
		t.Errorf("custom keyword = %v", got)
	}
	if got := ClassifyKeyword("stop", own); got != KeywordNone {
		t.Errorf("a default keyword still opts out with a custom list: %v", got)
	}
}

// TestCleanBody fails if a text with a control character, bad UTF-8, nothing
// in it or too many bytes is accepted, or if line breaks are lost.
func TestCleanBody(t *testing.T) {
	if got, ok := CleanBody("  hello\r\nworld  ", 100); !ok || got != "hello\nworld" {
		t.Errorf("CleanBody = %q, %v", got, ok)
	}
	for name, s := range map[string]string{
		"empty": "", "blank": " \n ", "nul": "a\x00b", "bell": "a\ab", "bad utf8": "a\xffb", "long": strings.Repeat("x", 101),
	} {
		if got, ok := CleanBody(s, 100); ok {
			t.Errorf("%s accepted as %q", name, got)
		}
	}
	if _, ok := CleanBody("é€😀", 100); !ok {
		t.Error("non-ASCII text refused")
	}
}

// TestSplitText fails if a long text is cut inside a character, a part is
// longer than the limit, the parts do not rebuild the text, or a short text
// is split.
func TestSplitText(t *testing.T) {
	if got := SplitText("short", 700); len(got) != 1 || got[0] != "short" {
		t.Errorf("short = %q", got)
	}
	for _, text := range []string{
		strings.Repeat("word ", 400),
		strings.Repeat("é", 1000),
		strings.Repeat("€", 700) + "😀😀😀",
		strings.Repeat("x", 2000),
	} {
		parts := SplitText(text, 700)
		if len(parts) < 2 {
			t.Fatalf("%d bytes not split", len(text))
		}
		for _, p := range parts {
			if len(p) > 700 || p == "" || !utf8.ValidString(p) {
				t.Errorf("part of %d bytes, valid=%v", len(p), utf8.ValidString(p))
			}
		}
		if strings.Join(parts, "") != text {
			t.Error("the parts do not rebuild the text")
		}
	}
}

// TestCleanTags fails if a bad tag is accepted, duplicates are kept, or the
// tags are not normalised.
func TestCleanTags(t *testing.T) {
	got, ok := CleanTags([]string{" Billing ", "billing", "VIP"})
	if !ok || len(got) != 2 || got[0] != "billing" || got[1] != "vip" {
		t.Errorf("CleanTags = %v, %v", got, ok)
	}
	if _, ok := CleanTags([]string{""}); ok {
		t.Error("empty tag accepted")
	}
	if _, ok := CleanTags([]string{strings.Repeat("x", MaxTagLen+1)}); ok {
		t.Error("long tag accepted")
	}
	if _, ok := CleanTags([]string{"a\x00"}); ok {
		t.Error("control character accepted")
	}
	many := make([]string, MaxTags+1)
	for i := range many {
		many[i] = "t" + strings.Repeat("x", i)
	}
	if _, ok := CleanTags(many); ok {
		t.Error("too many tags accepted")
	}
	if got, ok := CleanTags(nil); !ok || got == nil || len(got) != 0 {
		t.Errorf("CleanTags(nil) = %v, %v; want an empty list", got, ok)
	}
}
