package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/azrtydxb/hello/internal/events"
	"github.com/azrtydxb/hello/internal/safehttp"
	"github.com/azrtydxb/hello/internal/secret"
)

// Slack hosts. An incoming webhook URL must be on one of the webhook hosts;
// the bot API has one address. Nothing else is ever contacted.
var webhookHosts = []string{"hooks.slack.com", "hooks.slack-gov.com"}

const (
	// PostURL is chat.postMessage.
	PostURL = "https://slack.com/api/chat.postMessage"
	// RequestTimeout bounds one attempt.
	RequestTimeout = 10 * time.Second
	// maxResponse is how much of Slack's answer is read.
	maxResponse = 16 << 10
	// MaxConsecutiveFailures switches a destination off.
	MaxConsecutiveFailures = 50
)

// FieldError names one rejected input field.
type FieldError struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// ValidationError rejects an input; it wraps ErrInvalid.
type ValidationError struct{ Fields []FieldError }

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return "slack: invalid"
	}
	return "slack: " + e.Fields[0].Path + ": " + e.Fields[0].Message
}

// Unwrap makes errors.Is(err, ErrInvalid) hold.
func (e *ValidationError) Unwrap() error { return ErrInvalid }

// Input creates a destination.
type Input struct {
	Name       string
	Mode       string
	Channel    string
	Events     []string
	Extensions []string
	Enabled    bool
	// Secret is the incoming webhook URL (ModeWebhook) or the bot token
	// (ModeBot).
	Secret string
}

// Patch changes a destination; nil fields stay. A new Secret replaces the
// stored one; the mode cannot change.
type Patch struct {
	Name       *string
	Channel    *string
	Events     *[]string
	Extensions *[]string
	Enabled    *bool
	Secret     *string
}

// TestResult is the outcome of a test post.
type TestResult struct {
	Delivered bool   `json:"delivered"`
	Status    int    `json:"status"`
	Error     string `json:"error"`
}

// Service is the administration of destinations plus the sending.
type Service struct {
	Store Store
	Box   *secret.Box
	// PublicURL is HELLO_PUBLIC_URL, for the link in a message.
	PublicURL string
	Now       func() time.Time
	Log       *slog.Logger
	// Client and the base URLs are replaced in tests.
	Client  *http.Client
	PostURL string
	// SkipHostCheck lets a test use a local server as a webhook URL.
	SkipHostCheck bool

	mu    sync.Mutex
	tests map[int64][]time.Time
	hc    *http.Client
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}

func (s *Service) client() *http.Client {
	if s.Client != nil {
		return s.Client
	}
	if s.hc == nil {
		s.hc = safehttp.NewClient(safehttp.Options{Timeout: RequestTimeout})
	}
	return s.hc
}

func (s *Service) postURL() string {
	if s.PostURL != "" {
		return s.PostURL
	}
	return PostURL
}

func aad(id int64) string { return "slack:" + strconv.FormatInt(id, 10) }

var channelRe = regexp.MustCompile(`^[#@]?[A-Za-z0-9][A-Za-z0-9_.\-]{0,79}$`)

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

func cleanList(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
			out = append(out, v)
		}
	}
	slices.Sort(out)
	return out
}

// checkSecret validates the webhook URL or the token for a mode.
func (s *Service) checkSecret(mode, v string) string {
	switch mode {
	case ModeWebhook:
		u, err := url.Parse(v)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return "must be an https Slack incoming webhook URL"
		}
		if !s.SkipHostCheck && (!slices.Contains(webhookHosts, strings.ToLower(u.Hostname())) || u.Port() != "" || !strings.HasPrefix(u.Path, "/services/")) {
			return "must be a Slack incoming webhook URL (https://hooks.slack.com/services/...)"
		}
		if len(v) > 300 {
			return "is too long"
		}
	case ModeBot:
		if !strings.HasPrefix(v, "xoxb-") || len(v) > 300 || strings.ContainsFunc(v, unicode.IsSpace) {
			return "must be a Slack bot token (xoxb-...)"
		}
	}
	return ""
}

