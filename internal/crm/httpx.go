package crm

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/safehttp"
)

// Request is one call to a CRM's HTTP API, as the connectors of this
// package tree send it.
type Request struct {
	Method string
	URL    string
	Header map[string]string
	// Body is nil, a url.Values (form encoded), a Raw (as is) or anything
	// else (JSON).
	Body any
	// Authed marks a request that carries the access token: a 401 is then
	// ErrAuth, so the framework refreshes the token once.
	Authed bool
}

// Raw is a request body with its content type (a multipart upload).
type Raw struct {
	Type string
	Data []byte
}

// MaxResponse bounds how much of a response is read.
const MaxResponse = 1 << 20

// Do sends r and decodes a 2xx JSON answer into out (when not nil). Errors
// never carry the URL or a credential: a connector's URL may hold one (a
// Bitrix24 webhook), so the message is the CRM's own text only.
func Do(ctx context.Context, hc *http.Client, r Request, out any) error {
	var body *bytes.Reader
	ctype := ""
	switch b := r.Body.(type) {
	case nil:
		body = bytes.NewReader(nil)
	case url.Values:
		body, ctype = bytes.NewReader([]byte(b.Encode())), "application/x-www-form-urlencoded"
	case Raw:
		body, ctype = bytes.NewReader(b.Data), b.Type
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return &APIError{Msg: "encoding the request"}
		}
		body, ctype = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, r.URL, body)
	if err != nil {
		return &APIError{Msg: "building the request"}
	}
	req.Header.Set("Accept", "application/json")
	if ctype != "" {
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
		return &APIError{Retry: true, Msg: "network error"}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := safehttp.ReadBody(resp.Body, MaxResponse)
	if resp.StatusCode/100 != 2 {
		return StatusError(resp, raw, r.Authed)
	}
	if rerr != nil {
		return &APIError{Status: resp.StatusCode, Retry: true, Msg: "reading the response"}
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return &APIError{Status: resp.StatusCode, Msg: "unreadable response"}
		}
	}
	return nil
}

// StatusError turns a non-2xx answer into an error: 401 on an authed
// request is ErrAuth; 408, 429 and 5xx are retried (429 with its
// Retry-After); the rest are permanent.
func StatusError(resp *http.Response, raw []byte, authed bool) error {
	if resp.StatusCode == http.StatusUnauthorized && authed {
		return ErrAuth
	}
	e := &APIError{Status: resp.StatusCode}
	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Retry = true
		if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
			e.After = time.Duration(s) * time.Second
		}
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusRequestTimeout:
		e.Retry = true
	}
	e.Msg = errorText(raw)
	if e.Msg == "" {
		e.Msg = http.StatusText(resp.StatusCode)
	}
	return e
}

// errorText finds the CRM's own message in an error body: message, error
// (text or object), error_description or error_message.
func errorText(raw []byte) string {
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return ""
	}
	text := ""
	for _, k := range []string{"error_description", "message", "error_message", "error"} {
		switch v := m[k].(type) {
		case string:
			text = v
		case map[string]any:
			if s, ok := v["message"].(string); ok {
				text = s
			}
		}
		if text != "" {
			break
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}

// SameNumber reports whether two phone numbers end in the same digits (the
// last nine, or all of the shorter one); either needs seven digits.
func SameNumber(a, b string) bool {
	da, db := onlyDigits(a), onlyDigits(b)
	if len(da) < 7 || len(db) < 7 {
		return false
	}
	n := min(9, len(da), len(db))
	return da[len(da)-n:] == db[len(db)-n:]
}

// Tail is the last nine digits of a number.
func Tail(number string) string {
	d := onlyDigits(number)
	if len(d) > 9 {
		d = d[len(d)-9:]
	}
	return d
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CallTitle is the subject Hello gives a logged call.
func CallTitle(l CallLog) string {
	who := l.Number
	if l.Contact != nil && l.Contact.Name != "" {
		who = l.Contact.Name
	}
	switch {
	case l.Missed:
		return "Missed call from " + who
	case l.Direction == "outbound":
		return "Call to " + who
	}
	return "Call from " + who
}

// CallTime is when the call started, falling back to the first time known.
func CallTime(l CallLog) time.Time {
	for _, t := range []time.Time{l.StartedAt, l.AnsweredAt, l.EndedAt} {
		if !t.IsZero() {
			return t.UTC()
		}
	}
	return time.Now().UTC()
}
