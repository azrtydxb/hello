package kit

import (
	"context"
	"encoding/json"
	"net/http"
)

// Bearer is Do with the call shape of the OAuth connectors that talk JSON to
// one provider (Outlook, Google): an optional bearer token, a form or JSON
// body, and a failed call mapped as StatusError does (a refused access token
// is crm.ErrAuth).
type Bearer struct{ HC *http.Client }

// Do sends one request. token "" sends no Authorization header and makes a
// 401 a plain failure; body is nil, url.Values or a value marshalled as JSON.
func (b Bearer) Do(ctx context.Context, method, rawURL, token string, headers map[string]string, body, out any) error {
	h := map[string]string{}
	for k, v := range headers {
		h[k] = v
	}
	if token != "" {
		h["Authorization"] = "Bearer " + token
	}
	return Do(ctx, b.HC, Request{Method: method, URL: rawURL, Header: h, Body: body, Authed: token != "", Classify: oauthError}, out)
}

// oauthError reads the error shapes of Microsoft and Google: a plain OAuth
// code with its description ("invalid_grant: ..."), or an object carrying a
// message or a status. Anything else falls back to Message.
func oauthError(_ int, raw []byte) (string, bool) {
	var m struct {
		Error       json.RawMessage `json:"error"`
		Description string          `json:"error_description"`
	}
	if json.Unmarshal(raw, &m) != nil {
		return "", false
	}
	var plain string
	if json.Unmarshal(m.Error, &plain) == nil && plain != "" {
		if m.Description != "" {
			plain += ": " + m.Description
		}
		return plain, false
	}
	var nested struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(m.Error, &nested) == nil {
		if nested.Message != "" {
			return nested.Message, false
		}
		return nested.Status, false
	}
	return "", false
}
