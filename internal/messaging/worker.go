package messaging

// The carrier send and status workers (spec messaging-channels M-9a), for the
// channels that reach a carrier's web API. They run in hello-control on any
// number of replicas: the store claims each row once.
//
// At most once: a message is claimed (queued to sending) before the carrier
// is called and is never sent again. When the carrier's answer is lost the
// row stays sending and the maintenance sweeper marks it unknown ("may have
// been delivered"); nothing resends it. A message with no provider id is
// never retried automatically.
//
// Neither worker logs a body, a number or a credential.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// OutboundJob is a queued carrier text the store handed to a worker. It
// carries the text and the numbers, so it is never logged.
type OutboundJob struct {
	MessageID      int64
	ConversationID int64
	ChannelID      int64
	AddressID      int64
	Provider       string
	From           string
	To             string
	Body           string
	// AttemptID names this claim; a result for another attempt is ignored.
	AttemptID string
}

// Outcomes of a send attempt.
const (
	// OutcomeSent: the carrier accepted the text.
	OutcomeSent = "sent"
	// OutcomeFailed: the carrier refused it; it was not sent.
	OutcomeFailed = "failed"
	// OutcomeUnknown: the carrier may have accepted it; left as sending.
	OutcomeUnknown = "unknown"
)

// OutboundResult is what a send attempt came to.
type OutboundResult struct {
	Outcome        string
	ProviderID     string
	ProviderStatus string // the carrier's status at acceptance
	ErrorCode      string
	// OptOut: the carrier reports the customer opted out, so Hello records it.
	OptOut bool
	// BadCredentials: the carrier refused the account's credentials, so the
	// channel stops sending.
	BadCredentials bool
	// ChannelFailure counts toward the channel's degraded state.
	ChannelFailure bool
}

// PollJob is a sent carrier text whose delivery status is still open.
type PollJob struct {
	MessageID      int64
	ConversationID int64
	ChannelID      int64
	Provider       string
	ProviderID     string
	// Polls is how many times it was read before.
	Polls     int
	SentAt    time.Time
	AttemptID string
}

// PollResult is what a status read came to.
type PollResult struct {
	State provider.State
	// Changed is false when the read failed or showed nothing new.
	Read bool
	// Stop ends the polling without a final state (too old, or gone).
	Stop           bool
	BadCredentials bool
}

// WorkerStore is the persistence the workers need; *store.Store implements it.
type WorkerStore interface {
	ClaimOutbound(ctx context.Context, limit int) ([]OutboundJob, error)
	ChannelProviderConfig(ctx context.Context, channelID int64) (provider.Config, error)
	FinishOutbound(ctx context.Context, job OutboundJob, res OutboundResult) error
	ClaimPolls(ctx context.Context, limit int) ([]PollJob, error)
	FinishPoll(ctx context.Context, job PollJob, res PollResult) error
}

// Worker sends queued carrier texts and follows their delivery.
type Worker struct {
	Store   WorkerStore
	Metrics *Metrics
	Log     *slog.Logger
	// New builds a carrier adapter; nil uses the registry.
	New func(kind string, cfg provider.Config) (provider.SMSProvider, error)
	// Timeout bounds one carrier call (default 20 s).
	Timeout time.Duration
	Now     func() time.Time
}

