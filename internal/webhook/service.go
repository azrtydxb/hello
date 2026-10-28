package webhook

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mrand "math/rand/v2"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/safehttp"
	"github.com/azrtydxb/hello/internal/secret"
)

// Delivery limits (spec I-41, I-42).
const (
	// RequestTimeout bounds one attempt.
	RequestTimeout = 10 * time.Second
	// MaxResponseBody is how much of a receiver's response is read; the rest
	// is dropped with the connection.
	MaxResponseBody = 64 << 10
	// TestBodyLimit is how much of the response a test ping returns.
	TestBodyLimit = 1024
	// MaxEndpoints bounds the endpoints one deployment may have.
	MaxEndpoints = 100
)

// TestEventType is the type of the ping POST /webhooks/{id}/test sends. It is
// not a registered call event: no endpoint can subscribe to it.
const TestEventType = "webhook.test"

// FieldError names one rejected input field.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError rejects an input, naming each failing field. It wraps
// ErrInvalid.
type ValidationError struct{ Fields []FieldError }

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "webhook: invalid"
	}
	return "webhook: " + e.Fields[0].Path + ": " + e.Fields[0].Message
}

// Unwrap makes errors.Is(err, ErrInvalid) hold.
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// EndpointInput is a new endpoint.
type EndpointInput struct {
	Name         string
	URL          string
	Events       []string
	Extensions   []string
	Enabled      bool
	AllowPrivate bool
}

// Patch changes an endpoint; nil fields stay.
type Patch struct {
	Name         *string
	URL          *string
	Events       *[]string
	Extensions   *[]string
	Enabled      *bool
	AllowPrivate *bool
}

// TestResult is the outcome of a test ping.
type TestResult struct {
	// Delivered is true for a 2xx response.
	Delivered bool `json:"delivered"`
	// Status is the receiver's HTTP status, 0 when no response came.
	Status    int   `json:"status"`
	LatencyMs int64 `json:"latencyMs"`
	// Error is a short description of a transport failure.
	Error string `json:"error"`
	// Body is the first kilobyte of the response, when it is text.
	Body string `json:"body"`
	// EventID is the Hello-Event-Id the ping carried.
	EventID string `json:"eventId"`
}

// Service is the webhook logic over a Store: validation, secrets, the
// ingest hook and the SSRF-safe requests. The dispatcher uses it to send.
type Service struct {
	Store Store
	// Box seals the signing secrets.
	Box *secret.Box
	// AllowedCIDRs are the private ranges an endpoint with allowPrivate may
	// reach (HELLO_INTEGRATIONS_ALLOWED_CIDRS); none means no endpoint may be
	// private.
	AllowedCIDRs []netip.Prefix
	// Now and Rand are replaced in tests.
	Now  func() time.Time
	Rand func() float64
	// PublicClient and PrivateClient override the SSRF-safe clients (tests
	// that must reach a local server).
	PublicClient, PrivateClient *http.Client
	Log                         *slog.Logger

	publicC, privateC *http.Client
	// skipURLCheck lets a test point an endpoint at a local server.
	skipURLCheck bool
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) rnd() float64 {
	if s.Rand != nil {
		return s.Rand()
	}
	return mrand.Float64()
}

func (s *Service) log() *slog.Logger {
	if s.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return s.Log
}

// client is the HTTP client for an endpoint: one that refuses every private,
// loopback and link-local address, or, for an allowPrivate endpoint, one that
// also reaches the administrator's allow-list (and still never loopback,
// link-local or metadata addresses). Neither follows redirects.
func (s *Service) client(allowPrivate bool) *http.Client {
	if allowPrivate {
		if s.PrivateClient != nil {
			return s.PrivateClient
		}
		if s.privateC == nil {
			s.privateC = safehttp.NewClient(safehttp.Options{Timeout: RequestTimeout, Allow: s.AllowedCIDRs})
		}
		return s.privateC
	}
	if s.PublicClient != nil {
		return s.PublicClient
	}
	if s.publicC == nil {
		s.publicC = safehttp.NewClient(safehttp.Options{Timeout: RequestTimeout})
	}
	return s.publicC
}

