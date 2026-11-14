package messaging

// The pure part of the LAN chat widget (spec messaging-channels M-28 to
// M-29c): cleaning what a visitor types, opening hours, origins and the
// coarse facts kept about a visitor. Nothing here logs or stores anything.

import (
	"net/mail"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// ProviderChat is the provider of a chat site's channel: the widget talks to
// Hello directly, so there is no carrier.
const ProviderChat = "chat"

// PeerKindVisitor is the peer kind of a chat visitor.
const PeerKindVisitor = "visitor"

// Chat limits (M-29a.3).
const (
	// MaxChatChars is the longest visitor message, in characters.
	MaxChatChars = 2000
	// MaxChatUnanswered is how many messages a visitor may send before any
	// agent replies.
	MaxChatUnanswered = 200
	// MaxChatName and MaxChatEmail bound what a visitor types about
	// themselves.
	MaxChatName  = 100
	MaxChatEmail = 254
	// MaxChatHint bounds the page hint a visitor's browser sends.
	MaxChatHint = 500
)

// CleanChatText validates and normalises what a visitor typed: valid UTF-8,
// normalised (NFC), control and invisible formatting characters stripped
// (line breaks and tabs stay), trimmed. ok is false for text that is not
// UTF-8, is empty once cleaned, or has more than MaxChatChars characters.
func CleanChatText(s string) (string, bool) {
	if !utf8.ValidString(s) || len(s) > 4*MaxChatChars+64 {
		return "", false
	}
	s = norm.NFC.String(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r), r == utf8.RuneError:
			// stripped: includes the bidirectional overrides that make a
			// link look like another.
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if out == "" || utf8.RuneCountInString(out) > MaxChatChars {
		return "", false
	}
	return out, true
}

var linkRE = regexp.MustCompile(`(?i)\b(?:https?://|ftp://|www\.)\S+`)

// StripLinks replaces every URL in s with "[link removed]".
func StripLinks(s string) string { return linkRE.ReplaceAllString(s, "[link removed]") }

// CleanChatName validates a visitor's name: one line of at most MaxChatName
// printable characters.
func CleanChatName(s string) (string, bool) {
	s = strings.TrimSpace(norm.NFC.String(s))
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > MaxChatName {
		return "", false
	}
	for _, r := range s {
		if !unicode.IsPrint(r) {
			return "", false
		}
	}
	return s, true
}

// CleanChatEmail validates a visitor's email address: a bare address, no
// display name, at most MaxChatEmail characters.
func CleanChatEmail(s string) (string, bool) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > MaxChatEmail || strings.ContainsAny(s, " \t\r\n<>,;\"") {
		return "", false
	}
	a, err := mail.ParseAddress(s)
	if err != nil || a.Address != s || a.Name != "" {
		return "", false
	}
	return s, true
}

// PageHint reduces a page URL a browser reports to hostname plus path
// (never the query or fragment), at most MaxChatHint characters; "" when it
// is not a URL.
func PageHint(raw string) string {
	if len(raw) > 2048 {
		raw = raw[:2048]
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	h := u.Hostname() + u.EscapedPath()
	if utf8.RuneCountInString(h) > MaxChatHint {
		h = string([]rune(h)[:MaxChatHint])
	}
	return h
}

// UAFamily reduces a User-Agent to a coarse browser family; nothing else of
// it is kept.
func UAFamily(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "edg/"), strings.Contains(l, "edge/"):
		return "edge"
	case strings.Contains(l, "firefox/"), strings.Contains(l, "fxios"):
		return "firefox"
	case strings.Contains(l, "chrome/"), strings.Contains(l, "crios"), strings.Contains(l, "chromium"):
		return "chrome"
	case strings.Contains(l, "safari/"):
		return "safari"
	}
	return "other"
}

// ValidOrigin reports whether o is an origin a page can be served from:
// scheme http or https, a host, an optional port, nothing else.
func ValidOrigin(o string) bool {
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
		return false
	}
	return u.User == nil && (u.Path == "" || u.Path == "/") && u.RawQuery == "" && u.Fragment == "" && !strings.HasSuffix(o, "/") &&
		!strings.ContainsAny(o, " \t\r\n;,'\"")
}

var localSuffixes = []string{".lan", ".local", ".localdomain", ".internal", ".home.arpa", ".intranet", ".corp", ".private", ".lab", ".test"}

// OriginLooksPublic reports whether an origin's host looks like a public
// Internet name or address: the LAN widget is for intranet pages, and the
// administrator is warned when a site lists anything else.
func OriginLooksPublic(o string) bool {
	u, err := url.Parse(o)
	if err != nil || u.Hostname() == "" {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if ip, err := netip.ParseAddr(h); err == nil {
		ip = ip.Unmap()
		return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
	}
	if h == "localhost" || !strings.Contains(h, ".") {
		return false
	}
	for _, s := range localSuffixes {
		if strings.HasSuffix(h, s) {
			return false
		}
	}
	return true
}

// FrameAncestors is the frame-ancestors value for a site: its origins, or
// 'none' until some are configured.
func FrameAncestors(origins []string) string {
	if len(origins) == 0 {
		return "'none'"
	}
	return strings.Join(origins, " ")
}
