package messaging

// Provider-side deletion (spec messaging-channels M-20). An erase or a
// redaction queues one row per message Hello sent through a carrier API;
// RunBatch works through one request's rows while the administrator waits
// for the report, and Run retries what is left in the background. Nothing
// here logs a number, a text or a carrier message id.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/azrtydxb/hello/internal/messaging/provider"
)

// RedactJob is one carrier message to delete.
type RedactJob struct {
	ID         int64
	ChannelID  int64
	Provider   string
	ProviderID string
	// Attempts counts the tries, this one included.
	Attempts int
}

// Redaction outcomes, stored as the row's state.
const (
	RedactDone        = "done"
	RedactPending     = "pending"
	RedactFailed      = "failed"
	RedactUnsupported = "unsupported"
)

// RedactResult is what one try came to.
type RedactResult struct {
	// State is done, pending (try again later), failed or unsupported.
	State string
	// Error is a short reason without content (the carrier's code).
	Error string
	// RetryIn is when a pending row is tried again.
	RetryIn time.Duration
}

// RedactStore is the persistence of the deletions; *store.Store implements
// it.
type RedactStore interface {
	// ClaimRedactions leases due rows, of one batch or ("") of any.
	ClaimRedactions(ctx context.Context, batch string, limit int) ([]RedactJob, error)
	ChannelProviderConfig(ctx context.Context, channelID int64) (provider.Config, error)
	FinishRedaction(ctx context.Context, job RedactJob, res RedactResult) error
	// MaintainRedactions gives up on rows past their last try and drops
	// finished rows after 30 days.
	MaintainRedactions(ctx context.Context) error
}

// MaxRedactAttempts is how often a deletion is tried before it is failed:
// with the back-off below, about a day.
const MaxRedactAttempts = 12

// RedactBackoff is the wait before try n+1: one minute doubling, at most
// six hours.
func RedactBackoff(attempts int) time.Duration {
	d := time.Minute
	for i := 1; i < attempts && d < 6*time.Hour; i++ {
		d *= 2
	}
	return min(d, 6*time.Hour)
}

// RedactMetrics counts the tries by provider and result.
type RedactMetrics struct {
	Total *prometheus.CounterVec
}

// NewRedactMetrics registers hello_msg_provider_redactions_total.
func NewRedactMetrics(reg prometheus.Registerer) *RedactMetrics {
	m := &RedactMetrics{Total: prometheus.NewCounterVec(prometheus.CounterOpts{
		Name: "hello_msg_provider_redactions_total",
		Help: "Provider-side deletions of sent texts, by provider and result (done, pending, failed, unsupported).",
	}, []string{"provider", "result"})}
	if reg != nil {
		reg.MustRegister(m.Total)
	}
	return m
}

// Redactor deletes queued messages at their carriers.
type Redactor struct {
	Store   RedactStore
	Metrics *RedactMetrics
	Log     *slog.Logger
	// New builds a carrier adapter; nil uses the registry.
	New func(kind string, cfg provider.Config) (provider.SMSProvider, error)
	// Timeout bounds one carrier call (default 15 s).
	Timeout time.Duration
	// Every is the background pass interval (default 1 minute).
	Every time.Duration
}

func (r *Redactor) log() *slog.Logger {
	if r.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return r.Log
}

// RunBatch works through the due rows of one batch until none is left or
// budget runs out; what is left is retried by Run.
func (r *Redactor) RunBatch(ctx context.Context, batch string, budget time.Duration) {
	if batch == "" {
		return
	}
	bctx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	r.drain(bctx, batch)
}

// Run retries the due rows of every batch until ctx ends. Any number of
// replicas may run it: the store leases each row to one.
func (r *Redactor) Run(ctx context.Context) {
	every := r.Every
	if every <= 0 {
		every = time.Minute
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		r.drain(ctx, "")
		if err := r.Store.MaintainRedactions(ctx); err != nil && ctx.Err() == nil {
			r.log().Warn("provider redactions: maintenance failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (r *Redactor) drain(ctx context.Context, batch string) {
	for ctx.Err() == nil {
		jobs, err := r.Store.ClaimRedactions(ctx, batch, 20)
		if err != nil {
			if ctx.Err() == nil {
				r.log().Warn("provider redactions: claiming", "error", err)
			}
			return
		}
		if len(jobs) == 0 {
			return
		}
		for _, j := range jobs {
			res := r.One(ctx, j)
			fctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
			if err := r.Store.FinishRedaction(fctx, j, res); err != nil {
				r.log().Warn("provider redactions: recording a result", "id", j.ID, "error", err)
			}
			cancel()
			if r.Metrics != nil {
				r.Metrics.Total.WithLabelValues(j.Provider, res.State).Inc()
			}
		}
	}
}

// One tries one deletion and says what to record.
func (r *Redactor) One(ctx context.Context, j RedactJob) RedactResult {
	cfg, err := r.Store.ChannelProviderConfig(ctx, j.ChannelID)
	if err != nil {
		return r.retry(j, "credentials_unreadable")
	}
	newFn := r.New
	if newFn == nil {
		newFn = provider.New
	}
	p, err := newFn(j.Provider, cfg)
	if err != nil {
		return RedactResult{State: RedactUnsupported, Error: "no_adapter"}
	}
	red, ok := p.(provider.Redactor)
	if !ok {
		return RedactResult{State: RedactUnsupported, Error: "not_supported"}
	}
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	_, err = red.Redact(cctx, j.ProviderID)
	var rej *provider.Rejected
	switch {
	case err == nil:
		return RedactResult{State: RedactDone}
	case errors.Is(err, provider.ErrRedactUnsupported):
		// A carrier that redacts on its own (Telnyx's messaging profile
		// setting) says so, for the report.
		why := "not_supported"
		if pr, ok := p.(interface {
			ProfileRedaction(ctx context.Context) (enabled, ok bool)
		}); ok {
			switch on, read := pr.ProfileRedaction(cctx); {
			case read && on:
				why += "; profile_redaction_on"
			case read:
				why += "; profile_redaction_off"
			}
		}
		return RedactResult{State: RedactUnsupported, Error: why}
	case errors.Is(err, provider.ErrAuth):
		// The credentials may be fixed later: keep trying for the day.
		return r.retry(j, "credentials_refused")
	case errors.Is(err, provider.ErrNotFound):
		return RedactResult{State: RedactDone}
	case errors.As(err, &rej) && rej.Code != provider.CodeRateLimited:
		return RedactResult{State: RedactFailed, Error: "rejected_" + rej.Code}
	case errors.As(err, &rej):
		return r.retry(j, "rate_limited")
	}
	return r.retry(j, "carrier_unreachable")
}

func (r *Redactor) retry(j RedactJob, why string) RedactResult {
	if j.Attempts >= MaxRedactAttempts {
		return RedactResult{State: RedactFailed, Error: why + "; retries exhausted"}
	}
	return RedactResult{State: RedactPending, Error: why, RetryIn: RedactBackoff(j.Attempts)}
}
