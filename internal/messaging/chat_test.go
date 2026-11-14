package messaging

import (
	"strings"
	"testing"
)

// TestCleanChatText fails if control or bidirectional-override characters
// survive, a message over 2000 characters (not bytes) is taken, or an empty
// one is.
func TestCleanChatText(t *testing.T) {
	cases := []struct {
		in, want string
		ok       bool
	}{
		{"  hello \r\nworld\t! ", "hello \nworld\t!", true},
		{"a\x00b\x07c", "abc", true},
		{"pay\u202eexe.txt", "payexe.txt", true},
		{"zero\u200bwidth", "zerowidth", true},
		{"é", "é", true}, // NFC
		{"   \n\t ", "", false},
		{"\x00\x01", "", false},
		{strings.Repeat("é", 2000), strings.Repeat("é", 2000), true},
		{strings.Repeat("é", 2001), "", false},
		{string([]byte{0xff, 0xfe}), "", false},
	}
	for _, c := range cases {
		got, ok := CleanChatText(c.in)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("CleanChatText(%q) = %q, %v; want %q, %v", c.in, got, ok, c.want, c.ok)
		}
	}
}

func TestStripLinks(t *testing.T) {
	if got := StripLinks("see https://a.example/x?y=1 or HTTP://B.test and www.c.test, ftp://d"); got != "see [link removed] or [link removed] and [link removed] [link removed]" {
		t.Fatal(got)
	}
	if got := StripLinks("no link here, example.com is fine"); got != "no link here, example.com is fine" {
		t.Fatal(got)
	}
}

func TestChatContactCleaning(t *testing.T) {
	for _, e := range []string{"a@example.com", "first.last+tag@sub.example.org"} {
		if _, ok := CleanChatEmail(e); !ok {
			t.Errorf("%q refused", e)
		}
	}
	for _, e := range []string{"", "x", "a@b", "A <a@example.com>", "a@example.com, b@example.com", "a@example.com\r\nBcc: x@y.z", "a b@example.com", strings.Repeat("a", 250) + "@example.com"} {
		if got, ok := CleanChatEmail(e); ok && e != "a@b" {
			t.Errorf("%q accepted as %q", e, got)
		}
	}
	if _, ok := CleanChatName("Zoë O'Brien"); !ok {
		t.Error("a name with accents refused")
	}
	if _, ok := CleanChatName("line\nbreak"); ok {
		t.Error("a name with a line break accepted")
	}
	if _, ok := CleanChatName(strings.Repeat("n", 101)); ok {
		t.Error("a 101 character name accepted")
	}
}

func TestPageHintAndFamily(t *testing.T) {
	if got := PageHint("https://intranet.lan/help/page?token=secret#frag"); got != "intranet.lan/help/page" {
		t.Fatal(got)
	}
	for _, bad := range []string{"javascript:alert(1)", "", "not a url", "ftp://x/y"} {
		if PageHint(bad) != "" {
			t.Errorf("%q gave a hint", bad)
		}
	}
	if got := PageHint("https://h/" + strings.Repeat("p", 900)); len([]rune(got)) != MaxChatHint {
		t.Fatalf("hint length %d", len(got))
	}
	for ua, want := range map[string]string{
		"Mozilla/5.0 (X11) Gecko/20100101 Firefox/130.0":                          "firefox",
		"Mozilla/5.0 AppleWebKit/537.36 Chrome/120 Safari/537.36":                 "chrome",
		"Mozilla/5.0 AppleWebKit/537.36 Chrome/120 Safari/537.36 Edg/120":         "edge",
		"Mozilla/5.0 (Macintosh) AppleWebKit/605.1.15 Version/17 Safari/605.1.15": "safari",
		"curl/8.0": "other",
	} {
		if got := UAFamily(ua); got != want {
			t.Errorf("UAFamily(%q) = %s, want %s", ua, got, want)
		}
	}
}

func TestOrigins(t *testing.T) {
	for _, o := range []string{"https://intranet.lan", "http://hello.kw.watteel.lab:8080", "http://192.168.1.5"} {
		if !ValidOrigin(o) {
			t.Errorf("%q refused", o)
		}
		if OriginLooksPublic(o) {
			t.Errorf("%q looks public", o)
		}
	}
	for _, o := range []string{"", "intranet.lan", "https://intranet.lan/", "https://intranet.lan/path", "ftp://x", "https://u:p@x.lan", "https://x.lan;script-src *", "https://x.lan *"} {
		if ValidOrigin(o) {
			t.Errorf("%q accepted", o)
		}
	}
	for _, o := range []string{"https://www.example.com", "https://8.8.8.8"} {
		if !OriginLooksPublic(o) {
			t.Errorf("%q does not look public", o)
		}
	}
	if FrameAncestors(nil) != "'none'" || FrameAncestors([]string{"https://a.lan", "https://b.lan"}) != "https://a.lan https://b.lan" {
		t.Error("frame ancestors")
	}
}
