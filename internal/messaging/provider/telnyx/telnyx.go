// Package telnyx is the Telnyx adapter of the SMS provider interface (spec
// messaging-channels M-3): text-only sending, credential validation, number
// listing and status reads over Telnyx's v2 REST API. The API key goes in an
// Authorization header and nowhere else: not in a URL, an error or a log.
// Message bodies and numbers are not logged or put in errors.
package telnyx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/safehttp"
)

// Kind is the provider name stored on a channel.
const Kind = "telnyx"

// DefaultBaseURL is Telnyx's v2 REST API.
const DefaultBaseURL = "https://api.telnyx.com/v2"

// maxBody bounds what is read from an answer.
const maxBody = 1 << 20

// maxBodyChars is the longest text Hello sends in one Telnyx message (Telnyx
// segments it).
const maxBodyChars = 1600

// statusWindow is how long Hello keeps reading a text's status: 48 hours
// (spec M-9a), well inside the 10 days Telnyx answers for.
const statusWindow = 48 * time.Hour

// maxPages bounds how many pages of numbers are read.
const maxPages = 20

// errCodeBlocked is Telnyx's error 40300: the recipient replied STOP and the
// carrier blocks the send. [unverified against a live account]
const errCodeBlocked = "40300"

func init() { provider.Register(Kind, Factory()) }

// Option changes how the adapter reaches Telnyx; tests aim it at a fake.
type Option func(*Client)

// WithBaseURL replaces the API root. It is not configurable by an
// administrator: only Go code (tests) can set it.
func WithBaseURL(u string) Option { return func(c *Client) { c.base = strings.TrimRight(u, "/") } }

// WithHTTPClient replaces the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// Factory returns the registry factory, with options applied to every
// adapter it builds.
func Factory(opts ...Option) provider.Factory {
	return func(cfg provider.Config) (provider.SMSProvider, error) { return New(cfg, opts...) }
}

// Client is a Telnyx account.
type Client struct {
	cfg  provider.Config
	base string
	http *http.Client
}

// New builds an adapter. The API key is required.
func New(cfg provider.Config, opts ...Option) (*Client, error) {
	cfg.APIKey = strings.TrimSpace(cfg.APIKey)
	cfg.MessagingProfileID = strings.TrimSpace(cfg.MessagingProfileID)
	if cfg.APIKey == "" {
		return nil, errors.New("telnyx: the API key is required")
	}
	if cfg.MessagingProfileID != "" && !ValidID(cfg.MessagingProfileID) {
		return nil, errors.New("telnyx: the messaging profile ID is a UUID")
	}
	c := &Client{cfg: cfg, base: DefaultBaseURL, http: safehttp.NewClient(safehttp.Options{Timeout: 15 * time.Second})}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// ValidID reports whether s has the shape of a Telnyx identifier: a UUID.
func ValidID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if r != '-' {
				return false
			}
		case r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// Kind implements provider.SMSProvider.
func (c *Client) Kind() string { return Kind }

// Caps implements provider.SMSProvider.
func (c *Client) Caps() provider.Caps {
	return provider.Caps{MaxBody: maxBodyChars, MMS: false, StatusWindow: statusWindow, OptOut: "provider"}
}

// do sends one request and returns the status and the (bounded) body. A
// transport failure is ErrAmbiguous: the request may have reached Telnyx.
func (c *Client) do(ctx context.Context, method, path string, payload any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Hello-PBX")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// The error text of net/http can carry the URL: drop it.
		return 0, nil, fmt.Errorf("%w: request failed", provider.ErrAmbiguous)
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, fmt.Errorf("%w: answer unreadable", provider.ErrAmbiguous)
	}
	return resp.StatusCode, b, nil
}

// apiErrors is Telnyx's error envelope.
type apiErrors struct {
	Errors []struct {
		Code json.Number `json:"code"`
	} `json:"errors"`
}

func (e apiErrors) first() string {
	if len(e.Errors) == 0 {
		return ""
	}
	return e.Errors[0].Code.String()
}

// refusal turns a non-2xx answer into the adapter's error. Telnyx answers
// 403 both for a key that may not do this and for a send blocked by a STOP
// (error 40300), so the error code decides before the status does.
func refusal(status int, b []byte) error {
	var e apiErrors
	_ = json.Unmarshal(b, &e)
	code := e.first()
	switch {
	case code == errCodeBlocked:
		return &provider.Rejected{Code: provider.CodeOptedOut}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return provider.ErrAuth
	case status == http.StatusNotFound:
		return provider.ErrNotFound
	case status == http.StatusTooManyRequests:
		return &provider.Rejected{Code: provider.CodeRateLimited}
	case status >= 500:
		return fmt.Errorf("%w: carrier error %d", provider.ErrAmbiguous, status)
	}
	if code == "" || len(code) > 16 {
		code = fmt.Sprintf("http_%d", status)
	}
	return &provider.Rejected{Code: code}
}

// Validate implements provider.SMSProvider: the key is accepted and the
// account's messaging numbers can be listed.
func (c *Client) Validate(ctx context.Context) error {
	_, err := c.Numbers(ctx)
	return err
}

