// Package kit holds what the REST connectors of internal/crm share: one HTTP
// request helper that maps a CRM's failures onto the framework's errors, the
// phone-number comparison the contact lookups need, and the text a call
// becomes in a ticket or a note.
package kit

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/crm"
	"github.com/azrtydxb/hello/internal/safehttp"
)

// MaxBody bounds a response the helper reads.
const MaxBody = 1 << 20

// Request is one call to a CRM.
type Request struct {
	Method string
	// URL is the full address. It never appears in an error.
	URL    string
	Header map[string]string
	// Body is encoded as a form for url.Values, sent as is for []byte, and
	// marshalled as JSON for anything else.
	Body any
	// ContentType overrides the JSON default (ConnectWise patches).
	ContentType string
	// Authed marks a request that carries credentials: a 401 is then
	// crm.ErrAuth, so the framework signs in again instead of failing the job.
	Authed bool
	// Classify reads a CRM's own error body: its message (empty falls back to
	// Message) and whether the failure may pass although the status is not a
	// 5xx (a rate limit the CRM reports as 403). Nil uses the status alone.
	Classify func(status int, raw []byte) (msg string, retry bool)
}

// Do sends r and decodes a JSON answer into out (nil ignores the body).
func Do(ctx context.Context, hc *http.Client, r Request, out any) error {
	var rd *bytes.Reader
	ctype := r.ContentType
	switch b := r.Body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case url.Values:
		rd = bytes.NewReader([]byte(b.Encode()))
		if ctype == "" {
			ctype = "application/x-www-form-urlencoded"
		}
	case []byte:
		rd = bytes.NewReader(b)
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(raw)
		if ctype == "" {
			ctype = "application/json"
		}
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, rd)
	if err != nil {
		return &crm.APIError{Msg: "building the request"}
	}
	req.Header.Set("Accept", "application/json")
	if ctype != "" && r.Body != nil {
		req.Header.Set("Content-Type", ctype)
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	resp, err := hc.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &crm.APIError{Retry: true, Msg: "network error"}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := safehttp.ReadBody(resp.Body, MaxBody)
	if resp.StatusCode/100 != 2 {
		return statusError(resp, raw, r.Authed, r.Classify)
	}
	if rerr != nil {
		return &crm.APIError{Status: resp.StatusCode, Retry: true, Msg: "reading the response"}
	}
	if out != nil && len(bytes.TrimSpace(raw)) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return &crm.APIError{Status: resp.StatusCode, Msg: "unreadable response"}
		}
	}
	return nil
}

// StatusError maps a failed response onto the framework's errors: 401 on an
// authenticated request is crm.ErrAuth; 408, 429 and 5xx may pass (retry,
// with the CRM's Retry-After); everything else is permanent.
func StatusError(resp *http.Response, raw []byte, authed bool) error {
	return statusError(resp, raw, authed, nil)
}

func statusError(resp *http.Response, raw []byte, authed bool, classify func(int, []byte) (string, bool)) error {
	if resp.StatusCode == http.StatusUnauthorized && authed {
		return crm.ErrAuth
	}
	e := &crm.APIError{Status: resp.StatusCode}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Retry = true
		e.After = RetryAfter(resp.Header.Get("Retry-After"), time.Now())
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout:
		e.Retry = true
	}
	if classify != nil {
		msg, retry := classify(resp.StatusCode, raw)
		e.Msg = strings.Join(strings.Fields(msg), " ")
		if len(e.Msg) > 200 {
			e.Msg = e.Msg[:200]
		}
		if retry && !e.Retry {
			e.Retry = true
			e.After = RetryAfter(resp.Header.Get("Retry-After"), time.Now())
		}
	}
	if e.Msg == "" {
		e.Msg = Message(raw)
	}
	if e.Msg == "" {
		e.Msg = http.StatusText(resp.StatusCode)
	}
	return e
}

// RetryAfter reads a Retry-After header: seconds or an HTTP date.
func RetryAfter(h string, now time.Time) time.Duration {
	h = strings.TrimSpace(h)
	if s, err := strconv.Atoi(h); err == nil && s > 0 {
		return time.Duration(s) * time.Second
	}
	if t, err := http.ParseTime(h); err == nil && t.After(now) {
		return t.Sub(now)
	}
	return 0
}

// Message pulls a short human message out of a CRM's error body, whatever
// shape it has (message, error, description, errors, detail), single-line
// and at most 200 characters.
func Message(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	for _, k := range []string{"message", "error_description", "error", "description", "detail", "title", "errors"} {
		if s := text(m[k]); s != "" {
			s = strings.Join(strings.Fields(s), " ")
			if len(s) > 200 {
				s = s[:200]
			}
			return s
		}
	}
	return ""
}

func text(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []any:
		parts := make([]string, 0, len(t))
		for _, e := range t {
			if s := text(e); s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, "; ")
	case map[string]any:
		for _, k := range []string{"message", "description", "detail", "field"} {
			if s := text(t[k]); s != "" {
				return s
			}
		}
	}
	return ""
}

