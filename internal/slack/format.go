package slack

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/azrtydxb/hello/internal/events"
)

// maxField bounds an untrusted name or number put into a message.
const maxField = 80

// esc makes untrusted text safe for Slack's mrkdwn: Slack reads <...> as
// links and mentions (<!channel>, <@U123>), so angle brackets and the
// ampersand are escaped as Slack documents, control characters are dropped,
// formatting characters are neutralised and the text is shortened.
func esc(s string) string {
	var b strings.Builder
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if unicode.IsControl(r) || !unicode.IsPrint(r) {
			continue
		}
		if n++; n > maxField {
			b.WriteString("...")
			break
		}
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '*', '_', '~', '`':
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	return strings.TrimSpace(b.String())
}

// who renders one end of a call: "Name (number)", the number alone or
// "unknown".
func who(p events.Party) string {
	name, num := esc(p.Name), esc(p.Number)
	switch {
	case name != "" && num != "" && name != num:
		return "*" + name + "* (" + num + ")"
	case name != "":
		return "*" + name + "*"
	case num != "":
		return "*" + num + "*"
	}
	return "an unknown caller"
}

var missedReasons = map[string]string{
	"no_answer": "nobody answered", "busy": "the line was busy", "dnd": "do not disturb was on",
	"cancelled": "the caller hung up while it rang", "unreachable": "the extension was not reachable",
}

// Message is the Slack message for an event: a fallback text (used in
// notifications) and Block Kit blocks. publicURL, when set, adds a link to
// the console page where the item can be handled. ok is false for an event
// type that is not posted.
func Message(e events.Event, publicURL string) (text string, blocks []map[string]any, ok bool) {
	d := e.Data
	to := d.To
	if to.Extension == "" && d.Extension != "" {
		to.Extension = d.Extension
	}
	callee := "*" + esc(to.Extension) + "*"
	if to.Name != "" {
		callee = "*" + esc(to.Name) + "* (" + esc(to.Extension) + ")"
	}
	var head, plain, link, linkText string
	base := strings.TrimRight(publicURL, "/")
	switch e.Type {
	case events.CallMissed:
		head = ":telephone_receiver: Missed call from " + who(d.From) + " to " + callee
		if r, known := missedReasons[d.Reason]; known {
			head += " (" + r + ")"
		}
		plain = "Missed call from " + plainParty(d.From) + " to " + plainExt(to)
		if base != "" {
			link, linkText = base+"/history", "Call history"
		}
	case events.VoicemailReceived:
		head = ":mailbox_with_mail: New voicemail for " + callee + " from " + who(d.From)
		if d.DurationMs > 0 {
			head += ", " + dur(d.DurationMs)
		}
		plain = "New voicemail for " + plainExt(to) + " from " + plainParty(d.From)
		if base != "" {
			link, linkText = base+"/voicemail", "Open the voicemail box"
		}
	default:
		return "", nil, false
	}
	when := e.CreatedAt
	if when.IsZero() {
		when = time.Now()
	}
	ts := strconv.FormatInt(when.Unix(), 10)
	ctx := "<!date^" + ts + "^{date_short_pretty} at {time}|" + when.UTC().Format("2006-01-02 15:04 UTC") + ">"
	if link != "" {
		ctx += "  |  <" + link + "|" + linkText + ">"
	}
	blocks = []map[string]any{
		{"type": "section", "text": map[string]any{"type": "mrkdwn", "text": head}},
		{"type": "context", "elements": []map[string]any{{"type": "mrkdwn", "text": ctx}}},
	}
	return plain, blocks, true
}

func plainParty(p events.Party) string {
	switch {
	case p.Name != "" && p.Number != "":
		return esc(p.Name) + " (" + esc(p.Number) + ")"
	case p.Name != "":
		return esc(p.Name)
	case p.Number != "":
		return esc(p.Number)
	}
	return "an unknown caller"
}

func plainExt(p events.Party) string {
	if p.Name != "" {
		return esc(p.Name) + " (" + esc(p.Extension) + ")"
	}
	return esc(p.Extension)
}

func dur(ms int64) string {
	s := (ms + 500) / 1000
	if s < 60 {
		return fmt.Sprintf("%d s", s)
	}
	return fmt.Sprintf("%d min %02d s", s/60, s%60)
}

// Body is the JSON a post sends. channel is set in bot mode only.
func Body(e events.Event, publicURL, mode, channel string) ([]byte, bool) {
	text, blocks, ok := Message(e, publicURL)
	if !ok {
		return nil, false
	}
	m := map[string]any{"text": text, "blocks": blocks, "unfurl_links": false, "unfurl_media": false}
	if mode == ModeBot {
		m["channel"] = channel
	}
	b, err := json.Marshal(m)
	return b, err == nil
}