// ValidateURL checks a subscriber URL the way a delivery will reach it: https
// (http only for allowPrivate), no credentials, no fragment, and an address
// literal inside the permitted ranges. Host names are checked again at
// connect time, after resolution.
func (s *Service) ValidateURL(raw string, allowPrivate bool) error {
	if s.skipURLCheck {
		return nil
	}
	if allowPrivate && len(s.AllowedCIDRs) == 0 {
		return errors.New("private targets are not allowed on this deployment (HELLO_INTEGRATIONS_ALLOWED_CIDRS is empty)")
	}
	o := safehttp.Options{}
	if allowPrivate {
		o.Allow = s.AllowedCIDRs
	}
	if _, err := safehttp.ValidateURL(raw, !allowPrivate, o); err != nil {
		if errors.Is(err, safehttp.ErrBlockedAddress) {
			if allowPrivate {
				return errors.New("the address is not inside HELLO_INTEGRATIONS_ALLOWED_CIDRS (loopback, link-local and metadata addresses are never allowed)")
			}
			return errors.New("the address is private, loopback or link-local; an administrator can allow private targets with allowPrivate")
		}
		return err
	}
	return nil
}

func validName(n string) bool {
	if n == "" || n != strings.TrimSpace(n) || len([]rune(n)) > 64 {
		return false
	}
	for _, r := range n {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

func isExtension(v string) bool {
	if len(v) < 2 || len(v) > 10 {
		return false
	}
	for _, r := range v {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// check validates and normalises an endpoint's fields.
func (s *Service) check(e *Endpoint) error {
	var f []FieldError
	if !validName(e.Name) {
		f = append(f, FieldError{"name", "must be 1-64 printable characters without surrounding space"})
	}
	if err := s.ValidateURL(e.URL, e.AllowPrivate); err != nil {
		f = append(f, FieldError{"url", err.Error()})
	}
	e.Events = cleanList(e.Events)
	for _, t := range e.Events {
		if _, err := events.ParseTypes(t); err != nil {
			f = append(f, FieldError{"events", err.Error()})
			break
		}
	}
	e.Extensions = cleanList(e.Extensions)
	if len(e.Extensions) > 100 {
		f = append(f, FieldError{"extensions", "at most 100 extensions"})
	}
	for _, x := range e.Extensions {
		if !isExtension(x) {
			f = append(f, FieldError{"extensions", fmt.Sprintf("%q is not an extension number", x)})
			break
		}
	}
	if len(f) > 0 {
		return &ValidationError{Fields: f}
	}
	return nil
}

func aad(id int64) string { return "webhook:" + strconv.FormatInt(id, 10) }

// newSecret is a fresh signing secret: 256 bits, URL-safe.
func newSecret() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return "", err
	}
	return "whsec_" + base64.RawURLEncoding.EncodeToString(b), nil
}

// Create adds an endpoint and returns its signing secret, which is shown
// once. When an endpoint with the same URL and events exists (a Zapier
// REST-hook subscription repeated) it returns that one with an empty secret
// and created false.
func (s *Service) Create(ctx context.Context, actor string, in EndpointInput) (ep Endpoint, signing string, created bool, err error) {
	if s.Box == nil {
		return Endpoint{}, "", false, errors.New("webhook: no secret box to seal the signing secret")
	}
	e := Endpoint{Name: in.Name, URL: in.URL, Events: in.Events, Extensions: in.Extensions, Enabled: in.Enabled, AllowPrivate: in.AllowPrivate}
	if err := s.check(&e); err != nil {
		return Endpoint{}, "", false, err
	}
	all, err := s.Store.ListEndpoints(ctx)
	if err != nil {
		return Endpoint{}, "", false, err
	}
	if len(all) >= MaxEndpoints && !slices.ContainsFunc(all, func(x Endpoint) bool { return x.URL == e.URL && slices.Equal(x.Events, e.Events) }) {
		return Endpoint{}, "", false, &ValidationError{Fields: []FieldError{{"url", fmt.Sprintf("at most %d endpoints", MaxEndpoints)}}}
	}
	if e.ID, err = s.Store.NextEndpointID(ctx); err != nil {
		return Endpoint{}, "", false, err
	}
	if signing, err = newSecret(); err != nil {
		return Endpoint{}, "", false, err
	}
	if e.SecretEnc, err = s.Box.Seal(signing, aad(e.ID)); err != nil {
		return Endpoint{}, "", false, err
	}
	ep, created, err = s.Store.CreateEndpoint(ctx, actor, e)
	if err != nil || !created {
		return ep, "", false, err
	}
	return ep, signing, true, nil
}

// Update applies p. Enabling an endpoint clears its failure count and
// disabled reason; disabling one records ReasonManual.
func (s *Service) Update(ctx context.Context, actor string, id int64, p Patch) (Endpoint, error) {
	e, err := s.Store.GetEndpoint(ctx, id)
	if err != nil {
		return Endpoint{}, err
	}
	if p.Name != nil {
		e.Name = *p.Name
	}
	if p.URL != nil {
		e.URL = *p.URL
	}
	if p.Events != nil {
		e.Events = *p.Events
	}
	if p.Extensions != nil {
		e.Extensions = *p.Extensions
	}
	if p.AllowPrivate != nil {
		e.AllowPrivate = *p.AllowPrivate
	}
	reset := false
	if p.Enabled != nil && *p.Enabled != e.Enabled {
		e.Enabled = *p.Enabled
		if e.Enabled {
			e.DisabledReason, reset = "", true
		} else {
			e.DisabledReason = ReasonManual
		}
	}
	if err := s.check(&e); err != nil {
		return Endpoint{}, err
	}
	return s.Store.UpdateEndpoint(ctx, actor, e, reset)
}

// Rotate replaces the signing secret. The old one keeps signing for
// SecretWindow, so a receiver can accept both while it switches. The new
// secret is returned once.
func (s *Service) Rotate(ctx context.Context, actor string, id int64) (string, Endpoint, error) {
	if s.Box == nil {
		return "", Endpoint{}, errors.New("webhook: no secret box to seal the signing secret")
	}
	cur, err := s.Store.GetEndpoint(ctx, id)
	if err != nil {
		return "", Endpoint{}, err
	}
	signing, err := newSecret()
	if err != nil {
		return "", Endpoint{}, err
	}
	sealed, err := s.Box.Seal(signing, aad(id))
	if err != nil {
		return "", Endpoint{}, err
	}
	exp := s.now().Add(SecretWindow)
	ep, err := s.Store.SetSecrets(ctx, actor, id, sealed, cur.SecretEnc, &exp)
	if err != nil {
		return "", Endpoint{}, err
	}
	return signing, ep, nil
}

// signingSecrets opens the secrets that sign now: the current one and, until
// its window ends, the previous one.
func (s *Service) signingSecrets(id int64, cur, prev []byte, prevExpires *time.Time) ([]string, error) {
	if s.Box == nil {
		return nil, errors.New("no secret box")
	}
	c, err := s.Box.Open(cur, aad(id))
	if err != nil {
		return nil, err
	}
	out := []string{c}
	if len(prev) > 0 && prevExpires != nil && s.now().Before(*prevExpires) {
		if p, err := s.Box.Open(prev, aad(id)); err == nil {
			out = append(out, p)
		}
	}
	return out, nil
}

// Hook returns the ingest-transaction hook that queues a delivery for every
// endpoint an event matches.
func (s *Service) Hook() events.TxHook {
	return func(ctx context.Context, tx *sql.Tx, e events.Entry) error { return s.Store.Enqueue(ctx, tx, e) }
}

// Passthroughs the API uses.

// ListEndpoints returns every endpoint.
func (s *Service) ListEndpoints(ctx context.Context) ([]Endpoint, error) {
	return s.Store.ListEndpoints(ctx)
}

// GetEndpoint returns one endpoint.
func (s *Service) GetEndpoint(ctx context.Context, id int64) (Endpoint, error) {
	return s.Store.GetEndpoint(ctx, id)
}

// DeleteEndpoint removes an endpoint and its deliveries.
func (s *Service) DeleteEndpoint(ctx context.Context, actor string, id int64) error {
	return s.Store.DeleteEndpoint(ctx, actor, id)
}

// ListDeliveries lists deliveries, newest first.
func (s *Service) ListDeliveries(ctx context.Context, q DeliveryQuery) ([]Delivery, error) {
	return s.Store.ListDeliveries(ctx, q)
}

// GetDelivery returns a delivery with its attempt log.
func (s *Service) GetDelivery(ctx context.Context, id int64) (DeliveryDetail, error) {
	return s.Store.GetDelivery(ctx, id)
}

// Redeliver queues a delivery again with a fresh retry series.
func (s *Service) Redeliver(ctx context.Context, actor string, id int64) (Delivery, error) {
	return s.Store.Redeliver(ctx, actor, id, s.now())
}

// RedeliverDead queues every dead letter of an endpoint again.
func (s *Service) RedeliverDead(ctx context.Context, actor string, endpointID int64) (int64, error) {
	if _, err := s.Store.GetEndpoint(ctx, endpointID); err != nil {
		return 0, err
	}
	return s.Store.RedeliverDead(ctx, actor, endpointID, s.now())
}

// sendResult is what one HTTP attempt produced.
type sendResult struct {
	Status     int
	Err        error
	Latency    time.Duration
	RetryAfter time.Duration
	Body       []byte
}

// send POSTs body to url as a delivery signed by secrets. bodyLimit bytes of
// the response are kept (the rest of up to MaxResponseBody is read and
// dropped).
func (s *Service) send(ctx context.Context, url string, allowPrivate bool, secrets []string, body []byte,
	eventID, eventType, ref string, keep int, extra map[string]string) sendResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(body)))
	if err != nil {
		return sendResult{Err: err}
	}
	now := s.now()
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Hello-Webhooks/1")
	req.Header.Set(HeaderEventID, eventID)
	req.Header.Set(HeaderEventType, eventType)
	req.Header.Set(HeaderDelivery, ref)
	req.Header.Set(HeaderSignature, SignatureHeader(secrets, now, body))
	for k, v := range extra {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := s.client(allowPrivate).Do(req)
	lat := time.Since(start)
	if err != nil {
		return sendResult{Err: err, Latency: lat}
	}
	defer func() { _ = resp.Body.Close() }()
	r := sendResult{Status: resp.StatusCode, Latency: lat, RetryAfter: ParseRetryAfter(resp.Header.Get("Retry-After"), now)}
	b, _ := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBody))
	r.Body = b[:min(len(b), keep)]
	return r
}