// Basic is an HTTP Basic Authorization value.
func Basic(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

var labelRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

// Label validates one DNS label (a Zendesk or Freshdesk subdomain).
func Label(s string) bool { return labelRE.MatchString(s) }

var hostRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,63}$`)

// Host validates a public host name: no scheme, port, path or address. The
// SSRF-safe client still refuses private addresses; this keeps the setting
// to what a vendor documents.
func Host(s string) bool { return len(s) <= 253 && hostRE.MatchString(s) }

// Digits keeps the decimal digits of s.
func Digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Last4 is the last four digits of a number, the narrowest search term that
// survives however a CRM formats the stored number ("(555) 123-4567").
func Last4(number string) string {
	d := Digits(number)
	if len(d) > 4 {
		d = d[len(d)-4:]
	}
	return d
}

// Same reports whether a stored phone number is the looked-up number: equal
// digits, or equal in their last nine (or, for a shorter number, all but
// never fewer than seven), so +32 475 12 34 56 matches 0475/12.34.56.
func Same(stored, number string) bool {
	a, b := Digits(stored), Digits(number)
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	k := min(len(a), len(b), 9)
	return k >= 7 && a[len(a)-k:] == b[len(b)-k:]
}

// AnySame reports whether any of the stored numbers is the looked-up one.
func AnySame(number string, stored ...string) bool {
	for _, s := range stored {
		if Same(s, number) {
			return true
		}
	}
	return false
}

// Timestamp is the best start time of a call.
func Timestamp(l crm.CallLog) time.Time {
	for _, t := range []time.Time{l.StartedAt, l.AnsweredAt, l.EndedAt} {
		if !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}

// Who is the other party as a person reads it: the contact's name, else the
// number.
func Who(l crm.CallLog) string {
	if l.Contact != nil && l.Contact.Name != "" {
		return l.Contact.Name
	}
	return l.Number
}

// Title is the one-line subject of a call.
func Title(l crm.CallLog) string {
	switch {
	case l.Missed:
		return "Missed call from " + Who(l)
	case l.Direction == "outbound":
		return "Call to " + Who(l)
	}
	return "Call from " + Who(l)
}

// Clock formats a duration as 3m 12s.
func Clock(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh %dm %ds", h, m, s)
	case m > 0:
		return fmt.Sprintf("%dm %ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}

var outcomeText = map[crm.Outcome]string{
	crm.OutcomeAnswered: "answered", crm.OutcomeNoAnswer: "no answer", crm.OutcomeBusy: "busy",
	crm.OutcomeCancelled: "cancelled", crm.OutcomeFailed: "failed",
}

// Lines is what a call says about itself, one fact per line, for a ticket
// description or a note. The recording line is last and only there while the
// call has a link, so a note rewritten with a renewed link stays accurate.
func Lines(l crm.CallLog) []string {
	dir := "Inbound call"
	if l.Direction == "outbound" {
		dir = "Outbound call"
	}
	if l.Missed {
		dir = "Missed inbound call"
	}
	out := []string{fmt.Sprintf("%s: %s to %s", dir, orDash(l.From), orDash(l.To))}
	if l.Extension != "" {
		out = append(out, "Extension: "+l.Extension)
	}
	res := "Outcome: " + outcomeText[l.Outcome]
	if l.Outcome == crm.OutcomeAnswered && l.Duration > 0 {
		res += " (talk time " + Clock(l.Duration) + ")"
	} else if l.Duration > 0 {
		res += " (rang " + Clock(l.Duration) + ")"
	}
	out = append(out, res, "Started: "+Timestamp(l).Format("2006-01-02 15:04:05 UTC"))
	if l.RecordingURL != "" {
		out = append(out, "Recording: "+l.RecordingURL)
	}
	if l.CallID != "" {
		out = append(out, "Logged by Hello, call "+l.CallID)
	}
	return out
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// Text is Lines joined for a plain-text field.
func Text(l crm.CallLog) string { return strings.Join(Lines(l), "\n") }

// HTML is Lines for a field that takes HTML; every value is escaped and the
// recording link becomes an anchor.
func HTML(l crm.CallLog) string {
	var b strings.Builder
	for i, line := range Lines(l) {
		if i > 0 {
			b.WriteString("<br>")
		}
		if rest, ok := strings.CutPrefix(line, "Recording: "); ok {
			e := Escape(rest)
			b.WriteString(`Recording: <a href="` + e + `">` + e + `</a>`)
			continue
		}
		b.WriteString(Escape(line))
	}
	return b.String()
}

// Escape escapes text for HTML.
func Escape(s string) string {
	return strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;").Replace(s)
}

// RecordingNote is the note that carries a recording link.
func RecordingNote(url string) string { return "Recording: " + url }

// Clip cuts s to n runes.
func Clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}
