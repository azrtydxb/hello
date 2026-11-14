package chat

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/safehttp"
)

// TurnstileEndpoint is Cloudflare's server-side verification URL.
const TurnstileEndpoint = "https://challenges.cloudflare.com/turnstile/v0/siteverify"

// Turnstile verifies Cloudflare Turnstile tokens with the customer's own
// secret key. A token is valid for 300 seconds and works once; Cloudflare
// answers a replay with timeout-or-duplicate. The call goes through safehttp.
type Turnstile struct {
	Client   *http.Client
	Endpoint string
}

// NewTurnstile returns a verifier that reaches only public addresses.
func NewTurnstile() *Turnstile {
	return &Turnstile{Client: safehttp.NewClient(safehttp.Options{Timeout: 5 * time.Second}), Endpoint: TurnstileEndpoint}
}

// Verify reports whether token passes for secret. An error means the check
// could not be made (the caller refuses the session: it never fails open).
func (t *Turnstile) Verify(ctx context.Context, secret, token, remoteIP string) (bool, error) {
	if token == "" || len(token) > 2048 {
		return false, nil
	}
	form := url.Values{"secret": {secret}, "response": {token}}
	if remoteIP != "" {
		form.Set("remoteip", remoteIP)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := t.Client.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return false, errors.New("chat: turnstile verification failed with status " + resp.Status)
	}
	body, err := safehttp.ReadBody(resp.Body, 16<<10)
	if err != nil {
		return false, err
	}
	var out struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return false, err
	}
	return out.Success, nil
}
