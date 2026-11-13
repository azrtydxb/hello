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

// REST is the JSON-over-HTTPS client connectors share: it sends one request
// and turns a failure into the framework's errors (ErrAuth for a refused
// token, APIError with Retry for what may pass). Errors never carry the
// request URL or a token.
type REST struct {
	HC *http.Client
	// Classify reads a CRM's error body: its message, and whether the
	// failure may pass although the status is not a 5xx (a rate limit the
	// CRM reports as 403). Nil uses the status alone.
	Classify func(status int, raw []byte) (msg string, retry bool)
}

// Do sends one request. A url.Values body is form-encoded, anything else
// JSON. token, when set, is sent as a bearer token; extra headers are added
// as given. out, when set, receives the JSON answer.
func (r REST) Do(ctx context.Context, method, rawURL, token string, hdr map[string]string, body any, out any) error {
	var rd *bytes.Reader
	ctype := ""
	switch b := body.(type) {
	case nil:
		rd = bytes.NewReader(nil)
	case url.Values:
		rd, ctype = bytes.NewReader([]byte(b.Encode())), "application/x-www-form-urlencoded"
	default:
		raw, err := json.Marshal(b)
		if err != nil {
			return err
		}
		rd, ctype = bytes.NewReader(raw), "application/json"
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return &APIError{Msg: "building the request"}
	}
	req.Header.Set("Accept", "application/json")
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := r.HC.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &APIError{Retry: true, Msg: "network error"}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := safehttp.ReadBody(resp.Body, 1<<20)
	if resp.StatusCode/100 != 2 {
		return r.statusError(resp, raw, token != "")
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

func (r REST) statusError(resp *http.Response, raw []byte, authed bool) error {
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
	if r.Classify != nil {
		msg, retry := r.Classify(resp.StatusCode, raw)
		e.Msg = msg
		e.Retry = e.Retry || retry
		if retry && e.After == 0 {
			if s, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && s > 0 {
				e.After = time.Duration(s) * time.Second
			}
		}
	}
	e.Msg = strings.Join(strings.Fields(e.Msg), " ")
	if len(e.Msg) > 200 {
		e.Msg = e.Msg[:200]
	}
	if e.Msg == "" {
		e.Msg = http.StatusText(resp.StatusCode)
	}
	return e
}
