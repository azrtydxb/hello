package syslogsink

import (
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/azrtydxb/hello/internal/livestate"
)

// MaxLineBytes is the longest message kept; a longer one is cut.
const MaxLineBytes = 2048

// Parse turns one received datagram or frame into a Line: the optional
// <PRI> prefix is read for the severity and stripped, control characters
// are replaced, the text is redacted and cut to MaxLineBytes. ok is false
// for an empty line.
func Parse(raw string, source string, at time.Time) (livestate.SyslogLine, bool) {
	raw = strings.TrimRight(raw, "\r\n\x00")
	sev := -1
	if strings.HasPrefix(raw, "<") {
		if end := strings.IndexByte(raw, '>'); end > 1 && end <= 4 {
			if n, err := strconv.Atoi(raw[1:end]); err == nil && n >= 0 && n <= 191 {
				sev = n & 7
				raw = raw[end+1:]
			}
		}
	}
	msg := Redact(strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 || r == 0x7f || r == utf8.RuneError {
			return -1
		}
		return r
	}, raw))
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return livestate.SyslogLine{}, false
	}
	if len(msg) > MaxLineBytes {
		msg = strings.ToValidUTF8(msg[:MaxLineBytes], "")
	}
	return livestate.SyslogLine{At: at.UTC(), Source: source, Severity: sev, Message: msg}, true
}