func (s *Service) check(d *Destination, secretIn *string) error {
	var f []FieldError
	if !validName(d.Name) {
		f = append(f, FieldError{"name", "must be 1-64 printable characters without surrounding space"})
	}
	if d.Mode != ModeWebhook && d.Mode != ModeBot {
		f = append(f, FieldError{"mode", "must be webhook or bot"})
	}
	if d.Mode == ModeBot && !channelRe.MatchString(d.Channel) {
		f = append(f, FieldError{"channel", "is required for a bot: a channel name or id such as #phone or C0123456789"})
	}
	if d.Mode == ModeWebhook {
		d.Channel = ""
	}
	if secretIn != nil {
		if msg := s.checkSecret(d.Mode, *secretIn); msg != "" {
			f = append(f, FieldError{"secret", msg})
		}
	}
	d.Events = cleanList(d.Events)
	if len(d.Events) == 0 {
		f = append(f, FieldError{"events", "pick at least one event"})
	}
	for _, t := range d.Events {
		if !slices.Contains(EventTypes, t) {
			f = append(f, FieldError{"events", fmt.Sprintf("%q cannot be posted; one of %s", t, strings.Join(EventTypes, ", "))})
			break
		}
	}
	d.Extensions = cleanList(d.Extensions)
	if len(d.Extensions) > 100 {
		f = append(f, FieldError{"extensions", "at most 100 extensions"})
	}
	for _, x := range d.Extensions {
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

// Create adds a destination.
func (s *Service) Create(ctx context.Context, actor string, in Input) (Destination, error) {
	if s.Box == nil {
		return Destination{}, errors.New("slack: no secret box to seal the credential")
	}
	if len(in.Events) == 0 {
		in.Events = slices.Clone(EventTypes)
	}
	d := Destination{Name: in.Name, Mode: in.Mode, Channel: strings.TrimSpace(in.Channel), Events: in.Events, Extensions: in.Extensions, Enabled: in.Enabled}
	sec := strings.TrimSpace(in.Secret)
	if sec == "" {
		return Destination{}, &ValidationError{Fields: []FieldError{{"secret", "is required"}}}
	}
	if err := s.check(&d, &sec); err != nil {
		return Destination{}, err
	}
	all, err := s.Store.List(ctx)
	if err != nil {
		return Destination{}, err
	}
	if len(all) >= MaxDestinations {
		return Destination{}, &ValidationError{Fields: []FieldError{{"name", fmt.Sprintf("at most %d destinations", MaxDestinations)}}}
	}
	if d.ID, err = s.Store.NextID(ctx); err != nil {
		return Destination{}, err
	}
	if d.SecretEnc, err = s.Box.Seal(sec, aad(d.ID)); err != nil {
		return Destination{}, err
	}
	return s.Store.Create(ctx, actor, d)
}

// Update applies p. Enabling a destination clears its failure state;
// disabling one records ReasonManual.
func (s *Service) Update(ctx context.Context, actor string, id int64, p Patch) (Destination, error) {
	d, err := s.Store.Get(ctx, id)
	if err != nil {
		return Destination{}, err
	}
	if p.Name != nil {
		d.Name = *p.Name
	}
	if p.Channel != nil {
		d.Channel = strings.TrimSpace(*p.Channel)
	}
	if p.Events != nil {
		d.Events = *p.Events
	}
	if p.Extensions != nil {
		d.Extensions = *p.Extensions
	}
	reset := false
	if p.Enabled != nil && *p.Enabled != d.Enabled {
		d.Enabled = *p.Enabled
		if d.Enabled {
			d.DisabledReason, reset = "", true
		} else {
			d.DisabledReason = ReasonManual
		}
	}
	var sec *string
	if p.Secret != nil {
		v := strings.TrimSpace(*p.Secret)
		sec = &v
	}
	if err := s.check(&d, sec); err != nil {
		return Destination{}, err
	}
	d.SecretEnc = nil
	if sec != nil {
		if s.Box == nil {
			return Destination{}, errors.New("slack: no secret box to seal the credential")
		}
		if d.SecretEnc, err = s.Box.Seal(*sec, aad(id)); err != nil {
			return Destination{}, err
		}
		// A new credential is a fresh start.
		if !d.Enabled || d.DisabledReason == ReasonAuth || d.DisabledReason == ReasonGone {
			reset = true
		}
		if d.DisabledReason == ReasonAuth || d.DisabledReason == ReasonGone {
			d.Enabled, d.DisabledReason = true, ""
		}
	}
	return s.Store.Update(ctx, actor, d, reset)
}

// Hook returns the ingest-transaction hook that queues a post for every
// destination an event matches.
func (s *Service) Hook() events.TxHook { return s.Store.Enqueue }

// List, Get, Delete, Posts and Retry pass through to the store.
func (s *Service) List(ctx context.Context) ([]Destination, error) { return s.Store.List(ctx) }

// Get returns one destination.
func (s *Service) Get(ctx context.Context, id int64) (Destination, error) {
	return s.Store.Get(ctx, id)
}

// Delete removes a destination and its posts.
func (s *Service) Delete(ctx context.Context, actor string, id int64) error {
	return s.Store.Delete(ctx, actor, id)
}

// Posts lists posts, newest first.
func (s *Service) Posts(ctx context.Context, q PostQuery) ([]Post, error) {
	if q.Limit < 1 || q.Limit > 200 {
		q.Limit = 50
	}
	return s.Store.ListPosts(ctx, q)
}

// Retry queues a dead post again.
func (s *Service) Retry(ctx context.Context, actor string, id int64) (Post, error) {
	return s.Store.Retry(ctx, actor, id, s.now())
}

// result is what one HTTP attempt produced.
type result struct {
	Status     int
	Err        error
	RetryAfter time.Duration
	Body       []byte
}

// send posts body to a destination of the sealed secret.
func (s *Service) send(ctx context.Context, id int64, mode string, secretEnc, body []byte) (result, error) {
	if s.Box == nil {
		return result{}, errors.New("no secret box")
	}
	sec, err := s.Box.Open(secretEnc, aad(id))
	if err != nil {
		return result{}, err
	}
	target := sec
	if mode == ModeBot {
		target = s.postURL()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(string(body)))
	if err != nil {
		return result{Err: errors.New("bad_request")}, nil
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("User-Agent", "Hello-Slack/1")
	if mode == ModeBot {
		req.Header.Set("Authorization", "Bearer "+sec)
	}
	resp, err := s.client().Do(req)
	if err != nil {
		return result{Err: err}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	r := result{Status: resp.StatusCode}
	if v := resp.Header.Get("Retry-After"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			r.RetryAfter = min(max(time.Duration(n)*time.Second, 0), time.Hour)
		}
	}
	r.Body, _ = io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	return r, nil
}

// Classify maps Slack's answer to an outcome and a short secret-free error.
// An incoming webhook answers 200 "ok" or an HTTP error with a text reason; a
// bot answers 200 with {"ok":false,"error":"..."} for most failures.
func Classify(mode string, r result) (Outcome, string, string) {
	if r.Err != nil {
		var t interface{ Timeout() bool }
		switch {
		case errors.Is(r.Err, safehttp.ErrBlockedAddress):
			return OutcomeDead, "blocked_address", ""
		case errors.As(r.Err, &t) && t.Timeout():
			return OutcomeRetry, "timeout", ""
		}
		return OutcomeRetry, "connection_error", ""
	}
	switch {
	case r.Status == http.StatusTooManyRequests, r.Status == http.StatusRequestTimeout, r.Status >= 500:
		return OutcomeRetry, "http " + strconv.Itoa(r.Status), ""
	case r.Status == http.StatusNotFound || r.Status == http.StatusGone:
		if mode == ModeWebhook {
			return OutcomeDisable, "the Slack webhook no longer exists (http " + strconv.Itoa(r.Status) + ")", ReasonGone
		}
		return OutcomeDead, "http " + strconv.Itoa(r.Status), ""
	case r.Status == http.StatusUnauthorized || r.Status == http.StatusForbidden:
		return OutcomeDisable, "Slack refused the credential (http " + strconv.Itoa(r.Status) + ")", ReasonAuth
	case r.Status < 200 || r.Status >= 300:
		return OutcomeDead, "http " + strconv.Itoa(r.Status), ""
	}
	if mode == ModeWebhook {
		return OutcomeSuccess, "", ""
	}
	var a struct {
		OK    bool   `json:"ok"`
		Error string `json:"error"`
	}
	if json.Unmarshal(r.Body, &a) != nil {
		return OutcomeRetry, "unreadable_response", ""
	}
	if a.OK {
		return OutcomeSuccess, "", ""
	}
	e := safeSlackError(a.Error)
	switch a.Error {
	case "ratelimited", "internal_error", "fatal_error", "service_unavailable", "request_timeout":
		return OutcomeRetry, e, ""
	case "invalid_auth", "not_authed", "token_revoked", "token_expired", "account_inactive", "no_permission", "missing_scope":
		return OutcomeDisable, e, ReasonAuth
	}
	// channel_not_found, not_in_channel, is_archived, msg_too_long ...: the
	// post is dead; the others still work.
	return OutcomeDead, e, ""
}

// safeSlackError keeps only the snake_case code Slack returns.
func safeSlackError(s string) string {
	if s == "" || len(s) > 60 {
		return "slack_error"
	}
	for _, r := range s {
		if (r < 'a' || r > 'z') && r != '_' && (r < '0' || r > '9') {
			return "slack_error"
		}
	}
	return "slack: " + s
}

// Test posts a short message to the destination now. It does not queue a
// post and does not count against the destination's health (6 per minute per
// destination).
func (s *Service) Test(ctx context.Context, id int64) (TestResult, error) {
	d, err := s.Store.Get(ctx, id)
	if err != nil {
		return TestResult{}, err
	}
	s.mu.Lock()
	if s.tests == nil {
		s.tests = map[int64][]time.Time{}
	}
	now := s.now()
	recent := s.tests[id][:0]
	for _, t := range s.tests[id] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 6 {
		s.tests[id] = recent
		s.mu.Unlock()
		return TestResult{}, ErrRateLimited
	}
	s.tests[id] = append(recent, now)
	s.mu.Unlock()
	m := map[string]any{"text": "Hello is connected to this Slack channel. Missed calls and voicemail messages will appear here.", "unfurl_links": false}
	if d.Mode == ModeBot {
		m["channel"] = d.Channel
	}
	body, _ := json.Marshal(m)
	tctx, cancel := context.WithTimeout(ctx, RequestTimeout)
	defer cancel()
	r, err := s.send(tctx, d.ID, d.Mode, d.SecretEnc, body)
	if err != nil {
		return TestResult{Error: "secret_unreadable"}, nil
	}
	out, msg, _ := Classify(d.Mode, r)
	return TestResult{Delivered: out == OutcomeSuccess, Status: r.Status, Error: msg}, nil
}
