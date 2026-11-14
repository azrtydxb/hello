// Package twilio is the Twilio adapter of the SMS provider interface (spec
// messaging-channels M-3): text-only sending, credential validation, number
// listing and status reads over Twilio's REST API. The account's auth token
// goes in an HTTP Basic header and nowhere else: not in a URL, an error or a
// log. Message bodies and numbers are not logged or put in errors.
package twilio

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/azrtydxb/hello/internal/messaging/provider"
	"github.com/azrtydxb/hello/internal/safehttp"
)

// Kind is the provider name stored on a channel.
const Kind = "twilio"

// DefaultBaseURL is Twilio's REST API.
const DefaultBaseURL = "https://api.twilio.com/2010-04-01"

// DefaultMessagingURL is Twilio's Messaging API, which holds the Messaging
// Services, the A2P 10DLC brands and campaigns and the toll-free
// verifications.
const DefaultMessagingURL = "https://messaging.twilio.com/v1"

// maxBody bounds what is read from an answer.
const maxBody = 1 << 20

// maxBodyChars is Twilio's longest message body (it segments below that).
const maxBodyChars = 1600

// statusWindow is how long Twilio keeps a message's status readable that
// Hello relies on: polling stops after 48 hours (spec M-9a).
const statusWindow = 48 * time.Hour

func init() { provider.Register(Kind, Factory()) }

// Option changes how the adapter reaches Twilio; tests aim it at a fake.
type Option func(*Client)

// WithBaseURL replaces the API root. It is not configurable by an
// administrator: only Go code (tests) can set it.
func WithBaseURL(u string) Option { return func(c *Client) { c.base = strings.TrimRight(u, "/") } }

// WithMessagingURL replaces the root of the Messaging API (registrations).
// Only Go code (tests) can set it.
func WithMessagingURL(u string) Option {
	return func(c *Client) { c.messaging = strings.TrimRight(u, "/") }
}

// WithHTTPClient replaces the HTTP client.
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.http = h } }

// Factory returns the registry factory, with options applied to every
// adapter it builds.
func Factory(opts ...Option) provider.Factory {
	return func(cfg provider.Config) (provider.SMSProvider, error) { return New(cfg, opts...) }
}

// Client is a Twilio account.
type Client struct {
	cfg       provider.Config
	base      string
	messaging string
	http      *http.Client
}

// New builds an adapter. The account SID and the token are required.
func New(cfg provider.Config, opts ...Option) (*Client, error) {
	cfg.AccountSID = strings.TrimSpace(cfg.AccountSID)
	cfg.AuthToken = strings.TrimSpace(cfg.AuthToken)
	if !ValidAccountSID(cfg.AccountSID) {
		return nil, errors.New("twilio: the account SID looks like AC followed by 32 hex digits")
	}
	if cfg.AuthToken == "" {
		return nil, errors.New("twilio: the auth token is required")
	}
	c := &Client{cfg: cfg, base: DefaultBaseURL, messaging: DefaultMessagingURL, http: safehttp.NewClient(safehttp.Options{Timeout: 15 * time.Second})}
	for _, o := range opts {
		o(c)
	}
	return c, nil
}

// ValidAccountSID reports whether s has the shape of a Twilio account SID.
func ValidAccountSID(s string) bool { return validSID(s, "AC") }

// ValidServiceSID reports whether s has the shape of a Messaging Service SID.
func ValidServiceSID(s string) bool { return validSID(s, "MG") }

// validSID reports whether s is the two-letter prefix and 32 hex digits.
func validSID(s, prefix string) bool {
	if len(s) != 34 || !strings.HasPrefix(s, prefix) {
		return false
	}
	for _, r := range s[2:] {
		isHex := r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F'
		if !isHex {
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
// transport failure is ErrAmbiguous: the request may have reached Twilio.
func (c *Client) do(ctx context.Context, method, path string, form url.Values) (int, []byte, error) {
	return c.doAt(ctx, c.base, method, path, form)
}

// doAt is do against another API root.
func (c *Client) doAt(ctx context.Context, root, method, path string, form url.Values) (int, []byte, error) {
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, root+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.SetBasicAuth(c.cfg.AccountSID, c.cfg.AuthToken)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "Hello-PBX")
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
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

type apiError struct {
	Code json.Number `json:"code"`
}

// refusal turns a non-2xx answer into the adapter's error.
func refusal(status int, b []byte) error {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return provider.ErrAuth
	case status == http.StatusNotFound:
		return provider.ErrNotFound
	case status == http.StatusTooManyRequests:
		return &provider.Rejected{Code: provider.CodeRateLimited}
	case status >= 500:
		return fmt.Errorf("%w: carrier error %d", provider.ErrAmbiguous, status)
	}
	var e apiError
	_ = json.Unmarshal(b, &e)
	code := e.Code.String()
	if code == "" || len(code) > 16 {
		code = "http_" + strconv.Itoa(status)
	}
	return &provider.Rejected{Code: code}
}

// Validate implements provider.SMSProvider: the account is readable and
// active, and its numbers can be listed.
func (c *Client) Validate(ctx context.Context) error {
	status, b, err := c.do(ctx, http.MethodGet, "/Accounts/"+c.cfg.AccountSID+".json", nil)
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		if status == http.StatusNotFound {
			return provider.ErrAuth // an unknown SID is a credential problem
		}
		return refusal(status, b)
	}
	var a struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("%w: answer unreadable", provider.ErrAmbiguous)
	}
	if a.Status != "" && a.Status != "active" {
		return &provider.Rejected{Code: "account_" + safeToken(a.Status)}
	}
	_, err = c.Numbers(ctx)
	return err
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
	return b.String()
}