// Numbers implements provider.SMSProvider: the numbers with messaging
// settings, so each says whether it does SMS and MMS.
func (c *Client) Numbers(ctx context.Context) ([]provider.Number, error) {
	out := []provider.Number{}
	for page := 1; page <= maxPages; page++ {
		status, b, err := c.do(ctx, http.MethodGet, fmt.Sprintf("/phone_numbers/messaging?page%%5Bsize%%5D=250&page%%5Bnumber%%5D=%d", page), nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, refusal(status, b)
		}
		var r struct {
			Data []struct {
				ID          string `json:"id"`
				PhoneNumber string `json:"phone_number"`
				Type        string `json:"type"`
				Features    struct {
					SMS struct {
						DomesticTwoWay       bool `json:"domestic_two_way"`
						InternationalInbound bool `json:"international_inbound"`
						InternationalOut     bool `json:"international_outbound"`
					} `json:"sms"`
					MMS struct {
						DomesticTwoWay bool `json:"domestic_two_way"`
					} `json:"mms"`
				} `json:"features"`
			} `json:"data"`
			Meta struct {
				TotalPages int `json:"total_pages"`
			} `json:"meta"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("%w: answer unreadable", provider.ErrAmbiguous)
		}
		for _, n := range r.Data {
			sms := n.Features.SMS
			out = append(out, provider.Number{ID: n.ID, E164: n.PhoneNumber, Label: provider.Clean(n.Type, 32),
				SMS: sms.DomesticTwoWay || sms.InternationalInbound || sms.InternationalOut, MMS: n.Features.MMS.DomesticTwoWay})
		}
		if page >= r.Meta.TotalPages {
			break
		}
	}
	return out, nil
}

// Send implements provider.SMSProvider. Telnyx answers 200 with the
// message's id when it accepted the text. No webhook is set: the LAN-only
// build polls the message instead.
func (c *Client) Send(ctx context.Context, m provider.Outbound) (provider.Sent, error) {
	if m.Body == "" || len([]rune(m.Body)) > maxBodyChars {
		return provider.Sent{}, &provider.Rejected{Code: "body_length"}
	}
	req := map[string]any{"from": m.From, "to": m.To, "text": m.Body, "type": "SMS"}
	if c.cfg.MessagingProfileID != "" {
		req["messaging_profile_id"] = c.cfg.MessagingProfileID
	}
	status, b, err := c.do(ctx, http.MethodPost, "/messages", req)
	if err != nil {
		return provider.Sent{}, err
	}
	if status != http.StatusOK && status != http.StatusCreated && status != http.StatusAccepted {
		return provider.Sent{}, refusal(status, b)
	}
	var r struct {
		Data struct {
			ID string `json:"id"`
			To []struct {
				Status string `json:"status"`
			} `json:"to"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil || !ValidID(r.Data.ID) {
		// Telnyx said yes but Hello cannot tell which message: do not retry.
		return provider.Sent{}, fmt.Errorf("%w: answer unreadable", provider.ErrAmbiguous)
	}
	st := provider.StatusQueued
	if len(r.Data.To) > 0 {
		st = normalise(r.Data.To[0].Status)
	}
	return provider.Sent{ProviderID: r.Data.ID, Status: st}, nil
}

// Status implements provider.SMSProvider: GET /messages/{id}, which Telnyx
// answers for ten days after the send.
func (c *Client) Status(ctx context.Context, providerID string) (provider.State, error) {
	if !ValidID(providerID) {
		return provider.State{}, provider.ErrNotFound
	}
	status, b, err := c.do(ctx, http.MethodGet, "/messages/"+providerID, nil)
	if err != nil {
		return provider.State{}, err
	}
	if status != http.StatusOK {
		return provider.State{}, refusal(status, b)
	}
	var r struct {
		Data struct {
			To []struct {
				Status string `json:"status"`
			} `json:"to"`
			Errors []struct {
				Code json.Number `json:"code"`
			} `json:"errors"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return provider.State{}, fmt.Errorf("%w: answer unreadable", provider.ErrAmbiguous)
	}
	raw := ""
	if len(r.Data.To) > 0 {
		raw = r.Data.To[0].Status
	}
	st := provider.State{Status: normalise(raw)}
	if st.Status == provider.StatusFailed {
		code := ""
		if len(r.Data.Errors) > 0 {
			code = r.Data.Errors[0].Code.String()
		}
		switch {
		case code == errCodeBlocked:
			code = provider.CodeOptedOut
		case code == "" || len(code) > 16:
			code = safeToken(raw)
		}
		st.ErrorCode = code
	}
	return st, nil
}

// safeToken keeps a short lower-case token of letters, digits and _.
func safeToken(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			b.WriteRune(r)
		}
	}
	if b.Len() > 16 {
		return b.String()[:16]
	}
	if b.Len() == 0 {
		return "failed"
	}
	return b.String()
}

// normalise maps Telnyx's message statuses onto Hello's: queued and sending
// are not yet out; sent and delivery_unconfirmed (the carrier gave no
// receipt) are out; delivered is delivered; sending_failed and
// delivery_failed are failed. An unknown value is treated as in flight.
func normalise(s string) string {
	switch s {
	case "delivered", "webhook_delivered":
		return provider.StatusDelivered
	case "sending_failed", "delivery_failed":
		return provider.StatusFailed
	case "sent", "delivery_unconfirmed":
		return provider.StatusSent
	default:
		return provider.StatusQueued
	}
}