// newRef is a unique Hello-Delivery value.
func newRef() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "dlv_" + hex.EncodeToString(b)
}

// Test sends a webhook.test ping to the endpoint now, signed like a
// delivery, and reports what came back. It does not create a delivery or
// count against the endpoint's failures. The ping works on a disabled
// endpoint too, so a receiver can be fixed before it is switched back on.
func (s *Service) Test(ctx context.Context, id int64) (TestResult, error) {
	e, err := s.Store.GetEndpoint(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	secrets, err := s.signingSecrets(e.ID, e.SecretEnc, e.PreviousSecretEnc, e.PreviousSecretExpires)
	if err != nil {
		return TestResult{Error: "secret_unreadable"}, nil
	}
	now := s.now()
	ev := events.Event{APIVersion: events.APIVersion, ID: "evt_test_" + strings.TrimPrefix(newRef(), "dlv_"), Type: TestEventType, CreatedAt: now,
		Data: events.Data{CallID: "webhook-test"}}
	body, err := json.Marshal(ev)
	if err != nil {
		return TestResult{}, err
	}
	// The receiver gets the server's clock, to compare against its own when
	// signature timestamps are rejected.
	r := s.send(ctx, e.URL, e.AllowPrivate, secrets, body, ev.ID, ev.Type, newRef(), TestBodyLimit,
		map[string]string{"Date": now.Format(http.TimeFormat)})
	out := TestResult{Status: r.Status, LatencyMs: r.Latency.Milliseconds(), EventID: ev.ID, Delivered: r.Err == nil && r.Status >= 200 && r.Status < 300}
	if r.Err != nil {
		out.Error = ErrorText(0, r.Err)
	}
	out.Body = printableText(r.Body)
	return out, nil
}

// printableText returns b as text when it is valid printable UTF-8 and
// otherwise empty: a binary response is not shown.
func printableText(b []byte) string {
	s := strings.ToValidUTF8(string(b), "")
	if len(s) != len(b) {
		return ""
	}
	for _, r := range s {
		if r < ' ' && r != '\n' && r != '\r' && r != '\t' {
			return ""
		}
	}
	return s
}