// Numbers implements provider.SMSProvider.
func (c *Client) Numbers(ctx context.Context) ([]provider.Number, error) {
	status, b, err := c.do(ctx, http.MethodGet, "/Accounts/"+c.cfg.AccountSID+"/IncomingPhoneNumbers.json?PageSize=1000", nil)
	if err != nil {
		return nil, err
	}
	if status != http.StatusOK {
		return nil, refusal(status, b)
	}
	var r struct {
		Numbers []struct {
			SID          string `json:"sid"`
			PhoneNumber  string `json:"phone_number"`
			FriendlyName string `json:"friendly_name"`
			Capabilities struct {
				SMS   bool `json:"sms"`
				MMS   bool `json:"mms"`
				Voice bool `json:"voice"`
			} `json:"capabilities"`
		} `json:"incoming_phone_numbers"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, fmt.Errorf("%w: answer unreadable", provider.ErrAmbiguous)
	}
	out := make([]provider.Number, 0, len(r.Numbers))
	for _, n := range r.Numbers {
		out = append(out, provider.Number{ID: n.SID, E164: n.PhoneNumber, Label: n.FriendlyName,
			SMS: n.Capabilities.SMS, MMS: n.Capabilities.MMS, Voice: n.Capabilities.Voice})
	}
	return out, nil
}

// Send implements provider.SMSProvider. Twilio answers 201 with the
// message's SID when it accepted the text. No status callback is set: the
// LAN-only build polls the message instead.
func (c *Client) Send(ctx context.Context, m provider.Outbound) (provider.Sent, error) {
	if m.Body == "" || len([]rune(m.Body)) > maxBodyChars {
		return provider.Sent{}, &provider.Rejected{Code: "body_length"}
	}
	f := url.Values{"To": {m.To}, "Body": {m.Body}}
	if c.cfg.MessagingServiceSID != "" {
		f.Set("MessagingServiceSid", c.cfg.MessagingServiceSID)
	} else {
		f.Set("From", m.From)
	}
	status, b, err := c.do(ctx, http.MethodPost, "/Accounts/"+c.cfg.AccountSID+"/Messages.json", f)
	if err != nil {
		return provider.Sent{}, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return provider.Sent{}, refusal(status, b)
	}
	var r struct {
		SID    string `json:"sid"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(b, &r); err != nil || r.SID == "" {
		// Twilio said yes but Hello cannot tell which message: do not retry.
		return provider.Sent{}, fmt.Errorf("%w: answer unreadable", provider.ErrAmbiguous)
	}
	return provider.Sent{ProviderID: r.SID, Status: normalise(r.Status)}, nil
}

// Status implements provider.SMSProvider.
func (c *Client) Status(ctx context.Context, providerID string) (provider.State, error) {
	if !validMessageSID(providerID) {
		return provider.State{}, provider.ErrNotFound
	}
	status, b, err := c.do(ctx, http.MethodGet, "/Accounts/"+c.cfg.AccountSID+"/Messages/"+providerID+".json", nil)
	if err != nil {
		return provider.State{}, err
	}
	if status != http.StatusOK {
		return provider.State{}, refusal(status, b)
	}
	var r struct {
		Status    string      `json:"status"`
		ErrorCode json.Number `json:"error_code"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return provider.State{}, fmt.Errorf("%w: answer unreadable", provider.ErrAmbiguous)
	}
	st := provider.State{Status: normalise(r.Status)}
	if st.Status == provider.StatusFailed {
		st.ErrorCode = r.ErrorCode.String()
		if st.ErrorCode == "" || len(st.ErrorCode) > 16 {
			st.ErrorCode = safeToken(r.Status)
		}
	}
	return st, nil
}

func validMessageSID(s string) bool {
	if len(s) != 34 || (!strings.HasPrefix(s, "SM") && !strings.HasPrefix(s, "MM")) {
		return false
	}
	return true
}

// normalise maps Twilio's message statuses onto Hello's. accepted, scheduled,
// queued, sending and receiving are not yet out; sent is out; delivered and
// read are delivered; failed and undelivered are failed. An unknown value is
// treated as still in flight.
func normalise(s string) string {
	switch s {
	case "delivered", "read":
		return provider.StatusDelivered
	case "failed", "undelivered", "canceled":
		return provider.StatusFailed
	case "sent":
		return provider.StatusSent
	default:
		return provider.StatusQueued
	}
}