func (w *Worker) timeout() time.Duration {
	if w.Timeout > 0 {
		return w.Timeout
	}
	return 20 * time.Second
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func (w *Worker) adapter(ctx context.Context, kind string, channelID int64, cache map[int64]provider.SMSProvider) (provider.SMSProvider, error) {
	if p, ok := cache[channelID]; ok {
		return p, nil
	}
	cfg, err := w.Store.ChannelProviderConfig(ctx, channelID)
	if err != nil {
		return nil, err
	}
	var p provider.SMSProvider
	if w.New != nil {
		p, err = w.New(kind, cfg)
	} else {
		p, err = provider.New(kind, cfg)
	}
	if err != nil {
		return nil, err
	}
	cache[channelID] = p
	return p, nil
}

func (w *Worker) count(kind, result string) {
	if w.Metrics != nil {
		w.Metrics.Outbound.WithLabelValues(kind, result).Inc()
	}
}

func (w *Worker) warn(msg string, args ...any) {
	if w.Log != nil {
		w.Log.Warn(msg, args...)
	}
}

// SendOnce claims up to limit queued texts and sends each once. It returns
// how many it handled.
func (w *Worker) SendOnce(ctx context.Context, limit int) (int, error) {
	jobs, err := w.Store.ClaimOutbound(ctx, limit)
	if err != nil {
		return 0, err
	}
	cache := map[int64]provider.SMSProvider{}
	for _, j := range jobs {
		res := w.send(ctx, j, cache)
		w.count(j.Provider, res.Outcome)
		if err := w.Store.FinishOutbound(ctx, j, res); err != nil {
			// The row stays sending; the sweeper settles it. Not retried.
			w.warn("messaging send result not saved", "message", j.MessageID, "error", err)
		}
	}
	return len(jobs), nil
}

func (w *Worker) send(ctx context.Context, j OutboundJob, cache map[int64]provider.SMSProvider) OutboundResult {
	p, err := w.adapter(ctx, j.Provider, j.ChannelID, cache)
	if err != nil {
		// Nothing was sent: no adapter or no usable credentials.
		return OutboundResult{Outcome: OutcomeFailed, ErrorCode: "channel_unavailable"}
	}
	cctx, cancel := context.WithTimeout(ctx, w.timeout())
	defer cancel()
	sent, err := p.Send(cctx, provider.Outbound{ID: j.MessageID, From: j.From, To: j.To, Body: j.Body})
	if err == nil {
		return OutboundResult{Outcome: OutcomeSent, ProviderID: sent.ProviderID, ProviderStatus: sent.Status}
	}
	var rej *provider.Rejected
	switch {
	case errors.Is(err, provider.ErrAuth):
		return OutboundResult{Outcome: OutcomeFailed, ErrorCode: "credentials_invalid", BadCredentials: true}
	case errors.As(err, &rej):
		return OutboundResult{Outcome: OutcomeFailed, ErrorCode: rej.Code,
			OptOut: rej.Code == provider.CodeOptedOut, ChannelFailure: rej.Code == provider.CodeRateLimited}
	default:
		// The carrier may have the text. Never send it again.
		return OutboundResult{Outcome: OutcomeUnknown, ChannelFailure: true}
	}
}

// PollOnce reads the delivery status of up to limit sent texts. A final state
// stops the polling, and so does a text older than the carrier's status
// window or one the carrier no longer knows.
func (w *Worker) PollOnce(ctx context.Context, limit int) (int, error) {
	jobs, err := w.Store.ClaimPolls(ctx, limit)
	if err != nil {
		return 0, err
	}
	cache := map[int64]provider.SMSProvider{}
	for _, j := range jobs {
		res := w.poll(ctx, j, cache)
		if err := w.Store.FinishPoll(ctx, j, res); err != nil {
			w.warn("messaging status not saved", "message", j.MessageID, "error", err)
		}
	}
	return len(jobs), nil
}

func (w *Worker) poll(ctx context.Context, j PollJob, cache map[int64]provider.SMSProvider) PollResult {
	p, err := w.adapter(ctx, j.Provider, j.ChannelID, cache)
	if err != nil {
		return PollResult{}
	}
	if win := p.Caps().StatusWindow; win > 0 && w.now().Sub(j.SentAt) > win {
		return PollResult{Stop: true}
	}
	cctx, cancel := context.WithTimeout(ctx, w.timeout())
	defer cancel()
	st, err := p.Status(cctx, j.ProviderID)
	switch {
	case err == nil:
		return PollResult{State: st, Read: true}
	case errors.Is(err, provider.ErrAuth):
		return PollResult{BadCredentials: true, Stop: true}
	case errors.Is(err, provider.ErrNotFound):
		return PollResult{Stop: true}
	default:
		return PollResult{} // try again on the schedule
	}
}

// PollDelay is how long to wait before the nth status read of a text that
// was sent age ago: 5 seconds doubling to 5 minutes for the first hour, then
// every 30 minutes until the carrier's status window ends.
func PollDelay(n int, age time.Duration) time.Duration {
	if age >= time.Hour {
		return 30 * time.Minute
	}
	d := 5 * time.Second
	for i := 1; i < n && d < 5*time.Minute; i++ {
		d *= 2
	}
	if d > 5*time.Minute {
		d = 5 * time.Minute
	}
	return d
}
