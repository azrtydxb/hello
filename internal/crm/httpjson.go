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

// JSONClient is the small HTTP helper the OAuth connectors that talk JSON
// share (Outlook, Google): bearer auth, form or JSON bodies, and failures
// mapped to *APIError (retryable for network errors, 5xx, 408 and 429) or
// ErrAuth for a refused access token.
type JSONClient struct{ HC *http.Client }

// MaxResponse bounds how much of an answer is read.
const MaxResponse = 4 << 20

// Do sends one request. body is nil, url.Values (form) or a value marshalled
// as JSON. out, when not nil, receives the decoded answer. headers are extra
// request headers.
func (j JSONClient) Do(ctx context.Context, method, rawURL, token string, headers map[string]string, body, out any) error {
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
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := j.HC.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &APIError{Retry: true, Msg: "network error"}
	}
	defer func() { _ = resp.Body.Close() }()
	raw, rerr := safehttp.ReadBody(resp.Body, MaxResponse)
	if resp.StatusCode/100 != 2 {
		return JSONStatusError(resp, raw, token != "")
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

// JSONStatusError maps a failed response. The message is a short code or
// sentence from the provider, with whitespace collapsed and no body dump.
func JSONStatusError(resp *http.Response, raw []byte, authed bool) error {
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
	var m struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	if json.Unmarshal(raw, &m) == nil {
		var nested struct {
			Code    any    `json:"code"`
			Message string `json:"message"`
			Status  string `json:"status"`
		}
		var plain string
		switch {
		case json.Unmarshal(m.Error, &nested) == nil && (nested.Message != "" || nested.Status != ""):
			e.Msg = nested.Message
			if e.Msg == "" {
				e.Msg = nested.Status
			}
		case json.Unmarshal(m.Error, &plain) == nil && plain != "":
			e.Msg = plain
			if m.Description != "" {
				e.Msg += ": " + m.Description
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
